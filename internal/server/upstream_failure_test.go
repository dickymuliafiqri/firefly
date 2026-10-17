package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
	"github.com/stretchr/testify/assert"
)

// timeoutNetErr mimics the net.Error net/http returns when the transport's
// ResponseHeaderTimeout expires ("net/http: timeout awaiting response headers").
type timeoutNetErr struct{}

func (timeoutNetErr) Error() string   { return "net/http: timeout awaiting response headers" }
func (timeoutNetErr) Timeout() bool   { return true }
func (timeoutNetErr) Temporary() bool { return true }

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
			name:     "transport timeout is 504",
			err:      &openai.ErrUpstream{Status: 0, Cause: timeoutNetErr{}},
			wantCode: http.StatusGatewayTimeout,
			wantBody: "did not start responding in time",
		},
		{
			name: "wrapped transport timeout is 504",
			err: &openai.ErrUpstream{Status: 0, Cause: &url.Error{
				Op: "Post", URL: "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent",
				Err: timeoutNetErr{},
			}},
			wantCode: http.StatusGatewayTimeout,
			wantBody: "did not start responding in time",
		},
		{
			name:     "dial refusal is not a timeout",
			err:      &openai.ErrUpstream{Status: 0, Cause: errors.New("dial tcp 1.2.3.4:443: connect: connection refused")},
			wantCode: http.StatusBadGateway,
			wantBody: "upstream request failed",
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

// The 504 body used to append ue.Cause.Error(); for a transport timeout that is
// a *url.Error carrying the full upstream URL, and for the openai/anthropic
// protocols the base_url is operator-chosen (Azure, vLLM, self-hosted) and may
// embed a credential in its query string or userinfo. The tenant must learn the
// upstream timed out and nothing about where it lives.
func TestWriteUpstreamFailure_504DoesNotLeakUpstreamURL(t *testing.T) {
	t.Parallel()

	const upstreamURL = "https://user:s3cr3t@internal-vllm.example.com:8443/v1/responses?api-version=2024-10-21&key=sk-live-abc123"

	rec := httptest.NewRecorder()
	writeUpstreamFailure(rec, &openai.ErrUpstream{
		Status: 0,
		Cause:  &url.Error{Op: "Post", URL: upstreamURL, Err: timeoutNetErr{}},
	})

	assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "did not start responding in time")
	for _, secret := range []string{
		"internal-vllm.example.com", "s3cr3t", "api-version", "sk-live-abc123",
		"/v1/responses", "user:",
	} {
		assert.NotContains(t, body, secret, "504 body leaked %q", secret)
	}
}

// sanitizeUpstreamError is the single sanitizer behind both the log line and the
// persisted dashboard record, so its contract is asserted directly.
func TestSanitizeUpstreamError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil is empty",
			err:  nil,
			want: "",
		},
		{
			name: "url credentials and query are dropped",
			err: &url.Error{
				Op:  "Post",
				URL: "https://user:pass@host.example.com/v1/chat/completions?key=abc",
				Err: errors.New("dial tcp: connection refused"),
			},
			want: "Post https://host.example.com/v1/chat/completions: dial tcp: connection refused",
		},
		{
			name: "non url error passes through unchanged",
			err:  errors.New("boom"),
			want: "boom",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, sanitizeUpstreamError(tc.err))
		})
	}
}
