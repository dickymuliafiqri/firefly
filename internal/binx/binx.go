// Package binx provides reusable utilities for binary discovery, path resolution,
// and downloading/extracting executable release assets across platforms.
package binx

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// DefaultDownloadTimeout is the default maximum duration for downloading an asset.
	DefaultDownloadTimeout = 5 * time.Minute

	// DefaultUserAgent is the HTTP User-Agent sent when downloading binaries.
	DefaultUserAgent = "firefly-binary-manager"
)

// ExecutableName returns the platform-specific executable filename for baseName.
// On Windows, it appends ".exe" if not already present.
func ExecutableName(baseName string) string {
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(strings.ToLower(baseName), ".exe") {
			return baseName + ".exe"
		}
	}
	return baseName
}

// DefaultDir returns Firefly's standard binary storage directory.
// It prefers ~/.firefly/bin, falling back to data/bin when the user home
// directory cannot be determined.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return filepath.Join(home, ".firefly", "bin"), nil
	}
	return filepath.Join("data", "bin"), nil
}

// Find searches for an existing executable binary by name:
// 1. If customDir is specified, it strictly searches within customDir.
// 2. Otherwise:
//   - Checks system PATH via exec.LookPath
//   - Checks Firefly's home directory (~/.firefly/bin)
//   - Checks local directories (./data/bin and ./bin)
//
// Returns the resolved executable path and true if found, or ("", false) otherwise.
func Find(name string, customDir string) (string, bool) {
	exeName := ExecutableName(name)

	// 1. If custom directory is specified, search only within it.
	if customDir != "" {
		p := filepath.Join(customDir, exeName)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return p, true
		}
		return "", false
	}

	// 2. Check system PATH
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}

	// 3. Check user home ~/.firefly/bin
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		p := filepath.Join(homeDir, ".firefly", "bin", exeName)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return p, true
		}
	}

	// 4. Check relative paths
	candidates := []string{
		filepath.Join("data", "bin", exeName),
		filepath.Join("bin", exeName),
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return p, true
		}
	}

	return "", false
}

// ExtractTarGz extracts an entry named targetName from a gzip-compressed tar reader into w.
func ExtractTarGz(r io.Reader, targetName string, w io.Writer) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("decompress gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	targetBase := strings.ToLower(filepath.Base(targetName))

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}

		entryBase := strings.ToLower(filepath.Base(hdr.Name))
		if entryBase == targetBase || strings.TrimSuffix(entryBase, ".exe") == strings.TrimSuffix(targetBase, ".exe") {
			if _, err := io.Copy(w, tr); err != nil {
				return fmt.Errorf("extract archive entry: %w", err)
			}
			return nil
		}
	}

	return fmt.Errorf("archive did not contain expected binary %q", targetName)
}

// ExtractZip extracts an entry named targetName from a zip archive into w.
//
// Unlike a tar.gz, a zip central directory is at the end of the file, so this
// needs random access and therefore takes a ReaderAt (an *os.File or
// bytes.Reader) plus the archive size instead of a plain io.Reader. Callers
// that only have a stream must spool it to a temporary file first — buffering a
// multi-megabyte release archive in memory to avoid that is not acceptable.
func ExtractZip(r io.ReaderAt, size int64, targetName string, w io.Writer) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}

	targetBase := strings.TrimSuffix(strings.ToLower(filepath.Base(targetName)), ".exe")

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		entryBase := strings.TrimSuffix(strings.ToLower(filepath.Base(f.Name)), ".exe")
		if entryBase != targetBase {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %q: %w", f.Name, err)
		}
		_, copyErr := io.Copy(w, rc)
		closeErr := rc.Close()
		if copyErr != nil {
			return fmt.Errorf("extract archive entry: %w", copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close zip entry %q: %w", f.Name, closeErr)
		}
		return nil
	}

	return fmt.Errorf("archive did not contain expected binary %q", targetName)
}

// Archive identifies the container format of a release asset.
//
// It is an enum rather than a pair of booleans so "tar.gz and zip" is not a
// representable state: Download used to take a lone IsTarGz flag, and adding a
// second flag for Windows assets would have made that invalid combination the
// caller's problem to avoid.
type Archive string

const (
	// ArchiveNone means the response body is the executable itself.
	ArchiveNone Archive = ""
	// ArchiveTarGz means a gzip-compressed tar containing the executable.
	ArchiveTarGz Archive = "tar.gz"
	// ArchiveZip means a zip containing the executable (Windows release assets).
	ArchiveZip Archive = "zip"
)

// DownloadOptions configures the binary download and installation process.
type DownloadOptions struct {
	URL         string
	DestPath    string
	Archive     Archive
	ArchiveName string
	UserAgent   string
	Timeout     time.Duration
	Logger      *slog.Logger

	// ExpectedSHA256, when set, is the lowercase hex digest the downloaded
	// archive (or raw binary) must match. Verification happens before anything
	// is written to DestPath, so a corrupt, truncated, or substituted payload
	// never becomes a runnable file. It is the checksum the release publishes
	// in checksums.txt; an empty value disables the check.
	ExpectedSHA256 string

	// Progress, when set, is invoked while the payload is streaming with the
	// number of bytes written so far and the expected total (0 when the server
	// does not send a Content-Length). It is rate-limited to roughly ten calls
	// per second, so a long download does not turn into a callback storm.
	Progress func(written, total int64)
}

// Download fetches an executable from a remote URL and writes it atomically to
// DestPath. When Archive is ArchiveTarGz or ArchiveZip, the archive is unpacked
// and the ArchiveName entry (or DestPath's basename) is extracted. On Unix
// platforms the resulting file is granted 0755 executable permissions.
//
// The transfer is two-phase so verification cannot race publication:
//
//  1. the response body is streamed to DestPath+".download", hashed while it is
//     written, and checked against ExpectedSHA256;
//  2. only then is the executable materialized at DestPath+".tmp", synced, and
//     renamed over DestPath.
//
// A digest mismatch, a truncated body, or an unparsable archive therefore leaves
// DestPath exactly as it was.
func Download(ctx context.Context, opts DownloadOptions) error {
	if opts.URL == "" {
		return errors.New("download url is required")
	}
	if opts.DestPath == "" {
		return errors.New("destination path is required")
	}

	destDir := filepath.Dir(opts.DestPath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultDownloadTimeout
	}

	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}

	if opts.Logger != nil {
		opts.Logger.Info("downloading release binary", "url", opts.URL, "target", opts.DestPath)
	}

	// Phase 1: spool the payload to disk while hashing it. Spooling (rather than
	// extracting from the stream) is what makes zip assets work at all — a zip
	// central directory sits at the end of the file — and it keeps the checksum
	// check ahead of every write to DestPath.
	stagePath := opts.DestPath + ".download"
	if err := fetchToFile(ctx, opts.URL, stagePath, userAgent, timeout, opts.ExpectedSHA256, opts.Progress); err != nil {
		_ = os.Remove(stagePath)
		return err
	}
	defer os.Remove(stagePath)

	// Phase 2: materialize the executable next to DestPath, then publish it.
	tmpPath := opts.DestPath + ".tmp"
	published := false
	defer func() {
		if !published {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := writeExecutable(stagePath, tmpPath, opts); err != nil {
		return err
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpPath, 0755); err != nil {
			return fmt.Errorf("chmod binary: %w", err)
		}
	}

	// Remove destination first to handle Windows in-place replace semantics.
	_ = os.Remove(opts.DestPath)

	if err := os.Rename(tmpPath, opts.DestPath); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	published = true

	if opts.Logger != nil {
		opts.Logger.Info("binary downloaded and verified successfully", "path", opts.DestPath)
	}
	return nil
}

// fetchToFile streams url into path, returning once the body has been fully
// written and — when wantSHA256 is non-empty — confirmed against the digest.
// A Content-Length mismatch is rejected before the digest so a truncated
// transfer reports as such instead of as a checksum failure.
func fetchToFile(ctx context.Context, url, path, userAgent string, timeout time.Duration, wantSHA256 string, progress func(written, total int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download server returned %s", resp.Status)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("create staging file: %w", err)
	}

	hash := sha256.New()
	total := resp.ContentLength
	if total < 0 {
		total = 0
	}
	sink := &progressWriter{w: io.MultiWriter(f, hash), fn: progress, total: total}
	_, copyErr := io.Copy(sink, resp.Body)
	written := sink.written
	syncErr := f.Sync()
	closeErr := f.Close()

	if copyErr != nil {
		return fmt.Errorf("write download: %w", copyErr)
	}
	if progress != nil && written > 0 {
		progress(written, sink.total)
	}
	if syncErr != nil {
		return fmt.Errorf("sync download: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close download: %w", closeErr)
	}
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		return fmt.Errorf("truncated download: got %d bytes, expected %d", written, resp.ContentLength)
	}
	if wantSHA256 != "" {
		got := hex.EncodeToString(hash.Sum(nil))
		if !strings.EqualFold(got, wantSHA256) {
			return fmt.Errorf("checksum mismatch: got %s, expected %s", got, wantSHA256)
		}
	}
	return nil
}

// progressWriter counts the bytes passing through it and reports them to fn,
// throttled to roughly ten calls per second so a large archive does not produce
// a callback per 32 KiB network chunk. A total of 0 means "size unknown".
type progressWriter struct {
	w       io.Writer
	fn      func(written, total int64)
	total   int64
	written int64
	last    time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if p.fn != nil && n > 0 {
		now := time.Now()
		if now.Sub(p.last) >= 100*time.Millisecond {
			p.last = now
			p.fn(p.written, p.total)
		}
	}
	return n, err
}

// writeExecutable turns the verified staging file at stagePath into an
// executable at tmpPath according to opts.Archive.
func writeExecutable(stagePath, tmpPath string, opts DownloadOptions) error {
	switch opts.Archive {
	case ArchiveNone:
		if err := os.Rename(stagePath, tmpPath); err != nil {
			return fmt.Errorf("stage binary: %w", err)
		}
		return nil
	case ArchiveTarGz, ArchiveZip:
		// handled below
	default:
		return fmt.Errorf("unsupported archive format %q", opts.Archive)
	}

	targetEntry := opts.ArchiveName
	if targetEntry == "" {
		targetEntry = filepath.Base(opts.DestPath)
	}

	src, err := os.Open(stagePath)
	if err != nil {
		return fmt.Errorf("open staged archive: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create temporary binary file: %w", err)
	}

	extractErr := func() error {
		if opts.Archive == ArchiveTarGz {
			return ExtractTarGz(src, targetEntry, dst)
		}
		info, err := src.Stat()
		if err != nil {
			return fmt.Errorf("stat staged archive: %w", err)
		}
		return ExtractZip(src, info.Size(), targetEntry, dst)
	}()
	if extractErr != nil {
		_ = dst.Close()
		return extractErr
	}

	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		return fmt.Errorf("sync binary file: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("close binary file: %w", err)
	}
	return nil
}
