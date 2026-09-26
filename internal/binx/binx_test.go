package binx_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
		IsTarGz:     true,
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
