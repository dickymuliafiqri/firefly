// Package logging provides structured-logging helpers for the gateway: a slog
// handler that redacts secrets (and optionally message bodies) before they are
// encoded, plus the logger constructor that wires it in.
//
// Redaction happens at the HANDLER level, not the call site, so a future log
// statement that forgets to strip a header still cannot leak an API key. The
// wrapper runs before the inner handler encodes, so it applies uniformly to the
// JSON handler (and any handler we adopt later).
package logging

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
)

// redactedKeyPrefixes are attribute keys whose values must never be logged.
// Matching is case-insensitive on the key name.
var redactedKeys = map[string]struct{}{
	"authorization":       {},
	"auth":                {},
	"api_key":             {},
	"apikey":              {},
	"x-api-key":           {},
	"proxy-authorization": {},
	"cookie":              {},
	"set-cookie":          {},
	"password":            {},
	"secret":              {},
	"credential":          {},
	"token":               {},
	"access_token":        {},
	"admin-token":         {},
	"x-admin-token":       {},
	"bearer":              {},
}

// redactedValueMask is what a redacted value is replaced with.
const redactedValueMask = "[REDACTED]"

// sensitiveSubstrings catch keys that merely CONTAIN a sensitive token, e.g.
// "openai_api_key" or "upstream_token".
var sensitiveSubstrings = []string{
	"api_key", "apikey", "secret", "password", "credential", "_token", "-token", "authorization", "auth", "token",
}

// RedactHandler wraps a slog.Handler and scrubs sensitive attributes before they
// reach the inner handler. It is safe for concurrent use.
type RedactHandler struct {
	inner slog.Handler
	// RedactMessages, when true, replaces the log MESSAGE with a fixed string.
	// Intended only for handlers that might otherwise capture request bodies.
	RedactMessages bool
}

// NewRedactHandler wraps inner. Panics if inner is nil (programming error at
// construction, not a runtime condition).
func NewRedactHandler(inner slog.Handler) *RedactHandler {
	if inner == nil {
		panic("logging: NewRedactHandler: nil inner handler")
	}
	return &RedactHandler{inner: inner}
}

// Enabled delegates to the inner handler.
func (h *RedactHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle scrubs the record then forwards it. Group attributes are scrubbed
// recursively so `slog.Group("creds", "token", x)` cannot leak either.
func (h *RedactHandler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	if h.RedactMessages {
		nr = slog.NewRecord(r.Time, r.Level, "[redacted]", r.PC)
	}
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(scrub(a))
		return true
	})
	return h.inner.Handle(ctx, nr)
}

// WithAttrs scrubs the bound attributes and delegates.
func (h *RedactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	scrubbed := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		scrubbed[i] = scrub(a)
	}
	return &RedactHandler{inner: h.inner.WithAttrs(scrubbed), RedactMessages: h.RedactMessages}
}

// WithGroup delegates, preserving the wrapper so redaction keeps applying.
func (h *RedactHandler) WithGroup(name string) slog.Handler {
	return &RedactHandler{inner: h.inner.WithGroup(name), RedactMessages: h.RedactMessages}
}

// scrub redacts a single attribute, recursing into groups and collections. It returns a new
// Attr; the input is never mutated.
func scrub(a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		ga := a.Value.Group()
		out := make([]slog.Attr, len(ga))
		for i, sub := range ga {
			out[i] = scrub(sub)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, redactedValueMask)
	}
	if a.Value.Kind() == slog.KindString {
		str := a.Value.String()
		trimmed := strings.TrimSpace(str)
		if strings.HasPrefix(strings.ToLower(trimmed), "bearer ") && len(trimmed) > 7 {
			return slog.String(a.Key, "Bearer "+redactedValueMask)
		}
	}
	if a.Value.Kind() == slog.KindAny {
		switch v := a.Value.Any().(type) {
		case http.Header:
			scrubbed := make(http.Header, len(v))
			for k, vals := range v {
				if isSensitiveKey(k) {
					scrubbed[k] = []string{redactedValueMask}
				} else {
					scrubbed[k] = vals
				}
			}
			return slog.Any(a.Key, scrubbed)
		case map[string][]string:
			scrubbed := make(map[string][]string, len(v))
			for k, vals := range v {
				if isSensitiveKey(k) {
					scrubbed[k] = []string{redactedValueMask}
				} else {
					scrubbed[k] = vals
				}
			}
			return slog.Any(a.Key, scrubbed)
		case map[string]string:
			scrubbed := make(map[string]string, len(v))
			for k, val := range v {
				if isSensitiveKey(k) {
					scrubbed[k] = redactedValueMask
				} else {
					scrubbed[k] = val
				}
			}
			return slog.Any(a.Key, scrubbed)
		case map[string]any:
			scrubbed := make(map[string]any, len(v))
			for k, val := range v {
				if isSensitiveKey(k) {
					scrubbed[k] = redactedValueMask
				} else {
					scrubbed[k] = val
				}
			}
			return slog.Any(a.Key, scrubbed)
		}
	}
	return a
}

// isSensitiveKey reports whether a key looks like it holds a secret. The match
// is case-insensitive and also catches keys that only contain a sensitive token
// (e.g. "openai_api_key").
func isSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "" {
		return false
	}
	if _, ok := redactedKeys[k]; ok {
		return true
	}
	for _, s := range sensitiveSubstrings {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}
