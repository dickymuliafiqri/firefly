package singbox

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/sagernet/sing/common/metadata"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// Round-robin dialing and the per-node health gate.
//
// sing-box ships no round-robin outbound group (only `selector` and `urltest`),
// so rotation lives in the Manager: when the strategy is round_robin the
// default dial path skips the URLTest group and walks the node list itself,
// starting at a process-wide cursor so no node is favoured across restarts of
// the Box. A node whose dial fails enters a KeyRing-style cooldown and is
// skipped by later rotations until it expires; a successful dial or probe
// clears it. When every node is cooling the rotation degrades to a plain round
// robin over all of them rather than failing the request — the cooldown is a
// heuristic, and trying a possibly-recovered node beats a guaranteed error.
const (
	// rrDialAttempts bounds how many nodes one dial may try before giving up,
	// mirroring the WARP retry budget so a burst of dead nodes cannot turn one
	// request into an unbounded scan of the pool.
	rrDialAttempts = 3
	// nodeCooldownBase/nodeCooldownMax bound the exponential cooldown applied
	// per consecutive failure: 5s, 10s, 20s ... capped at 5m (the same cap the
	// key ring puts on a Retry-After).
	nodeCooldownBase = 5 * time.Second
	nodeCooldownMax  = 5 * time.Minute
)

// nodeHealthState is one node's failure and health check bookkeeping.
// An expired or zero until means the node is selectable again; fails keeps
// growing on consecutive failures so the backoff lengthens. When checked is true,
// latencyMs and lastCheck track the most recent probe response time and timestamp.
type nodeHealthState struct {
	fails     int
	until     time.Time
	latencyMs int64
	lastCheck time.Time
	lastErr   string
	checked   bool
}

// cooling reports whether the node is still inside its cooldown at now.
func (h nodeHealthState) cooling(now time.Time) bool {
	return !h.until.IsZero() && now.Before(h.until)
}

// cooldownFor returns the cooldown duration for a failure ordinal (1-based).
func cooldownFor(fails int) time.Duration {
	if fails < 1 {
		fails = 1
	}
	shift := fails - 1
	if shift > 6 {
		shift = 6
	}
	d := nodeCooldownBase << shift
	if d > nodeCooldownMax {
		d = nodeCooldownMax
	}
	return d
}

// Strategy returns the active node-selection strategy. The value is stored in
// an atomic so the hot dial path never takes the Manager's mutex just to read
// it; an unset store reads as the URLTest default.
func (m *Manager) Strategy() string {
	if s, ok := m.strategy.Load().(string); ok && s != "" {
		return s
	}
	return domain.SingboxStrategyURLTest
}

// SetStrategy changes the node-selection strategy. Invalid input resets the
// strategy to the URLTest default rather than rejecting the call, so the
// dashboard can never wedge the manager into an unusable value.
func (m *Manager) SetStrategy(s string) {
	m.strategy.Store(domain.NormalizeSingboxStrategy(s))
}

// markNodeSuccess clears a node's cooldown after a successful dial or probe.
// If the node has not been explicitly probed, the entry is removed; if it has
// been probed, its failure counters are cleared while retaining the latency.
func (m *Manager) markNodeSuccess(tag string) {
	m.healthMu.Lock()
	if h, ok := m.health[tag]; ok {
		if !h.checked {
			delete(m.health, tag)
		} else {
			h.fails = 0
			h.until = time.Time{}
			h.lastErr = ""
			m.health[tag] = h
		}
	}
	m.healthMu.Unlock()
}

// recordNodeProbe records the result of an explicit health check probe.
func (m *Manager) recordNodeProbe(tag string, latency time.Duration, err error) {
	m.healthMu.Lock()
	h := m.health[tag]
	h.checked = true
	h.lastCheck = time.Now()
	if err == nil {
		ms := latency.Milliseconds()
		if ms <= 0 {
			ms = 1
		}
		h.latencyMs = ms
		h.fails = 0
		h.until = time.Time{}
		h.lastErr = ""
	} else {
		h.fails++
		h.until = time.Now().Add(cooldownFor(h.fails))
		h.latencyMs = 0
		h.lastErr = err.Error()
	}
	m.health[tag] = h
	m.healthMu.Unlock()
}

// markNodeFailure charges a failed dial to the node and extends its cooldown
// exponentially (5s → 5m). The first failure of a node is logged at Info so an
// operator sees a pool shrinking without wading through per-request Debug noise.
func (m *Manager) markNodeFailure(tag string, err error) {
	m.healthMu.Lock()
	h := m.health[tag]
	h.fails++
	h.until = time.Now().Add(cooldownFor(h.fails))
	if err != nil {
		h.lastErr = err.Error()
	}
	m.health[tag] = h
	fails, until := h.fails, h.until
	m.healthMu.Unlock()

	if fails == 1 {
		m.logger.Info("sing-box node entering cooldown", "tag", tag, "err", err)
	} else {
		m.logger.Debug("sing-box node still failing", "tag", tag, "fails", fails, "until", until, "err", err)
	}
}

// noteNodeOutcome charges a dial result to tag when it names a current node.
// Group tags (auto/select/direct) and tags no longer in the subscription are
// ignored: they have no health of their own to gate.
func (m *Manager) noteNodeOutcome(tag string, err error) {
	m.mu.RLock()
	isNode := false
	for i := range m.nodes {
		if m.nodes[i].Tag == tag {
			isNode = true
			break
		}
	}
	m.mu.RUnlock()
	if !isNode {
		return
	}
	if err != nil {
		m.markNodeFailure(tag, err)
		return
	}
	m.markNodeSuccess(tag)
}

// healthSnapshot copies the current cooldown state for read-only use (status
// reporting and dial planning) without holding healthMu across either.
func (m *Manager) healthSnapshot() map[string]nodeHealthState {
	m.healthMu.Lock()
	out := make(map[string]nodeHealthState, len(m.health))
	for k, v := range m.health {
		out[k] = v
	}
	m.healthMu.Unlock()
	return out
}

// pruneHealth drops health entries for nodes that a refresh removed, so a
// renamed or retired node cannot inherit another node's cooldown (or keep the
// map growing across many refreshes).
func (m *Manager) pruneHealth(nodes []Node) {
	m.healthMu.Lock()
	for tag := range m.health {
		found := false
		for i := range nodes {
			if nodes[i].Tag == tag {
				found = true
				break
			}
		}
		if !found {
			delete(m.health, tag)
		}
	}
	m.healthMu.Unlock()
}

// dialRoundRobin rotates across the healthy node set and dials the next
// rrDialAttempts candidates in order. ok is false only when the pool is empty,
// which tells the caller to fail open to direct exactly like an unstarted Box.
func (m *Manager) dialRoundRobin(ctx context.Context, network, addr string) (conn net.Conn, err error, ok bool) {
	m.mu.RLock()
	nodes := make([]Node, len(m.nodes))
	copy(nodes, m.nodes)
	m.mu.RUnlock()
	if len(nodes) == 0 {
		return nil, nil, false
	}

	now := time.Now()
	health := m.healthSnapshot()
	healthy := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if !health[n.Tag].cooling(now) {
			healthy = append(healthy, n)
		}
	}
	if len(healthy) == 0 {
		// Every node is cooling: rotate over the whole pool anyway. The
		// cooldowns expire on their own, but a hard failure here would take
		// down requests that a recovered node could serve.
		m.logger.Warn("all sing-box nodes are cooling; rotating over the full pool")
		healthy = nodes
	}

	start := int((m.rrCursor.Add(1) - 1) % uint64(len(healthy)))
	dest := metadata.ParseSocksaddr(addr)
	var lastErr error
	for i := 0; i < rrDialAttempts && i < len(healthy); i++ {
		n := healthy[(start+i)%len(healthy)]
		ob, found := m.outbound(n.Tag)
		if !found {
			continue
		}
		c, dialErr := ob.DialContext(ctx, network, dest)
		if dialErr == nil {
			m.markNodeSuccess(n.Tag)
			return c, nil, true
		}
		m.markNodeFailure(n.Tag, dialErr)
		lastErr = dialErr
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("sing-box: no usable outbound in pool")
	}
	return nil, lastErr, true
}

// coolingTags returns, for the status endpoint, tags currently in cooldown
// along with when each expires. Kept tiny so the dashboard can badge nodes.
func (m *Manager) coolingTags() map[string]time.Time {
	health := m.healthSnapshot()
	now := time.Now()
	out := make(map[string]time.Time)
	for tag, h := range health {
		if h.cooling(now) {
			out[tag] = h.until
		}
	}
	return out
}
