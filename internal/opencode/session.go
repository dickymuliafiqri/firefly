package opencode

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

const (
	HeaderSession = "x-opencode-session"
	HeaderRequest = "x-opencode-request"
	HeaderClient  = "x-opencode-client"
	HeaderProject = "x-opencode-project"

	maxSessionLength = 256
)

// ResolveSessionID extracts or deterministically generates the mandatory x-opencode-session header.
// If the inbound request already provided x-opencode-session, it is trimmed, length-validated, and reused.
// Otherwise, an opaque, deterministic hash is generated: ses_<32-hex-characters>.
func ResolveSessionID(h http.Header, tenant, reqID string) string {
	if h != nil {
		if raw := h.Get(HeaderSession); raw != "" {
			trimmed := strings.TrimSpace(raw)
			if len(trimmed) > 0 && len(trimmed) <= maxSessionLength {
				return trimmed
			}
		}
	}

	// Deterministic derivation so the same client conversation maintains session continuity.
	hasher := sha256.New()
	hasher.Write([]byte("opencode-session\x00"))
	if tenant != "" {
		hasher.Write([]byte(tenant))
	} else {
		hasher.Write([]byte("generic"))
	}
	hasher.Write([]byte("\x00"))
	if reqID != "" {
		hasher.Write([]byte(reqID))
	} else {
		hasher.Write([]byte("default"))
	}

	digest := hex.EncodeToString(hasher.Sum(nil))
	if len(digest) > 32 {
		digest = digest[:32]
	}
	return "ses_" + digest
}

// GenerateRequestID creates a pseudo-random request identifier in the OpenCode format (msg_<32-hex-chars>).
func GenerateRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "msg_" + hex.EncodeToString(b[:])
}
