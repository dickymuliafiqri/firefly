package opencode

import (
	"net/http"
	"strings"
	"sync"

	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
)

const (
	HeaderSession = "x-opencode-session"
	HeaderRequest = "x-opencode-request"
	HeaderClient  = "x-opencode-client"
	HeaderProject = "x-opencode-project"

	maxSessionLength = 256
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

// GenerateDescendingID creates a timestamp-based 26-character ID identical to the official
// OpenCode client: 12 hex characters from ~(timestamp_ms*0x1000 + counter) and 14 base62 random chars.
func GenerateDescendingID() string {
	return upstream.GenerateOpenCodeTimestampID(true)
}

// GenerateAscendingID creates a timestamp-based 26-character ID identical to the official
// OpenCode client: 12 hex characters from (timestamp_ms*0x1000 + counter) and 14 base62 random chars.
func GenerateAscendingID() string {
	return upstream.GenerateOpenCodeTimestampID(false)
}

// GenerateSessionID creates a new canonical OpenCode session identifier (ses_<26-chars>).
func GenerateSessionID() string {
	return upstream.GenerateOpenCodeSessionID()
}

// GenerateRequestID creates a new canonical OpenCode request identifier (msg_<26-chars>).
func GenerateRequestID() string {
	return upstream.GenerateOpenCodeRequestID()
}

var (
	sessionMu    sync.RWMutex
	sessionCache = make(map[string]string)
)

func getCachedSession(key string) string {
	if key == "" {
		return GenerateSessionID()
	}
	sessionMu.RLock()
	s, ok := sessionCache[key]
	sessionMu.RUnlock()
	if ok {
		return s
	}

	sessionMu.Lock()
	defer sessionMu.Unlock()
	if s, ok = sessionCache[key]; ok {
		return s
	}
	if len(sessionCache) >= 1000 {
		// Evict an arbitrary entry
		for k := range sessionCache {
			delete(sessionCache, k)
			break
		}
	}
	s = GenerateSessionID()
	sessionCache[key] = s
	return s
}

// ResolveSessionID extracts or deterministically caches the mandatory x-opencode-session header.
// Matches 9router's _getOcSession: if a valid canonical ses_<26> ID is supplied, it is used.
// Otherwise, a valid timestamped session ID is generated with Date.now() and cached by key
// (inbound session, connection, or tenant) to ensure OpenCode Console timestamp validation passes.
func ResolveSessionID(h http.Header, tenant, reqID string) string {
	if h != nil {
		for _, key := range []string{HeaderSession, "x-session-id", "session-id"} {
			if raw := h.Get(key); raw != "" {
				trimmed := strings.TrimSpace(raw)
				if IsValidSessionID(trimmed) {
					return trimmed
				}
				if len(trimmed) > 0 {
					return getCachedSession(trimmed)
				}
			}
		}
	}

	if tenant != "" {
		return getCachedSession(tenant)
	}

	return GenerateSessionID()
}
