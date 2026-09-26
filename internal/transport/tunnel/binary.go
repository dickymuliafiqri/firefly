package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"

	"github.com/dickymuliafiqri/firefly/internal/binx"
)

// DownloaderFunc defines the function signature for downloading the cloudflared binary.
type DownloaderFunc func(ctx context.Context, destPath string, logger *slog.Logger) error

// ReleaseAsset returns the official Cloudflare GitHub release download URL
// for the specified operating system and CPU architecture.
// The boolean return value indicates whether the asset is packaged as a .tgz archive.
func ReleaseAsset(goos, goarch string) (string, bool, error) {
	baseURL := "https://github.com/cloudflare/cloudflared/releases/latest/download/"

	switch goos {
	case "windows":
		switch goarch {
		case "amd64", "arm64":
			return baseURL + "cloudflared-windows-amd64.exe", false, nil
		case "386":
			return baseURL + "cloudflared-windows-386.exe", false, nil
		default:
			return "", false, fmt.Errorf("unsupported windows architecture: %s", goarch)
		}
	case "linux":
		switch goarch {
		case "amd64":
			return baseURL + "cloudflared-linux-amd64", false, nil
		case "arm64":
			return baseURL + "cloudflared-linux-arm64", false, nil
		case "arm":
			return baseURL + "cloudflared-linux-arm", false, nil
		case "386":
			return baseURL + "cloudflared-linux-386", false, nil
		default:
			return "", false, fmt.Errorf("unsupported linux architecture: %s", goarch)
		}
	case "darwin":
		switch goarch {
		case "arm64":
			return baseURL + "cloudflared-darwin-arm64.tgz", true, nil
		case "amd64":
			return baseURL + "cloudflared-darwin-amd64.tgz", true, nil
		default:
			return "", false, fmt.Errorf("unsupported darwin architecture: %s", goarch)
		}
	default:
		return "", false, fmt.Errorf("unsupported operating system for automated download: %s", goos)
	}
}

// BinaryName returns the platform-specific executable name.
func BinaryName() string {
	return binx.ExecutableName("cloudflared")
}

// DefaultBinaryDir returns the default directory where firefly stores downloaded binaries.
func DefaultBinaryDir() (string, error) {
	return binx.DefaultDir()
}

// FindBinary searches for an existing cloudflared executable.
func FindBinary(customDir string) (string, bool) {
	return binx.Find("cloudflared", customDir)
}

// DownloadBinary downloads the official cloudflared release binary to destPath.
func DownloadBinary(ctx context.Context, destPath string, logger *slog.Logger) error {
	assetURL, isTarGz, err := ReleaseAsset(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	return binx.Download(ctx, binx.DownloadOptions{
		URL:         assetURL,
		DestPath:    destPath,
		IsTarGz:     isTarGz,
		ArchiveName: "cloudflared",
		UserAgent:   "firefly-tunnel-installer",
		Logger:      logger,
	})
}
