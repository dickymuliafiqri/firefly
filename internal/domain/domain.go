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

// globalRotation is a process-wide monotonically increasing rotation counter
// used to seed the starting offset of key selection. It deliberately lives
// OUTSIDE the KeyRing so that rotation progress survives snapshot hot-swaps:
// every config/DB reload rebuilds the KeyRing (and would otherwise reset a
// per-ring cursor to 0), which — combined with frequent reloads triggered by
// usage metering bumping api_keys.updated_at — collapsed all traffic onto the
// first few slots. Anchoring the offset to a global counter keeps round-robin
// and least-inflight scans advancing evenly regardless of reload frequency.
var globalRotation atomic.Uint64

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
	// ProtocolGrokCLI is the Grok CLI / Grok Build wire format (OpenAI Responses API
	// on cli-chat-proxy.grok.com, authenticated with an xAI OAuth bearer token).
	ProtocolGrokCLI Protocol = "grok-cli"
	// ProtocolOpenCode is the OpenCode (Free and Go subscription) wire format
	// (opencode.ai, supporting chat/completions and responses endpoints with session isolation).
	ProtocolOpenCode Protocol = "opencode"
	// ProtocolOpenCodeGo is an alias for OpenCode Go subscription.
	ProtocolOpenCodeGo Protocol = "opencode-go"
	// ProtocolQoder is the Qoder IDE wire format (api3/api2.qoder.sh COSY-signed
	// agent_chat_generation endpoint, authenticated with a device/job/PAT token
	// harvested into the key ring; requests are WAF-encoded and COSY-signed).
	ProtocolQoder Protocol = "qoder"
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
	Ref               string
	APIKeyID          int64  // Database row ID in api_keys (if loaded from Turso)
	Secret            string // Resolved secret plaintext from ENV
	RPS               float64
	MaxConcurrent     int
	Inflight          atomic.Int64
	CooldownUntil     atomic.Int64 // Unix nanoseconds timestamp
	Revoked           atomic.Bool
	ConsecutiveErrors atomic.Int64
}

// String implements fmt.Stringer to ensure secrets are never printed.
func (ks *KeySlot) String() string {
	if ks == nil {
		return "<nil>"
	}
	return fmt.Sprintf("KeySlot{Ref:%s, RPS:%.2f, MaxConcurrent:%d, Inflight:%d, Revoked:%t, ConsecutiveErrors:%d}",
		ks.Ref, ks.RPS, ks.MaxConcurrent, ks.Inflight.Load(), ks.Revoked.Load(), ks.ConsecutiveErrors.Load())
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
		Ref               string  `json:"ref"`
		APIKeyID          int64   `json:"api_key_id,omitempty"`
		RPS               float64 `json:"rps,omitempty"`
		MaxConcurrent     int     `json:"max_concurrent,omitempty"`
		Inflight          int64   `json:"inflight"`
		CooldownUntil     int64   `json:"cooldown_until,omitempty"`
		Revoked           bool    `json:"revoked"`
		ConsecutiveErrors int64   `json:"consecutive_errors,omitempty"`
	}
	return json.Marshal(keySlotSafe{
		Ref:               ks.Ref,
		APIKeyID:          ks.APIKeyID,
		RPS:               ks.RPS,
		MaxConcurrent:     ks.MaxConcurrent,
		Inflight:          ks.Inflight.Load(),
		CooldownUntil:     ks.CooldownUntil.Load(),
		Revoked:           ks.Revoked.Load(),
		ConsecutiveErrors: ks.ConsecutiveErrors.Load(),
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
	kr := &KeyRing{
		Strategy: strategy,
		Slots:    slots,
		byRef:    byRef,
	}
	// Seed the starting cursor from the process-wide rotation counter so that
	// rotation progress SURVIVES snapshot hot-swaps. Every config/DB reload
	// rebuilds the KeyRing; if the cursor started at 0 each time, selection
	// would always restart at Slots[0]. Combined with frequent reloads (e.g.
	// usage metering bumping api_keys.updated_at), that collapsed all traffic
	// onto the first few keys. Seeding from globalRotation keeps each freshly
	// built ring picking up roughly where the previous generation left off,
	// while the per-ring cursor keeps selection deterministic within a single
	// ring's lifetime (which the round-robin unit tests rely on).
	kr.cursor.Store(globalRotation.Add(1))
	return kr
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

// ClearCooldowns releases the cooldown deadline on every slot in the ring and
// reports how many slots were actually serving one at nowNano. An already
// expired deadline is released too — nothing resets the field once it lapses —
// but it is not counted as live. Revocation is a separate state and is left
// untouched: a 401 condemns the credential, while a cooldown mirrors a condition
// that may no longer hold (a quota keyed to the egress IP that a WARP rotation
// has just replaced).
func (kr *KeyRing) ClearCooldowns(nowNano int64) int {
	if kr == nil {
		return 0
	}
	live := 0
	for _, slot := range kr.Slots {
		if slot == nil {
			continue
		}
		if until := slot.CooldownUntil.Swap(0); until > nowNano {
			live++
		}
	}
	return live
}

// ResetConsecutiveErrors clears the consecutive credential-error counter on every
// slot in the ring and reports how many slots were actually carrying one. The
// counter arms the KeyErrorThreshold deactivate/delete action, and the 429 that
// drove an automatic WARP rotation was charged to the slot even though the quota
// it reflects belongs to the egress IP the rotation just replaced — the same
// reasoning as ClearCooldowns. Revocation is left untouched: once the threshold
// action has fired it cannot be undone by resetting the counter that armed it.
func (kr *KeyRing) ResetConsecutiveErrors() int {
	if kr == nil {
		return 0
	}
	reset := 0
	for _, slot := range kr.Slots {
		if slot == nil {
			continue
		}
		if slot.ConsecutiveErrors.Swap(0) != 0 {
			reset++
		}
	}
	return reset
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

// KeyErrorRule binds a single upstream HTTP error status to a threshold-counted
// action executed against an individual key slot. E.g. {429 -> cooldown 5m} and
// {403 -> delete} both fire on threshold 1. Rules override the global
// KeyErrorThreshold/KeyErrorAction when a rule for that status code exists.
type KeyErrorRule struct {
	// StatusCode is the upstream HTTP error status (e.g. 401, 403, 429).
	StatusCode int `json:"status_code"`
	// Threshold is the number of consecutive errors of THIS status before the
	// action executes. 1 = fire immediately on the first such error.
	Threshold int `json:"threshold"`
	// Action is "deactivate" (default), "delete", or "cooldown".
	Action string `json:"action"`
	// CooldownDurationS is the cooldown length in seconds when Action == "cooldown".
	// Falls back to KeyCooldownDurationMs (or 300s) when <= 0.
	CooldownDurationS int `json:"cooldown_duration_s,omitempty"`
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

	// KeyErrorThreshold is the number of consecutive 4xx/quota errors before
	// triggering KeyErrorAction on an individual key slot. 0 = disabled.
	KeyErrorThreshold int
	// KeyErrorAction defines what happens when KeyErrorThreshold is reached:
	// "deactivate" (default), "delete", or "cooldown".
	KeyErrorAction string
	// KeyCooldownDurationMs is the cooldown duration in ms when KeyErrorAction is "cooldown".
	KeyCooldownDurationMs int
	// KeyErrorRules defines granular per-HTTP-status error handling rules (e.g. 429 -> cooldown 5m, 403 -> delete).
	// When a matching rule exists for an error code, it takes precedence over the global KeyErrorThreshold/KeyErrorAction.
	KeyErrorRules []KeyErrorRule

	// ProbeModel is the designated model to use for deep health and quota verification.
	// When empty, health checks fall back to reachability checks or registered catalog models.
	ProbeModel string

	// EgressMode defines the outbound transport routing for this upstream:
	// "direct" (default host interface), "warp" (built-in Cloudflare WARP),
	// or "proxy" (custom HTTP/SOCKS5 proxy URL).
	EgressMode string

	// ProxyURL is the target proxy endpoint when EgressMode is "proxy"
	// (e.g. "socks5://user:pass@host:port" or "http://host:port").
	ProxyURL string
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

	cursorInit atomic.Bool
	cursor     atomic.Uint64
}

// NextCursor increments and returns the cursor for round-robin routing.
//
// The cursor is lazily seeded from the process-wide globalRotation counter on
// first use so that rotation progress survives catalog hot-swaps: every reload
// rebuilds Combo values, and a per-combo cursor that started at 0 each time
// would always restart round-robin at Models[0]. Seeding from globalRotation
// lets a freshly rebuilt combo pick up roughly where the previous generation
// left off, while remaining deterministic within a single snapshot's lifetime.
func (c *Combo) NextCursor() uint64 {
	if c.cursorInit.CompareAndSwap(false, true) {
		c.cursor.Store(globalRotation.Add(1))
	}
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
	TenantStatusExhausted TenantStatus = "exhausted"
	TenantStatusExpired   TenantStatus = "expired"
)

// Tenant is a resolved tenant with its policy. APIKey is the primary plaintext lookup key.
// KeyHash is retained for backward-compatibility.
type Tenant struct {
	APIKey        string // Plaintext gateway API key (e.g. sk-gw-...)
	KeyHash       string // Deprecated: canonical sha256 lookup hash (fallback)
	Name          string
	Status        TenantStatus
	AllowedModels []string // ["*"] means all enabled models
	CredentialRef string   // optional override; empty = inherit from upstream
	RateLimit     RateLimit
	MaxTokens     int64         // 0 = unlimited, >0 = quota cap for (tokens_in + tokens_out)
	UsedTokens    *atomic.Int64 // In-memory atomic token usage counter
	ExpiresAt     int64         // Unix timestamp in ms; 0 = never expires
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

// IsActive reports whether the tenant is in active status.
func (t *Tenant) IsActive() bool {
	if t == nil {
		return false
	}
	return t.Status == TenantStatusActive
}

// IsExpired reports whether the tenant's expiration timestamp has passed.
// Accepts nowMs in milliseconds. Supports ExpiresAt in either seconds (< 1e11) or milliseconds (>= 1e11).
func (t *Tenant) IsExpired(nowMs int64) bool {
	if t == nil || t.ExpiresAt <= 0 {
		return false
	}
	expMs := t.ExpiresAt
	if expMs < 100_000_000_000 {
		expMs *= 1000
	}
	return nowMs > expMs
}

// IsQuotaExceeded reports whether the tenant has exhausted their max token quota.
func (t *Tenant) IsQuotaExceeded() bool {
	if t == nil || t.MaxTokens <= 0 || t.UsedTokens == nil {
		return false
	}
	return t.UsedTokens.Load() >= t.MaxTokens
}

// RemainingTokens returns the remaining token balance (0 if exhausted, -1 if unlimited).
func (t *Tenant) RemainingTokens() int64 {
	if t == nil || t.MaxTokens <= 0 {
		return -1
	}
	if t.UsedTokens == nil {
		return t.MaxTokens
	}
	rem := t.MaxTokens - t.UsedTokens.Load()
	if rem < 0 {
		return 0
	}
	return rem
}

// Target is the resolved routing decision for a single request: which upstream
// to call, under which upstream model name, with which credential.
type Target struct {
	Upstream      *Upstream
	UpstreamModel string
	CredentialRef string
	KeySlot       *KeySlot
}
