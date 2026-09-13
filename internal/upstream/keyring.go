package upstream

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// ErrAllKeysExhausted is returned when all key slots in a KeyRing are unavailable
// (all in cooldown, concurrency saturated, or revoked).
var ErrAllKeysExhausted = domain.ErrAllKeysExhausted

// ErrNoKeysConfigured is returned when a KeyRing has no key slots configured.
var ErrNoKeysConfigured = errors.New("no keys configured")

const (
	// DefaultCooldownDuration is applied when a 429 response omits Retry-After or has an invalid value.
	DefaultCooldownDuration = 30 * time.Second

	// MaxCooldownDuration caps any parsed Retry-After value to prevent upstream misbehavior.
	MaxCooldownDuration = 5 * time.Minute
)

// KeyRing wraps a domain.KeyRing with high-throughput selection,
// lazy cooldown management, and HTTP 429/401 penalty handling.
type KeyRing struct {
	*domain.KeyRing
}

// NewKeyRing constructs a KeyRing with the given strategy and slots.
func NewKeyRing(strategy domain.KeyStrategy, slots []*domain.KeySlot) *KeyRing {
	return &KeyRing{
		KeyRing: domain.NewKeyRing(strategy, slots),
	}
}

// FromDomain creates a KeyRing wrapping an existing domain.KeyRing.
func FromDomain(ring *domain.KeyRing) *KeyRing {
	if ring == nil {
		return nil
	}
	return &KeyRing{KeyRing: ring}
}

// SelectKey picks an available KeySlot using the real wall clock time.
// It is lock-free and allocation-free on the happy path.
func (kr *KeyRing) SelectKey() (*domain.KeySlot, error) {
	return kr.SelectKeyAt(time.Now().UnixNano())
}

// SelectKeyAt picks an available KeySlot evaluated at nowNano.
// Returns ErrAllKeysExhausted if all slots are in cooldown, saturated, or revoked.
func (kr *KeyRing) SelectKeyAt(nowNano int64) (*domain.KeySlot, error) {
	if kr == nil || kr.KeyRing == nil || len(kr.Slots) == 0 {
		return nil, ErrAllKeysExhausted
	}
	return kr.KeyRing.SelectKey(nowNano)
}

// MarkCooldown sets a cooldown penalty on the slot with the given ref using the wall clock.
func (kr *KeyRing) MarkCooldown(ref string, duration time.Duration) {
	kr.MarkCooldownAt(ref, duration, time.Now().UnixNano())
}

// MarkCooldownAt sets a cooldown penalty on the slot with the given ref evaluated at nowNano.
// It is thread-safe and preserves the longest cooldown under concurrent calls.
func (kr *KeyRing) MarkCooldownAt(ref string, duration time.Duration, nowNano int64) {
	if kr == nil || kr.KeyRing == nil {
		return
	}
	kr.KeyRing.MarkCooldown(ref, duration, nowNano)
}

// MarkRevoked permanently revokes a key slot (e.g. on HTTP 401 Invalid Key)
// until the next configuration reload.
func (kr *KeyRing) MarkRevoked(ref string) {
	if kr == nil || kr.KeyRing == nil {
		return
	}
	kr.KeyRing.MarkRevoked(ref)
}

// Handle429 parses the Retry-After header and places the key slot into cooldown.
// It returns the effective cooldown duration applied.
func (kr *KeyRing) Handle429(ref string, retryAfterHeader string) time.Duration {
	d := ParseRetryAfter(retryAfterHeader, DefaultCooldownDuration, MaxCooldownDuration)
	kr.MarkCooldown(ref, d)
	return d
}

// Handle401 marks the key slot as permanently revoked.
func (kr *KeyRing) Handle401(ref string) {
	kr.MarkRevoked(ref)
}

// ParseRetryAfter parses an HTTP Retry-After header value according to RFC 7231 Section 7.1.3.
// It supports integer seconds, decimal seconds, and HTTP-date (RFC 1123, RFC 850, ANSI C).
// If the header is missing, invalid, or non-positive, defaultDuration is returned.
// Any parsed duration is capped by maxDuration (if maxDuration > 0).
func ParseRetryAfter(header string, defaultDuration, maxDuration time.Duration) time.Duration {
	if defaultDuration <= 0 {
		defaultDuration = DefaultCooldownDuration
	}
	if maxDuration <= 0 {
		maxDuration = MaxCooldownDuration
	}
	header = strings.TrimSpace(header)
	if header == "" {
		return defaultDuration
	}

	// 1. Try parsing as integer seconds
	if secs, err := strconv.ParseInt(header, 10, 64); err == nil {
		if secs <= 0 {
			return defaultDuration
		}
		d := time.Duration(secs) * time.Second
		if d > maxDuration {
			return maxDuration
		}
		return d
	}

	// 1b. Try parsing as decimal seconds (e.g. "1.5")
	if fSecs, err := strconv.ParseFloat(header, 64); err == nil {
		if fSecs <= 0 {
			return defaultDuration
		}
		d := time.Duration(fSecs * float64(time.Second))
		if d > maxDuration {
			return maxDuration
		}
		return d
	}

	// 2. Try parsing as HTTP-date (RFC 1123, RFC 850, ANSI C)
	if t, err := http.ParseTime(header); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return defaultDuration
		}
		if d > maxDuration {
			return maxDuration
		}
		return d
	}

	return defaultDuration
}
