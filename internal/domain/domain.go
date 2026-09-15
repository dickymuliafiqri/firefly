// Package domain holds the pure, dependency-light core types of the gateway.
// It must not import net/http or encoding/json at the type level; transport
// and config layers translate into these types.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"
)

// ErrAllKeysExhausted is returned when all key slots in a KeyRing are unavailable
// (all in cooldown, concurrency saturated, or revoked).
var ErrAllKeysExhausted = errors.New("all keys exhausted")

// Protocol identifies the wire dialect spoken by an upstream. It is the
// expansion point for non-OpenAI backends.
type Protocol string

const (
	// ProtocolUnknown is the zero value. An upstream with this protocol is
	// invalid and must be rejected by validation.
	ProtocolUnknown Protocol = ""
	// ProtocolOpenAI is the native OpenAI-compatible wire format.
	ProtocolOpenAI Protocol = "openai"
	// ProtocolAnthropic is the native Anthropic Messages wire format.
	ProtocolAnthropic Protocol = "anthropic"
	// ProtocolAntigravity is the Google Cloud Code / Antigravity wire format.
	ProtocolAntigravity Protocol = "antigravity"
	// ProtocolCline is the Cline API wire format.
	ProtocolCline Protocol = "cline"
	// ProtocolCodeBuddyCN is the CodeBuddy China (copilot.tencent.com) wire format.
	ProtocolCodeBuddyCN Protocol = "codebuddy-cn"
	// ProtocolCodeBuddyIntl is the CodeBuddy International (codebuddy.ai) wire format.
	ProtocolCodeBuddyIntl Protocol = "codebuddy-intl"
)


// KeyStrategy defines how a KeyRing selects credentials from its pool.
type KeyStrategy string

const (
	KeyStrategyUnknown       KeyStrategy = ""
	KeyStrategyRoundRobin    KeyStrategy = "round_robin"
	KeyStrategyLeastInflight KeyStrategy = "least_inflight"
)

// KeySlot represents one API key/credential within a KeyRing pool.
// It tracks in-flight concurrency and cooldown status atomically.
type KeySlot struct {
	Ref           string
	Secret        string // Resolved secret plaintext from ENV
	RPS           float64
	MaxConcurrent int
	Inflight      atomic.Int64
	CooldownUntil atomic.Int64 // Unix nanoseconds timestamp
	Revoked       atomic.Bool
}

// String implements fmt.Stringer to ensure secrets are never printed.
func (ks *KeySlot) String() string {
	if ks == nil {
		return "<nil>"
	}
	return fmt.Sprintf("KeySlot{Ref:%s, RPS:%.2f, MaxConcurrent:%d, Inflight:%d, Revoked:%t}",
		ks.Ref, ks.RPS, ks.MaxConcurrent, ks.Inflight.Load(), ks.Revoked.Load())
}

// Format implements fmt.Formatter to mask ks.Secret for all verbs (%v, %+v, %#v, %s, %q).
func (ks *KeySlot) Format(f fmt.State, verb rune) {
	if ks == nil {
		_, _ = fmt.Fprint(f, "<nil>")
		return
	}
	switch verb {
	case 's', 'v':
		if f.Flag('+') || f.Flag('#') {
			_, _ = fmt.Fprintf(f, "KeySlot{Ref:%q, Secret:\"[REDACTED]\", RPS:%.2f, MaxConcurrent:%d, Inflight:%d, CooldownUntil:%d, Revoked:%t}",
				ks.Ref, ks.RPS, ks.MaxConcurrent, ks.Inflight.Load(), ks.CooldownUntil.Load(), ks.Revoked.Load())
		} else {
			_, _ = fmt.Fprintf(f, "KeySlot(%s)", ks.Ref)
		}
	case 'q':
		_, _ = fmt.Fprintf(f, "%q", ks.String())
	default:
		_, _ = fmt.Fprintf(f, "KeySlot(%s)", ks.Ref)
	}
}

// MarshalJSON implements json.Marshaler to ensure secrets are never serialized into JSON.
func (ks *KeySlot) MarshalJSON() ([]byte, error) {
	if ks == nil {
		return []byte("null"), nil
	}
	type keySlotSafe struct {
		Ref           string  `json:"ref"`
		RPS           float64 `json:"rps,omitempty"`
		MaxConcurrent int     `json:"max_concurrent,omitempty"`
		Inflight      int64   `json:"inflight"`
		CooldownUntil int64   `json:"cooldown_until,omitempty"`
		Revoked       bool    `json:"revoked"`
	}
	return json.Marshal(keySlotSafe{
		Ref:           ks.Ref,
		RPS:           ks.RPS,
		MaxConcurrent: ks.MaxConcurrent,
		Inflight:      ks.Inflight.Load(),
		CooldownUntil: ks.CooldownUntil.Load(),
		Revoked:       ks.Revoked.Load(),
	})
}

// IsInCooldown reports whether the key slot is currently in cooldown.
func (ks *KeySlot) IsInCooldown(nowNano int64) bool {
	until := ks.CooldownUntil.Load()
	return until > 0 && nowNano < until
}

// IsAvailable reports whether the key slot is eligible to take traffic.
func (ks *KeySlot) IsAvailable(nowNano int64) bool {
	if ks.Revoked.Load() {
		return false
	}
	if ks.IsInCooldown(nowNano) {
		return false
	}
	if ks.MaxConcurrent > 0 && ks.Inflight.Load() >= int64(ks.MaxConcurrent) {
		return false
	}
	return true
}

// KeyRing holds a collection of credential slots for an upstream and tracks cursor.
type KeyRing struct {
	Strategy KeyStrategy
	Slots    []*KeySlot
	byRef    map[string]*KeySlot
	cursor   atomic.Uint64
}

// NewKeyRing constructs a KeyRing with the given strategy and slots.
func NewKeyRing(strategy KeyStrategy, slots []*KeySlot) *KeyRing {
	if strategy == "" {
		strategy = KeyStrategyRoundRobin
	}
	byRef := make(map[string]*KeySlot, len(slots))
	for _, s := range slots {
		if s != nil {
			byRef[s.Ref] = s
		}
	}
	return &KeyRing{
		Strategy: strategy,
		Slots:    slots,
		byRef:    byRef,
	}
}

// SlotCount returns the number of slots in the ring.
func (kr *KeyRing) SlotCount() int {
	if kr == nil {
		return 0
	}
	return len(kr.Slots)
}

// PrimarySlot returns the primary (first) slot in the ring, or nil if empty.
func (kr *KeyRing) PrimarySlot() *KeySlot {
	if kr == nil || len(kr.Slots) == 0 {
		return nil
	}
	return kr.Slots[0]
}

// SlotByRef returns the slot with the given ref, or nil if not found.
func (kr *KeyRing) SlotByRef(ref string) *KeySlot {
	if kr == nil {
		return nil
	}
	return kr.byRef[ref]
}

// SelectKey picks an available KeySlot according to the configured Strategy.
// It is lock-free and allocation-free on the happy path.
func (kr *KeyRing) SelectKey(nowNano int64) (*KeySlot, error) {
	if kr == nil || len(kr.Slots) == 0 {
		return nil, ErrAllKeysExhausted
	}

	switch kr.Strategy {
	case KeyStrategyLeastInflight:
		return kr.selectLeastInflight(nowNano)
	default:
		return kr.selectRoundRobin(nowNano)
	}
}

func (kr *KeyRing) selectRoundRobin(nowNano int64) (*KeySlot, error) {
	n := len(kr.Slots)
	if n == 1 {
		slot := kr.Slots[0]
		if slot != nil && slot.IsAvailable(nowNano) {
			return slot, nil
		}
		return nil, ErrAllKeysExhausted
	}

	start := kr.cursor.Add(1) - 1
	for i := 0; i < n; i++ {
		idx := (start + uint64(i)) % uint64(n)
		slot := kr.Slots[idx]
		if slot != nil && slot.IsAvailable(nowNano) {
			return slot, nil
		}
	}
	return nil, ErrAllKeysExhausted
}

func (kr *KeyRing) selectLeastInflight(nowNano int64) (*KeySlot, error) {
	n := len(kr.Slots)
	if n == 1 {
		slot := kr.Slots[0]
		if slot != nil && slot.IsAvailable(nowNano) {
			return slot, nil
		}
		return nil, ErrAllKeysExhausted
	}

	start := kr.cursor.Add(1) - 1
	var bestSlot *KeySlot
	bestInflight := int64(math.MaxInt64)

	for i := 0; i < n; i++ {
		idx := (start + uint64(i)) % uint64(n)
		slot := kr.Slots[idx]
		if slot == nil || !slot.IsAvailable(nowNano) {
			continue
		}
		inflight := slot.Inflight.Load()
		if inflight < bestInflight {
			bestInflight = inflight
			bestSlot = slot
			if inflight == 0 {
				break
			}
		}
	}

	if bestSlot == nil {
		return nil, ErrAllKeysExhausted
	}
	return bestSlot, nil
}

// MarkCooldown sets a cooldown duration on a slot by ref, using CAS to ensure
// the latest/longest cooldown is preserved under concurrent calls.
func (kr *KeyRing) MarkCooldown(ref string, duration time.Duration, nowNano int64) {
	slot := kr.SlotByRef(ref)
	if slot == nil {
		return
	}
	if duration <= 0 {
		duration = 30 * time.Second
	}
	until := nowNano + duration.Nanoseconds()
	for {
		cur := slot.CooldownUntil.Load()
		if cur >= until {
			break
		}
		if slot.CooldownUntil.CompareAndSwap(cur, until) {
			break
		}
	}
}

// MarkRevoked marks the slot with the given ref as revoked.
func (kr *KeyRing) MarkRevoked(ref string) {
	slot := kr.SlotByRef(ref)
	if slot != nil {
		slot.Revoked.Store(true)
	}
}

// Capabilities describes what a model supports. All fields default to false.
type Capabilities struct {
	Stream     bool
	Tools      bool
	Vision     bool
	JSONMode   bool
	Embeddings bool
	Audio      bool
}

// Upstream is a fully resolved backend definition. All defaults are applied
// and the credential has been resolved to its ENV name (never its value).
type Upstream struct {
	Name                string
	Protocol            Protocol
	BaseURL             string
	BaseURLs            []string
	CredentialRef       string
	KeyStrategy         KeyStrategy
	KeyRing             *KeyRing
	TimeoutMs           int
	IdleTimeoutMs       int
	MaxIdleConnsPerHost int
	MaxConnsPerHost     int
	ExtraHeaders        map[string]string
	AllowInsecure       bool

	// StreamIdleTimeoutMs is the maximum duration a streaming response may go
	// WITHOUT delivering a byte before the relay aborts the stream. It bounds a
	// stalled upstream so a hung SSE stream cannot pin a goroutine, a connection,
	// and a credential concurrency slot forever. 0 means "unset" and the
	// translation layer substitutes a default. This is distinct from TimeoutMs
	// (time-to-first-byte) and IdleTimeoutMs (keep-alive pooling).
	StreamIdleTimeoutMs int

	// CredentialRPS and CredentialMaxConcurrent are the explicit ceilings for
	// this credential, guarding against multiple tenants on one upstream key
	// collectively exceeding the provider's aggregate limit. Zero = unbounded.
	CredentialRPS           float64
	CredentialMaxConcurrent int
	Disabled                bool
	Inflight                atomic.Int64
}

// RoutingStrategy dictates how requests for a model are distributed across candidate upstreams.
type RoutingStrategy string

const (
	// RoutingStrategyFailover routes to primary upstream, falling back only on failure/breaker trip.
	RoutingStrategyFailover RoutingStrategy = "failover"
	// RoutingStrategyRoundRobin distributes requests across all healthy candidate upstreams evenly.
	RoutingStrategyRoundRobin RoutingStrategy = "round_robin"
	// RoutingStrategyLeastInflight routes to the candidate upstream with the fewest active requests.
	RoutingStrategyLeastInflight RoutingStrategy = "least_inflight"
)

// URLForAttempt returns the base URL for the given attempt index.
// If BaseURLs contains multiple URLs, it cycles through them deterministically.
// Otherwise it returns BaseURL.
func (u *Upstream) URLForAttempt(attempt int) string {
	if u == nil {
		return ""
	}
	if len(u.BaseURLs) > 0 {
		idx := attempt % len(u.BaseURLs)
		if idx < 0 {
			idx = -idx
		}
		return strings.TrimRight(u.BaseURLs[idx], "/")
	}
	return strings.TrimRight(u.BaseURL, "/")
}

// ModelEntry is a single entry in the public model catalog.
type ModelEntry struct {
	PublicName    string
	Upstream      string // Upstream.Name
	UpstreamModel string
	Capabilities  Capabilities
	MaxContext    int
	Enabled       bool
}

// Combo is a virtual model that distributes requests across a group of concrete models.
type Combo struct {
	Name     string
	Strategy RoutingStrategy
	Models   []string // concrete model public names
	Enabled  bool
	cursor   atomic.Uint64
}

// NextCursor increments and returns the cursor for round-robin routing.
func (c *Combo) NextCursor() uint64 {
	return c.cursor.Add(1) - 1
}

// RateLimit is a per-tenant token-bucket + concurrency policy.
// RPS == 0 means "no rate limit" (explicit opt-out).
// MaxConcurrent == 0 means "unlimited" (discouraged).
type RateLimit struct {
	RPS           float64
	Burst         int
	MaxConcurrent int
}

// TenantStatus enumerates tenant lifecycle states.
type TenantStatus string

const (
	TenantStatusUnknown   TenantStatus = ""
	TenantStatusActive    TenantStatus = "active"
	TenantStatusSuspended TenantStatus = "suspended"
)

// Tenant is a resolved tenant with its policy. The plaintext key is never
// stored; KeyHash is the canonical lookup key.
type Tenant struct {
	KeyHash       string
	Name          string
	Status        TenantStatus
	AllowedModels []string // ["*"] means all enabled models
	CredentialRef string   // optional override; empty = inherit from upstream
	RateLimit     RateLimit
	Metadata      map[string]string
}

// AllowsModel reports whether the tenant may use the given public model name.
func (t Tenant) AllowsModel(publicName string) bool {
	for _, m := range t.AllowedModels {
		if m == "*" || m == publicName {
			return true
		}
	}
	return false
}

// Target is the resolved routing decision for a single request: which upstream
// to call, under which upstream model name, with which credential.
type Target struct {
	Upstream      *Upstream
	UpstreamModel string
	CredentialRef string
	KeySlot       *KeySlot
}
