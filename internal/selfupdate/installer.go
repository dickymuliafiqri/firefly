package selfupdate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/binx"
)

// Stage names the step an install is on. The dashboard renders these directly,
// so they are stable identifiers rather than prose; the human-readable text
// travels in Progress.Message.
type Stage string

const (
	StageResolving   Stage = "resolving"
	StageDownloading Stage = "downloading"
	StageVerifying   Stage = "verifying"
	StageInstalling  Stage = "installing"
)

// Describe renders a human-readable label for a stage, so a consumer that shows
// progress without receiving an explicit message still has something to say.
func (s Stage) Describe() string {
	switch s {
	case StageResolving:
		return "Resolving the release"
	case StageDownloading:
		return "Downloading the update"
	case StageVerifying:
		return "Verifying the download"
	case StageInstalling:
		return "Installing the update"
	default:
		return string(s)
	}
}

// Progress is one update event. Written/Total are populated only while
// downloading, and Total is 0 when the server sends no Content-Length.
type Progress struct {
	Stage   Stage  `json:"stage"`
	Message string `json:"message"`
	Written int64  `json:"written,omitempty"`
	Total   int64  `json:"total,omitempty"`
}

// ReportFunc receives progress events. It must not block: it is called from the
// goroutine performing the install.
type ReportFunc func(Progress)

// InstallResult describes a completed replacement.
type InstallResult struct {
	// Previous is the version that was running before the swap.
	Previous string
	// Installed is the release tag that is now on disk.
	Installed string
	// Path is where the new executable was written.
	Path string
	// Backup is the previous executable's new location, set only on platforms
	// that cannot overwrite a running image (Windows). It is removed on the next
	// start.
	Backup string
}

// InstallRequest is the input to Installer.Install.
type InstallRequest struct {
	// Release is the release to install.
	Release *Release
	// Env is the host's self-update capability, from DetectEnvironment.
	Env Environment
	// Current is the running build's version, used to refuse a no-op or
	// downgrade and to report what was replaced.
	Current string
	// Report, when set, receives stage and byte progress.
	Report ReportFunc
}

// Installer downloads and swaps the executable.
type Installer struct {
	client *Client
	logger *slog.Logger
}

// NewInstaller builds an Installer over a release client.
func NewInstaller(client *Client, logger *slog.Logger) *Installer {
	return &Installer{client: client, logger: logger}
}

// Install downloads the release asset for this platform, verifies its SHA-256
// against the release's checksums.txt, and atomically replaces the running
// executable. It does not restart the process.
//
// Nothing is written over the executable until the payload has been downloaded
// in full and its digest has matched, so every failure mode — a truncated
// transfer, a mismatched digest, an archive missing its entry, a missing
// checksum file — leaves a working binary in place and returns an error the
// operator can act on.
func (i *Installer) Install(ctx context.Context, req InstallRequest) (InstallResult, error) {
	var result InstallResult

	rel := req.Release
	if rel == nil {
		return result, fmt.Errorf("no release to install")
	}
	if rel.Draft {
		return result, fmt.Errorf("release %s is a draft", rel.Tag)
	}
	if !req.Env.Available() {
		return result, fmt.Errorf("self-update is unavailable: %s", req.Env.Reason)
	}
	if req.Current != "" && !IsNewer(rel.Tag, req.Current) {
		return result, fmt.Errorf("version %s is not newer than the running %s", rel.Tag, req.Current)
	}
	if _, ok := ParseVersion(rel.Tag); !ok {
		return result, fmt.Errorf("release tag %q is not a version this build can compare", rel.Tag)
	}

	report := req.Report
	if report == nil {
		report = func(Progress) {}
	}

	report(Progress{Stage: StageResolving, Message: "Resolving the release for " + runtime.GOOS + "/" + runtime.GOARCH})

	assetName, archive, err := AssetName(rel.Tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return result, err
	}
	asset, ok := rel.Asset(assetName)
	if !ok {
		return result, fmt.Errorf("release %s has no asset %q", rel.Tag, assetName)
	}
	if asset.URL == "" {
		return result, fmt.Errorf("release %s asset %q has no download url", rel.Tag, assetName)
	}
	if err := i.client.validateAssetURL(asset.URL); err != nil {
		return result, err
	}

	// The digest comes from the release's own checksums.txt, fetched over the
	// same authenticated-by-TLS channel as the asset. An absent or incomplete
	// checksum file is fatal: an unverifiable binary is worse than an old one.
	sums, err := i.fetchChecksums(ctx, rel)
	if err != nil {
		return result, err
	}
	want, ok := sums[assetName]
	if !ok {
		return result, fmt.Errorf("release %s checksums.txt does not list %q", rel.Tag, assetName)
	}

	stagePath := StagingName(req.Env.Executable)
	defer os.Remove(stagePath)

	total := asset.Size
	report(Progress{Stage: StageDownloading, Message: "Downloading " + assetName, Total: total})

	// binx.Download hashes the payload before it publishes anything, so the
	// digest sits between the network and the filesystem, not after it.
	if err := binx.Download(ctx, binx.DownloadOptions{
		URL:            asset.URL,
		DestPath:       stagePath,
		Archive:        archive,
		ArchiveName:    ExecutableName(runtime.GOOS),
		ExpectedSHA256: want,
		UserAgent:      userAgent,
		Logger:         i.logger,
		Progress: func(written, reported int64) {
			if reported > 0 {
				total = reported
			}
			report(Progress{
				Stage:   StageDownloading,
				Message: "Downloading " + assetName,
				Written: written,
				Total:   total,
			})
		},
	}); err != nil {
		return result, fmt.Errorf("download %s: %w", assetName, err)
	}

	report(Progress{Stage: StageVerifying, Message: "Verifying checksum"})
	if err := validateExecutable(stagePath, runtime.GOOS); err != nil {
		return result, err
	}

	report(Progress{Stage: StageInstalling, Message: "Replacing " + req.Env.Executable})
	backup, err := publish(stagePath, req.Env.Executable)
	if err != nil {
		return result, err
	}

	result = InstallResult{
		Previous:  req.Current,
		Installed: rel.Tag,
		Path:      req.Env.Executable,
		Backup:    backup,
	}

	if i.logger != nil {
		i.logger.Info("self-update installed",
			"from", req.Current, "to", rel.Tag, "path", req.Env.Executable, "backup", backup)
	}
	return result, nil
}

// fetchChecksums downloads and parses the release's checksums.txt. The asset
// URL is preferred over a constructed download URL so a re-uploaded or renamed
// asset is picked up; a release that does not publish the file is rejected here
// rather than producing an unverified install later.
func (i *Installer) fetchChecksums(ctx context.Context, rel *Release) (map[string]string, error) {
	asset, ok := rel.Asset(ChecksumAsset)
	if !ok || asset.URL == "" {
		return nil, fmt.Errorf("release %s does not publish %s, so the download cannot be verified", rel.Tag, ChecksumAsset)
	}
	if err := i.client.validateAssetURL(asset.URL); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("build checksum request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := i.client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", ChecksumAsset, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: server returned %s", ChecksumAsset, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ChecksumAsset, err)
	}
	return ParseChecksums(bytes.NewReader(body))
}

// publish moves staging onto target.
//
// On Unix a rename replaces the running executable's directory entry while the
// running process keeps its open inode, so the swap is a single atomic step. On
// Windows the running image is locked against deletion and overwrite, so the old
// file is renamed aside first and the rollback path puts it back if the second
// rename fails — the operator must never be left with no executable at all.
func publish(staging, target string) (backup string, err error) {
	if runtime.GOOS != "windows" {
		if err := os.Rename(staging, target); err != nil {
			return "", fmt.Errorf("replace the executable: %w", err)
		}
		return "", nil
	}

	backup = BackupName(target)
	// A leftover backup from a previous update is still locked by the process
	// that wrote it; ignore the failure and let the rename below overwrite it.
	_ = os.Remove(backup)

	if err := os.Rename(target, backup); err != nil {
		return "", fmt.Errorf("move the running executable aside: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		if restoreErr := os.Rename(backup, target); restoreErr != nil {
			return "", fmt.Errorf("publish the new executable: %w (the previous one is at %s and could not be restored: %v)", err, backup, restoreErr)
		}
		return "", fmt.Errorf("publish the new executable: %w", err)
	}
	return backup, nil
}

// CleanupStale removes artifacts a previous update could not delete, and is
// meant to be called once at startup. On Windows the replaced executable stays
// locked until the process that ran it exits, so the backup of the previous
// update is only removable after the new process has started.
func CleanupStale(logger *slog.Logger) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	RemoveStaleArtifacts(exe, logger)
}

// RemoveStaleArtifacts deletes the backup and staging paths beside executable,
// logging at debug level because their absence is the normal case.
func RemoveStaleArtifacts(executable string, logger *slog.Logger) {
	for _, path := range []string{BackupName(executable), StagingName(executable)} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			if logger != nil {
				// Expected on the first start after an update: the previous
				// image is still mapped by the process that just exited.
				logger.Debug("could not remove update artifact", "path", path, "err", err)
			}
			continue
		}
		if logger != nil {
			logger.Debug("removed update artifact", "path", path)
		}
	}
}

// executablePrefixes lists the magic bytes a native executable for a platform
// starts with.
var executablePrefixes = map[string][][]byte{
	"linux":   {{0x7F, 'E', 'L', 'F'}},
	"windows": {{'M', 'Z'}},
	"darwin": {
		{0xFE, 0xED, 0xFA, 0xCE}, // 32-bit big-endian Mach-O
		{0xFE, 0xED, 0xFA, 0xCF}, // 64-bit big-endian Mach-O
		{0xCE, 0xFA, 0xED, 0xFE}, // 32-bit little-endian Mach-O
		{0xCF, 0xFA, 0xED, 0xFE}, // 64-bit little-endian Mach-O
		{0xCA, 0xFE, 0xBA, 0xBE}, // universal binary
	},
}

// validateExecutable checks that the staged file starts with a magic number the
// target platform can execute.
//
// The checksum already proves the archive is the one the release published, so
// this is not a security control; it catches the mundane failure of extracting
// the wrong archive entry — or of a release pipeline that packaged something
// else — before that file becomes the gateway's executable. A platform with no
// known prefix is accepted, since guessing would be worse than not checking.
func validateExecutable(path, goos string) error {
	prefixes, ok := executablePrefixes[goos]
	if !ok {
		return nil
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("inspect the downloaded executable: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat the downloaded executable: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("the downloaded executable is empty")
	}

	head := make([]byte, 4)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return fmt.Errorf("read the downloaded executable: %w", err)
	}
	head = head[:n]

	for _, p := range prefixes {
		if bytes.HasPrefix(head, p) {
			return nil
		}
	}
	return fmt.Errorf("the downloaded file is not a %s executable", goos)
}

// validateAssetURL refuses a download URL outside GitHub.
//
// Downloads are already digest-verified against a file that the same release
// publishes, so this closes the remaining gap: an unexpected API response
// pointing the gateway at an arbitrary host would otherwise cause it to fetch —
// and, if the digest check were ever weakened, execute — a binary from anywhere.
// A client pointed at a non-GitHub API (tests, a fork's mirror) relaxes the check,
// since its assets live elsewhere by construction.
func (c *Client) validateAssetURL(rawURL string) error {
	if !c.restrictAssetHosts {
		return nil
	}
	if strings.HasPrefix(rawURL, "https://github.com/") ||
		strings.HasPrefix(rawURL, "https://objects.githubusercontent.com/") ||
		strings.HasPrefix(rawURL, "https://github-releases.githubusercontent.com/") {
		return nil
	}
	return fmt.Errorf("refusing to download an update from an unexpected location: %s", rawURL)
}
