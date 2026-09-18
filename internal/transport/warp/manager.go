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
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// Session represents an active WireGuard connection to Cloudflare WARP.
type Session struct {
	Dev        *device.Device
	TNet       *netstack.Net
	PublicIPv4 string
	Colo       string
	Endpoint   string
	LatencyMs  int64
	CreatedAt  time.Time
	closed     atomic.Bool
}

// Close gracefully terminates the underlying WireGuard device.
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
	current        atomic.Pointer[Session]
	sfg            singleflight.Group
	logger         *slog.Logger
	httpClient     *http.Client
	licenseKey     string
	identityPath   string
	activeSessions atomic.Int64
	lastRotatedAt  atomic.Int64 // Unix nanoseconds
	lastError      atomic.Pointer[string]
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
	return &Manager{
		logger:       logger.With("component", "warp"),
		httpClient:   &http.Client{Timeout: 20 * time.Second},
		licenseKey:   strings.TrimSpace(licenseKey),
		identityPath: idPath,
	}
}

// Close terminates the current active WARP session.
func (m *Manager) Close() {
	if s := m.current.Swap(nil); s != nil {
		s.Close()
	}
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
	if id.Keys == nil || id.Registration == nil || len(id.Registration.Config.Peers) == 0 {
		return nil, errors.New("invalid saved identity")
	}
	return &id, nil
}

func (m *Manager) saveIdentity(keys *KeyPair, reg *RegistrationResponse) {
	if m.identityPath == "" {
		return
	}
	id := SavedIdentity{
		Keys:         keys,
		Registration: reg,
		SavedAt:      time.Now(),
	}
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(m.identityPath), 0755)
	_ = os.WriteFile(m.identityPath, b, 0600)
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
	dev := device.NewDevice(tunDev, bind, device.NewLogger(device.LogLevelVerbose, "[WARP] "))

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

	latency := time.Since(start).Milliseconds()

	session := &Session{
		Dev:       dev,
		TNet:      tnet,
		Endpoint:  peerEndpoint,
		LatencyMs: latency,
		CreatedAt: time.Now(),
	}

	publicIP, colo := m.probeEdgeTrace(tnet)
	if publicIP != "" {
		session.PublicIPv4 = publicIP
	} else {
		session.PublicIPv4 = v4Str
	}
	session.Colo = colo

	return session, nil
}

// DialContext connects to the target address through the active userspace WireGuard tunnel.
func (m *Manager) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	session := m.current.Load()
	if session == nil {
		// Initialize session on first use: restore from saved identity if available
		if ident, err := m.loadSavedIdentity(); err == nil && ident != nil {
			start := time.Now()
			s, err := m.buildSession(ident.Keys, ident.Registration, start)
			if err == nil {
				m.current.Store(s)
				m.lastRotatedAt.Store(ident.SavedAt.UnixNano())
				session = s
				m.logger.Info("restored cloudflare warp identity from local cache",
					"public_ip", s.PublicIPv4,
					"colo", s.Colo)
			}
		}
		if session == nil {
			var err error
			session, err = m.Rotate(ctx)
			if err != nil {
				return nil, fmt.Errorf("initialize warp tunnel: %w", err)
			}
		}
	}

	m.activeSessions.Add(1)
	conn, err := session.TNet.DialContext(ctx, network, address)
	if err != nil {
		m.activeSessions.Add(-1)
		return nil, err
	}

	return &wrappedConn{
		Conn: conn,
		onClose: func() {
			m.activeSessions.Add(-1)
		},
	}, nil
}

// Rotate negotiates a new WireGuard identity with Cloudflare, updates the active session,
// and gracefully drains previous connections. Concurrent calls are coalesced via singleflight.
func (m *Manager) Rotate(ctx context.Context) (*Session, error) {
	val, err, _ := m.sfg.Do("rotate", func() (any, error) {
		m.logger.Info("rotating cloudflare warp identity and tunnel session...")
		start := time.Now()

		keys, err := GenerateKeyPair()
		if err != nil {
			m.recordError(err)
			return nil, fmt.Errorf("generate wireguard keypair: %w", err)
		}

		reg, err := RegisterDevice(ctx, m.httpClient, keys, m.licenseKey)
		if err != nil {
			m.recordError(err)
			return nil, fmt.Errorf("register warp device: %w", err)
		}

		session, err := m.buildSession(keys, reg, start)
		if err != nil {
			m.recordError(err)
			return nil, err
		}

		m.saveIdentity(keys, reg)

		// Swap active session
		oldSession := m.current.Swap(session)
		m.lastRotatedAt.Store(time.Now().UnixNano())
		m.lastError.Store(nil)

		m.logger.Info("cloudflare warp rotation successful",
			"public_ip", session.PublicIPv4,
			"colo", session.Colo,
			"latency_ms", session.LatencyMs)

		// Drain old session in background after grace period
		if oldSession != nil {
			go func(old *Session) {
				time.Sleep(20 * time.Second)
				old.Close()
			}(oldSession)
		}

		return session, nil
	})

	if err != nil {
		return nil, err
	}
	return val.(*Session), nil
}

// RotateAsync triggers rotation in a background goroutine without blocking the caller.
func (m *Manager) RotateAsync(upstreamName string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.logger.Error("recovered from panic during async warp rotation", "panic", r, "upstream", upstreamName)
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		m.logger.Info("triggering background warp rotation due to upstream 429", "upstream", upstreamName)
		if _, err := m.Rotate(ctx); err != nil {
			m.logger.Warn("async warp rotation failed", "err", err, "upstream", upstreamName)
		}
	}()
}

// probeEdgeTrace dials https://cloudflare.com/cdn-cgi/trace through netstack to inspect edge routing.
func (m *Manager) probeEdgeTrace(tnet *netstack.Net) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tr := &http.Transport{
		DialContext: tnet.DialContext,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://1.1.1.1/cdn-cgi/trace", nil)
	if err != nil {
		return "", ""
	}

	resp, err := client.Do(req)
	if err != nil {
		m.logger.Warn("cloudflare trace probe failed", "error", err)
		return "", ""
	}
	defer resp.Body.Close()

	var ip, colo string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ip=") {
			ip = strings.TrimPrefix(line, "ip=")
		} else if strings.HasPrefix(line, "colo=") {
			colo = strings.TrimPrefix(line, "colo=")
		}
	}
	return ip, colo
}

// Status returns a point-in-time snapshot of the WARP tunnel health.
func (m *Manager) Status() Status {
	session := m.current.Load()
	lastRotated := m.lastRotatedAt.Load()
	var rotatedTime time.Time
	if lastRotated > 0 {
		rotatedTime = time.Unix(0, lastRotated)
	}

	var errMsg string
	if ptr := m.lastError.Load(); ptr != nil {
		errMsg = *ptr
	}

	if session == nil {
		return Status{
			Enabled:        false,
			ActiveSessions: int(m.activeSessions.Load()),
			RotatedAt:      rotatedTime,
			Error:          errMsg,
		}
	}

	return Status{
		Enabled:            true,
		PublicIPv4:         session.PublicIPv4,
		Colo:               session.Colo,
		Endpoint:           session.Endpoint,
		HandshakeLatencyMs: session.LatencyMs,
		ActiveSessions:     int(m.activeSessions.Load()),
		RotatedAt:          rotatedTime,
		Error:              errMsg,
	}
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
