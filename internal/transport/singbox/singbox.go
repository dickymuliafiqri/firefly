package singbox

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	"github.com/sagernet/sing/common/metadata"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

const (
	// urlTestHealthURL is the HTTP probe used by the urltest group to pick the
	// fastest node. Cloudflare's generate_204 is a common, cheap choice that
	// does not return a body.
	urlTestHealthURL = "https://cp.cloudflare.com/generate_204"
	// urlTestInterval is the URLTest group's evaluation interval.
	urlTestInterval = 3 * time.Minute
	// urlTestTolerance is the latency tolerance (ms) before switching.
	urlTestTolerance = 50
	// healthProbeInterval is how often the singbox.health worker probes nodes.
	healthProbeInterval = 5 * time.Minute
	// healthProbeTimeout bounds a single node probe.
	healthProbeTimeout = 10 * time.Second
)

// Metrics is the narrow observability surface the Manager reports to. It is
// satisfied structurally by *metrics.Metrics, so this package does not depend
// on the observability package (keeping the transport layer free of that edge).
// All methods must tolerate a nil receiver on the caller's side; the Manager
// also skips reporting when no observer was set.
type Metrics interface {
	ObserveSingboxRefresh(result string)
	SetSingboxNodes(n int)
	SetSingboxReady(ready bool)
	ObserveSingboxNodeLatency(tag string, d time.Duration)
}

// Dialer is the interface warp.EgressConfig and upstream.Pool expect: a
// context-aware dialer. The Manager itself, and any per-tag dialer handed out
// by TaggedDialer, satisfy it.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Status summarizes the sing-box manager's runtime state for API/UI and
// observability.
type Status struct {
	Running    bool   `json:"running"`
	BoxCreated bool   `json:"box_created"`
	NodeCount  int    `json:"node_count"`
	DefaultTag string `json:"default_tag"`
	// SubscriptionURL is populated only for authenticated callers. The public
	// GET /api/singbox/status response clears it (see handleGetSingboxStatus)
	// because a subscription URL is a token-bearing credential. Unauthenticated
	// callers learn only whether one is configured, via SubscriptionConfigured.
	SubscriptionURL        string        `json:"subscription_url,omitempty"`
	SubscriptionConfigured bool          `json:"subscription_configured"`
	RefreshInterval        time.Duration `json:"refresh_interval"`
	LastRefreshAt          *time.Time    `json:"last_refresh_at,omitempty"`
	LastRefreshError       string        `json:"last_refresh_error,omitempty"`
	Nodes                  []NodeStatus  `json:"nodes,omitempty"`
	Ready                  bool          `json:"ready"`
	// Strategy is the active node-selection mode (urltest | round_robin),
	// always normalized so the dashboard never renders an empty value.
	Strategy string `json:"strategy"`
}

// NodeStatus lists a parsed node for the UI without exposing the full
// sing-box outbound configuration.
type NodeStatus struct {
	Tag    string `json:"tag"`
	Type   string `json:"type"`
	Server string `json:"server"`
	Port   uint16 `json:"port"`
	// Cooling is true while the node sits in a health-gate cooldown after
	// consecutive dial failures; the rotation skips it until CooldownUntil.
	Cooling       bool      `json:"cooling,omitempty"`
	CooldownUntil time.Time `json:"cooldown_until,omitempty"`
	// Failures is the current consecutive-failure count behind the cooldown.
	Failures int `json:"failures,omitempty"`
}

// Manager owns a single sing-box Box and maps Firefly upstreams to outbound
// tags. Fail-open: if Box cannot be started (no subscription, malformed
// subscription, etc.), DialContext will bypass sing-box and use a direct
// net.Dialer. That preserves connectivity while a subscription is loaded and
// mirrors the decision in the execution plan (boot failure → fail-open to
// direct). There are no inbound/route/DNS components; we only dial out.
type Manager struct {
	mu           sync.RWMutex
	box          *box.Box
	nodes        []Node
	defaultTag   string
	subURL       string
	refreshEvery time.Duration
	logger       *slog.Logger
	stopRefresh  context.CancelFunc

	// ready is true once the Box has started successfully. If false, we
	// fail-open on every dial.
	ready       atomic.Bool
	lastRefresh atomic.Pointer[time.Time]
	lastErr     atomic.Value // string

	// strategy is the node-selection mode (domain.SingboxStrategyURLTest or
	// domain.SingboxStrategyRoundRobin). Stored in an atomic so the dial path
	// reads it without taking mu; see roundrobin.go.
	strategy atomic.Value // string

	// health guards the per-node cooldown map, and rrCursor is the rotation
	// offset for round_robin dialing. Both live in roundrobin.go: a separate
	// mutex keeps a slow health bookkeeping write off the Box-swap lock.
	healthMu sync.Mutex
	health   map[string]nodeHealthState
	rrCursor atomic.Uint64

	metrics Metrics
}

// NewManager returns a new Manager. If logger is nil, slog.Default() is used.
// The Manager starts with no subscription; Refresh must be called to load the
// initial set of nodes before dials succeed through sing-box.
func NewManager(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	// strategy is left unset here: Strategy() then reads the URLTest default.
	return &Manager{
		defaultTag: domain.SingboxDefaultOutboundTag,
		logger:     logger.With("component", "singbox"),
		health:     make(map[string]nodeHealthState),
	}
}

// SetMetrics wires an optional metrics observer. Safe to call before Run.
func (m *Manager) SetMetrics(metrics Metrics) {
	m.mu.Lock()
	m.metrics = metrics
	m.mu.Unlock()
}

// SetDefaultTag changes the fallback tag used by DialTag when the requested
// tag cannot be resolved. It is safe for concurrent use.
func (m *Manager) SetDefaultTag(tag string) {
	m.mu.Lock()
	if domain.ValidSingboxTag(tag) {
		m.defaultTag = tag
	} else if tag != "" {
		m.defaultTag = domain.SingboxDefaultOutboundTag
	}
	m.mu.Unlock()
}

// Stop stops the background refresh goroutine if one exists, closes the
// active Box, and clears the ready flag (dials fall open to direct). It is
// idempotent.
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.stopRefresh != nil {
		m.stopRefresh()
		m.stopRefresh = nil
	}
	b := m.box
	m.box = nil
	m.mu.Unlock()
	m.ready.Store(false)
	if b != nil {
		_ = b.Close()
	}
}

// SubscriptionURL returns the configured subscription URL.
func (m *Manager) SubscriptionURL() string {
	m.mu.RLock()
	u := m.subURL
	m.mu.RUnlock()
	return u
}

// RefreshInterval returns the configured refresh interval.
func (m *Manager) RefreshInterval() time.Duration {
	m.mu.RLock()
	d := m.refreshEvery
	m.mu.RUnlock()
	return d
}

// SetSubscriptionURL sets the subscription URL and refresh interval. It is
// safe for concurrent use.
func (m *Manager) SetSubscriptionURL(url string, refresh time.Duration) {
	m.mu.Lock()
	m.subURL = url
	m.refreshEvery = refresh
	m.mu.Unlock()
}

// Ready reports whether the Box is started and dials will go through
// sing-box. It is safe for concurrent use.
func (m *Manager) Ready() bool {
	return m.ready.Load()
}

// Status returns a snapshot of the manager's runtime state for API/UI.
func (m *Manager) Status() Status {
	// Cooldown bookkeeping lives under its own mutex (see roundrobin.go);
	// take it first so the snapshot never holds mu while waiting on healthMu
	// in one order and the reverse elsewhere.
	m.healthMu.Lock()
	health := make(map[string]nodeHealthState, len(m.health))
	for k, v := range m.health {
		health[k] = v
	}
	m.healthMu.Unlock()

	m.mu.RLock()
	defer m.mu.RUnlock()
	st := Status{
		Running:                m.ready.Load(),
		BoxCreated:             m.box != nil,
		NodeCount:              len(m.nodes),
		DefaultTag:             m.defaultTag,
		SubscriptionURL:        m.subURL,
		SubscriptionConfigured: m.subURL != "",
		RefreshInterval:        m.refreshEvery,
		Strategy:               m.Strategy(),
	}
	if t := m.lastRefresh.Load(); t != nil {
		st.LastRefreshAt = t
	}
	if e := m.lastErr.Load(); e != nil {
		if s, ok := e.(string); ok {
			st.LastRefreshError = s
		}
	}
	st.Ready = st.Running
	now := time.Now()
	for _, n := range m.nodes {
		ns := NodeStatus{Tag: n.Tag, Type: n.Type, Server: n.Server, Port: n.Port}
		if h, ok := health[n.Tag]; ok {
			ns.Failures = h.fails
			if h.cooling(now) {
				ns.Cooling = true
				ns.CooldownUntil = h.until
			}
		}
		st.Nodes = append(st.Nodes, ns)
	}
	return st
}

// BuildOutbounds constructs the programmatic outbound list for the CURRENT
// node set: node outbounds, urltest:auto, selector:select (with auto+nodes),
// direct. Callers outside this package should prefer it; Refresh uses
// outboundsFor with the freshly parsed nodes so a new Box is never built from
// the previous subscription's nodes.
func (m *Manager) BuildOutbounds(defaultTag string) []option.Outbound {
	m.mu.RLock()
	nodes := m.nodes
	m.mu.RUnlock()
	return outboundsFor(nodes, defaultTag)
}

// outboundsFor builds the outbound list for an explicit node set and default
// tag. It holds no locks and is safe to call before the node swap.
func outboundsFor(nodes []Node, defaultTag string) []option.Outbound {
	out := make([]option.Outbound, 0, 3+len(nodes))
	// 1) Node outbounds
	for _, n := range nodes {
		out = append(out, n.Outbound)
	}
	// 2) URLTest "auto"
	urltestOpts := &option.URLTestOutboundOptions{
		Outbounds:                 make([]string, 0, len(nodes)),
		URL:                       urlTestHealthURL,
		Interval:                  badoption.Duration(urlTestInterval),
		Tolerance:                 urlTestTolerance,
		IdleTimeout:               badoption.Duration(30 * time.Minute),
		InterruptExistConnections: false,
	}
	for _, n := range nodes {
		urltestOpts.Outbounds = append(urltestOpts.Outbounds, n.Tag)
	}
	out = append(out, option.Outbound{
		Type:    constant.TypeURLTest,
		Tag:     domain.SingboxDefaultOutboundTag,
		Options: urltestOpts,
	})
	// 3) Selector "select"
	selectorOuts := make([]string, 0, 1+len(urltestOpts.Outbounds))
	selectorOuts = append(selectorOuts, domain.SingboxDefaultOutboundTag)
	selectorOuts = append(selectorOuts, urltestOpts.Outbounds...)
	sel := &option.SelectorOutboundOptions{
		Outbounds: selectorOuts,
		Default:   defaultTag,
	}
	if !domain.ValidSingboxTag(defaultTag) {
		sel.Default = domain.SingboxDefaultOutboundTag
	}
	out = append(out, option.Outbound{
		Type:    constant.TypeSelector,
		Tag:     domain.SingboxSelectorOutboundTag,
		Options: sel,
	})
	// 4) Direct
	out = append(out, option.Outbound{
		Type:    constant.TypeDirect,
		Tag:     domain.SingboxDirectOutboundTag,
		Options: &option.DirectOutboundOptions{},
	})
	return out
}

// Run refreshes immediately, then periodically at the configured interval until
// ctx is cancelled. It is the periodic worker wrapped by supervisor.Go in main;
// each iteration is best-effort and failures are recorded for observability.
// It stops any previously running loop first, so it is safe to call again.
func (m *Manager) Run(ctx context.Context) {
	m.Stop()
	runCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.stopRefresh = cancel
	m.mu.Unlock()

	// Initial fetch is best-effort; the manager fails open to direct on error.
	if m.SubscriptionURL() != "" {
		if err := m.Refresh(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Warn("sing-box subscription refresh failed; singbox egress stays direct", "err", err)
		}
	}

	for {
		interval := m.RefreshInterval()
		if interval <= 0 {
			interval = time.Duration(domain.DefaultSingboxRefreshIntervalS) * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-runCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if m.SubscriptionURL() == "" {
			continue
		}
		if err := m.Refresh(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Warn("scheduled sing-box subscription refresh failed; keeping the previous box", "err", err)
		}
	}
}

// HealthLoop periodically probes each node outbound and records per-tag latency
// so the operator can see which nodes are actually reachable. It is the worker
// wrapped by supervisor.Go as "singbox.health". The loop exits when ctx is
// cancelled. Probes are TCP connects to the node's own server:port through the
// node outbound, so a node that cannot be dialled records a failure without
// touching the box's own routing.
func (m *Manager) HealthLoop(ctx context.Context) {
	// Stagger the first probe so it does not run before the initial refresh has
	// had a chance to build the Box.
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	for {
		m.probeNodes(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(healthProbeInterval):
		}
	}
}

// probeNodes dials each node outbound once and records the round-trip latency.
func (m *Manager) probeNodes(ctx context.Context) {
	if !m.ready.Load() {
		return
	}
	m.mu.RLock()
	nodes := make([]Node, len(m.nodes))
	copy(nodes, m.nodes)
	m.mu.RUnlock()
	for _, n := range nodes {
		if ctx.Err() != nil {
			return
		}
		addr := net.JoinHostPort(n.Server, strconv.Itoa(int(n.Port)))
		start := time.Now()
		probeCtx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
		conn, err := m.DialTag(probeCtx, n.Tag, "tcp", addr)
		cancel()
		if err != nil {
			m.logger.Debug("sing-box node probe failed", "tag", n.Tag, "err", err)
			continue
		}
		_ = conn.Close()
		m.withMetrics(func(mx Metrics) { mx.ObserveSingboxNodeLatency(n.Tag, time.Since(start)) })
	}
}

// Refresh fetches the subscription, parses it, rebuilds outbounds, and
// swaps the running Box in place (stop old, start new). On error it leaves the
// current Box untouched and records the failure for observability.
func (m *Manager) Refresh(ctx context.Context) error {
	raw, err := FetchSubscription(ctx, m.SubscriptionURL())
	if err != nil {
		m.lastErr.Store(err.Error())
		m.withMetrics(func(mx Metrics) { mx.ObserveSingboxRefresh("error") })
		return err
	}
	parsed := ParseSubscription(raw)

	// Build a fresh Box from the new node list, then swap it in.
	newBox := m.buildBox(ctx, parsed.Nodes)

	m.mu.Lock()
	oldBox := m.box
	m.box = newBox
	m.nodes = parsed.Nodes
	m.mu.Unlock()
	// A refreshed subscription may retire nodes; drop their cooldowns so a
	// re-added node starts clean (and the map cannot grow without bound).
	m.pruneHealth(parsed.Nodes)

	if newBox != nil {
		if startErr := newBox.Start(); startErr != nil {
			m.ready.Store(false)
			m.lastErr.Store(startErr.Error())
			m.withMetrics(func(mx Metrics) {
				mx.ObserveSingboxRefresh("error")
				mx.SetSingboxReady(false)
			})
			if oldBox != nil {
				_ = oldBox.Close()
			}
			return startErr
		}
		m.ready.Store(true)
		m.logger.Info("sing-box box started", "nodes", len(parsed.Nodes), "skipped", parsed.SkippedLines, "duplicates", parsed.Duplicates)
	}
	if oldBox != nil {
		_ = oldBox.Close()
	}

	now := time.Now()
	m.lastRefresh.Store(&now)
	m.lastErr.Store("")
	m.withMetrics(func(mx Metrics) {
		mx.ObserveSingboxRefresh("ok")
		mx.SetSingboxNodes(len(parsed.Nodes))
		mx.SetSingboxReady(m.ready.Load())
	})
	if !m.ready.Load() {
		// The Box could not be created (no usable nodes, or box.New failed);
		// dials fail open to direct. Log it loudly once per refresh so the
		// fallback is never silent.
		m.logger.Warn("sing-box egress unavailable; falling back to direct dial",
			"nodes", len(parsed.Nodes))
	}
	return nil
}

// withMetrics invokes fn with the configured observer under a read lock, if one
// was wired. The callback runs outside the lock via a local copy.
func (m *Manager) withMetrics(fn func(Metrics)) {
	m.mu.RLock()
	mx := m.metrics
	m.mu.RUnlock()
	if mx != nil {
		fn(mx)
	}
}

// buildBox constructs a box.Box from the node list, wired with the
// include.Context registries and no inbounds/routes. It returns nil if the
// Box cannot be created (so dials fail open to direct).
func (m *Manager) buildBox(ctx context.Context, nodes []Node) *box.Box {
	m.mu.RLock()
	defaultTag := m.defaultTag
	m.mu.RUnlock()

	opts := box.Options{
		Context: include.Context(context.Background()),
	}
	// Build from the freshly parsed nodes, not m.nodes: Refresh builds the new
	// Box before it swaps the node list in, so reading m.nodes here would wire
	// the new Box to the previous subscription's nodes.
	out := outboundsFor(nodes, defaultTag)
	opts.Options.Outbounds = out
	opts.Options.Inbounds = nil
	opts.Options.Route = &option.RouteOptions{}

	b, err := box.New(opts)
	if err != nil {
		m.logger.Warn("failed to create sing-box box", "err", err)
		return nil
	}
	return b
}

// DialContext dials through the manager's default outbound. Under the
// round_robin strategy it rotates across the healthy node set itself (see
// roundrobin.go); under urltest it dials the built-in URLTest group, which
// picks the fastest live node. If the box is not ready (failed to start, no
// subscription, etc.), it falls back to a direct net.Dialer so connectivity is
// preserved.
func (m *Manager) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if m.currentBox() == nil || !m.ready.Load() {
		return m.directDial(ctx, network, addr)
	}
	if m.Strategy() == domain.SingboxStrategyRoundRobin {
		conn, err, ok := m.dialRoundRobin(ctx, network, addr)
		if ok {
			return conn, err
		}
		// Empty pool: fall through to the fail-open direct dial below.
		return m.directDial(ctx, network, addr)
	}
	ob, ok := m.outbound(m.defaultTag)
	if !ok {
		ob, ok = m.outbound(domain.SingboxDirectOutboundTag)
		if !ok {
			return m.directDial(ctx, network, addr)
		}
	}
	// sing-box's Dialer interface uses metadata.Socksaddr
	dest := metadata.ParseSocksaddr(addr)
	return ob.DialContext(ctx, network, dest)
}

// DialTag dials through the given outbound tag, falling back to the default
// tag (which itself fails open to direct when the box is not ready).
func (m *Manager) DialTag(ctx context.Context, tag string, network, addr string) (net.Conn, error) {
	if m.currentBox() == nil || !m.ready.Load() {
		return m.directDial(ctx, network, addr)
	}
	ob, ok := m.outbound(tag)
	if !ok {
		// The requested tag is unknown (stale upstream config, removed node):
		// fall back without charging anything to it — the dial below does not
		// actually traverse that node, so its outcome is not its health.
		ob, ok = m.outbound(m.defaultTag)
		if !ok {
			ob, ok = m.outbound(domain.SingboxDirectOutboundTag)
			if !ok {
				return m.directDial(ctx, network, addr)
			}
		}
		dest := metadata.ParseSocksaddr(addr)
		return ob.DialContext(ctx, network, dest)
	}
	dest := metadata.ParseSocksaddr(addr)
	conn, err := ob.DialContext(ctx, network, dest)
	// The dial really went through this tag: charge the outcome to the node's
	// health so the cooldown gate sees probe and traffic results alike. Group
	// tags (auto/select/direct) are ignored by noteNodeOutcome.
	m.noteNodeOutcome(tag, err)
	return conn, err
}

// TaggedDialer returns a Dialer bound to the given tag, for use as a
// SingboxDialer in an upstream's EgressConfig. It returns nil if the tag is
// not valid, so the caller can decide to fall back.
func (m *Manager) TaggedDialer(tag string) Dialer {
	if !domain.ValidSingboxTag(tag) {
		return nil
	}
	return &taggedDialer{m: m, tag: tag}
}

// outbound resolves a tag from the running box. It reads m.box exactly once,
// through currentBox, so a concurrent Refresh swap cannot race the read and a
// concurrent Stop() cannot turn the subsequent dereference into a nil panic.
func (m *Manager) outbound(tag string) (adapter.Outbound, bool) {
	b := m.currentBox()
	if b == nil {
		return nil, false
	}
	mgr := b.Outbound()
	if mgr == nil {
		return nil, false
	}
	return mgr.Outbound(tag)
}

// RedactURL replaces every occurrence of subURL in msg with a placeholder, so a
// fetch error that embeds the subscription URL cannot leak the credential to an
// unauthenticated caller.
func RedactURL(msg, subURL string) string {
	if subURL == "" || msg == "" {
		return msg
	}
	return strings.ReplaceAll(msg, subURL, "[REDACTED]")
}

// currentBox returns the active Box pointer under the read lock. Dial paths
// must use this rather than reading m.box directly, so a concurrent Refresh
// swap cannot race with a dial.
func (m *Manager) currentBox() *box.Box {
	m.mu.RLock()
	b := m.box
	m.mu.RUnlock()
	return b
}

// directDial is the fail-open path: a plain net.Dialer that does not go
// through sing-box. Used when the Box is not ready or the tag cannot be
// resolved.
func (m *Manager) directDial(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return d.DialContext(ctx, network, addr)
}

// taggedDialer is a per-tag dialer handed to upstream transport configuration.
type taggedDialer struct {
	m   *Manager
	tag string
}

func (d *taggedDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.m.DialTag(ctx, d.tag, network, addr)
}
