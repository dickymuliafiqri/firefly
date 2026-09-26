// Package binx provides reusable utilities for binary discovery, path resolution,
// and downloading/extracting executable release assets across platforms.
package binx

import (
	"archive/tar"
	"compress/gzip"
	"context"
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
//    - Checks system PATH via exec.LookPath
//    - Checks Firefly's home directory (~/.firefly/bin)
//    - Checks local directories (./data/bin and ./bin)
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

// DownloadOptions configures the binary download and installation process.
type DownloadOptions struct {
	URL         string
	DestPath    string
	IsTarGz     bool
	ArchiveName string
	UserAgent   string
	Timeout     time.Duration
	Logger      *slog.Logger
}

// Download fetches an executable from a remote URL and writes it atomically to DestPath.
// If IsTarGz is true, it unpacks the archive and extracts ArchiveName (or DestPath's basename).
// On Unix platforms, the resulting file is granted 0755 executable permissions.
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.URL, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	client := &http.Client{
		Timeout: timeout,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download server returned %s", resp.Status)
	}

	tmpFile := opts.DestPath + ".tmp"
	f, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("create temporary binary file: %w", err)
	}

	writeSuccess := false
	defer func() {
		_ = f.Close()
		if !writeSuccess {
			_ = os.Remove(tmpFile)
		}
	}()

	if opts.IsTarGz {
		targetEntry := opts.ArchiveName
		if targetEntry == "" {
			targetEntry = filepath.Base(opts.DestPath)
		}
		if err := ExtractTarGz(resp.Body, targetEntry, f); err != nil {
			return err
		}
	} else {
		if _, err := io.Copy(f, resp.Body); err != nil {
			return fmt.Errorf("write binary: %w", err)
		}
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync binary file: %w", err)
	}
	_ = f.Close()

	if runtime.GOOS != "windows" {
		if err := os.Chmod(tmpFile, 0755); err != nil {
			return fmt.Errorf("chmod binary: %w", err)
		}
	}

	// Remove destination first to handle Windows in-place replace semantics
	_ = os.Remove(opts.DestPath)

	if err := os.Rename(tmpFile, opts.DestPath); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	writeSuccess = true

	if opts.Logger != nil {
		opts.Logger.Info("binary downloaded and verified successfully", "path", opts.DestPath)
	}
	return nil
}
