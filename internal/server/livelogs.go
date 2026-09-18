package server

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/dickymuliafiqri/firefly/internal/storage/analytics"
)

// LiveLog represents a single inbound API request / upstream proxy transaction.
type LiveLog = analytics.RequestLog

const maxLiveLogHistory = 100

// LiveLogHub manages an in-memory ring buffer of recent request logs and
// broadcasts live events to SSE subscribers with non-blocking sends.
type LiveLogHub struct {
	mu             sync.RWMutex
	history        []LiveLog
	subscribers    map[chan LiveLog]struct{}
	totalInTokens  atomic.Int64
	totalOutTokens atomic.Int64
	totalReqs      atomic.Int64
	store          *analytics.Store
}

// NewLiveLogHub creates an initialized LiveLogHub.
func NewLiveLogHub() *LiveLogHub {
	return &LiveLogHub{
		history:     make([]LiveLog, 0, maxLiveLogHistory),
		subscribers: make(map[chan LiveLog]struct{}),
	}
}

// AttachStore binds an analytics persistent store to the hub and loads initial state.
func (h *LiveLogHub) AttachStore(store *analytics.Store) {
	if h == nil || store == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.store = store

	ctx := context.Background()
	if hist, err := store.History(ctx, maxLiveLogHistory); err == nil && len(hist) > 0 {
		h.history = hist
	}
	if sum, err := store.Summary(ctx); err == nil {
		h.totalInTokens.Store(sum.InputTokens)
		h.totalOutTokens.Store(sum.OutputTokens)
		h.totalReqs.Store(sum.TotalRequests)
	}
}

// Clear flushes history from memory and persistent storage.
func (h *LiveLogHub) Clear() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.history = make([]LiveLog, 0, maxLiveLogHistory)
	store := h.store
	h.mu.Unlock()

	if store != nil {
		_ = store.ClearHistory(context.Background())
	}
}

// Publish adds or updates a log entry in history and notifies all active subscribers.
func (h *LiveLogHub) Publish(log LiveLog) {
	if h == nil {
		return
	}
	h.mu.Lock()

	// If entry already exists (e.g. updating in-flight log upon completion), update in-place
	found := false
	prevStatus := 0
	for i := range h.history {
		if h.history[i].ID == log.ID {
			prevStatus = h.history[i].Status
			h.history[i] = log
			found = true
			break
		}
	}

	if !found {
		// Prepend newest first, cap at maxLiveLogHistory
		h.history = append([]LiveLog{log}, h.history...)
		if len(h.history) > maxLiveLogHistory {
			h.history = h.history[:maxLiveLogHistory]
		}
	}

	// Track cumulative metrics upon request completion (Status > 0) only if not already counted
	if log.Status > 0 && (!found || prevStatus == 0) {
		h.totalReqs.Add(1)
		if log.TokensIn > 0 {
			h.totalInTokens.Add(int64(log.TokensIn))
		}
		if log.TokensOut > 0 {
			h.totalOutTokens.Add(int64(log.TokensOut))
		}
	}

	// Broadcast non-blocking to active subscribers
	for ch := range h.subscribers {
		select {
		case ch <- log:
		default:
			// Slow consumer: skip to prevent blocking the request path
		}
	}

	store := h.store
	h.mu.Unlock()

	if store != nil {
		_ = store.Record(context.Background(), log)
	}
}

// CumulativeTotals returns the recorded input tokens, output tokens, and completed requests.
func (h *LiveLogHub) CumulativeTotals() (inTokens, outTokens, totalReqs int64) {
	if h == nil {
		return 0, 0, 0
	}
	return h.totalInTokens.Load(), h.totalOutTokens.Load(), h.totalReqs.Load()
}

// Snapshot returns a copy of recent logs (newest first).
func (h *LiveLogHub) Snapshot() []LiveLog {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()

	out := make([]LiveLog, len(h.history))
	copy(out, h.history)
	return out
}

// Subscribe registers a new subscriber channel. The caller must call the returned
// cleanup function when done (e.g. on client disconnect).
func (h *LiveLogHub) Subscribe() (<-chan LiveLog, func()) {
	if h == nil {
		ch := make(chan LiveLog)
		close(ch)
		return ch, func() {}
	}
	ch := make(chan LiveLog, 32)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subscribers, ch)
			h.mu.Unlock()
		})
	}
	return ch, unsub
}
