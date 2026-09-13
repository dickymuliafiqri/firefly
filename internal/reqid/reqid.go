// Package reqid carries the per-request correlation id through context without
// depending on any transport package. It is a leaf: httpx and openai both import
// it, so neither has to import the other just to share the request id.
package reqid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type ctxKey int

const ctxKeyRequestID ctxKey = 0

// HeaderRequestID is the header carrying a client-supplied correlation id.
const HeaderRequestID = "X-Request-Id"

// From returns the request id stored in ctx, or "".
func From(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

// With stores id in ctx.
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// New generates a random 128-bit hex id.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-unknown"
	}
	return hex.EncodeToString(b[:])
}
