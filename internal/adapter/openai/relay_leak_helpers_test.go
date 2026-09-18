package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs goleak for the whole openai package. The streaming relay spawns
// a watchdog goroutine per stream, so a missed stop() on ANY path (normal end,
// idle abort, client disconnect) would leak a goroutine and, transitively, pin
// the upstream connection. goleak turns that from a slow production bleed into a
// hard test failure.
//
// We ignore only the net/http/httptest background dial goroutines that outlive a
// test by a scheduling tick; the relay's own goroutines are deliberately NOT
// ignored, which is the whole point.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	)
}

// leakUpstream is a fake upstream that streams a configurable number of SSE
// events, then either ends cleanly, stalls forever (to exercise the idle
// watchdog), or holds the connection open with no bytes at all.
type leakUpstream struct {
	events       int
	stallAfter   bool          // block forever after `events`, before [DONE]
	blockFirst   chan struct{} // if non-nil, hold the response with zero bytes
	stallRelease chan struct{} // closed by the test to release a stalled handler
	mu           sync.Mutex
	served       int
}

func (u *leakUpstream) handler(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.served++
	u.mu.Unlock()

	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flush", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")

	if u.blockFirst != nil {
		w.WriteHeader(http.StatusOK)
		f.Flush()
		select {
		case <-u.blockFirst:
		case <-r.Context().Done():
		}
		return
	}

	w.WriteHeader(http.StatusOK)
	for i := 0; i < u.events; i++ {
		if _, err := io.WriteString(w, "data: {\"i\":"+itoa(i)+"}\n\n"); err != nil {
			return // client went away
		}
		f.Flush()
		if r.Context().Err() != nil {
			return
		}
	}
	if u.stallAfter {
		select {
		case <-u.stallRelease: // test lets us finish
		case <-r.Context().Done():
		}
		return
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	f.Flush()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// flushRecorder is a ResponseWriter that is also an http.Flusher so RelaySSE
// takes the streaming path.
type flushRecorder struct{ *httptest.ResponseRecorder }

func (r *flushRecorder) Flush() {}
