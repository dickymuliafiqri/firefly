package selfupdate

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/binx"
)

func TestAssetName(t *testing.T) {
	cases := []struct {
		goos    string
		goarch  string
		want    string
		archive binx.Archive
	}{
		{"linux", "amd64", "firefly_v1.44.13_linux_amd64.tar.gz", binx.ArchiveTarGz},
		{"linux", "arm64", "firefly_v1.44.13_linux_arm64.tar.gz", binx.ArchiveTarGz},
		{"darwin", "arm64", "firefly_v1.44.13_darwin_arm64.tar.gz", binx.ArchiveTarGz},
		{"windows", "amd64", "firefly_v1.44.13_windows_amd64.zip", binx.ArchiveZip},
		{"windows", "arm64", "firefly_v1.44.13_windows_arm64.zip", binx.ArchiveZip},
	}

	for _, tc := range cases {
		name, archive, err := AssetName("v1.44.13", tc.goos, tc.goarch)
		if err != nil {
			t.Fatalf("AssetName(%s/%s) failed: %v", tc.goos, tc.goarch, err)
		}
		if name != tc.want {
			t.Fatalf("AssetName(%s/%s) = %q, want %q", tc.goos, tc.goarch, name, tc.want)
		}
		if archive != tc.archive {
			t.Fatalf("AssetName(%s/%s) archive = %q, want %q", tc.goos, tc.goarch, archive, tc.archive)
		}
	}
}

func TestAssetNameUnsupported(t *testing.T) {
	for _, tc := range []struct{ goos, goarch string }{
		{"linux", "386"},
		{"freebsd", "amd64"},
		{"plan9", "amd64"},
	} {
		_, _, err := AssetName("v1.44.13", tc.goos, tc.goarch)
		if err == nil {
			t.Fatalf("AssetName(%s/%s) succeeded, want an error", tc.goos, tc.goarch)
		}
		if !strings.Contains(err.Error(), "no release asset") {
			t.Fatalf("AssetName(%s/%s) error = %v, want a no-asset error", tc.goos, tc.goarch, err)
		}
	}
}

func TestExecutableName(t *testing.T) {
	if got := ExecutableName("linux"); got != "firefly" {
		t.Fatalf("ExecutableName(linux) = %q, want %q", got, "firefly")
	}
	if got := ExecutableName("windows"); got != "firefly.exe" {
		t.Fatalf("ExecutableName(windows) = %q, want %q", got, "firefly.exe")
	}
}

func TestAssetMatchesPlatform(t *testing.T) {
	// The release matrix must be recognizable from the asset name alone, which is
	// what the dashboard shows the operator before they approve a download.
	name, _, err := AssetName("v1.44.13", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("no asset for the test host: %v", err)
	}
	if !AssetMatchesPlatform(name, runtime.GOOS, runtime.GOARCH) {
		t.Fatalf("AssetMatchesPlatform(%q, %s, %s) = false, want true", name, runtime.GOOS, runtime.GOARCH)
	}
	if AssetMatchesPlatform(name, "linux", "ppc64") {
		t.Fatal("AssetMatchesPlatform matched a foreign platform")
	}
	if AssetMatchesPlatform(ChecksumAsset, runtime.GOOS, runtime.GOARCH) {
		t.Fatal("AssetMatchesPlatform matched checksums.txt")
	}
}

func TestStagingAndBackupNames(t *testing.T) {
	dir := "/usr/local/bin"
	exe := filepath.Join(dir, "firefly")
	if got, want := StagingName(exe), filepath.Join(dir, ".firefly.update"); got != want {
		t.Fatalf("StagingName = %q, want %q", got, want)
	}
	if got, want := BackupName(exe), filepath.Join(dir, "firefly.old"); got != want {
		t.Fatalf("BackupName = %q, want %q", got, want)
	}
}
