package binx_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/binx"
)

func TestExecutableName(t *testing.T) {
	name := binx.ExecutableName("testprog")
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(name, ".exe") {
			t.Errorf("expected .exe suffix on windows, got %q", name)
		}
	} else {
		if strings.HasSuffix(name, ".exe") {
			t.Errorf("did not expect .exe suffix on non-windows, got %q", name)
		}
	}
}

func TestDefaultDir(t *testing.T) {
	dir, err := binx.DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir returned error: %v", err)
	}
	if dir == "" {
		t.Fatal("DefaultDir returned empty path")
	}
}

func TestFind_CustomDir(t *testing.T) {
	tmpDir := t.TempDir()
	binName := binx.ExecutableName("mock-binary")
	binPath := filepath.Join(tmpDir, binName)

	// Not found initially
	if _, found := binx.Find("mock-binary", tmpDir); found {
		t.Fatal("expected binary not to be found before creation")
	}

	// Create dummy binary
	if err := os.WriteFile(binPath, []byte("echo binary"), 0755); err != nil {
		t.Fatalf("failed to create dummy binary: %v", err)
	}

	// Found in customDir
	resolved, found := binx.Find("mock-binary", tmpDir)
	if !found {
		t.Fatal("expected binary to be found in custom dir")
	}
	if resolved != binPath {
		t.Fatalf("got %q, want %q", resolved, binPath)
	}
}

func TestExtractTarGz(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte("#!/bin/sh\necho test\n")
	hdr := &tar.Header{
		Name: "test-target",
		Mode: 0755,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write tar content: %v", err)
	}
	_ = tw.Close()
	_ = gw.Close()

	var extracted bytes.Buffer
	if err := binx.ExtractTarGz(&buf, "test-target", &extracted); err != nil {
		t.Fatalf("ExtractTarGz failed: %v", err)
	}
	if !bytes.Equal(extracted.Bytes(), content) {
		t.Fatalf("content mismatch: got %q, want %q", extracted.String(), string(content))
	}
}

func TestDownload_RawAndArchive(t *testing.T) {
	rawContent := []byte("raw-executable-content")

	var archiveBuf bytes.Buffer
	gw := gzip.NewWriter(&archiveBuf)
	tw := tar.NewWriter(gw)
	archiveContent := []byte("archive-binary-content")
	_ = tw.WriteHeader(&tar.Header{
		Name: "my-app",
		Mode: 0755,
		Size: int64(len(archiveContent)),
	})
	_, _ = tw.Write(archiveContent)
	_ = tw.Close()
	_ = gw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/raw":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(rawContent)
		case "/archive.tgz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(archiveBuf.Bytes())
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()

	// 1. Raw download
	rawTarget := filepath.Join(tmpDir, "raw-app")
	err := binx.Download(context.Background(), binx.DownloadOptions{
		URL:      srv.URL + "/raw",
		DestPath: rawTarget,
	})
	if err != nil {
		t.Fatalf("Download raw failed: %v", err)
	}
	data, err := os.ReadFile(rawTarget)
	if err != nil {
		t.Fatalf("read raw downloaded file: %v", err)
	}
	if !bytes.Equal(data, rawContent) {
		t.Fatalf("got raw content %q, want %q", string(data), string(rawContent))
	}

	// 2. Archive download & extract
	archiveTarget := filepath.Join(tmpDir, "my-app")
	err = binx.Download(context.Background(), binx.DownloadOptions{
		URL:         srv.URL + "/archive.tgz",
		DestPath:    archiveTarget,
		Archive:     binx.ArchiveTarGz,
		ArchiveName: "my-app",
	})
	if err != nil {
		t.Fatalf("Download archive failed: %v", err)
	}
	data, err = os.ReadFile(archiveTarget)
	if err != nil {
		t.Fatalf("read archive downloaded file: %v", err)
	}
	if !bytes.Equal(data, archiveContent) {
		t.Fatalf("got archive content %q, want %q", string(data), string(archiveContent))
	}
}

// zipArchive builds an in-memory zip holding one entry, mimicking the
// `zip -j` Windows release asset.
func zipArchive(t *testing.T, entryName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(entryName)
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatalf("write zip entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// sha256Hex is the digest the release publishes in checksums.txt.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Download must unpack a zip asset and match the target entry by its base name,
// with the .exe suffix ignored on both sides. This is the Windows release asset
// path, which has no tar.gz equivalent.
func TestDownload_ZipArchive(t *testing.T) {
	content := []byte("windows-binary-content")
	archive := zipArchive(t, "firefly.exe", content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	target := filepath.Join(t.TempDir(), "firefly.exe")
	err := binx.Download(context.Background(), binx.DownloadOptions{
		URL:            srv.URL,
		DestPath:       target,
		Archive:        binx.ArchiveZip,
		ArchiveName:    "firefly",
		ExpectedSHA256: sha256Hex(archive),
	})
	if err != nil {
		t.Fatalf("Download zip failed: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("got %q, want %q", string(got), string(content))
	}
}

// A checksum mismatch must be reported and must leave DestPath untouched: a
// failed update may not replace a working binary with an unverified one.
func TestDownload_ChecksumMismatchKeepsDestination(t *testing.T) {
	content := []byte("new-content")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "firefly")
	original := []byte("existing-binary")
	if err := os.WriteFile(target, original, 0755); err != nil {
		t.Fatalf("seed destination: %v", err)
	}

	err := binx.Download(context.Background(), binx.DownloadOptions{
		URL:            srv.URL,
		DestPath:       target,
		ExpectedSHA256: sha256Hex([]byte("something-else")),
	})
	if err == nil {
		t.Fatal("expected a checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want a checksum mismatch", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("destination was modified: got %q, want %q", string(got), string(original))
	}

	// No staging residue may survive a failed attempt.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("leftover files after failure: %v", names)
	}
}

// The digest is checked over the archive bytes, not the extracted binary, which
// is what checksums.txt covers.
func TestDownload_ChecksumVerifiedAgainstArchiveBytes(t *testing.T) {
	content := []byte("binary-content")
	archive := zipArchive(t, "firefly.exe", content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	target := filepath.Join(t.TempDir(), "firefly.exe")
	err := binx.Download(context.Background(), binx.DownloadOptions{
		URL:            srv.URL,
		DestPath:       target,
		Archive:        binx.ArchiveZip,
		ArchiveName:    "firefly",
		ExpectedSHA256: sha256Hex(content),
	})
	if err == nil {
		t.Fatal("expected mismatch against the inner binary digest, got nil")
	}
}

// ExtractZip reports a missing entry instead of silently producing an empty file.
func TestExtractZip_MissingEntry(t *testing.T) {
	archive := zipArchive(t, "present.exe", []byte("x"))
	var out bytes.Buffer
	err := binx.ExtractZip(bytes.NewReader(archive), int64(len(archive)), "absent", &out)
	if err == nil {
		t.Fatal("expected an error for a missing entry, got nil")
	}
	if !strings.Contains(err.Error(), "did not contain") {
		t.Fatalf("error = %v, want a missing-entry error", err)
	}
}

// Download reports transfer progress while streaming, ending on the full size so
// the dashboard's progress bar can reach 100%.
func TestDownload_ReportsProgress(t *testing.T) {
	content := bytes.Repeat([]byte("firefly-"), 4096)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	var mu sync.Mutex
	var calls [][2]int64
	err := binx.Download(context.Background(), binx.DownloadOptions{
		URL:      srv.URL,
		DestPath: filepath.Join(t.TempDir(), "firefly"),
		Progress: func(written, total int64) {
			mu.Lock()
			calls = append(calls, [2]int64{written, total})
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Download failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("expected at least one progress callback")
	}
	last := calls[len(calls)-1]
	if last[0] != int64(len(content)) || last[1] != int64(len(content)) {
		t.Fatalf("final progress = %v, want %d/%d", last, len(content), len(content))
	}
}
