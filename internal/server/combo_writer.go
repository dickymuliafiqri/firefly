package server

import (
	"bytes"
	"net/http"
	"sync"
)

// comboMaxErrorBuffer caps how much of an uncommitted error body a
// comboAttemptWriter holds. A provider that answers a 5xx with an HTML page can
// otherwise make one failed combo attempt allocate megabytes right before the
// attempt is thrown away. Anything past the cap is dropped, and the cap is only
// ever reached for a response that never becomes visible to the client.
const comboMaxErrorBuffer = 64 << 10

// comboAttemptWriter is the shadow ResponseWriter for one combo attempt.
//
// It exists because a combo member can only be abandoned while the client has
// seen nothing: fallback after a commit would splice two responses together.
// Success is therefore permitted immediately — the first byte of a < 400
// response goes straight to the client, which is what keeps SSE token delivery
// as fast as it was before — while an error response is buffered so it can be
// discarded when the next member is tried.
//
// Header is shadow too. A provider's Content-Type, X-Request-Id or cookie
// written while the attempt was still buffered must not leak into the response
// of the member that finally succeeded, so headers are copied to the real
// writer only at Permit.
type comboAttemptWriter struct {
	dst http.ResponseWriter

	mu        sync.Mutex
	hdr       http.Header
	status    int
	buf       bytes.Buffer
	permitted bool
	discarded bool
}

func newComboAttemptWriter(dst http.ResponseWriter) *comboAttemptWriter {
	return &comboAttemptWriter{dst: dst, hdr: make(http.Header)}
}

// Header returns the shadow header map. It is applied to dst only on Permit.
func (w *comboAttemptWriter) Header() http.Header {
	return w.hdr
}

// WriteHeader records the status while uncommitted. A status >= 400 keeps the
// attempt discardable, so its body is still buffered instead of streamed.
func (w *comboAttemptWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.permitted {
		w.dst.WriteHeader(status)
		return
	}
	if w.discarded || w.status != 0 {
		return
	}
	w.status = status
}

// Write implements io.Writer. A response that is already known to fail keeps
// buffering (bounded by comboMaxErrorBuffer); anything else commits the attempt
// and becomes a pass-through.
func (w *comboAttemptWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.discarded {
		// The attempt is already abandoned; report success so the caller stops
		// writing instead of seeing a spurious error.
		return len(p), nil
	}
	if w.permitted {
		return w.dst.Write(p)
	}
	if w.status >= 400 {
		if remaining := comboMaxErrorBuffer - w.buf.Len(); remaining > 0 {
			if len(p) <= remaining {
				w.buf.Write(p)
			} else {
				w.buf.Write(p[:remaining])
			}
		}
		return len(p), nil
	}

	w.permitLocked()
	return w.dst.Write(p)
}

// Flush implements http.Flusher. Flushing a healthy attempt commits it (SSE must
// not be held back), and flushing an error attempt is a no-op because nothing
// has been written to the client yet.
func (w *comboAttemptWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.discarded {
		return
	}
	if w.permitted {
		flushIfPossible(w.dst)
		return
	}
	if w.status >= 400 {
		return
	}
	w.permitLocked()
	flushIfPossible(w.dst)
}

// Committed reports whether bytes are on their way to the client. Adapters that
// detect this interface use it to decide whether an error can still be turned
// into a clean status code; the combo attempt loop uses it to refuse a retry
// that would corrupt a partially delivered response.
func (w *comboAttemptWriter) Committed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.permitted
}

// Status reports the status this attempt produced, or 0 when it produced none.
// The adapter layer returns nil after relaying an upstream error body, so this
// is the only reliable way for the attempt loop to learn that the attempt
// failed.
func (w *comboAttemptWriter) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

// Permit commits the attempt: shadow headers are copied to the real writer, the
// recorded status is applied and any buffered body is replayed. It is
// idempotent, and it is refused after Discard.
func (w *comboAttemptWriter) Permit() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.permitted || w.discarded {
		return
	}
	w.permitLocked()
}

func (w *comboAttemptWriter) permitLocked() {
	w.permitted = true
	dstHdr := w.dst.Header()
	for key, values := range w.hdr {
		dstHdr[key] = values
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.dst.WriteHeader(w.status)
	if w.buf.Len() > 0 {
		_, _ = w.dst.Write(w.buf.Bytes())
	}
	w.buf.Reset()
}

// Discard abandons the attempt: the buffered body, the shadow headers and the
// recorded status are dropped and never reach the client. It is a no-op once
// the attempt was committed, because a committed response cannot be taken back.
func (w *comboAttemptWriter) Discard() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.permitted || w.discarded {
		return
	}
	w.discarded = true
	w.buf.Reset()
	w.hdr = make(http.Header)
}

func flushIfPossible(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
