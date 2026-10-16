package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/selfupdate"
)

// releaseFixture is a stand-in GitHub Releases endpoint: it serves the repo's
// latest-release JSON, the platform archive, and checksums.txt, so the manager
// can be exercised end to end without touching the network.
type releaseFixture struct {
	t       *testing.T
	server  *httptest.Server
	tag     string
	payload []byte
	archive string
	asset   []byte
	sums    string
	// omitChecksums makes the release publish no checksums.txt, so an install
	// must be refused rather than performed unverified.
	omitChecksums bool
	// omitAsset makes the release publish no archive for this platform.
	omitAsset bool
	apiHits   int
}

func newReleaseFixture(t *testing.T, tag string) *releaseFixture {
	t.Helper()
	f := &releaseFixture{t: t, tag: tag, payload: testPayload()}
	f.archive = mustAssetName(t, tag)
	f.asset = buildArchive(t, f.archive, selfupdate.ExecutableName(runtime.GOOS), f.payload)
	sum := sha256.Sum256(f.asset)
	f.sums = fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), f.archive)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+selfupdate.DefaultRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		f.apiHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.releaseJSON())
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(f.asset)
	})
	mux.HandleFunc("/checksums", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(f.sums))
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *releaseFixture) releaseJSON() map[string]any {
	assets := []map[string]any{}
	if !f.omitAsset {
		assets = append(assets, map[string]any{
			"name":                 f.archive,
			"size":                 len(f.asset),
			"browser_download_url": f.server.URL + "/asset",
		})
	}
	if !f.omitChecksums {
		assets = append(assets, map[string]any{
			"name":                 selfupdate.ChecksumAsset,
			"size":                 len(f.sums),
			"browser_download_url": f.server.URL + "/checksums",
		})
	}
	return map[string]any{
		"tag_name":     f.tag,
		"name":         "Release " + f.tag,
		"body":         "What changed in " + f.tag,
		"html_url":     "https://github.com/" + selfupdate.DefaultRepo + "/releases/tag/" + f.tag,
		"prerelease":   false,
		"draft":        false,
		"published_at": time.Now().UTC().Format(time.RFC3339),
		"assets":       assets,
	}
}

func (f *releaseFixture) client() *selfupdate.Client {
	return selfupdate.NewClient(selfupdate.WithAPIBase(f.server.URL))
}

// clientWithTTL builds a client whose runtime cache is short enough that a test
// can watch it expire instead of sleeping out the production fifteen minutes.
func (f *releaseFixture) clientWithTTL(d time.Duration) *selfupdate.Client {
	return selfupdate.NewClient(selfupdate.WithAPIBase(f.server.URL), selfupdate.WithTTL(d))
}

// targetDir creates the directory that stands in for the install location and
// the (fake) executable already living there.
func (f *releaseFixture) targetDir() (dir, exe string) {
	f.t.Helper()
	dir = f.t.TempDir()
	exe = filepath.Join(dir, selfupdate.ExecutableName(runtime.GOOS))
	if err := os.WriteFile(exe, testPayload(), 0o755); err != nil {
		f.t.Fatalf("seed executable: %v", err)
	}
	return dir, exe
}

func mustAssetName(t *testing.T, tag string) string {
	t.Helper()
	name, _, err := selfupdate.AssetName(tag, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skipf("no release asset for this platform: %v", err)
	}
	return name
}

// buildArchive packs payload under entry using the archive format the release
// workflow publishes for goos.
func buildArchive(t *testing.T, name, entry string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if strings.HasSuffix(name, ".zip") {
		zw := zip.NewWriter(&buf)
		w, err := zw.Create(entry)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write(payload); err != nil {
			t.Fatalf("zip write: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("zip close: %v", err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: entry, Mode: 0o755, Size: int64(len(payload))}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// testPayload is a stand-in executable: the installer checks magic bytes only,
// because the digest has already proven authenticity.
func testPayload() []byte {
	switch runtime.GOOS {
	case "windows":
		return append([]byte{'M', 'Z'}, bytes.Repeat([]byte{0x90}, 64)...)
	case "darwin":
		return append([]byte{0xCF, 0xFA, 0xED, 0xFE}, bytes.Repeat([]byte{0x90}, 64)...)
	default:
		return append([]byte{0x7F, 'E', 'L', 'F'}, bytes.Repeat([]byte{0x90}, 64)...)
	}
}

// newTestManager wires a manager against the fixture, with an install location
// that is a temp directory rather than the real test binary.
func newTestManager(t *testing.T, f *releaseFixture, opts ...func(*UpdateConfig)) (*UpdateManager, UpdateConfig) {
	t.Helper()
	dir, exe := f.targetDir()
	cfg := UpdateConfig{
		CurrentVersion: "v1.0.0",
		CurrentCommit:  "deadbeef",
		BuildDate:      "2026-01-01T00:00:00Z",
		Client:         f.client(),
		RestartDelay:   50 * time.Millisecond,
		Environment: &selfupdate.Environment{
			Executable: exe,
			Dir:        dir,
			Writable:   true,
		},
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return NewUpdateManager(cfg), cfg
}

// waitForInstall polls until the background install settles.
func waitForInstall(t *testing.T, mgr *UpdateManager) *updateInstallDTO {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if dto := mgr.installDTO(); dto != nil && (dto.Done || dto.Error != "") {
			return dto
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the install to finish")
	return nil
}

func TestUpdateManagerStatusReportsAvailableRelease(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	mgr, _ := newTestManager(t, f)

	status := mgr.Status(context.Background())

	if status.CurrentVersion != "v1.0.0" {
		t.Errorf("current_version = %q, want v1.0.0", status.CurrentVersion)
	}
	if !status.UpdateAvailable {
		t.Fatalf("update_available = false, want true (check_error=%q)", status.CheckError)
	}
	if status.Latest == nil {
		t.Fatal("latest release is missing")
	}
	if status.Latest.Tag != "v1.2.0" {
		t.Errorf("latest.tag = %q, want v1.2.0", status.Latest.Tag)
	}
	if status.Latest.Asset != f.archive {
		t.Errorf("latest.asset = %q, want %q", status.Latest.Asset, f.archive)
	}
	if status.Latest.AssetSize != int64(len(f.asset)) {
		t.Errorf("latest.asset_size = %d, want %d", status.Latest.AssetSize, len(f.asset))
	}
	if !strings.Contains(status.Latest.Notes, "What changed") {
		t.Errorf("latest.notes = %q, want the release body", status.Latest.Notes)
	}
	if !status.Environment.Available {
		t.Errorf("environment should be available in a writable temp dir: %s", status.Environment.Reason)
	}
	if status.CheckedAt == "" {
		t.Error("checked_at must be stamped even when the check succeeded")
	}
	if status.Install != nil {
		t.Error("install must be absent until an install starts")
	}
}

func TestUpdateManagerStatusHidesAReleaseItAlreadyRuns(t *testing.T) {
	f := newReleaseFixture(t, "v1.0.0")
	mgr, _ := newTestManager(t, f)

	status := mgr.Status(context.Background())

	if status.UpdateAvailable {
		t.Error("a release equal to the running version is not an update")
	}
	if status.Latest != nil {
		t.Errorf("latest = %+v, want nil when nothing is actionable", status.Latest)
	}
}

func TestUpdateManagerStatusDegradesWhenTheLookupFails(t *testing.T) {
	f := newReleaseFixture(t, "v9.9.9")
	mgr, _ := newTestManager(t, f, func(c *UpdateConfig) {
		c.Client = f.clientWithTTL(time.Millisecond)
		c.StatusTTL = time.Millisecond
	})
	// Reach the API, learn the release, then take the API away. The client's
	// stale-cache fallback must keep the panel populated.
	if status := mgr.Status(context.Background()); status.Latest == nil {
		t.Fatalf("first status has no release: %s", status.CheckError)
	}
	f.server.Close()
	time.Sleep(20 * time.Millisecond) // let the short caches expire

	status := mgr.Status(context.Background())

	if status.CheckError == "" {
		t.Error("a failed refresh must be reported in check_error")
	}
	if status.Latest == nil {
		t.Error("the previously fetched release must survive a failed refresh")
	}
	if !status.Stale {
		t.Error("a release that could not be refreshed must be flagged stale")
	}
	if !status.UpdateAvailable {
		t.Error("the stale release is still newer, so the update stays offered")
	}
}

func TestUpdateManagerCachesTheReleaseLookup(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	mgr, _ := newTestManager(t, f)

	mgr.Status(context.Background())
	mgr.Status(context.Background())

	if f.apiHits != 1 {
		t.Errorf("api hits = %d, want 1 (the short cache must absorb a poll burst)", f.apiHits)
	}
}
func TestUpdateManagerApplyInstallsAndRequestsRestart(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	restarts := make(chan string, 1)
	mgr, cfg := newTestManager(t, f, func(c *UpdateConfig) {
		c.Restart = func(reason string) { restarts <- reason }
	})

	if status := mgr.Status(context.Background()); !status.UpdateAvailable {
		t.Fatalf("fixture is not offering an update: %s", status.CheckError)
	}
	if err := mgr.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	dto := waitForInstall(t, mgr)
	if dto.Error != "" {
		t.Fatalf("install failed: %s", dto.Error)
	}
	if dto.Installed != "v1.2.0" || dto.Previous != "v1.0.0" {
		t.Errorf("install result = %s -> %s, want v1.0.0 -> v1.2.0", dto.Previous, dto.Installed)
	}
	if dto.Percent != 100 {
		t.Errorf("percent = %d, want 100 once done", dto.Percent)
	}
	if !dto.Restarting {
		t.Error("restarting should be set once the install succeeds")
	}
	if got := readFile(t, cfg.Environment.Executable); !bytes.Equal(got, testPayload()) {
		t.Errorf("installed executable has %d bytes, want %d", len(got), len(testPayload()))
	}

	select {
	case reason := <-restarts:
		if !strings.Contains(reason, "v1.2.0") {
			t.Errorf("restart reason = %q, want it to name the installed version", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the restart hook was never called")
	}
}

func TestUpdateManagerApplyRejectsAConcurrentInstall(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	mgr, _ := newTestManager(t, f)
	if status := mgr.Status(context.Background()); !status.UpdateAvailable {
		t.Fatalf("fixture is not offering an update: %s", status.CheckError)
	}

	mgr.mu.Lock()
	mgr.running = true
	mgr.mu.Unlock()

	if err := mgr.Apply(context.Background()); err != ErrUpdateInProgress {
		t.Fatalf("Apply during an install = %v, want ErrUpdateInProgress", err)
	}
}

func TestUpdateManagerApplyRejectsAnUnavailableHost(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	mgr, _ := newTestManager(t, f, func(c *UpdateConfig) {
		c.Environment = &selfupdate.Environment{
			Executable: c.Environment.Executable,
			Dir:        c.Environment.Dir,
			Container:  true,
			Reason:     "Firefly is running in a container; update the image instead",
		}
	})
	mgr.release = mustLatest(t, mgr, context.Background())

	err := mgr.Apply(context.Background())
	if err == nil {
		t.Fatal("Apply should refuse a container deployment")
	}
	if !strings.Contains(err.Error(), "container") {
		t.Errorf("error = %q, want it to explain the container refusal", err)
	}
}

func TestUpdateManagerApplyRejectsAnOlderRelease(t *testing.T) {
	f := newReleaseFixture(t, "v0.9.0")
	mgr, _ := newTestManager(t, f)
	mgr.release = mustLatest(t, mgr, context.Background())

	err := mgr.Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "newest") {
		t.Fatalf("Apply of an older release = %v, want a refusal", err)
	}
}

func TestUpdateManagerApplyRequiresAKnownRelease(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	mgr, _ := newTestManager(t, f)

	err := mgr.Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "check for updates first") {
		t.Fatalf("Apply without a release = %v, want a refusal", err)
	}
}

func TestUpdateManagerApplyReportsAVerificationFailure(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	f.omitChecksums = true
	mgr, _ := newTestManager(t, f)
	mgr.release = mustLatest(t, mgr, context.Background())

	if err := mgr.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dto := waitForInstall(t, mgr)
	if dto.Error == "" {
		t.Fatal("an unverifiable release must fail the install")
	}
	if !strings.Contains(dto.Error, selfupdate.ChecksumAsset) {
		t.Errorf("error = %q, want it to name the missing %s", dto.Error, selfupdate.ChecksumAsset)
	}
}

func TestUpdateManagerApplyReportsAMissingAsset(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	f.omitAsset = true
	mgr, _ := newTestManager(t, f)
	mgr.release = mustLatest(t, mgr, context.Background())

	if err := mgr.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if dto := waitForInstall(t, mgr); dto.Error == "" {
		t.Fatal("a release without this platform's archive must fail the install")
	}
}

func TestUpdateManagerApplyLeavesNoStagingFile(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	f.omitChecksums = true
	mgr, cfg := newTestManager(t, f)
	mgr.release = mustLatest(t, mgr, context.Background())

	if err := mgr.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	waitForInstall(t, mgr)

	if _, err := os.Stat(selfupdate.StagingName(cfg.Environment.Executable)); !os.IsNotExist(err) {
		t.Errorf("staging file survived a failed install (stat err = %v)", err)
	}
}

// --- handlers -------------------------------------------------------------

func TestHandleGetUpdateStatusRequiresAnAdminToken(t *testing.T) {
	deps := RouterDeps{AdminToken: "secret"}
	rec := httptest.NewRecorder()
	deps.handleGetUpdateStatus(rec, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleGetUpdateStatusWithoutAManagerFailsClosed(t *testing.T) {
	rec := httptest.NewRecorder()
	RouterDeps{}.handleGetUpdateStatus(rec, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleApplyUpdateWithoutAManagerFailsClosed(t *testing.T) {
	rec := httptest.NewRecorder()
	RouterDeps{}.handleApplyUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/update/apply", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleApplyUpdateAcceptsAndReportsProgress(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	mgr, _ := newTestManager(t, f)
	deps := RouterDeps{SelfUpdate: mgr}
	if status := mgr.Status(context.Background()); !status.UpdateAvailable {
		t.Fatalf("fixture is not offering an update: %s", status.CheckError)
	}

	rec := httptest.NewRecorder()
	deps.handleApplyUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/update/apply", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if accepted, _ := body["accepted"].(bool); !accepted {
		t.Errorf("accepted = %v, want true", body["accepted"])
	}
	// Let the detached install finish before the test's temp directory is
	// removed, or RemoveAll races the download.
	waitForInstall(t, mgr)
}

func TestHandleApplyUpdateRefusesAnUnavailableHost(t *testing.T) {
	f := newReleaseFixture(t, "v1.2.0")
	mgr, _ := newTestManager(t, f, func(c *UpdateConfig) {
		c.Environment = &selfupdate.Environment{Reason: "self-update is disabled by FIREFLY_DISABLE_SELF_UPDATE"}
	})
	mgr.release = mustLatest(t, mgr, context.Background())

	rec := httptest.NewRecorder()
	RouterDeps{SelfUpdate: mgr}.handleApplyUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/update/apply", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "disabled") {
		t.Errorf("body = %s, want it to explain the refusal", rec.Body.String())
	}
}

// mustLatest primes the manager's release cache directly, so a test can drive
// Apply without going through Status first.
func mustLatest(t *testing.T, mgr *UpdateManager, ctx context.Context) *selfupdate.Release {
	t.Helper()
	rel, err := mgr.client.Latest(ctx)
	if err != nil {
		t.Fatalf("prime release: %v", err)
	}
	mgr.mu.Lock()
	mgr.release = rel
	mgr.releaseAt = time.Now()
	mgr.mu.Unlock()
	return rel
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
