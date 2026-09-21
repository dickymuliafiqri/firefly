package warp

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

const (
	// DefaultSessionGrace bounds how long a superseded tunnel stays open for the
	// connections it already carries. Long-lived SSE streams are the reason a
	// session is drained instead of closed the moment it loses its slot.
	DefaultSessionGrace = 60 * time.Second

	// DefaultMinRotateInterval throttles 429-driven background rotations. Without
	// it, a sustained 429 storm performs one Cloudflare registration back-to-back
	// per burst; singleflight only coalesces strictly concurrent calls.
	DefaultMinRotateInterval = 60 * time.Second

	defaultRotateTimeout = 30 * time.Second
	defaultProbeTimeout  = 5 * time.Second

	rotateFlightKey = "warp-rotate"
	ensureFlightKey = "warp-ensure"
)

// errClosed is returned once the manager has been shut down.
var errClosed = errors.New("warp manager is closed")

// Session represents an active WireGuard connection to Cloudflare WARP.
type Session struct {
	Dev  *device.Device
	TNet *netstack.Net

	// PublicIP is the egress address reported by the Cloudflare edge for this
	// tunnel. It is empty until a probe succeeds: the WARP-assigned 172.16/12
	// address is an internal tunnel IP and must never be presented as public.
	PublicIP     string
	InternalIPv4 string

	Colo      string
	Endpoint  string
	LatencyMs int64
	CreatedAt time.Time

	conns  atomic.Int64
	closed atomic.Bool
}

// ActiveConnections reports how many dialed connections still pin this session.
func (s *Session) ActiveConnections() int64 {
	if s == nil {
		return 0
	}
	return s.conns.Load()
}

// acquireConn pins the session for the lifetime of one connection and reports
// whether the session is still usable. Close may still win the race after this
// returns true; the dial then fails, which is why callers must release on error.
func (s *Session) acquireConn() bool {
	if s == nil || s.closed.Load() {
		return false
	}
	s.conns.Add(1)
	if s.closed.Load() {
		s.conns.Add(-1)
		return false
	}
	return true
}

func (s *Session) releaseConn() {
	if s == nil {
		return
	}
	s.conns.Add(-1)
}

// Close gracefully terminates the underlying WireGuard device. It is idempotent
// and safe to call from several draining goroutines.
func (s *Session) Close() {
	if s == nil || s.closed.Swap(true) {
		return
	}
	defer func() {
		_ = recover()
	}()
	if s.Dev != nil {
		_ = s.Dev.Down()
		s.Dev.Close()
	}
}

// SavedIdentity stores WireGuard credentials and registration details across process restarts.
type SavedIdentity struct {
	Keys         *KeyPair              `json:"keys"`
	Registration *RegistrationResponse `json:"registration"`
	SavedAt      time.Time             `json:"saved_at"`
}

// Manager coordinates userspace WireGuard sessions, dynamic IP rotation, and status telemetry.
type Manager struct {
	current      atomic.Pointer[Session]
	sfg          singleflight.Group
	logger       *slog.Logger
	httpClient   *http.Client
	licenseKey   string
	identityPath string

	// registrationURL and edgeProbe are seams for offline tests; production keeps
	// the Cloudflare defaults.
	registrationURL string
	edgeProbe       func(tnet *netstack.Net) (publicIPv4, colo string)

	sessionGrace      time.Duration
	minRotateInterval time.Duration
	rotateTimeout     time.Duration
	probeTimeout      time.Duration

	connTotal atomic.Int64
	draining  atomic.Int64
	drainWG   sync.WaitGroup

	// publishMu serializes session publication with shutdown. A drain registered
	// while Close is already waiting for one is WaitGroup misuse (Add must not
	// race Wait), so installSession publishes under this lock and Close takes it
	// before it starts waiting.
	publishMu sync.Mutex

	// asyncRotations counts background 429-driven rotations. They are bounded by
	// their own timeout and canceled by bgCtx, so shutdown does not wait on them.
	asyncRotations atomic.Int64

	// onRotate lets the caller (main) drop keep-alive sockets that were pooled on
	// the previous egress IP; a new session is useless if every request keeps
	// reusing an old connection.
	onRotate    atomic.Pointer[func()]
	closeOnce   sync.Once
	closed      atomic.Bool
	bgCtx       context.Context
	bgCancel    context.CancelFunc
	drainDone   chan struct{}
	lastRotated atomic.Int64 // Unix nanoseconds, when the active session was established
	lastAttempt atomic.Int64 // Unix nanoseconds, last rotation attempt admitted by the throttle
	lastError   atomic.Pointer[string]
}

// NewManager constructs a new WARP Manager with optional identity persistence.
func NewManager(logger *slog.Logger, licenseKey string, identityPath ...string) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	var idPath string
	if len(identityPath) > 0 {
		idPath = identityPath[0]
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		logger:            logger.With("component", "warp"),
		httpClient:        &http.Client{Timeout: 20 * time.Second},
		licenseKey:        strings.TrimSpace(licenseKey),
		identityPath:      idPath,
		registrationURL:   DefaultRegistrationURL,
		sessionGrace:      DefaultSessionGrace,
		minRotateInterval: DefaultMinRotateInterval,
		rotateTimeout:     defaultRotateTimeout,
		probeTimeout:      defaultProbeTimeout,
		bgCtx:             ctx,
		bgCancel:          cancel,
		drainDone:         make(chan struct{}),
	}
	m.edgeProbe = m.probeEdgeTrace
	return m
}

// SetRotationObserver registers a callback invoked after the active session
// changes. It runs on the rotation goroutine and must not block.
func (m *Manager) SetRotationObserver(fn func()) {
	if fn == nil {
		m.onRotate.Store(nil)
		return
	}
	m.onRotate.Store(&fn)
}

func (m *Manager) notifyRotation() {
	if ptr := m.onRotate.Load(); ptr != nil && *ptr != nil {
		(*ptr)()
	}
}

// Close terminates the active session and reaps every draining one.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.closeOnce.Do(func() {
		m.publishMu.Lock()
		m.closed.Store(true)
		m.bgCancel()
		// Tell draining goroutines to stop waiting for idle: the process is going
		// away and the sockets die with it.
		close(m.drainDone)
		session := m.current.Swap(nil)
		m.publishMu.Unlock()

		if session != nil {
			session.Close()
		}
		m.drainWG.Wait()
	})
}

func (m *Manager) loadSavedIdentity() (*SavedIdentity, error) {
	if m.identityPath == "" {
		return nil, errors.New("no identity path configured")
	}
	b, err := os.ReadFile(m.identityPath)
	if err != nil {
		return nil, err
	}
	var id SavedIdentity
	if err := json.Unmarshal(b, &id); err != nil {
		return nil, err
	}
	if id.Keys == nil || len(id.Keys.PrivateKeyHex) != 64 {
		return nil, errors.New("saved identity has no usable private key")
	}
	if id.Registration == nil || len(id.Registration.Config.Peers) == 0 {
		return nil, errors.New("saved identity has no peer")
	}
	return &id, nil
}

// saveIdentity persists the tunnel identity atomically. The file holds a
// WireGuard private key, so a torn write is worse than a lost write: it is
// staged in a sibling temp file and renamed, and the directory is 0700.
func (m *Manager) saveIdentity(keys *KeyPair, reg *RegistrationResponse) {
	if m.identityPath == "" {
		return
	}
	b, err := json.MarshalIndent(SavedIdentity{Keys: keys, Registration: reg, SavedAt: time.Now()}, "", "  ")
	if err != nil {
		m.logger.Error("could not encode warp identity", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.identityPath), 0700); err != nil {
		m.logger.Warn("could not create warp identity directory", "error", err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.identityPath), ".warp_identity_*")
	if err != nil {
		m.logger.Warn("could not stage warp identity file", "error", err)
		return
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0600); err != nil {
		m.logger.Warn("could not restrict warp identity permissions", "error", err)
	}
	if _, err := tmp.Write(b); err != nil {
		m.logger.Warn("could not write warp identity file", "error", err)
		return
	}
	if err := tmp.Sync(); err != nil {
		m.logger.Warn("could not sync warp identity file", "error", err)
	}
	if err := tmp.Close(); err != nil {
		m.logger.Warn("could not close warp identity file", "error", err)
		return
	}
	if err := os.Rename(tmpName, m.identityPath); err != nil {
		m.logger.Warn("could not persist warp identity", "error", err)
		return
	}
	m.logger.Debug("warp identity persisted", "path", m.identityPath)
}

// buildSession constructs and starts a userspace WireGuard session for given credentials.
func (m *Manager) buildSession(keys *KeyPair, reg *RegistrationResponse, start time.Time) (*Session, error) {
	v4Str := reg.Config.Interface.Addresses.V4
	if idx := strings.Index(v4Str, "/"); idx != -1 {
		v4Str = v4Str[:idx]
	}
	if v4Str == "" {
		v4Str = "172.16.0.2"
	}
	v4Addr, err := netip.ParseAddr(v4Str)
	if err != nil {
		return nil, fmt.Errorf("parse internal ipv4 %q: %w", v4Str, err)
	}

	v6Str := reg.Config.Interface.Addresses.V6
	if idx := strings.Index(v6Str, "/"); idx != -1 {
		v6Str = v6Str[:idx]
	}
	var localAddrs []netip.Addr
	if v6Str != "" {
		if v6Addr, err := netip.ParseAddr(v6Str); err == nil {
			localAddrs = []netip.Addr{v4Addr, v6Addr}
		}
	}
	if len(localAddrs) == 0 {
		localAddrs = []netip.Addr{v4Addr}
	}

	dnsServers := []netip.Addr{
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("1.0.0.1"),
	}

	tunDev, tnet, err := netstack.CreateNetTUN(localAddrs, dnsServers, 1420)
	if err != nil {
		return nil, fmt.Errorf("create userspace net tun: %w", err)
	}

	var reserved [3]byte
	if reg.Config.ClientID != "" {
		dec, err := base64.StdEncoding.DecodeString(reg.Config.ClientID)
		if err == nil && len(dec) == 3 {
			copy(reserved[:], dec)
		}
	}

	bind := newWarpBind(conn.NewDefaultBind(), reserved)
	dev := device.NewDevice(tunDev, bind, m.deviceLogger())

	peer := reg.Config.Peers[0]
	peerPubKeyB64 := peer.PublicKey
	if peerPubKeyB64 == "" {
		peerPubKeyB64 = DefaultPeerPublicKey
	}
	peerPubBytes, err := base64.StdEncoding.DecodeString(peerPubKeyB64)
	if err != nil {
		dev.Close()
		return nil, fmt.Errorf("decode peer public key: %w", err)
	}

	peerEndpoint := peer.Endpoint.Host
	if peerEndpoint == "" {
		peerEndpoint = peer.Endpoint.V4
	}
	if peerEndpoint == "" || peerEndpoint == ":0" || strings.HasPrefix(peerEndpoint, "0.0.0.0") {
		peerEndpoint = DefaultPeerEndpoint
	}
	if strings.HasSuffix(peerEndpoint, ":0") {
		peerEndpoint = strings.TrimSuffix(peerEndpoint, ":0") + ":2408"
	} else if !strings.Contains(peerEndpoint, ":") {
		peerEndpoint += ":2408"
	}
	if udpAddr, err := net.ResolveUDPAddr("udp", peerEndpoint); err == nil && udpAddr != nil {
		peerEndpoint = udpAddr.String()
	}

	uapiConfig := fmt.Sprintf(
		"private_key=%s\npublic_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\nallowed_ip=::/0\npersistent_keepalive_interval=25\n",
		keys.PrivateKeyHex,
		hex.EncodeToString(peerPubBytes),
		peerEndpoint,
	)

	if err := dev.IpcSet(uapiConfig); err != nil {
		dev.Close()
		return nil, fmt.Errorf("configure wireguard uapi: %w", err)
	}

	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("start wireguard device: %w", err)
	}

	session := &Session{
		Dev:          dev,
		TNet:         tnet,
		InternalIPv4: v4Str,
		Endpoint:     peerEndpoint,
		LatencyMs:    time.Since(start).Milliseconds(),
		CreatedAt:    time.Now(),
	}

	if m.edgeProbe != nil {
		session.PublicIP, session.Colo = m.edgeProbe(tnet)
	} else {
		session.PublicIP, session.Colo = m.probeEdgeTrace(tnet)
	}

	return session, nil
}

// deviceLogger routes the WireGuard state machine into slog instead of stdout.
// At verbose level it emits a line per handshake, which would both flood the
// log stream and bypass the redacting handler.
func (m *Manager) deviceLogger() *device.Logger {
	l := m.logger.With("wireguard", "netstack")
	return &device.Logger{
		Verbosef: func(format string, args ...any) { l.Debug(fmt.Sprintf(format, args...)) },
		Errorf:   func(format string, args ...any) { l.Warn(fmt.Sprintf(format, args...)) },
	}
}

// DialContext connects to the target address through the active userspace WireGuard tunnel.
func (m *Manager) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if m.closed.Load() {
		return nil, errClosed
	}

	session, err := m.ensureSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("initialize warp tunnel: %w", err)
	}

	// A session retired between selection and dial (drain deadline, concurrent
	// rotation) must not accept new work: fall back to whatever is current once.
	if !session.acquireConn() {
		if session, err = m.ensureSession(ctx); err != nil {
			return nil, fmt.Errorf("initialize warp tunnel: %w", err)
		}
		if !session.acquireConn() {
			return nil, errors.New("warp tunnel is shutting down")
		}
	}

	conn, err := session.TNet.DialContext(ctx, network, address)
	if err != nil {
		session.releaseConn()
		return nil, err
	}

	m.connTotal.Add(1)
	return &wrappedConn{
		Conn: conn,
		onClose: func() {
			m.connTotal.Add(-1)
			session.releaseConn()
		},
	}, nil
}

// ensureSession returns the active session, establishing one on cold start. All
// callers share one in-flight cold start, so a burst of first requests cannot
// register and start several tunnels at once.
func (m *Manager) ensureSession(ctx context.Context) (*Session, error) {
	if s := m.current.Load(); s != nil {
		return s, nil
	}
	if m.closed.Load() {
		return nil, errClosed
	}

	return m.runFlight(ctx, ensureFlightKey, func() (*Session, error) {
		if s := m.current.Load(); s != nil {
			return s, nil
		}

		// Restoring the cached identity avoids a fresh Cloudflare registration on
		// every restart.
		s, err := m.restoreSession()
		if err == nil {
			if active := m.installSession(s); active != nil {
				return active, nil
			}
			return nil, errClosed
		}
		if !errors.Is(err, os.ErrNotExist) {
			m.logger.Warn("could not reuse cached warp identity; registering a new device", "error", err)
		}

		return m.rotate()
	})
}

// runFlight coalesces fn under key. A caller whose own context expires stops
// waiting but does not cancel the shared flight: the first request's deadline
// must not destroy setup work every other request is waiting on. The flight
// itself runs on the manager context, so shutdown still cancels it.
func (m *Manager) runFlight(ctx context.Context, key string, fn func() (*Session, error)) (*Session, error) {
	res := m.sfg.DoChan(key, func() (any, error) {
		session, err := fn()
		if err != nil {
			return nil, err
		}
		return session, nil
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-res:
		if r.Err != nil {
			return nil, r.Err
		}
		session, ok := r.Val.(*Session)
		if !ok || session == nil {
			return nil, errors.New("warp tunnel unavailable")
		}
		return session, nil
	}
}

// restoreSession rebuilds a tunnel from the persisted identity. The session is
// probed before it is published: a cached peer is the one case where the server
// side may already be gone, and handing out a dead tunnel would fail every
// request with a dial timeout instead of falling back to a fresh registration.
func (m *Manager) restoreSession() (*Session, error) {
	ident, err := m.loadSavedIdentity()
	if err != nil {
		return nil, err
	}

	session, err := m.buildSession(ident.Keys, ident.Registration, time.Now())
	if err != nil {
		return nil, fmt.Errorf("restore warp identity: %w", err)
	}
	if session.PublicIP == "" {
		session.Close()
		return nil, errors.New("cached warp identity is unreachable through the tunnel")
	}

	m.lastRotated.Store(ident.SavedAt.UnixNano())
	m.lastError.Store(nil)
	m.logger.Info("restored cloudflare warp identity from local cache",
		"public_ip", session.PublicIP,
		"internal_ip", session.InternalIPv4,
		"colo", session.Colo,
		"saved_at", ident.SavedAt)

	return session, nil
}

// installSession publishes s as the active session and drains the one it
// replaced. Publication is serialized with Close, which is what keeps a losing
// build from being orphaned (a build that is older than what already won is
// closed instead of published) and what keeps a drain from being registered
// once shutdown is already waiting for the last one.
//
// It returns the session that is active now, which callers must use: the one
// they built is not guaranteed to be it. nil means the manager is closed.
func (m *Manager) installSession(s *Session) *Session {
	m.publishMu.Lock()

	if m.closed.Load() {
		m.publishMu.Unlock()
		s.Close()
		return nil
	}
	old := m.current.Load()
	if old != nil && old.CreatedAt.After(s.CreatedAt) {
		m.publishMu.Unlock()
		s.Close()
		return old
	}

	m.current.Store(s)
	if old != nil && old != s {
		// Registered under publishMu, so this Add cannot race the Wait in Close.
		m.drainSession(old)
	}
	m.publishMu.Unlock()

	return s
}

// drainSession closes a superseded tunnel once its connections finish, bounded
// by the session grace period and by shutdown.
func (m *Manager) drainSession(old *Session) {
	m.draining.Add(1)
	m.drainWG.Add(1)

	go func() {
		defer m.drainWG.Done()
		defer m.draining.Add(-1)
		defer old.Close()

		deadline := time.NewTimer(m.sessionGrace)
		defer deadline.Stop()

		// One reused ticker: time.After in a poll loop allocates a timer per tick.
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-m.drainDone:
				return
			case <-deadline.C:
				if open := old.ActiveConnections(); open > 0 {
					m.logger.Warn("warp session drain grace expired with connections still open",
						"open_connections", open,
						"grace", m.sessionGrace.String())
				}
				return
			case <-ticker.C:
				if old.ActiveConnections() == 0 {
					m.logger.Debug("drained superseded warp session")
					return
				}
			}
		}
	}()
}

// Rotate negotiates a new WireGuard identity with Cloudflare, publishes it as the
// active session, and drains previous connections. Concurrent calls are
// coalesced via singleflight. This entry point is forced: it ignores the
// background rotation throttle, so the admin endpoint stays responsive.
func (m *Manager) Rotate(ctx context.Context) (*Session, error) {
	return m.runFlight(ctx, rotateFlightKey, m.rotate)
}

// rotate is the shared body of Rotate and the cold-start path. It runs inside a
// singleflight call, so it must never re-enter Rotate on the same key.
func (m *Manager) rotate() (*Session, error) {
	if m.closed.Load() {
		return nil, errClosed
	}

	m.logger.Info("rotating cloudflare warp identity and tunnel session...")
	start := time.Now()

	// Bound the flight on the manager context, not the caller's: the caller may
	// have walked away, and shutdown must still cancel work in progress.
	ctx, cancel := context.WithTimeout(m.bgCtx, m.rotateTimeout)
	defer cancel()

	keys, err := GenerateKeyPair()
	if err != nil {
		m.recordError(err)
		return nil, fmt.Errorf("generate wireguard keypair: %w", err)
	}

	reg, err := RegisterDevice(ctx, m.httpClient, keys, m.licenseKey, m.registrationURL)
	if err != nil {
		m.recordError(err)
		return nil, fmt.Errorf("register warp device: %w", err)
	}

	session, err := m.buildSession(keys, reg, start)
	if err != nil {
		m.recordError(err)
		return nil, err
	}
	if reg.LicenseError != "" {
		m.logger.Warn("could not apply warp license; continuing with the registered plan",
			"error", reg.LicenseError)
	}

	// Publish first, then persist. If a newer session won the race this build is
	// discarded, and caching its identity would replace the credentials of the
	// tunnel that is actually serving traffic.
	built := session
	session = m.installSession(session)
	if session == nil {
		return nil, errClosed
	}
	if session != built {
		m.logger.Info("kept the newer warp session; the one just negotiated was discarded",
			"active_public_ip", session.PublicIP)
		return session, nil
	}

	m.saveIdentity(keys, reg)
	now := time.Now()
	m.lastRotated.Store(now.UnixNano())
	m.lastAttempt.Store(now.UnixNano())
	m.lastError.Store(nil)

	// Idle keep-alive connections pooled by the upstream transports still point at
	// the previous egress IP; drop them or the new IP is never used.
	m.notifyRotation()

	m.logger.Info("cloudflare warp rotation successful",
		"public_ip", session.PublicIP,
		"internal_ip", session.InternalIPv4,
		"colo", session.Colo,
		"latency_ms", session.LatencyMs)

	return session, nil
}

// RotateAsync triggers a background rotation without blocking the caller. It is
// the 429-driven path and therefore throttled: rotating per 429 would burn
// through Cloudflare registration limits during a quota storm.
func (m *Manager) RotateAsync(upstreamName string) {
	if m.closed.Load() {
		return
	}
	if !m.rotationAllowed(time.Now()) {
		m.logger.Debug("skipping warp rotation, previous rotation is still within the interval",
			"upstream", upstreamName,
			"interval", m.minRotateInterval.String())
		return
	}

	m.asyncRotations.Add(1)
	go func() {
		defer m.asyncRotations.Add(-1)
		defer func() {
			if r := recover(); r != nil {
				m.logger.Error("recovered from panic during async warp rotation", "panic", r, "upstream", upstreamName)
			}
		}()

		m.logger.Info("triggering background warp rotation due to upstream 429", "upstream", upstreamName)
		if _, err := m.Rotate(m.bgCtx); err != nil && !m.closed.Load() {
			m.logger.Warn("async warp rotation failed", "err", err, "upstream", upstreamName)
		}
	}()
}

// rotationAllowed claims the current throttle window. Claiming before spawning
// the goroutine is what keeps a 429 burst from queueing up dozens of rotations.
func (m *Manager) rotationAllowed(now time.Time) bool {
	interval := m.minRotateInterval.Nanoseconds()
	if interval <= 0 {
		return true
	}
	next := now.UnixNano()
	for {
		last := m.lastAttempt.Load()
		if last != 0 && next-last < interval {
			return false
		}
		if m.lastAttempt.CompareAndSwap(last, next) {
			return true
		}
	}
}

// probeEdgeTrace reads https://cloudflare.com/cdn-cgi/trace through netstack to
// learn the tunnel's public egress address and the edge cologne that served it.
func (m *Manager) probeEdgeTrace(tnet *netstack.Net) (string, string) {
	if tnet == nil {
		return "", ""
	}

	ctx, cancel := context.WithTimeout(m.bgCtx, m.probeTimeout)
	defer cancel()

	tr := &http.Transport{
		DialContext:     tnet.DialContext,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	// The transport keeps the tunnel referenced by an idle connection; without
	// this the probe pins the device it was meant to measure.
	defer tr.CloseIdleConnections()

	client := &http.Client{Transport: tr, Timeout: m.probeTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DefaultTraceURL, nil)
	if err != nil {
		return "", ""
	}

	resp, err := client.Do(req)
	if err != nil {
		m.logger.Warn("cloudflare trace probe failed", "error", err)
		return "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		m.logger.Warn("cloudflare trace probe returned a non-success status", "status", resp.StatusCode)
		return "", ""
	}

	var ip, colo string
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 8<<10))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "ip="):
			ip = strings.TrimPrefix(line, "ip=")
		case strings.HasPrefix(line, "colo="):
			colo = strings.TrimPrefix(line, "colo=")
		}
	}
	return strings.TrimSpace(ip), strings.TrimSpace(colo)
}

// Status returns a point-in-time snapshot of the WARP tunnel health.
func (m *Manager) Status() Status {
	session := m.current.Load()

	lastRotated := m.lastRotated.Load()
	var rotatedTime time.Time
	if lastRotated > 0 {
		rotatedTime = time.Unix(0, lastRotated)
	}

	var errMsg string
	if ptr := m.lastError.Load(); ptr != nil {
		errMsg = *ptr
	}

	st := Status{
		ActiveConnections: int(m.connTotal.Load()),
		DrainingSessions:  int(m.draining.Load()),
		RotatedAt:         rotatedTime,
		Error:             errMsg,
	}

	if session == nil {
		return st
	}

	st.Enabled = true
	st.PublicIP = session.PublicIP
	st.InternalIPv4 = session.InternalIPv4
	st.Colo = session.Colo
	st.Endpoint = session.Endpoint
	st.HandshakeLatencyMs = session.LatencyMs
	return st
}

func (m *Manager) recordError(err error) {
	if err == nil {
		m.lastError.Store(nil)
		return
	}
	msg := err.Error()
	m.lastError.Store(&msg)
}

type wrappedConn struct {
	net.Conn
	onClose func()
	closed  atomic.Bool
}

func (w *wrappedConn) Close() error {
	if w.closed.Swap(true) {
		return nil
	}
	if w.onClose != nil {
		w.onClose()
	}
	return w.Conn.Close()
}
