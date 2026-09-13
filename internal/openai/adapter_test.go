package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"


	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

// fixedPool hands the adapter an http.Client bound to a test server.
type fixedPool struct{ c *http.Client }

func (p fixedPool) Client(*domain.Upstream) *http.Client { return p.c }

// allowAllBreaker never opens and records reports.
type allowAllBreaker struct {
	mu      sync.Mutex
	reports []bool
}

func (b *allowAllBreaker) Allow(string) error { return nil }
func (b *allowAllBreaker) Report(_ string, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reports = append(b.reports, ok)
}

// newTestAdapter spins up a test upstream and returns an adapter wired to it.
func newTestAdapter(t *testing.T, h http.HandlerFunc, secrets map[string]string) (*Adapter, *allowAllBreaker, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	b := &allowAllBreaker{}
	a := NewAdapter(
		fixedPool{c: srv.Client()},
		b,
		Config{SecretLookup: func(ref string) (string, bool) { v, ok := secrets[ref]; return v, ok }},
	)
	return a, b, srv.URL
}

func upstreamTarget(baseURL, model string) *domain.Target {
	return &domain.Target{
		Upstream:      &domain.Upstream{Name: "u", BaseURL: baseURL, CredentialRef: "UP_KEY"},
		UpstreamModel: model,
		CredentialRef: "UP_KEY",
	}
}

// retryN is a no-op backoff policy for tests.
type retryN struct{ attempts int }

func (r retryN) Attempts() int                  { return r.attempts }
func (r retryN) Wait(context.Context, int) bool { return true }

func TestForwardRewritesModelAndInjectsSecret(t *testing.T) {
	var gotBody map[string]any
	var gotAuth, gotClientKey string
	a, breaker, base := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotClientKey = r.Header.Get("X-Api-Key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}, map[string]string{"UP_KEY": "sk-real-upstream"})

	rec := httptest.NewRecorder()
	err := a.Forward(context.Background(), upstreamTarget(base, "gpt-4o-2024-08-06"), ports.ForwardRequest{
		Method:    http.MethodPost,
		Path:      "/chat/completions",
		BodyBytes: []byte(`{"model":"gpt-4o","messages":[],"stream":false}`),
		Headers:   http.Header{"X-Api-Key": []string{"client-supplied"}, "Authorization": []string{"Bearer client-key"}},
	}, rec)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}

	if gotBody["model"] != "gpt-4o-2024-08-06" {
		t.Fatalf("model = %v, want rewritten", gotBody["model"])
	}
	if _, ok := gotBody["messages"]; !ok {
		t.Fatal("messages field was dropped during rewrite")
	}
	if gotAuth != "Bearer sk-real-upstream" {
		t.Fatalf("upstream Authorization = %q", gotAuth)
	}
	if gotClientKey != "" {
		t.Fatalf("client X-Api-Key must be scrubbed, got %q", gotClientKey)
	}
	if len(breaker.reports) != 1 || !breaker.reports[0] {
		t.Fatalf("breaker reports = %v, want [true]", breaker.reports)
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestForwardMissingSecretFails(t *testing.T) {
	a, _, base := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called without a secret")
	}, map[string]string{})

	rec := httptest.NewRecorder()
	err := a.Forward(context.Background(), upstreamTarget(base, "m"), ports.ForwardRequest{
		Method: http.MethodPost, Path: "/chat/completions", BodyBytes: []byte(`{}`),
	}, rec)
	if err == nil {
		t.Fatal("expected error when credential is unset")
	}
}

func TestForwardInvalidJSONBodyFails(t *testing.T) {
	a, _, base := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called with an unparseable body")
	}, map[string]string{"UP_KEY": "sk"})
	err := a.Forward(context.Background(), upstreamTarget(base, "m"), ports.ForwardRequest{
		Method: http.MethodPost, Path: "/chat/completions", BodyBytes: []byte(`{oops`),
	}, httptest.NewRecorder())
	if err == nil {
		t.Fatal("expected an error for invalid JSON body")
	}
	var ue *ErrUpstream
	if errors.As(err, &ue) {
		t.Fatal("parse errors are client errors, not ErrUpstream")
	}
}

func TestForwardRelaysUpstreamErrorBodyVerbatim(t *testing.T) {
	errBody := `{"error":{"message":"bad key","type":"invalid_request_error","code":"invalid_api_key"}}`
	a, _, base := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, errBody)
	}, map[string]string{"UP_KEY": "sk"})

	rec := httptest.NewRecorder()
	if err := a.Forward(context.Background(), upstreamTarget(base, "m"), ports.ForwardRequest{
		Method: http.MethodPost, Path: "/chat/completions", BodyBytes: []byte(`{"model":"m"}`),
	}, rec); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != errBody {
		t.Fatalf("body = %s, want verbatim relay", rec.Body.String())
	}
}

func TestForwardWrapsNonOpenAIErrorBody(t *testing.T) {
	a, _, base := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `<html>502 Bad Gateway</html>`)
	}, map[string]string{"UP_KEY": "sk"})

	rec := httptest.NewRecorder()
	_ = a.Forward(context.Background(), upstreamTarget(base, "m"), ports.ForwardRequest{
		Method: http.MethodPost, Path: "/chat/completions", BodyBytes: []byte(`{"model":"m"}`),
	}, rec)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	var env APIError
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not an OpenAI error envelope: %s", rec.Body.String())
	}
	if env.Error.Type != TypeAPI {
		t.Fatalf("error type = %q, want %q", env.Error.Type, TypeAPI)
	}
}

func TestForwardStreamRelaysSSEVerbatim(t *testing.T) {
	events := "data: {\"delta\":\"He\"}\n\ndata: {\"delta\":\"llo\"}\n\ndata: [DONE]\n\n"
	a, _, base := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, line := range strings.SplitAfter(events, "\n") {
			_, _ = io.WriteString(w, line)
			if fl != nil {
				fl.Flush()
			}
		}
	}, map[string]string{"UP_KEY": "sk"})

	rec := httptest.NewRecorder()
	err := a.Forward(context.Background(), upstreamTarget(base, "m"), ports.ForwardRequest{
		Method: http.MethodPost, Path: "/chat/completions",
		BodyBytes: []byte(`{"model":"m","stream":true}`), Stream: true,
	}, rec)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if rec.Body.String() != events {
		t.Fatalf("stream body = %q, want verbatim relay", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatal("terminating [DONE] sentinel lost")
	}
}

func TestForwardRetriesPreFirstByte5xx(t *testing.T) {
	var calls int
	a, _, base := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}, map[string]string{"UP_KEY": "sk"})
	a.cfg.Retry = retryN{attempts: 2}

	rec := httptest.NewRecorder()
	err := a.Forward(context.Background(), upstreamTarget(base, "m"), ports.ForwardRequest{
		Method: http.MethodPost, Path: "/chat/completions", BodyBytes: []byte(`{"model":"m"}`),
	}, rec)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if calls != 2 {
		t.Fatalf("upstream called %d times, want 2 (one retry)", calls)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after successful retry", rec.Code)
	}
}

func TestForwardDoesNotRetryAfterStreamingStarts(t *testing.T) {
	var calls int
	a, _, base := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"delta\":\"partial\"}\n\n")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		panic(http.ErrAbortHandler) // unclean close mid-stream
	}, map[string]string{"UP_KEY": "sk"})
	a.cfg.Retry = retryN{attempts: 3}

	rec := httptest.NewRecorder()
	_ = a.Forward(context.Background(), upstreamTarget(base, "m"), ports.ForwardRequest{
		Method: http.MethodPost, Path: "/chat/completions",
		BodyBytes: []byte(`{"model":"m","stream":true}`), Stream: true,
	}, rec)
	if calls != 1 {
		t.Fatalf("upstream called %d times, want 1 (no retry after streaming)", calls)
	}
}

func TestForwardMultiKeyFailoverOn429(t *testing.T) {
	k1 := &domain.KeySlot{Ref: "K1", Secret: "sk-1"}
	k2 := &domain.KeySlot{Ref: "K2", Secret: "sk-2"}
	ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{k1, k2})

	var usedKeys []string
	a, _, base := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		usedKeys = append(usedKeys, auth)
		if auth == "Bearer sk-1" {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited","type":"tokens"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true,"key":"k2"}`)
	}, map[string]string{"K1": "sk-1", "K2": "sk-2"})

	target := &domain.Target{
		Upstream: &domain.Upstream{
			Name: "u", BaseURL: base, KeyRing: ring,
		},
		UpstreamModel: "m",
		CredentialRef: "K1",
		KeySlot:       k1,
	}

	rec := httptest.NewRecorder()
	err := a.Forward(context.Background(), target, ports.ForwardRequest{
		Method: http.MethodPost, Path: "/chat/completions", BodyBytes: []byte(`{"model":"m"}`),
	}, rec)
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after key failover; body=%s", rec.Code, rec.Body.String())
	}
	if len(usedKeys) != 2 {
		t.Fatalf("expected 2 attempts, got %d: %v", len(usedKeys), usedKeys)
	}
	if usedKeys[0] != "Bearer sk-1" || usedKeys[1] != "Bearer sk-2" {
		t.Fatalf("unexpected keys used: %v", usedKeys)
	}
	if !k1.IsInCooldown(time.Now().UnixNano()) {
		t.Fatal("expected k1 to be placed in cooldown")
	}
}

func TestForwardMultiKeyFailoverOn401(t *testing.T) {
	k1 := &domain.KeySlot{Ref: "K1", Secret: "sk-1"}
	k2 := &domain.KeySlot{Ref: "K2", Secret: "sk-2"}
	ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{k1, k2})

	var usedKeys []string
	a, _, base := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		usedKeys = append(usedKeys, auth)
		if auth == "Bearer sk-1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}, map[string]string{"K1": "sk-1", "K2": "sk-2"})

	target := &domain.Target{
		Upstream: &domain.Upstream{
			Name: "u", BaseURL: base, KeyRing: ring,
		},
		UpstreamModel: "m",
		CredentialRef: "K1",
		KeySlot:       k1,
	}

	rec := httptest.NewRecorder()
	err := a.Forward(context.Background(), target, ports.ForwardRequest{
		Method: http.MethodPost, Path: "/chat/completions", BodyBytes: []byte(`{"model":"m"}`),
	}, rec)
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after 401 failover; body=%s", rec.Code, rec.Body.String())
	}
	if !k1.Revoked.Load() {
		t.Fatal("expected k1 to be marked revoked on 401")
	}
}

func TestForwardAllKeysExhaustedRelays429(t *testing.T) {
	k1 := &domain.KeySlot{Ref: "K1", Secret: "sk-1"}
	k2 := &domain.KeySlot{Ref: "K2", Secret: "sk-2"}
	ring := domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{k1, k2})

	a, _, base := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited"}}`)
	}, map[string]string{"K1": "sk-1", "K2": "sk-2"})

	target := &domain.Target{
		Upstream: &domain.Upstream{
			Name: "u", BaseURL: base, KeyRing: ring,
		},
		UpstreamModel: "m",
		CredentialRef: "K1",
		KeySlot:       k1,
	}

	rec := httptest.NewRecorder()
	err := a.Forward(context.Background(), target, ports.ForwardRequest{
		Method: http.MethodPost, Path: "/chat/completions", BodyBytes: []byte(`{"model":"m"}`),
	}, rec)
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 when all keys fail; body=%s", rec.Code, rec.Body.String())
	}
	if ra := rec.Header().Get("Retry-After"); ra != "10" {
		t.Fatalf("Retry-After = %q, want 10", ra)
	}
}

type waitCancelRetryPolicy struct{}

func (w waitCancelRetryPolicy) Attempts() int { return 3 }
func (w waitCancelRetryPolicy) Wait(ctx context.Context, attempt int) bool {
	<-ctx.Done()
	return false
}

func TestForwardContextCancelledDuringRetryWait(t *testing.T) {
	a, _, base := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"server error"}`)
	}, map[string]string{"UP_KEY": "sk"})

	a.cfg.Retry = waitCancelRetryPolicy{}

	ctx, cancel := context.WithCancel(context.Background())
	// cancel context asynchronously shortly after start
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	rec := httptest.NewRecorder()
	err := a.Forward(ctx, upstreamTarget(base, "m"), ports.ForwardRequest{
		Method: http.MethodPost, Path: "/chat/completions", BodyBytes: []byte(`{"model":"m"}`),
	}, rec)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

