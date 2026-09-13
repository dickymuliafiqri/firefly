// package openai (headers.go): request header scrubbing and context helpers.

package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/reqid"
)

// hopByHopHeaders are connection-scoped and must never be forwarded upstream.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"proxy-connection":    true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// scrubbedHeaders are inbound headers we must NEVER forward: the client's auth
// (we substitute our own upstream credential) and headers that describe the
// client connection rather than the payload.
var scrubbedHeaders = map[string]bool{
	"authorization":  true, // replaced with the upstream secret
	"host":           true, // http.Client sets Host from the URL
	"content-length": true, // recomputed after model rewrite
	"x-api-key":      true, // some OpenAI-compatible vendors use this; scrub it
	"api-key":        true, // Azure OpenAI header
}

// copyUpstreamHeaders copies client headers to the upstream request, dropping
// hop-by-hop and credential headers. It lowercases for matching but preserves
// the original canonical key when setting.
func copyUpstreamHeaders(dst, src http.Header) {
	for k, vals := range src {
		lk := strings.ToLower(k)
		if hopByHopHeaders[lk] || scrubbedHeaders[lk] {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

// writeUpstreamError relays an upstream error response. If the body already
// looks like an OpenAI error envelope, it is passed through verbatim (preserving
// the upstream's message and code); otherwise the body is wrapped in our
// envelope so clients always see a consistent shape.
func writeUpstreamError(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	if looksLikeAPIError(body) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	// Cap message length to avoid relaying huge HTML error pages.
	if len(msg) > 512 {
		msg = msg[:512] + "…"
	}
	WriteError(w, status, StatusToType(status), msg)
}

// looksLikeAPIError reports whether body parses as {"error": {...}}.
func looksLikeAPIError(body []byte) bool {
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return len(probe.Error) > 0
}

// requestID pulls the gateway request id from ctx for upstream correlation.
func requestID(ctx context.Context) string { return reqid.From(ctx) }
