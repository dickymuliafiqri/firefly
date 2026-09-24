package warp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/tun/netstack"
)

const (
	testPublicIP = "203.0.113.7"
	testColo     = "TEST"
)

// registrationBody mimics the payload api.cloudflareclient.com returns for a new
// device. The peer points at loopback so no test can reach the real Internet.
const registrationBody = `{
	"id": "dev_test_123",
	"type": "Android",
	"name": "Firefly Gateway",
	"key": "test_key",
	"created_at": "2026-01-02T03:04:05.678Z",
	"account": {"id": "acc_1", "account_type": "free", "warp": true},
	"config": {
		"client_id": "AAEC",
		"peers": [
			{
				"public_key": "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
				"endpoint": {"v4": "127.0.0.1:2408"}
			}
		],
		"interface": {
			"addresses": {"v4": "172.16.0.2/32", "v6": "2606:4700::1/128"}
		}
	},
	"token": "tok_test"
}`

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// harness wires a Manager to a local registration endpoint and a stubbed edge
// probe, so lifecycle behaviour is observable without Cloudflare.
type harness struct {
	mgr      *Manager
	server   *httptest.Server
	regCalls atomic.Int64
	licCalls atomic.Int64
	licCode  atomic.Int64
	licAuth  atomic.Value // string
	delay    atomic.Int64 // nanoseconds, simulates a slow Cloudflare round trip
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWithProbe(t, func(*netstack.Net) (string, string) {
		return testPublicIP, testColo
	})
}

func newHarnessWithProbe(t *testing.T, probe func(*netstack.Net) (string, string)) *harness {
	t.Helper()

	h := &harness{}
	h.licCode.Store(int64(http.StatusOK))
	h.licAuth.Store("")

	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reg"):
			h.regCalls.Add(1)
			if d := h.delay.Load(); d > 0 {
				time.Sleep(time.Duration(d))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, registrationBody)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/account"):
			h.licCalls.Add(1)
			h.licAuth.Store(r.Header.Get("Authorization"))
			w.WriteHeader(int(h.licCode.Load()))
			_, _ = io.WriteString(w, `{"result":{"account_type":"plus"}}`)
		default:
			http.Error(w, "unexpected request "+r.URL.Path, http.StatusNotFound)
		}
	}))

	mgr := NewManager(quietLogger(), "")
	mgr.httpClient = h.server.Client()
	mgr.registrationURL = h.server.URL + "/reg"
	mgr.edgeProbe = probe
	mgr.sessionGrace = 2 * time.Second
	mgr.autoRotateInterval = 0

	h.mgr = mgr
	t.Cleanup(mgr.Close)
	t.Cleanup(h.server.Close)

	return h
}

// slowRegistration makes the next registration slow enough for concurrent
// callers to pile up on the singleflight key.
func (h *harness) slowRegistration(d time.Duration) {
	h.delay.Store(int64(d))
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s: %s", timeout, msg)
}

func registrationResponse(t *testing.T) *RegistrationResponse {
	t.Helper()
	var reg RegistrationResponse
	if err := json.Unmarshal([]byte(registrationBody), &reg); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	return &reg
}

// A rotation storm is the failure the previous implementation had: ten callers
// that want a new IP must produce one registration, not ten tunnels.
func TestManager_RotateCoalescesConcurrentCallers(t *testing.T) {
	h := newHarness(t)
	h.slowRegistration(150 * time.Millisecond)

	const callers = 10
	sessions := make([]*Session, callers)
	errs := make([]error, callers)

	var wg sync.WaitGroup
	for i := range sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sessions[i], errs[i] = h.mgr.Rotate(ctx)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: Rotate failed: %v", i, err)
		}
	}
	if got := h.regCalls.Load(); got != 1 {
		t.Errorf("expected 1 Cloudflare registration for %d concurrent rotations, got %d", callers, got)
	}
	for i := 1; i < len(sessions); i++ {
		if sessions[i] != sessions[0] {
			t.Fatalf("callers %d and 0 received different sessions; singleflight did not coalesce", i)
		}
	}

	st := h.mgr.Status()
	if !st.Enabled || st.PublicIP != testPublicIP {
		t.Errorf("expected active session with public ip %s, got %+v", testPublicIP, st)
	}
}

// The cold-start path had the same herd problem and bypassed singleflight
// entirely: N first requests built N tunnels and each Store() orphaned the last.
func TestManager_ColdStartRegistersOnce(t *testing.T) {
	h := newHarness(t)
	h.slowRegistration(150 * time.Millisecond)

	const callers = 20
	sessions := make([]*Session, callers)

	var wg sync.WaitGroup
	for i := range sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, err := h.mgr.ensureSession(ctx)
			if err != nil {
				t.Errorf("caller %d: ensureSession failed: %v", i, err)
				return
			}
			sessions[i] = s
		}(i)
	}
	wg.Wait()

	if got := h.regCalls.Load(); got != 1 {
		t.Errorf("expected 1 registration for %d concurrent cold starts, got %d", callers, got)
	}
	for i := 1; i < len(sessions); i++ {
		if sessions[i] != sessions[0] {
			t.Fatalf("cold start produced more than one session (caller %d vs 0)", i)
		}
	}
	if drained := h.mgr.Status().DrainingSessions; drained != 0 {
		t.Errorf("expected no draining sessions after a single cold start, got %d", drained)
	}
}

// The original cold-start bug: with a saved identity, every concurrent first
// request rebuilt the tunnel outside singleflight and Store() orphaned the
// previous one, leaking a WireGuard device and UDP socket per request.
func TestManager_ColdStartWithSavedIdentityBuildsOneTunnel(t *testing.T) {
	h := newHarness(t)

	h.mgr.identityPath = filepath.Join(t.TempDir(), "warp_identity.json")
	keys, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	var probes atomic.Int64
	h.mgr.edgeProbe = func(*netstack.Net) (string, string) {
		probes.Add(1)
		return testPublicIP, testColo
	}
	h.mgr.saveIdentity(keys, registrationResponse(t))

	const callers = 20
	sessions := make([]*Session, callers)

	var wg sync.WaitGroup
	for i := range sessions {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := h.mgr.ensureSession(t.Context())
			if err != nil {
				t.Errorf("caller %d: ensureSession failed: %v", i, err)
				return
			}
			sessions[i] = s
		}(i)
	}
	wg.Wait()

	if got := probes.Load(); got != 1 {
		t.Errorf("expected 1 tunnel build for %d concurrent cold starts, got %d", callers, got)
	}
	if got := h.regCalls.Load(); got != 0 {
		t.Errorf("a usable cached identity must not register a new device, got %d registrations", got)
	}
	for i := 1; i < len(sessions); i++ {
		if sessions[i] != sessions[0] {
			t.Fatalf("caller %d got a different session than caller 0", i)
		}
	}
}

// A caller that walks away must not kill the shared flight: the tunnel it was
// waiting for is what every other request needs.
func TestManager_CancelledCallerLeavesFlightRunning(t *testing.T) {
	h := newHarness(t)
	h.slowRegistration(200 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.mgr.Rotate(ctx); err == nil {
		t.Fatal("expected a cancelled caller to get an error")
	}

	waitFor(t, 5*time.Second, func() bool {
		return h.mgr.Status().Enabled && h.regCalls.Load() == 1
	}, "the shared rotation should finish after the caller gave up")
}

func TestManager_AutoRotationPeriodicallyRotates(t *testing.T) {
	h := newHarness(t)
	// Fast interval for the test
	h.mgr.SetAutoRotateInterval(50 * time.Millisecond)
	h.mgr.StartAutoRotation()

	waitFor(t, 2*time.Second, func() bool {
		return h.regCalls.Load() >= 2
	}, "auto-rotation failed to trigger periodic device registrations")

	if got := h.mgr.autoRotateInterval; got <= 0 {
		t.Errorf("expected positive autoRotateInterval, got %v", got)
	}
	st := h.mgr.Status()
	if st.NextRotationAt.IsZero() {
		t.Errorf("expected valid NextRotationAt")
	}
}

// A superseded tunnel must survive for the streams it already carries, and must
// go away as soon as they finish instead of after a fixed sleep.
func TestManager_DrainWaitsForOpenConnections(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()
	mgr.sessionGrace = 5 * time.Second

	old := &Session{}
	if !old.acquireConn() {
		t.Fatal("fresh session refused a connection")
	}

	mgr.drainSession(old)
	time.Sleep(400 * time.Millisecond)
	if old.closed.Load() {
		t.Fatal("drain closed a session that still had an open connection")
	}
	if got := mgr.Status().DrainingSessions; got != 1 {
		t.Errorf("expected 1 draining session, got %d", got)
	}

	old.releaseConn()
	waitFor(t, 5*time.Second, func() bool { return old.closed.Load() },
		"session was never closed after its last connection was released")
	waitFor(t, 5*time.Second, func() bool { return mgr.Status().DrainingSessions == 0 },
		"draining gauge never returned to zero")
}

// Shutdown must not have to wait out the grace period.
func TestManager_CloseReapsDrainingSessions(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	mgr.sessionGrace = time.Hour

	old := &Session{}
	old.acquireConn()
	mgr.drainSession(old)

	start := time.Now()
	mgr.Close()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Close blocked for %s waiting on a drain", elapsed)
	}
	if !old.closed.Load() {
		t.Error("Close left a draining session open")
	}
	if got := mgr.Status().DrainingSessions; got != 0 {
		t.Errorf("expected 0 draining sessions after Close, got %d", got)
	}

	if _, err := mgr.DialContext(context.Background(), "tcp", "example.com:443"); err == nil {
		t.Error("expected dial on a closed manager to fail")
	}
	mgr.RotateAsync("late")
	if !old.closed.Load() {
		t.Error("rotation after Close must not resurrect sessions")
	}
}

// The WARP-assigned 172.16/12 address is not a public IP; reporting it as one
// made the dashboard claim rotation had happened when it had not.
func TestManager_StatusKeepsPublicAndInternalIPApart(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	mgr.current.Store(&Session{InternalIPv4: "172.16.0.2"})

	st := mgr.Status()
	if st.PublicIP != "" {
		t.Errorf("expected no public ip before a successful probe, got %q", st.PublicIP)
	}
	if st.InternalIPv4 != "172.16.0.2" {
		t.Errorf("expected internal ip to be reported, got %q", st.InternalIPv4)
	}
}

// A cached peer may already be gone server-side. An identity that cannot produce
// a trace answer must be discarded rather than handed to traffic.
func TestManager_RestoreDiscardsUnreachableIdentity(t *testing.T) {
	h := newHarness(t)

	dir := t.TempDir()
	h.mgr.identityPath = filepath.Join(dir, "warp_identity.json")
	keys, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	h.mgr.saveIdentity(keys, registrationResponse(t))

	var probes atomic.Int64
	h.mgr.edgeProbe = func(*netstack.Net) (string, string) {
		if probes.Add(1) == 1 {
			return "", "" // the restored tunnel is dead
		}
		return testPublicIP, testColo
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := h.mgr.ensureSession(ctx)
	if err != nil {
		t.Fatalf("ensureSession: %v", err)
	}
	if session.PublicIP != testPublicIP {
		t.Errorf("expected a freshly registered session, got public ip %q", session.PublicIP)
	}
	if got := h.regCalls.Load(); got != 1 {
		t.Errorf("expected 1 registration after the cached identity failed, got %d", got)
	}
	if got := probes.Load(); got != 2 {
		t.Errorf("expected the cached identity to be probed once before being dropped, got %d probes", got)
	}
}

func TestManager_SaveIdentityRoundTrips(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	path := filepath.Join(t.TempDir(), "nested", "warp_identity.json")
	mgr.identityPath = path

	keys, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	reg := registrationResponse(t)
	mgr.saveIdentity(keys, reg)

	loaded, err := mgr.loadSavedIdentity()
	if err != nil {
		t.Fatalf("loadSavedIdentity: %v", err)
	}
	if loaded.Keys.PrivateKeyHex != keys.PrivateKeyHex {
		t.Error("private key did not survive the round trip")
	}
	if loaded.Registration.Config.ClientID != reg.Config.ClientID {
		t.Error("client id did not survive the round trip")
	}

	// The write must be atomic: no staged temp file may outlive it.
	strays, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".warp_identity_*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(strays) != 0 {
		t.Errorf("leftover identity temp files: %v", strays)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("identity file missing: %v", err)
	}
}

func TestRegisterDevice_AppliesLicense(t *testing.T) {
	h := newHarness(t)

	keys, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	reg, err := RegisterDevice(t.Context(), h.server.Client(), keys, "lic_test", h.server.URL+"/reg")
	if err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	if !reg.LicenseApplied {
		t.Error("expected the license to be reported as applied")
	}
	if reg.LicenseError != "" {
		t.Errorf("unexpected license error: %s", reg.LicenseError)
	}
	if h.licCalls.Load() != 1 {
		t.Errorf("expected 1 license request, got %d", h.licCalls.Load())
	}
	if got, _ := h.licAuth.Load().(string); !strings.HasPrefix(got, "Bearer ") {
		t.Errorf("license request lost its bearer token: %q", got)
	}
	if reg.Config.Interface.Addresses.V4 != "172.16.0.2/32" {
		t.Errorf("unexpected interface address: %q", reg.Config.Interface.Addresses.V4)
	}
}

// A rejected WARP+ license used to be discarded with the error, so a gateway that
// silently stayed on the free plan looked healthy.
func TestRegisterDevice_ReportsLicenseFailure(t *testing.T) {
	h := newHarness(t)
	h.licCode.Store(int64(http.StatusBadRequest))

	keys, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	reg, err := RegisterDevice(t.Context(), h.server.Client(), keys, "lic_test", h.server.URL+"/reg")
	if err != nil {
		t.Fatalf("a bad license must not fail registration: %v", err)
	}
	if reg.LicenseApplied {
		t.Error("license reported as applied after a 400")
	}
	if !strings.Contains(reg.LicenseError, "400") {
		t.Errorf("expected the status code in the license error, got %q", reg.LicenseError)
	}
	if strings.Contains(reg.LicenseError, "lic_test") || strings.Contains(reg.LicenseError, "tok_test") {
		t.Errorf("license error leaked a credential: %q", reg.LicenseError)
	}
}

func TestRegisterDevice_RejectsUnhealthyRegistration(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, strings.Repeat("rate limited ", 100))
	}))
	defer ts.Close()

	keys, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	_, err = RegisterDevice(t.Context(), ts.Client(), keys, "", ts.URL+"/reg")
	if err == nil {
		t.Fatal("expected a 429 registration to fail")
	}
	if len(err.Error()) > 400 {
		t.Errorf("error body was not bounded: %d bytes", len(err.Error()))
	}
}

// The status payload is consumed by the dashboard, which renders
// "Initial session" only when the rotation timestamp is absent.
func TestManager_StatusOmitsZeroRotationTime(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	b, err := json.Marshal(mgr.Status())
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if strings.Contains(string(b), "last_rotated_at") {
		t.Errorf("expected no rotation timestamp before the first session, got %s", b)
	}
	if !strings.Contains(string(b), `"active_connections":0`) {
		t.Errorf("expected an explicit zero connection count, got %s", b)
	}
}

// A cold-start restore that finishes after a real rotation must not displace the
// newer tunnel, and must not have its identity cached.
func TestManager_InstallKeepsNewerSession(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	defer mgr.Close()

	newer := &Session{PublicIP: testPublicIP, CreatedAt: time.Now()}
	mgr.current.Store(newer)

	stale := &Session{PublicIP: "198.51.100.9", CreatedAt: time.Now().Add(-time.Minute)}
	if got := mgr.installSession(stale); got != newer {
		t.Errorf("expected the newer session to stay active, got %+v", got)
	}
	if !stale.closed.Load() {
		t.Error("the discarded session must be closed, not orphaned")
	}
	if got := mgr.Status().PublicIP; got != testPublicIP {
		t.Errorf("active public ip changed to %q", got)
	}

	// Equal timestamps must not displace anything: the new build wins by CAS.
	fresh := &Session{PublicIP: "198.51.100.10", CreatedAt: time.Now()}
	if got := mgr.installSession(fresh); got != fresh {
		t.Errorf("expected a genuinely newer session to be installed, got %+v", got)
	}
	waitFor(t, 5*time.Second, func() bool { return newer.closed.Load() },
		"the superseded session was never drained")
}

// Publish and shutdown are serialized. A publisher must therefore either finish
// before Close starts waiting or be refused outright: a drain registered while
// Close is waiting for one is WaitGroup misuse, and it panics the process
// instead of shutting down. The window is a few instructions wide, so this hammers
// the interleaving and asserts the shutdown outcome that follow from the lock.
func TestManager_ConcurrentPublishAndClose(t *testing.T) {
	const publishers = 8

	for iter := 0; iter < 500; iter++ {
		mgr := NewManager(quietLogger(), "")
		mgr.sessionGrace = time.Millisecond
		mgr.current.Store(&Session{CreatedAt: time.Now().Add(-time.Second)})

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(publishers + 1)
		for p := 0; p < publishers; p++ {
			go func() {
				defer wg.Done()
				<-start
				mgr.installSession(&Session{CreatedAt: time.Now()})
			}()
		}
		go func() {
			defer wg.Done()
			<-start
			mgr.Close()
		}()
		close(start)
		wg.Wait()

		if left := mgr.draining.Load(); left != 0 {
			t.Fatalf("iteration %d: Close returned with %d drains still registered", iter, left)
		}
		if s := mgr.current.Load(); s != nil {
			t.Fatalf("iteration %d: a publish resurrected the active session after shutdown", iter)
		}
	}
}

// A publish that loses to shutdown must report it instead of returning a closed
// session that the caller would log as a successful rotation.
func TestManager_InstallAfterCloseReturnsNil(t *testing.T) {
	mgr := NewManager(quietLogger(), "")
	mgr.current.Store(&Session{CreatedAt: time.Now()})
	mgr.Close()

	s := &Session{}
	if got := mgr.installSession(s); got != nil {
		t.Fatalf("expected nil once the manager is closed, got %+v", got)
	}
	if !s.closed.Load() {
		t.Error("an unpublished session must be closed")
	}
}

// A dial that races shutdown must fail fast: it used to load the cached
// identity, build a WireGuard device and probe it before giving up.
func TestManager_EnsureSessionFailsFastWhenClosed(t *testing.T) {
	h := newHarness(t)
	h.mgr.Close()

	if _, err := h.mgr.ensureSession(context.Background()); !errors.Is(err, errClosed) {
		t.Fatalf("expected errClosed, got %v", err)
	}
	if got := h.regCalls.Load(); got != 0 {
		t.Errorf("a closed manager must not register a device, got %d registrations", got)
	}
}

func TestSession_ConnectionRefcount(t *testing.T) {
	t.Parallel()

	s := &Session{}
	if !s.acquireConn() {
		t.Fatal("expected a live session to accept a connection")
	}
	if got := s.ActiveConnections(); got != 1 {
		t.Fatalf("expected 1 active connection, got %d", got)
	}

	s.releaseConn()
	if got := s.ActiveConnections(); got != 0 {
		t.Fatalf("expected 0 active connections, got %d", got)
	}

	s.Close()
	if s.acquireConn() {
		t.Error("a closed session must refuse new connections")
	}
	if got := s.ActiveConnections(); got != 0 {
		t.Errorf("a refused acquire must not leave a reference behind, got %d", got)
	}
}

// The upstream whose 429 triggered a rotation must learn about it exactly once,
// even when the burst delivered several 429s.
func TestManager_AutoRotationObserverFiresOnSuccess(t *testing.T) {
	h := newHarness(t)

	var mu sync.Mutex
	var names []string
	h.mgr.SetAutoRotationObserver(func(name string) {
		mu.Lock()
		names = append(names, name)
		mu.Unlock()
	})

	for range 4 {
		h.mgr.RotateAsync("opencode-free")
	}

	waitFor(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(names) == 1
	}, "auto-rotation observer never fired for a successful rotation")

	// Give any surplus goroutine a chance to race past the throttle.
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(names) != 1 || names[0] != "opencode-free" {
		t.Fatalf("observer calls = %v, want exactly [opencode-free]", names)
	}
}

// A rotation that failed changed no IP, so no cooldown may be released.
func TestManager_AutoRotationObserverSkipsFailedRotation(t *testing.T) {
	h := newHarness(t)

	var fired atomic.Int64
	h.mgr.SetAutoRotationObserver(func(string) { fired.Add(1) })

	h.server.Close() // registration is now unreachable
	h.mgr.RotateAsync("opencode-free")

	time.Sleep(500 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Fatalf("observer fired %d times for a failed rotation, want 0", got)
	}
}
