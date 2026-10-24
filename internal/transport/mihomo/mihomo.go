package mihomo

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

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

func init() {
	// Quiet internal Mihomo log output so it doesn't flood stdout
	log.SetLevel(log.WARNING)
}

const (
	healthProbeInterval = 60 * time.Second
	healthProbeTimeout  = 8 * time.Second
)

// Metrics is the observability interface the Manager reports to.
type Metrics interface {
	ObserveSingboxRefresh(result string)
	SetSingboxNodes(n int)
	SetSingboxReady(ready bool)
	ObserveSingboxNodeLatency(tag string, d time.Duration)
}

// Dialer is a context-aware network dialer.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Status summarizes the mihomo manager's runtime state for API/UI.
type Status struct {
	Running                bool          `json:"running"`
	BoxCreated             bool          `json:"box_created"`
	NodeCount              int           `json:"node_count"`
	DefaultTag             string        `json:"default_tag"`
	SubscriptionURL        string        `json:"subscription_url,omitempty"`
	SubscriptionConfigured bool          `json:"subscription_configured"`
	RefreshInterval        time.Duration `json:"refresh_interval"`
	LastRefreshAt          *time.Time    `json:"last_refresh_at,omitempty"`
	LastRefreshError       string        `json:"last_refresh_error,omitempty"`
	Nodes                  []NodeStatus  `json:"nodes,omitempty"`
	Ready                  bool          `json:"ready"`
	Strategy               string        `json:"strategy"`
}

// NodeStatus describes the status of a single node in the pool.
type NodeStatus struct {
	Tag           string     `json:"tag"`
	Type          string     `json:"type"`
	Server        string     `json:"server"`
	Port          uint16     `json:"port"`
	Cooling       bool       `json:"cooling,omitempty"`
	CooldownUntil time.Time  `json:"cooldown_until,omitempty"`
	Failures      int        `json:"failures,omitempty"`
	LatencyMs     *int64     `json:"latency_ms,omitempty"`
	LastCheckAt   *time.Time `json:"last_check_at,omitempty"`
	Status        string     `json:"status"` // "active", "cooling", "unreachable", "idle"
	LastError     string     `json:"last_error,omitempty"`
}

type nodeHealthState struct {
	latencyMs *int64
	lastCheck *time.Time
	status    string
	lastErr   string
}

// Manager manages the embedded Mihomo proxy pool, background subscription updates,
// health checking, and outbound dialing.
type Manager struct {
	mu           sync.RWMutex
	proxies      map[string]C.Proxy
	nodes        []Node
	health       map[string]*nodeHealthState
	defaultTag   string
	strategy     string
	subURL       string
	refreshEvery time.Duration
	logger       *slog.Logger
	stopRefresh  context.CancelFunc

	ready       atomic.Bool
	lastRefresh atomic.Pointer[time.Time]
	lastErr     atomic.Value // string

	metrics Metrics

	nodeProbe func(ctx context.Context, p C.Proxy) (time.Duration, error)
}

// NewManager creates a new Mihomo Manager.
func NewManager(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		defaultTag: domain.SingboxDefaultOutboundTag,
		strategy:   domain.SingboxStrategyURLTest,
		logger:     logger.With("component", "mihomo"),
		health:     make(map[string]*nodeHealthState),
	}
	m.nodeProbe = m.defaultNodeProbe
	return m
}

// SetNodeProbeForTest overrides the probe function for testing.
func (m *Manager) SetNodeProbeForTest(fn func(ctx context.Context, p C.Proxy) (time.Duration, error)) {
	m.nodeProbe = fn
}

// SetMetrics wires an observability observer.
func (m *Manager) SetMetrics(metrics Metrics) {
	m.mu.Lock()
	m.metrics = metrics
	m.mu.Unlock()
}

// SetDefaultTag sets the fallback tag when none is specified.
func (m *Manager) SetDefaultTag(tag string) {
	m.mu.Lock()
	if domain.ValidSingboxTag(tag) {
		m.defaultTag = tag
	} else if tag != "" {
		m.defaultTag = domain.SingboxDefaultOutboundTag
	}
	m.mu.Unlock()
}

// SetStrategy sets the node selection strategy (fallback, round_robin, urltest, consistent_hashing).
func (m *Manager) SetStrategy(strategy string) {
	m.mu.Lock()
	m.strategy = domain.NormalizeSingboxStrategy(strategy)
	m.mu.Unlock()
}

// Strategy returns the current strategy.
func (m *Manager) Strategy() string {
	m.mu.RLock()
	s := m.strategy
	m.mu.RUnlock()
	if s == "" {
		return domain.SingboxStrategyURLTest
	}
	return s
}

// SetSubscriptionURL sets the subscription URL and refresh cadence.
func (m *Manager) SetSubscriptionURL(url string, refresh time.Duration) {
	m.mu.Lock()
	m.subURL = url
	m.refreshEvery = refresh
	m.mu.Unlock()
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

// Ready reports whether Mihomo is ready to proxy traffic.
func (m *Manager) Ready() bool {
	return m.ready.Load()
}

// Stop stops the background workers, clears proxies, and resets ready flag.
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.stopRefresh != nil {
		m.stopRefresh()
		m.stopRefresh = nil
	}
	proxies := m.proxies
	m.proxies = nil
	m.nodes = nil
	m.mu.Unlock()

	m.ready.Store(false)
	for _, p := range proxies {
		_ = p.Close()
	}
}

// Run periodically refreshes the subscription.
func (m *Manager) Run(ctx context.Context) {
	m.Stop()
	runCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.stopRefresh = cancel
	m.mu.Unlock()

	if m.SubscriptionURL() != "" {
		if err := m.Refresh(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Warn("mihomo subscription refresh failed; egress stays direct", "err", err)
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
			m.logger.Warn("scheduled mihomo subscription refresh failed; keeping previous proxies", "err", err)
		}
	}
}

// Refresh fetches and compiles the subscription.
func (m *Manager) Refresh(ctx context.Context) error {
	url := m.SubscriptionURL()
	if url == "" {
		m.Stop()
		m.ready.Store(false)
		m.lastErr.Store("")
		return nil
	}

	raw, err := FetchSubscription(ctx, url)
	if err != nil {
		m.lastErr.Store(err.Error())
		m.withMetrics(func(mx Metrics) {
			mx.ObserveSingboxRefresh("error")
		})
		return err
	}

	m.mu.RLock()
	strat := m.strategy
	defTag := m.defaultTag
	m.mu.RUnlock()

	parsed, err := ParseSubscription(raw, strat, defTag)
	if err != nil {
		m.lastErr.Store(err.Error())
		m.withMetrics(func(mx Metrics) {
			mx.ObserveSingboxRefresh("error")
		})
		return err
	}

	m.mu.Lock()
	oldProxies := m.proxies
	m.proxies = parsed.Proxies
	m.nodes = parsed.Nodes

	// Keep existing health info for nodes that persist across refresh
	newHealth := make(map[string]*nodeHealthState, len(parsed.Nodes))
	for _, n := range parsed.Nodes {
		if h, ok := m.health[n.Tag]; ok {
			newHealth[n.Tag] = h
		} else {
			newHealth[n.Tag] = &nodeHealthState{status: "idle"}
		}
	}
	m.health = newHealth
	m.mu.Unlock()

	for _, p := range oldProxies {
		_ = p.Close()
	}

	m.ready.Store(len(parsed.Nodes) > 0)
	now := time.Now()
	m.lastRefresh.Store(&now)
	m.lastErr.Store("")

	m.withMetrics(func(mx Metrics) {
		mx.ObserveSingboxRefresh("ok")
		mx.SetSingboxNodes(len(parsed.Nodes))
		mx.SetSingboxReady(m.ready.Load())
	})

	m.logger.Info("mihomo proxies loaded", "nodes", len(parsed.Nodes), "skipped", parsed.SkippedLines, "duplicates", parsed.Duplicates)
	return nil
}

// HealthLoop runs periodic health probes across all nodes.
func (m *Manager) HealthLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Second):
	}
	m.probeNodes(ctx)

	ticker := time.NewTicker(healthProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.probeNodes(ctx)
		}
	}
}

// CheckHealth runs an on-demand health check across all nodes.
func (m *Manager) CheckHealth(ctx context.Context) {
	m.probeNodes(ctx)
}

func (m *Manager) probeNodes(ctx context.Context) {
	if !m.ready.Load() {
		return
	}
	m.mu.RLock()
	nodes := make([]Node, len(m.nodes))
	copy(nodes, m.nodes)
	m.mu.RUnlock()

	if len(nodes) == 0 {
		return
	}

	sem := make(chan struct{}, 10)
	var wg sync.WaitGroup

	for _, n := range nodes {
		node := n
		if node.Proxy == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			probeCtx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
			defer cancel()

			dur, err := m.nodeProbe(probeCtx, node.Proxy)
			now := time.Now()

			m.mu.Lock()
			h, ok := m.health[node.Tag]
			if !ok {
				h = &nodeHealthState{}
				m.health[node.Tag] = h
			}
			h.lastCheck = &now
			if err != nil {
				h.status = "unreachable"
				h.lastErr = err.Error()
				h.latencyMs = nil
			} else {
				h.status = "active"
				h.lastErr = ""
				ms := dur.Milliseconds()
				h.latencyMs = &ms
			}
			m.mu.Unlock()

			if err == nil {
				m.withMetrics(func(mx Metrics) {
					mx.ObserveSingboxNodeLatency(node.Tag, dur)
				})
			}
		}()
	}
	wg.Wait()
}

func (m *Manager) defaultNodeProbe(ctx context.Context, p C.Proxy) (time.Duration, error) {
	if p == nil {
		return 0, errors.New("nil proxy")
	}
	delay, err := p.URLTest(ctx, defaultHealthCheckURL, nil)
	if err != nil {
		return 0, err
	}
	return time.Duration(delay) * time.Millisecond, nil
}

// Status returns a runtime snapshot of the Manager for the dashboard/API.
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()

	nodeStatuses := make([]NodeStatus, 0, len(m.nodes))
	for _, n := range m.nodes {
		ns := NodeStatus{
			Tag:    n.Tag,
			Type:   n.Type,
			Server: n.Server,
			Port:   n.Port,
			Status: "idle",
		}
		if h, ok := m.health[n.Tag]; ok {
			ns.Status = h.status
			ns.LatencyMs = h.latencyMs
			ns.LastCheckAt = h.lastCheck
			ns.LastError = h.lastErr
		}
		nodeStatuses = append(nodeStatuses, ns)
	}

	var lastErr string
	if v := m.lastErr.Load(); v != nil {
		lastErr, _ = v.(string)
	}

	return Status{
		Running:                m.stopRefresh != nil,
		BoxCreated:             len(m.proxies) > 0,
		NodeCount:              len(m.nodes),
		DefaultTag:             m.defaultTag,
		SubscriptionURL:        m.subURL,
		SubscriptionConfigured: m.subURL != "",
		RefreshInterval:        m.refreshEvery,
		LastRefreshAt:          m.lastRefresh.Load(),
		LastRefreshError:       lastErr,
		Nodes:                  nodeStatuses,
		Ready:                  m.ready.Load(),
		Strategy:               m.strategy,
	}
}

// DialContext dials through the default outbound group or falls open to direct dial.
func (m *Manager) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if !m.ready.Load() {
		return m.directDial(ctx, network, addr)
	}

	m.mu.RLock()
	tag := m.defaultTag
	if tag == "" {
		tag = domain.SingboxDefaultOutboundTag
	}
	target := m.proxies[tag]
	if target == nil {
		target = m.proxies[domain.SingboxDefaultOutboundTag]
	}
	if target == nil {
		target = m.proxies["DIRECT"]
	}
	m.mu.RUnlock()

	if target == nil {
		return m.directDial(ctx, network, addr)
	}

	return m.dialProxy(ctx, target, addr)
}

// DialTag dials through the named tag, falling back to DialContext.
func (m *Manager) DialTag(ctx context.Context, tag string, network, addr string) (net.Conn, error) {
	if !m.ready.Load() {
		return m.directDial(ctx, network, addr)
	}

	tag = strings.TrimSpace(tag)
	if tag == "" || tag == domain.SingboxDefaultOutboundTag {
		return m.DialContext(ctx, network, addr)
	}
	if strings.EqualFold(tag, domain.SingboxDirectOutboundTag) || strings.EqualFold(tag, "DIRECT") {
		return m.directDial(ctx, network, addr)
	}

	m.mu.RLock()
	target := m.proxies[tag]
	m.mu.RUnlock()

	if target == nil {
		return m.DialContext(ctx, network, addr)
	}

	return m.dialProxy(ctx, target, addr)
}

func (m *Manager) dialProxy(ctx context.Context, target C.Proxy, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, err
	}

	metadata := &C.Metadata{
		Host:    host,
		DstPort: uint16(port),
		NetWork: C.TCP,
	}

	conn, err := target.DialContext(ctx, metadata)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (m *Manager) directDial(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 30 * time.Second}
	return d.DialContext(ctx, network, addr)
}

// TaggedDialer returns a Dialer pinned to a specific tag.
func (m *Manager) TaggedDialer(tag string) Dialer {
	return &taggedDialer{m: m, tag: tag}
}

type taggedDialer struct {
	m   *Manager
	tag string
}

func (d *taggedDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.m.DialTag(ctx, d.tag, network, addr)
}

// RedactURL replaces every occurrence of subURL in msg with a placeholder.
func RedactURL(msg, subURL string) string {
	if msg == "" || subURL == "" {
		return msg
	}
	return strings.ReplaceAll(msg, subURL, "[REDACTED]")
}

func (m *Manager) withMetrics(fn func(Metrics)) {
	m.mu.RLock()
	mx := m.metrics
	m.mu.RUnlock()
	if mx != nil {
		fn(mx)
	}
}
