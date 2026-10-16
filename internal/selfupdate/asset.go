package selfupdate

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/binx"
)

// AssetPrefix and ChecksumAsset mirror .github/workflows/release.yml, which
// names each archive "firefly_${TAG}_${GOOS}_${GOARCH}" and publishes the
// matching digests as checksums.txt. Both are part of the wire contract with
// the release pipeline: change one without the other and self-update silently
// finds nothing.
const (
	AssetPrefix   = "firefly_"
	ChecksumAsset = "checksums.txt"
	// BinaryBase is the executable name inside every archive.
	BinaryBase = "firefly"
)

// supportedPlatforms is the release build matrix. Asking for anything else gets
// a "no asset for this platform" error rather than a confusing 404 from GitHub.
var supportedPlatforms = map[string][]string{
	"linux":   {"amd64", "arm64"},
	"darwin":  {"amd64", "arm64"},
	"windows": {"amd64", "arm64"},
}

// AssetName returns the archive filename holding the Firefly executable for a
// platform, plus the container format to unpack it with. Windows ships a zip;
// every other platform ships a tar.gz.
func AssetName(tag, goos, goarch string) (string, binx.Archive, error) {
	arches, ok := supportedPlatforms[goos]
	if !ok {
		return "", binx.ArchiveNone, unsupportedPlatformError(goos, goarch)
	}
	supported := false
	for _, a := range arches {
		if a == goarch {
			supported = true
			break
		}
	}
	if !supported {
		return "", binx.ArchiveNone, unsupportedPlatformError(goos, goarch)
	}

	name := fmt.Sprintf("%s%s_%s_%s", AssetPrefix, tag, goos, goarch)
	if goos == "windows" {
		return name + ".zip", binx.ArchiveZip, nil
	}
	return name + ".tar.gz", binx.ArchiveTarGz, nil
}

func unsupportedPlatformError(goos, goarch string) error {
	return fmt.Errorf("no release asset is published for %s/%s (supported: %s)",
		goos, goarch, strings.Join(supportedPlatformList(), ", "))
}

// supportedPlatformList renders the build matrix as "goos/goarch" strings, most
// specific first, for inclusion in an operator-facing error.
func supportedPlatformList() []string {
	out := make([]string, 0, len(supportedPlatforms))
	for goos, arches := range supportedPlatforms {
		for _, goarch := range arches {
			out = append(out, goos+"/"+goarch)
		}
	}
	sort.Strings(out)
	return out
}

// ExecutableName is the platform's Firefly executable filename ("firefly" or
// "firefly.exe"), which is also the archive entry that must be extracted.
func ExecutableName(goos string) string {
	if goos == "windows" {
		return BinaryBase + ".exe"
	}
	return BinaryBase
}

// AssetMatchesPlatform reports whether a release asset name belongs to the
// given platform. The dashboard uses it to show the operator which artifact
// will be downloaded before they approve anything.
func AssetMatchesPlatform(name, goos, goarch string) bool {
	if !strings.HasPrefix(name, AssetPrefix) {
		return false
	}
	trimmed := strings.TrimSuffix(strings.TrimSuffix(name, ".tar.gz"), ".zip")
	return strings.HasSuffix(trimmed, "_"+goos+"_"+goarch)
}

// BackupName is the side path a Windows update moves the previous executable to
// before publishing the new one. Windows refuses to delete or overwrite a
// running image, so the running file must be renamed out of the way first; the
// name is stable so the next boot can clean it up.
func BackupName(executable string) string {
	return executable + ".old"
}

// StagingName is where a downloaded archive is unpacked before it is published
// over the running executable. It lives beside the executable so the final
// rename stays within one filesystem.
func StagingName(executable string) string {
	return filepath.Join(filepath.Dir(executable), "."+filepath.Base(executable)+".update")
}
