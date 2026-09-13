// Package httpx provides HTTP transport helpers: middleware, context carriers,
// and small utilities shared by handlers. It must not contain business logic.
package httpx

import (
	"context"
	"net/http"

	"github.com/dickymuliafiqri/firefly/internal/reqid"
)

type ctxKey int

const (
	ctxKeyTenant ctxKey = iota
	ctxKeyTarget
)

// RequestIDFrom returns the request id stored in ctx, or "".
func RequestIDFrom(ctx context.Context) string { return reqid.From(ctx) }

// WithRequestID stores id in ctx.
func WithRequestID(ctx context.Context, id string) context.Context { return reqid.With(ctx, id) }

// HeaderRequestID is the header carrying a client-supplied correlation id.
const HeaderRequestID = reqid.HeaderRequestID

// NewRequestID generates a random 128-bit hex id.
func NewRequestID() string { return reqid.New() }

// RequestIDMiddleware ensures every request has a correlation id, taken from the
// inbound header if present or generated otherwise, and echoes it back.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderRequestID)
		if id == "" {
			id = NewRequestID()
		}
		w.Header().Set(HeaderRequestID, id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}
