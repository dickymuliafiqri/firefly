package server

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/observability/trace"
)

// The visualizer API is admin-only: a trace carries tenant names, routing
// decisions, and masked credential hints, so every endpoint is gated by
// authorizeAdmin (dashboard session or admin token) — the same gate as the
// provider/key CRUD. Capture itself only runs while a subscriber is attached,
// so opening the page is what turns tracing on.
const (
	visualizerMaxBody = 4 << 10
	visualizerTicker  = 15 * time.Second
)

// handleOptionsVisualizer handles CORS preflight for the visualizer API.
func (deps RouterDeps) handleOptionsVisualizer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	w.WriteHeader(http.StatusNoContent)
}

// authorizeVisualizer rejects non-admin callers.
func (deps RouterDeps) authorizeVisualizer(w http.ResponseWriter, r *http.Request) bool {
	if deps.authorizeAdmin(r) {
		return true
	}
	openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid dashboard session or admin token required")
	return false
}

// visualizerRecorder returns the live recorder, or fails closed (HTTP 503) when
// the process was started without one.
func (deps RouterDeps) visualizerRecorder(w http.ResponseWriter) (*trace.Recorder, bool) {
	if deps.Traces == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "visualizer recorder is not configured")
		return nil, false
	}
	return deps.Traces, true
}

// handleVisualizerTraces returns the ring of finished traces plus recorder stats.
func (deps RouterDeps) handleVisualizerTraces(w http.ResponseWriter, r *http.Request) {
	if !deps.authorizeVisualizer(w, r) {
		return
	}
	rec, ok := deps.visualizerRecorder(w)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"traces": rec.Snapshot(),
		"stats":  rec.Stats(),
	})
}

// handleVisualizerCapture toggles capture on or off.
func (deps RouterDeps) handleVisualizerCapture(w http.ResponseWriter, r *http.Request) {
	if !deps.authorizeVisualizer(w, r) {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, visualizerMaxBody)).Decode(&body); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "invalid JSON body")
		return
	}
	rec, ok := deps.visualizerRecorder(w)
	if !ok {
		return
	}
	rec.SetEnabled(body.Enabled)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"stats": rec.Stats()})
}

// handleVisualizerClear drops every remembered trace.
func (deps RouterDeps) handleVisualizerClear(w http.ResponseWriter, r *http.Request) {
	if !deps.authorizeVisualizer(w, r) {
		return
	}
	rec, ok := deps.visualizerRecorder(w)
	if !ok {
		return
	}
	rec.Clear()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"stats": rec.Stats()})
}

// handleVisualizerEvents streams live traces as Server-Sent Events. Attaching a
// subscriber is what enables capture, and it stops as soon as the last browser
// disconnects, so an unopened dashboard costs nothing.
func (deps RouterDeps) handleVisualizerEvents(w http.ResponseWriter, r *http.Request) {
	if !deps.authorizeVisualizer(w, r) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "streaming unsupported")
		return
	}
	rec, ok := deps.visualizerRecorder(w)
	if !ok {
		return
	}
	if rec.Stats().Subscribers >= trace.MaxSubscribers {
		openai.WriteError(w, http.StatusTooManyRequests, openai.TypeAPI, "too many visualizer streams")
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := rec.Subscribe(256)
	defer unsub()

	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	ticker := time.NewTicker(visualizerTicker)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			_, _ = w.Write([]byte("event: closed\ndata: {}\n\n"))
			flusher.Flush()
			return
		case data, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write([]byte("data: ")); err != nil {
				return
			}
			if _, err := w.Write(data); err != nil {
				return
			}
			if _, err := w.Write([]byte("\n\n")); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			stats, err := json.Marshal(map[string]any{"type": "stats", "stats": rec.Stats()})
			if err != nil {
				continue
			}
			if _, err := w.Write(append(append([]byte("data: "), stats...), '\n', '\n')); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
