package tunnel

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReleaseAsset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		goos      string
		goarch    string
		wantURL   string
		wantTarGz bool
		wantErr   bool
	}{
		{
			goos:      "windows",
			goarch:    "amd64",
			wantURL:   "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe",
			wantTarGz: false,
		},
		{
			goos:      "windows",
			goarch:    "arm64",
			wantURL:   "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe",
			wantTarGz: false,
		},
		{
			goos:      "windows",
			goarch:    "386",
			wantURL:   "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-386.exe",
			wantTarGz: false,
		},
		{
			goos:      "linux",
			goarch:    "amd64",
			wantURL:   "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64",
			wantTarGz: false,
		},
		{
			goos:      "linux",
			goarch:    "arm64",
			wantURL:   "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-arm64",
			wantTarGz: false,
		},
		{
			goos:      "linux",
			goarch:    "arm",
			wantURL:   "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-arm",
			wantTarGz: false,
		},
		{
			goos:      "darwin",
			goarch:    "arm64",
			wantURL:   "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-darwin-arm64.tgz",
			wantTarGz: true,
		},
		{
			goos:      "darwin",
			goarch:    "amd64",
			wantURL:   "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-darwin-amd64.tgz",
			wantTarGz: true,
		},
		{
			goos:    "solaris",
			goarch:  "amd64",
			wantErr: true,
		},
		{
			goos:    "windows",
			goarch:  "mips",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.goos+"/"+tt.goarch, func(t *testing.T) {
			t.Parallel()
			gotURL, gotTarGz, err := ReleaseAsset(tt.goos, tt.goarch)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotURL != tt.wantURL {
				t.Errorf("got url %q, want %q", gotURL, tt.wantURL)
			}
			if gotTarGz != tt.wantTarGz {
				t.Errorf("got isTarGz %v, want %v", gotTarGz, tt.wantTarGz)
			}
		})
	}
}

func TestBinaryName(t *testing.T) {
	name := BinaryName()
	if runtime.GOOS == "windows" {
		if name != "cloudflared.exe" {
			t.Errorf("expected cloudflared.exe, got %q", name)
		}
	} else {
		if name != "cloudflared" {
			t.Errorf("expected cloudflared, got %q", name)
		}
	}
}

func TestFindBinary_CustomDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	exeName := BinaryName()
	binPath := filepath.Join(tmpDir, exeName)

	if err := os.WriteFile(binPath, []byte("fake-binary"), 0755); err != nil {
		t.Fatalf("failed to write fake binary: %v", err)
	}

	found, ok := FindBinary(tmpDir)
	if !ok {
		t.Fatalf("expected FindBinary to return true")
	}
	if found != binPath {
		t.Errorf("got %q, want %q", found, binPath)
	}
}

func TestManager_ResolveBinary_UsesMockDownloader(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	downloaded := false

	mockDownloader := func(ctx context.Context, destPath string, logger *slog.Logger) error {
		downloaded = true
		return os.WriteFile(destPath, []byte("mock-downloaded-bin"), 0755)
	}

	m := NewManager(Config{
		Mode:       ModeQuick,
		LocalURL:   "http://127.0.0.1:8080",
		BinDir:     tmpDir,
		Downloader: mockDownloader,
	})

	bin, err := m.resolveBinary(context.Background())
	if err != nil {
		t.Fatalf("resolveBinary failed: %v", err)
	}

	if !downloaded {
		t.Errorf("expected downloader to be invoked")
	}

	expectedPath := filepath.Join(tmpDir, BinaryName())
	if bin != expectedPath {
		t.Errorf("got %q, want %q", bin, expectedPath)
	}

	// Calling resolveBinary again should find existing binary without invoking downloader
	downloaded = false
	bin2, err := m.resolveBinary(context.Background())
	if err != nil {
		t.Fatalf("second resolveBinary failed: %v", err)
	}
	if downloaded {
		t.Errorf("downloader should not be called again when binary exists")
	}
	if bin2 != expectedPath {
		t.Errorf("got %q, want %q", bin2, expectedPath)
	}
}
