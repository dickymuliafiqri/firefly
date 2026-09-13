package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// logTo renders one record through a redacting JSON handler into a buffer.
func logTo(t *testing.T, attrs []any) string {
	t.Helper()
	var buf bytes.Buffer
	h := NewRedactHandler(slog.NewJSONHandler(&buf, nil))
	l := slog.New(h)
	l.Info("msg", attrs...)
	return buf.String()
}

// TestRedactsAuthorizationAndAPIKey ensures secret-bearing keys are masked.
func TestRedactsAuthorizationAndAPIKey(t *testing.T) {
	out := logTo(t, []any{
		"authorization", "Bearer sk-super-secret",
		"api_key", "sk-abcd1234",
		"X-Api-Key", "sk-lower",
		"openai_api_key", "sk-nested",
		"tenant", "alpha",
		"path", "/v1/chat/completions",
	})
	for _, secret := range []string{"sk-super-secret", "sk-abcd1234", "sk-lower", "sk-nested"} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q leaked into logs: %s", secret, out)
		}
	}
	if !strings.Contains(out, redactedValueMask) {
		t.Fatalf("expected redaction mask in output: %s", out)
	}
	if !strings.Contains(out, "alpha") {
		t.Fatalf("non-secret attr was dropped: %s", out)
	}
}

// TestRedactsGroupedSecrets ensures nested groups cannot leak.
func TestRedactsGroupedSecrets(t *testing.T) {
	out := logTo(t, []any{
		slog.Group("creds", "token", "tok-123", "user", "bob"),
	})
	if strings.Contains(out, "tok-123") {
		t.Fatalf("grouped secret leaked: %s", out)
	}
	if !strings.Contains(out, "bob") {
		t.Fatalf("non-secret group attr dropped: %s", out)
	}
}

// TestRedactsBoundAttrsViaWith covers the WithAttrs path.
func TestRedactsBoundAttrsViaWith(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(NewRedactHandler(slog.NewJSONHandler(&buf, nil))).With("password", "hunter2", "service", "gw")
	l.Info("started")
	out := buf.String()
	if strings.Contains(out, "hunter2") {
		t.Fatalf("bound secret leaked: %s", out)
	}
	if !strings.Contains(out, "gw") {
		t.Fatalf("bound non-secret dropped: %s", out)
	}
}

// TestMessageRedaction replaces the message when configured.
func TestMessageRedaction(t *testing.T) {
	var buf bytes.Buffer
	h := &RedactHandler{inner: slog.NewJSONHandler(&buf, nil), RedactMessages: true}
	slog.New(h).Info("secret prompt body here")
	if strings.Contains(buf.String(), "secret prompt") {
		t.Fatalf("message not redacted: %s", buf.String())
	}
}

// TestNonSecretKeysUntouched guards against over-redaction of normal fields.
func TestNonSecretKeysUntouched(t *testing.T) {
	out := logTo(t, []any{"request_id", "abc", "status", 200, "model", "gpt-4o"})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output not valid JSON: %v", err)
	}
	if m["request_id"] != "abc" || m["model"] != "gpt-4o" {
		t.Fatalf("non-secret values changed: %v", m)
	}
}

// TestNewDefaultsToInfo builds a logger and confirms it does not panic and
// applies redaction end-to-end.
func TestNewDefaultsToInfo(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, "warn")
	l.Info("should be filtered") // below level -> nothing
	l.Warn("visible", "api_key", "sk-x")
	out := buf.String()
	if strings.Contains(out, "should be filtered") {
		t.Fatalf("info log emitted at warn level: %s", out)
	}
	if strings.Contains(out, "sk-x") {
		t.Fatalf("secret leaked through New(): %s", out)
	}
}

// TestRedactsHTTPHeadersAndMaps verifies that maps and http.Headers with sensitive keys are scrubbed.
func TestRedactsHTTPHeadersAndMaps(t *testing.T) {
	headers := http.Header{
		"Authorization":   []string{"Bearer secret-auth-header-value"},
		"X-Admin-Token":   []string{"admin-secret-val"},
		"X-Api-Key":       []string{"client-api-key-val"},
		"Content-Type":    []string{"application/json"},
		"X-Custom-Header": []string{"custom-safe-value"},
	}
	rawMap := map[string]string{
		"Authorization": "Bearer inline-map-secret",
		"normal_field": "safe",
	}

	out := logTo(t, []any{
		"headers", headers,
		"meta", rawMap,
		"auth_field", "Bearer secret-inline-bearer",
	})

	for _, secret := range []string{
		"secret-auth-header-value",
		"admin-secret-val",
		"client-api-key-val",
		"inline-map-secret",
		"secret-inline-bearer",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q leaked in log output:\n%s", secret, out)
		}
	}

	for _, safe := range []string{
		"application/json",
		"custom-safe-value",
		"safe",
	} {
		if !strings.Contains(out, safe) {
			t.Fatalf("safe value %q was erroneously removed:\n%s", safe, out)
		}
	}
}

