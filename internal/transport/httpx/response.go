package httpx

import (
	"encoding/json"
	"net/http"
)

// HeaderCommitted reports whether a status line has already been sent, using
// the responseController probe or statusRecorder when the writer exposes it.
// If the writer does not expose Committed(), false is returned.
func HeaderCommitted(w http.ResponseWriter) bool {
	if rec, ok := w.(interface{ Committed() bool }); ok {
		return rec.Committed()
	}
	return false
}

// WriteJSON sets the Content-Type header to application/json, writes the given
// HTTP status code, and serializes v as JSON.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

// SetCORS sets permissive cross-origin resource sharing headers for browser dashboard access.
func SetCORS(w http.ResponseWriter, methods string) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if methods != "" {
		w.Header().Set("Access-Control-Allow-Methods", methods)
	}
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
}
