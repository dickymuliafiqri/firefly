package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
	"github.com/stretchr/testify/assert"
)

// writeUpstreamFailure is the single place that turns a forwarding error into a
// client-visible body, so its classification is what an operator sees. A
// circuit-open rejection must never be reported as a generic gateway fault:
// several adapters (antigravity, cline, codebuddy, grok, qoder) return the bare
// sentinel, and every request for the next 10s used to answer the same opaque
// 502 "upstream request failed" — indistinguishable from a real 5xx from the
// provider.
func TestWriteUpstreamFailure_Mapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		wantCode int
		// wantBody is empty when nothing may be written (the response was
		// already relayed, or the client is gone).
		wantBody string
	}{
		{
			name:     "bare circuit open is 503",
			err:      upstream.ErrCircuitOpen,
			wantCode: http.StatusServiceUnavailable,
			wantBody: "circuit open",
		},
		{
			name:     "wrapped circuit open is 503",
			err:      fmt.Errorf("antigravity: %w", upstream.ErrCircuitOpen),
			wantCode: http.StatusServiceUnavailable,
			wantBody: "circuit open",
		},
		{
			name:     "transport error stays 502",
			err:      &openai.ErrUpstream{Status: 0, Cause: errors.New("dial tcp: connection refused")},
			wantCode: http.StatusBadGateway,
			wantBody: "upstream request failed",
		},
		{
			name:     "upstream 5xx with no body stays 502",
			err:      &openai.ErrUpstream{Status: http.StatusInternalServerError},
			wantCode: http.StatusBadGateway,
			wantBody: "upstream request failed",
		},
		{
			name:     "plain error stays 502",
			err:      errors.New("boom"),
			wantCode: http.StatusBadGateway,
			wantBody: "upstream request failed",
		},
		{
			name:     "relayed 4xx body is not overwritten",
			err:      &openai.ErrUpstream{Status: http.StatusUnauthorized},
			wantCode: http.StatusOK, // untouched recorder default
		},
		{
			name:     "client cancellation writes nothing",
			err:      &openai.ErrUpstream{Status: 0, Cause: context.Canceled},
			wantCode: http.StatusOK, // untouched recorder default
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			writeUpstreamFailure(rec, tc.err)

			assert.Equal(t, tc.wantCode, rec.Code)
			if tc.wantBody == "" {
				assert.Empty(t, rec.Body.String(), "nothing may be written for an already-answered request")
				return
			}
			assert.Contains(t, rec.Body.String(), tc.wantBody)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		})
	}
}
