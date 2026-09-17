package opencode

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	HeaderSession = "x-opencode-session"
	HeaderRequest = "x-opencode-request"
	HeaderClient  = "x-opencode-client"
	HeaderProject = "x-opencode-project"

	maxSessionLength = 256

	// opencodeIDAlphabet is base62 used by official OpenCode client for request and session IDs.
	opencodeIDAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// IsValidSessionID reports whether s matches the OpenCode canonical session format:
// ^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$ (30 characters total).
func IsValidSessionID(s string) bool {
	if len(s) != 30 || !strings.HasPrefix(s, "ses_") {
		return false
	}
	for i := 4; i < 16; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	for i := 16; i < 30; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

// deriveCanonicalID derives a deterministic canonical OpenCode ID matching
// ^(ses_|msg_)[0-9a-f]{12}[0-9A-Za-z]{14}$ from an arbitrary seed.
// The first 6 bytes of sha256(seed) provide 12 lowercase hex characters,
// and the next 14 bytes are mapped to base62.
func deriveCanonicalID(prefix, seed string) string {
	sum := sha256.Sum256([]byte(seed))
	val := int64(sum[0])<<40 | int64(sum[1])<<32 | int64(sum[2])<<24 | int64(sum[3])<<16 | int64(sum[4])<<8 | int64(sum[5])
	hexPart := fmt.Sprintf("%012x", val&0xFFFFFFFFFFFF)
	var base62Part [14]byte
	for i := 0; i < 14; i++ {
		base62Part[i] = opencodeIDAlphabet[int(sum[6+i])%len(opencodeIDAlphabet)]
	}
	return prefix + hexPart + string(base62Part[:])
}

func generateTimestampID(descending bool) string {
	now := time.Now().UnixMilli()
	var rnd [2]byte
	_, _ = rand.Read(rnd[:])
	counter := (int64(rnd[0])<<8 | int64(rnd[1]))&0x0FFF + 1
	var value int64
	if descending {
		value = ^(now*0x1000 + counter)
	} else {
		value = now*0x1000 + counter
	}

	var randBytes [14]byte
	_, _ = rand.Read(randBytes[:])
	var base62 [14]byte
	for i := range base62 {
		base62[i] = opencodeIDAlphabet[int(randBytes[i])%len(opencodeIDAlphabet)]
	}
	return fmt.Sprintf("%012x%s", value&0xFFFFFFFFFFFF, string(base62[:]))
}

// GenerateDescendingID creates a timestamp-based 26-character ID identical to the official
// OpenCode client: 12 hex characters from ~(timestamp_ms*0x1000 + counter) and 14 base62 random chars.
func GenerateDescendingID() string {
	return generateTimestampID(true)
}

// GenerateAscendingID creates a timestamp-based 26-character ID identical to the official
// OpenCode client: 12 hex characters from (timestamp_ms*0x1000 + counter) and 14 base62 random chars.
func GenerateAscendingID() string {
	return generateTimestampID(false)
}

// GenerateSessionID creates a new canonical OpenCode session identifier (ses_<26-chars>).
func GenerateSessionID() string {
	return "ses_" + GenerateDescendingID()
}

// GenerateRequestID creates a new canonical OpenCode request identifier (msg_<26-chars>).
func GenerateRequestID() string {
	return "msg_" + GenerateAscendingID()
}

// ResolveSessionID extracts or deterministically generates the mandatory x-opencode-session header.
// If the inbound request provides a session header matching the canonical OpenCode format,
// it is preserved. If an inbound session is provided with a non-canonical format, it is
// deterministically mapped to a valid canonical session ID to prevent upstream 403 FreeTierError.
// Otherwise, a deterministic session ID is derived from tenant and reqID, or freshly generated.
func ResolveSessionID(h http.Header, tenant, reqID string) string {
	if h != nil {
		if raw := h.Get(HeaderSession); raw != "" {
			trimmed := strings.TrimSpace(raw)
			if IsValidSessionID(trimmed) {
				return trimmed
			}
			if len(trimmed) > 0 && len(trimmed) <= maxSessionLength {
				return deriveCanonicalID("ses_", "opencode-inbound\x00"+trimmed)
			}
		}
	}

	if tenant != "" || reqID != "" {
		seed := "opencode-session\x00" + tenant + "\x00" + reqID
		return deriveCanonicalID("ses_", seed)
	}

	return GenerateSessionID()
}
