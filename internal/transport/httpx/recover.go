package httpx

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"runtime/debug"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
)

// ErrNotHijackable is returned when the underlying writer does not implement http.Hijacker.
var ErrNotHijackable = errors.New("httpx: underlying ResponseWriter does not support hijacking")

// RecoverMiddleware converts a panic in a downstream handler into a 500 with an
// OpenAI-shaped error body, and logs the stack. It must wrap every handler.
func RecoverMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					// A panic may occur after the header is written (e.g. mid-
					// stream). In that case we can only log; the client will see
					// a truncated stream and detect it via the missing [DONE].
					logger.Error("panic recovered",
						"panic", fmt.Sprint(rec),
						"path", r.URL.Path,
						"request_id", RequestIDFrom(r.Context()),
						"stack", string(debug.Stack()),
					)
					if !headerWritten(w) {
						openai.WriteError(w, http.StatusInternalServerError, "internal_error", "internal server error")
					}
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// headerWritten reports whether the response header has been sent. We detect
// this via a statusRecorder wrapper when present; otherwise assume not written.
func headerWritten(w http.ResponseWriter) bool {
	if sr, ok := w.(*statusRecorder); ok {
		return sr.wroteHeader
	}
	return false
}

// IsNil reports whether v is nil, INCLUDING a typed-nil pointer stored in an
// interface. A plain `v == nil` check is false for an interface holding
// (*Registry)(nil), so a mis-wired dependency would sail past a guard and panic
// on first dereference deep in a handler. Detecting the typed-nil at the
// boundary turns that latent crash into a clean fail-closed response.
func IsNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}

// statusRecorder wraps http.ResponseWriter to capture status and bytes written,
// and preserves optional interfaces (Flusher, Hijacker, ReaderFrom) that
// streaming requires.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.wroteHeader {
		return // ignore duplicate WriteHeader
	}
	s.status = code
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.status = http.StatusOK
		s.wroteHeader = true
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

// Flush implements http.Flusher so SSE streaming works through the wrapper.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker when supported.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, ErrNotHijackable
}

// Committed reports whether the status line has been sent. Used by handlers to
// avoid writing a second error envelope after a response has started.
func (s *statusRecorder) Committed() bool { return s.wroteHeader }

// Unwrap exposes the underlying writer (Go 1.20+ ResponseController).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
