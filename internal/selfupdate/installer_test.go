package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// testBinary returns a payload that starts with a magic number the test host's
// platform recognizes, so Install's executable sanity check passes.
func testBinary() []byte {
	var magic []byte
	switch runtime.GOOS {
	case "linux":
		magic = []byte{0x7F, 'E', 'L', 'F'}
	case "windows":
		magic = []byte{'M', 'Z'}
	case "darwin":
		magic = []byte{0xCF, 0xFA, 0xED, 0xFE}
	default:
		magic = []byte("test")
	}
	return append(magic, []byte("firefly-self-update-test-payload")...)
}

// buildArchive packages payload the way the release pipeline does: a zip on
// Windows, a tar.gz elsewhere, holding one entry named for the platform binary.
func buildArchive(t *testing.T, goos string, payload []byte) []byte {
	t.Helper()
	entry := ExecutableName(goos)

	if goos == "windows" {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.Create(entry)
		if err != nil {
			t.Fatalf("create zip entry: %v", err)
		}
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("write zip entry: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("close zip: %v", err)
		}
		return buf.Bytes()
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: entry, Mode: 0755, Size: int64(len(payload))}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("write tar entry: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fixture is a fake release server plus the paths an install writes to. It
// serves exactly the asset this host would download and a checksums.txt that
// matches it, so a test only overrides the field it cares about.
type fixture struct {
	*httptest.Server

	target   string
	original []byte
	payload  []byte
	release  *Release
	env      Environment

	mu           sync.Mutex
	archive      []byte
	checksumBody string
	assetCalls   int
}

func newFixture(t *testing.T, payload []byte) *fixture {
	t.Helper()

	assetName, _, err := AssetName("v9.9.9", runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("no release asset for the test host: %v", err)
	}
	archive := buildArchive(t, runtime.GOOS, payload)

	f := &fixture{
		payload:      payload,
		archive:      archive,
		checksumBody: digestOf(archive) + "  " + assetName + "\n",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		body := f.archive
		f.assetCalls++
		f.mu.Unlock()
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/checksums", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		body := f.checksumBody
		f.mu.Unlock()
		_, _ = io.WriteString(w, body)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)

	dir := t.TempDir()
	f.target = filepath.Join(dir, ExecutableName(runtime.GOOS))
	f.original = []byte("the-previously-installed-binary")
	if err := os.WriteFile(f.target, f.original, 0755); err != nil {
		t.Fatalf("seed the existing binary: %v", err)
	}
	f.env = Environment{Executable: f.target, Dir: dir, Writable: true}

	f.release = &Release{
		Tag: "v9.9.9",
		Assets: []Asset{
			{Name: assetName, URL: f.URL + "/asset", Size: int64(len(archive))},
			{Name: ChecksumAsset, URL: f.URL + "/checksums"},
		},
	}
	return f
}

func (f *fixture) assetName() string { return f.release.Assets[0].Name }

// dropChecksums removes checksums.txt from the release, as an unsigned release
// would be.
func (f *fixture) dropChecksums() {
	kept := f.release.Assets[:0]
	for _, a := range f.release.Assets {
		if a.Name != ChecksumAsset {
			kept = append(kept, a)
		}
	}
	f.release.Assets = kept
}

func (f *fixture) installer() *Installer {
	return NewInstaller(NewClient(WithAPIBase(f.URL), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))), nil)
}

func (f *fixture) install(t *testing.T, current string) (InstallResult, error) {
	t.Helper()
	return f.installer().Install(context.Background(), InstallRequest{
		Release: f.release,
		Env:     f.env,
		Current: current,
	})
}

func (f *fixture) readTarget(t *testing.T) []byte {
	t.Helper()
	got, err := os.ReadFile(f.target)
	if err != nil {
		t.Fatalf("read the installed binary: %v", err)
	}
	return got
}

func TestInstallReplacesExecutable(t *testing.T) {
	f := newFixture(t, testBinary())

	var stages []Stage
	result, err := f.installer().Install(context.Background(), InstallRequest{
		Release: f.release,
		Env:     f.env,
		Current: "v1.0.0",
		Report:  func(p Progress) { stages = append(stages, p.Stage) },
	})
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}

	if result.Installed != "v9.9.9" || result.Previous != "v1.0.0" {
		t.Fatalf("result = %+v, want v9.9.9 installed over v1.0.0", result)
	}
	if result.Path != f.target {
		t.Fatalf("result.Path = %q, want %q", result.Path, f.target)
	}
	if got := f.readTarget(t); !bytes.Equal(got, f.payload) {
		t.Fatalf("installed payload mismatch: got %d bytes, want %d", len(got), len(f.payload))
	}

	// Staging must not survive the update that consumed it.
	if _, err := os.Stat(StagingName(f.target)); err == nil {
		t.Fatalf("%s survived a successful update", StagingName(f.target))
	}
	if len(stages) == 0 {
		t.Fatal("no progress was reported")
	}
	for _, want := range []Stage{StageResolving, StageDownloading, StageVerifying, StageInstalling} {
		found := false
		for _, s := range stages {
			if s == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("progress never reported stage %q (got %v)", want, stages)
		}
	}
}

func TestInstallRejectsChecksumMismatch(t *testing.T) {
	f := newFixture(t, testBinary())
	f.checksumBody = digestOf([]byte("a different archive")) + "  " + f.assetName() + "\n"

	_, err := f.install(t, "v1.0.0")
	if err == nil {
		t.Fatal("expected an error for a checksum mismatch")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want a checksum mismatch", err)
	}
	if got := f.readTarget(t); !bytes.Equal(got, f.original) {
		t.Fatal("the executable was replaced despite a failed verification")
	}
}

func TestInstallRefusesReleaseWithoutChecksums(t *testing.T) {
	f := newFixture(t, testBinary())
	f.dropChecksums()

	_, err := f.install(t, "v1.0.0")
	if err == nil {
		t.Fatal("expected an error when the release publishes no checksums.txt")
	}
	if !strings.Contains(err.Error(), "cannot be verified") {
		t.Fatalf("error = %v, want an unverifiable-release error", err)
	}
	if got := f.readTarget(t); !bytes.Equal(got, f.original) {
		t.Fatal("the executable was replaced without verification")
	}
}

func TestInstallRefusesChecksumsMissingTheAsset(t *testing.T) {
	f := newFixture(t, testBinary())
	f.checksumBody = digestOf(f.archive) + "  firefly_v9.9.9_plan9_amd64.tar.gz\n"

	_, err := f.install(t, "v1.0.0")
	if err == nil {
		t.Fatal("expected an error when checksums.txt does not list the asset")
	}
	if !strings.Contains(err.Error(), "does not list") {
		t.Fatalf("error = %v, want a missing-digest error", err)
	}
}

func TestInstallRefusesNonNewerVersion(t *testing.T) {
	f := newFixture(t, testBinary())

	for _, current := range []string{"v9.9.9", "v10.0.0"} {
		if _, err := f.install(t, current); err == nil {
			t.Fatalf("expected an error for current version %s", current)
		}
	}
	if calls := f.assetCalls; calls != 0 {
		t.Fatalf("the asset was downloaded %d times for a refused update", calls)
	}
}

func TestInstallRefusesUnavailableEnvironment(t *testing.T) {
	f := newFixture(t, testBinary())
	f.env.Reason = "the directory holding the executable is not writable"

	_, err := f.install(t, "v1.0.0")
	if err == nil {
		t.Fatal("expected an error for an unavailable environment")
	}
	if !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("error = %v, want the environment reason in the message", err)
	}
}

func TestInstallRejectsForeignAssetHost(t *testing.T) {
	f := newFixture(t, testBinary())

	// A client left at the default API base keeps the GitHub asset-host
	// allowlist, which the fixture's localhost URL must fail.
	inst := NewInstaller(NewClient(WithHTTPClient(&http.Client{})), nil)

	_, err := inst.Install(context.Background(), InstallRequest{
		Release: f.release,
		Env:     f.env,
		Current: "v1.0.0",
	})
	if err == nil {
		t.Fatal("expected an error for an asset outside the allowed hosts")
	}
	if !strings.Contains(err.Error(), "unexpected location") {
		t.Fatalf("error = %v, want a rejected-host error", err)
	}
}

func TestInstallRejectsNonExecutablePayload(t *testing.T) {
	// A digest-correct archive whose entry is not an executable must still be
	// refused: extracting the wrong entry is the failure this catches.
	f := newFixture(t, []byte("definitely not an executable"))

	_, err := f.install(t, "v1.0.0")
	if err == nil {
		t.Fatal("expected an error for a payload that is not an executable")
	}
	if !strings.Contains(err.Error(), "not a") {
		t.Fatalf("error = %v, want a not-an-executable error", err)
	}
	if got := f.readTarget(t); !bytes.Equal(got, f.original) {
		t.Fatal("a non-executable payload replaced the running binary")
	}
}
