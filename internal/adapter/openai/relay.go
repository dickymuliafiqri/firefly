package openai

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// errStreamIdle indicates the upstream stopped delivering bytes for longer than
// the configured idle budget; the relay aborts the stream to reclaim resources.
var errStreamIdle = errors.New("upstream stream idle timeout")

// flusher is the subset of http.ResponseWriter needed for streaming.
type flusher interface {
	Flush()
}

// atomicTime is a tiny wrapper so idleBody can store/load time.Time atomically.
type atomicTime struct{ v atomic.Int64 }

func (a *atomicTime) Store(t time.Time) { a.v.Store(t.UnixNano()) }
func (a *atomicTime) Load() time.Time   { return time.Unix(0, a.v.Load()) }

var (
	readerPool = sync.Pool{
		New: func() any {
			return bufio.NewReaderSize(nil, 32*1024)
		},
	}
	copyBufferPool = sync.Pool{
		New: func() any {
			b := make([]byte, 32*1024)
			return &b
		},
	}
	bufferPool = sync.Pool{
		New: func() any {
			return bytes.NewBuffer(make([]byte, 0, 16*1024))
		},
	}
)

// RelaySSE streams an upstream SSE response to the client, flushing after each
// event so tokens arrive incrementally instead of being buffered until the
// stream ends.
//
// Behavior:
//   - Copies bytes through unchanged (we are a transparent proxy of the SSE
//     framing); we do NOT parse/normalize events, so non-OpenAI-compatible
//     extensions survive intact.
//   - Flushes after every line that carries data (and after blank separators),
//     which is what makes token-by-token streaming visible to clients.
//   - Honors ctx cancellation: on client disconnect the loop returns promptly
//     (both via the reader erroring and via the ctx select below).
//   - Enforces an IDLE timeout: if the upstream delivers no byte for idleTimeout,
//     the read is aborted via a watchdog that closes the body. This is what
//     prevents a stalled upstream from pinning a goroutine, a connection, and a
//     credential concurrency slot forever. A non-positive idleTimeout disables
//     the watchdog.
//   - Memory recycling: uses sync.Pool for reader buffers and zero-copy slice
//     reads (ReadSlice) to avoid heap allocations per streaming event.
//   - Returns the number of bytes written. A write error means the CLIENT is
//     gone; callers should NOT retry.
//
// It deliberately does not add the terminating "data: [DONE]\n\n" itself — that
// is OpenAI's job and is already present in OpenAI's stream. We relay verbatim.
//
// The body is closed by RelaySSE on return (idempotent with the caller's
// deferred close) so the idle watchdog's Close unblocks a stuck Read.
func RelaySSE(ctx context.Context, w http.ResponseWriter, body io.ReadCloser, idleTimeout time.Duration) (int64, error) {
	if err := ctx.Err(); err != nil {
		_ = body.Close()
		return 0, err
	}

	f, _ := w.(flusher)

	// Set SSE headers. Cache-Control + X-Accel-Buffering defeat intermediary
	// buffering (nginx) that would otherwise coalesce chunks.
	h := w.Header()
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "text/event-stream")
	}
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if f != nil {
		f.Flush()
	}

	// Stream watchdog: monitors both idle silence and client context cancellation.
	// When client disconnects or upstream stalls, the watchdog closes the body immediately
	// to unblock any pending Read() on the wire and free resources promptly.
	wrapped, stopWatchdog := newStreamWatchdog(ctx, body, idleTimeout)
	body = wrapped
	defer stopWatchdog()

	br := readerPool.Get().(*bufio.Reader)
	br.Reset(body)
	defer func() {
		br.Reset(nil)
		readerPool.Put(br)
	}()

	var written int64
	for {
		// Fast-path cancellation: if the client went away, stop before issuing
		// another read.
		select {
		case <-ctx.Done():
			return written, ctx.Err()
		default:
		}

		line, err := br.ReadSlice('\n')
		if len(line) > 0 {
			if r, ok := body.(idleResetter); ok {
				r.Touch() // any delivered byte resets the idle deadline
			}
			n, werr := w.Write(line)
			written += int64(n)
			if werr != nil {
				return written, werr
			}
			// Flush on every line: SSE events are line-delimited, so this
			// yields ≤1 line of latency, which is what streaming clients want.
			if f != nil {
				f.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				// Line longer than 32KB: already wrote and flushed partial chunk;
				// continue reading remainder of the line.
				continue
			}
			if errors.Is(err, io.EOF) {
				return written, nil
			}
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return written, context.Canceled
			}
			if errors.Is(err, errStreamIdle) {
				return written, fmt.Errorf("%w: idle timeout elapsed", errStreamIdle)
			}
			return written, err
		}
	}
}

// idleResetter is implemented by the idle-wrapped body so the relay can reset
// the deadline whenever a delivered byte arrives.
type idleResetter interface{ Touch() }

// newStreamWatchdog wraps body so that failing to Touch() within idleTimeout,
// OR client context cancellation, causes the underlying body to be closed immediately,
// unblocking a pending Read. It returns the wrapper and a stop function that disarms
// the watchdog.
func newStreamWatchdog(ctx context.Context, body io.ReadCloser, idleTimeout time.Duration) (io.ReadCloser, func()) {
	w := &idleBody{
		ReadCloser: body,
		idle:       idleTimeout,
		ctx:        ctx,
		done:       make(chan struct{}),
	}
	w.last.Store(time.Now())
	go w.watch()
	return w, w.stop
}

// newIdleWatchdog is kept for backward compatibility and delegates to newStreamWatchdog with Background context.
func newIdleWatchdog(body io.ReadCloser, idleTimeout time.Duration) (io.ReadCloser, func()) {
	return newStreamWatchdog(context.Background(), body, idleTimeout)
}

// idleBody is an io.ReadCloser that closes the underlying body if no Read
// activity occurs within the idle window, or if the client disconnects.
type idleBody struct {
	io.ReadCloser
	idle     time.Duration
	ctx      context.Context
	last     atomicTime
	done     chan struct{}
	once     sync.Once
	fired    atomic.Bool
	canceled atomic.Bool
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.Touch()
		return n, err
	}
	if b.canceled.Load() {
		return 0, context.Canceled
	}
	if b.fired.Load() {
		// The watchdog closed the body; surface a precise classification rather
		// than the transport's "use of closed connection"-style error.
		return 0, errStreamIdle
	}
	return n, err
}

// Touch records activity, resetting the idle deadline.
func (b *idleBody) Touch() { b.last.Store(time.Now()) }

// stop disarms the watchdog exactly once so the goroutine exits without closing.
func (b *idleBody) stop() { b.once.Do(func() { close(b.done) }) }

// watch monitors client disconnects and idle timeouts.
func (b *idleBody) watch() {
	var tickerChan <-chan time.Time
	if b.idle > 0 {
		tick := b.idle / 4
		if tick < 50*time.Millisecond {
			tick = 50 * time.Millisecond
		}
		t := time.NewTicker(tick)
		defer t.Stop()
		tickerChan = t.C
	}

	for {
		select {
		case <-b.done:
			return
		case <-b.ctx.Done():
			// Client disconnected: close upstream body immediately to stop token generation
			// and unblock any pending Read() on the wire.
			b.canceled.Store(true)
			_ = b.ReadCloser.Close()
			return
		case <-tickerChan:
			if b.idle > 0 && time.Since(b.last.Load()) >= b.idle {
				// Closing the body makes the blocked Read return an error,
				// unblocking the relay loop immediately. Mark `fired` first so
				// the in-flight Read classifies the abort as errStreamIdle.
				b.fired.Store(true)
				_ = b.ReadCloser.Close()
				return
			}
		}
	}
}

// RelayBuffered copies a non-streaming JSON body through, buffering it so the
// caller can (a) know the size for Content-Length and (b) inspect the body for
// error mapping before committing a status code.
//
// It returns the body bytes; the caller writes the status + body.
func RelayBuffered(body io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = 32 << 20 // 32 MiB safety cap
	}
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= 512*1024 {
			buf.Reset()
			bufferPool.Put(buf)
		}
	}()

	copyBuf := copyBufferPool.Get().(*[]byte)
	n, err := io.CopyBuffer(buf, io.LimitReader(body, maxBytes+1), *copyBuf)
	copyBufferPool.Put(copyBuf)

	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n > maxBytes {
		return nil, errors.New("upstream response exceeds max buffered size")
	}
	res := make([]byte, buf.Len())
	copy(res, buf.Bytes())
	return res, nil
}
