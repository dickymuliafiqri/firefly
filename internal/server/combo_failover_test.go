package server

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

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
)

// scriptedReply is one upstream's canned answer. It is written to the writer
// exactly the way a real adapter relays an upstream response — status first,
// then body — which is what makes these tests meaningful: the adapter returns
// nil after relaying an error body, so the failover gate must read the status
// from the attempt writer rather than from the error value.
type scriptedReply struct {
	status int
	body   string
	err    error
	// block yields a member that never produces a first byte until its context
	// is cancelled, used to exercise the TTFB watchdog.
	block bool
	// stream writes a committed SSE frame, used to prove a response that already
	// reached the client is never retried.
	stream bool
}

// scriptedAdapter answers per upstream name and records the order in which
// upstreams were contacted plus the body each one received.
type scriptedAdapter struct {
	mu       sync.Mutex
	calls    []string
	bodies   []string
	byName   map[string]scriptedReply
	fallback scriptedReply
}

func (a *scriptedAdapter) Protocol() domain.Protocol { return domain.ProtocolOpenAI }

func (a *scriptedAdapter) Forward(ctx context.Context, t *domain.Target, req ports.ForwardRequest, w io.Writer) error {
	name := ""
	if t != nil && t.Upstream != nil {
		name = t.Upstream.Name
	}
	a.mu.Lock()
	a.calls = append(a.calls, name)
	a.bodies = append(a.bodies, string(req.BodyBytes))
	reply, ok := a.byName[name]
	a.mu.Unlock()
	if !ok {
		reply = a.fallback
	}

	if reply.block {
		<-ctx.Done()
		return ctx.Err()
	}
	if reply.err != nil {
		return reply.err
	}

	rw, isRW := w.(http.ResponseWriter)
	if reply.stream {
		if isRW {
			rw.Header().Set("Content-Type", "text/event-stream")
			rw.WriteHeader(http.StatusOK)
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return nil
	}

	status := reply.status
	if status == 0 {
		status = http.StatusOK
	}
	if isRW {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(status)
	}
	if reply.body != "" {
		_, _ = io.WriteString(w, reply.body)
	}
	return nil
}

func (a *scriptedAdapter) callNames() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

func (a *scriptedAdapter) callBodies() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.bodies...)
}

// comboFixture describes the catalog a failover test needs.
type comboFixture struct {
	// members are the concrete model public names backing each upstream, keyed
	// by upstream name. The model public name is "m-<upstream>".
	members []string
	combos  map[string]*domain.Combo
	order   []string
	// systemPrompts specializes a member model with its own directive.
	systemPrompts map[string]string
	// credentialMaxConcurrent bounds a single upstream credential so a leaked
	// ticket across attempts becomes a visible 429.
	credentialMaxConcurrent int
}

func newComboFixture(members []string, combos map[string]*domain.Combo, order []string) comboFixture {
	return comboFixture{members: members, combos: combos, order: order}
}

func (f comboFixture) deps(t *testing.T, adapter ports.UpstreamAdapter) (RouterDeps, *domain.CatalogSnapshot, *domain.Tenant) {
	t.Helper()

	upstreams := make(map[string]*domain.Upstream, len(f.members))
	models := make(map[string]*domain.ModelEntry, len(f.members))
	enabled := make([]string, 0, len(f.members))
	upstreamOrder := make([]string, 0, len(f.members))
	for _, u := range f.members {
		upstreamOrder = append(upstreamOrder, u)
		upstreams[u] = &domain.Upstream{
			Name:                    u,
			Protocol:                domain.ProtocolOpenAI,
			BaseURL:                 "https://" + u + "/v1",
			CredentialRef:           "cred-" + u,
			CredentialMaxConcurrent: f.credentialMaxConcurrent,
		}
		public := "m-" + u
		models[public] = &domain.ModelEntry{
			PublicName:    public,
			Upstream:      u,
			UpstreamModel: "up-" + u,
			Enabled:       true,
			SystemPrompt:  f.systemPrompts[public],
		}
		enabled = append(enabled, public)
	}

	tenant := &domain.Tenant{
		Name:          "combo-tenant",
		APIKey:        testKey,
		Status:        domain.TenantStatusActive,
		AllowedModels: []string{"*"},
		RateLimit:     domain.RateLimit{RPS: 1000, Burst: 1000, MaxConcurrent: 100},
	}
	tenant.EnsureCounters()
	hash := tenant.KeyHash

	opts := []domain.SnapshotOption{}
	if len(f.combos) > 0 {
		opts = append(opts, domain.WithCombos(f.combos, f.order))
	}
	snap := domain.NewCatalogSnapshot(
		1,
		upstreams,
		upstreamOrder,
		models,
		enabled,
		map[string]*domain.Tenant{hash: tenant},
		[]string{hash},
		opts...,
	)

	deps := RouterDeps{
		Snapshots:   fakeProvider{snap},
		TenantStore: auth.NewStore(fakeProvider{snap}),
		Limiter:     limits.New(),
		Adapter:     adapter,
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
		LiveLogs:    NewLiveLogHub(),
	}
	return deps, snap, tenant
}

// postChat drives forwardEndpoint directly with the fixture tenant already in
// context, bypassing auth exactly as the budget tests do.
// postChatWithin is postChat with a client deadline, which the 429 rule needs:
// "wait, or try another provider" can only be answered against the time the
// caller is still willing to wait.
func postChatWithin(t *testing.T, deps RouterDeps, tenant *domain.Tenant, model string, budget time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	return postChatCtx(t, deps, tenant, model, false, budget)
}

func postChat(t *testing.T, deps RouterDeps, tenant *domain.Tenant, model string, stream bool) *httptest.ResponseRecorder {
	t.Helper()
	return postChatCtx(t, deps, tenant, model, stream, 0)
}

func postChatCtx(t *testing.T, deps RouterDeps, tenant *domain.Tenant, model string, stream bool, budget time.Duration) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]`
	if stream {
		body += `,"stream":true`
	}
	body += `}`

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	ctx := httpx.WithTenant(req.Context(), tenant)
	if budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	deps.forwardEndpoint("/chat/completions", openAIIngress{})(rec, req)
	return rec
}

// TestComboFailover_5xxMovesToNextMember is the core case: the first member
// answers 500, the body is discarded, and the second member answers the client.
func TestComboFailover_5xxMovesToNextMember(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {status: http.StatusInternalServerError, body: `{"error":"boom"}`},
		"u2": {status: http.StatusOK, body: `{"ok":"from-u2"}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)
	rec := postChat(t, deps, tenant, "combo", false)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, "from-u2") {
		t.Fatalf("body = %q, want the second member's response", got)
	}
	if got := adapter.callNames(); len(got) != 2 || got[0] != "u1" || got[1] != "u2" {
		t.Fatalf("upstream calls = %v, want [u1 u2]", got)
	}
}

// TestComboFailover_DoesNotConsumeNestedMembers proves the failover unit is the
// top-level member: with c=[a,b], a=[a1,a2], b=[b1,b2], only a1 and b1 are
// contacted. Consuming a2 would silently double the request's provider cost.
func TestComboFailover_DoesNotConsumeNestedMembers(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {status: http.StatusInternalServerError, body: `{"e":1}`},
		"u2": {status: http.StatusOK, body: `{"ok":1}`},
	}}
	f := newComboFixture([]string{"u1", "u2", "u3", "u4"}, map[string]*domain.Combo{
		"a":     {Name: "a", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
		"b":     {Name: "b", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u3", "m-u4"}, Enabled: true},
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"a", "b"}, Enabled: true},
	}, []string{"a", "b", "combo"})
	f.systemPrompts = nil

	deps, _, tenant := f.deps(t, adapter)
	rec := postChat(t, deps, tenant, "combo", false)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := adapter.callNames()
	if len(got) != 2 || got[0] != "u1" || got[1] != "u3" {
		t.Fatalf("upstream calls = %v, want [u1 u3] (a1 then b1, never a2)", got)
	}
}

// TestComboFailover_400IsNotRetried pins that a request the provider rejected
// as malformed is relayed verbatim: retrying it against another provider would
// just bill a second account for the same invalid payload.
func TestComboFailover_400IsNotRetried(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {status: http.StatusBadRequest, body: `{"error":"bad payload"}`},
		"u2": {status: http.StatusOK, body: `{"ok":1}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)
	rec := postChat(t, deps, tenant, "combo", false)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := adapter.callNames(); len(got) != 1 {
		t.Fatalf("upstream calls = %v, want exactly one", got)
	}
	if !strings.Contains(rec.Body.String(), "bad payload") {
		t.Fatalf("body = %q, want the upstream error relayed", rec.Body.String())
	}
}

// TestComboFailover_CommittedStreamIsNotRetried pins the commit rule: once a
// byte reached the client, a retry would splice two responses together.
func TestComboFailover_CommittedStreamIsNotRetried(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {stream: true},
		"u2": {status: http.StatusOK, body: `{"ok":"u2"}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)
	rec := postChat(t, deps, tenant, "combo", true)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := adapter.callNames(); len(got) != 1 || got[0] != "u1" {
		t.Fatalf("upstream calls = %v, want [u1] only", got)
	}
	if !strings.Contains(rec.Body.String(), `"content":"hi"`) {
		t.Fatalf("body = %q, want the streamed frame", rec.Body.String())
	}
}

// TestComboFailover_ClientCancelIsNotRetried pins that an aborted client stops
// all further provider work.
func TestComboFailover_ClientCancelIsNotRetried(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {err: context.Canceled},
		"u2": {status: http.StatusOK, body: `{"ok":1}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)
	postChat(t, deps, tenant, "combo", false)

	if got := adapter.callNames(); len(got) != 1 {
		t.Fatalf("upstream calls = %v, want exactly one", got)
	}
}

// TestComboFailover_CredentialStatusesAreRetried covers the credential and
// routing failures that mean "this member cannot serve anyone".
func TestComboFailover_CredentialStatusesAreRetried(t *testing.T) {
	for _, status := range []int{
		http.StatusUnauthorized,
		http.StatusPaymentRequired,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		529, // non-standard, but shipped by some providers
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			adapter := &scriptedAdapter{byName: map[string]scriptedReply{
				"u1": {status: status, body: `{"e":1}`},
				"u2": {status: http.StatusOK, body: `{"ok":1}`},
			}}
			f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
				"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
			}, []string{"combo"})

			deps, _, tenant := f.deps(t, adapter)
			rec := postChat(t, deps, tenant, "combo", false)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			if got := adapter.callNames(); len(got) != 2 {
				t.Fatalf("upstream calls = %v, want 2", got)
			}
		})
	}
}

// TestComboFailover_429DependsOnRetryAfter pins the rate-limit exception: a
// short wait is cheaper than a second provider, a long one (or none) is not.
func TestComboFailover_429DependsOnRetryAfter(t *testing.T) {
	run := func(t *testing.T, retryAfter string, wantCalls int) {
		t.Helper()
		adapter := &scriptedAdapter{byName: map[string]scriptedReply{
			"u1": {status: http.StatusTooManyRequests, body: `{"e":"slow down"}`},
			"u2": {status: http.StatusOK, body: `{"ok":1}`},
		}}
		f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
			"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
		}, []string{"combo"})

		deps, _, tenant := f.deps(t, adapter)
		// Wrap the adapter so the 429 carries a Retry-After header, which a real
		// adapter copies from the upstream response.
		deps.Adapter = &retryAfterAdapter{inner: adapter, retryAfter: retryAfter}
		postChatWithin(t, deps, tenant, "combo", 30*time.Second)

		if got := adapter.callNames(); len(got) != wantCalls {
			t.Fatalf("upstream calls = %v, want %d", got, wantCalls)
		}
	}

	t.Run("no header is retried", func(t *testing.T) { run(t, "", 2) })
	t.Run("short wait is relayed", func(t *testing.T) { run(t, "1", 1) })
	t.Run("long wait is retried", func(t *testing.T) { run(t, "600", 2) })
}

// retryAfterAdapter sets Retry-After on the attempt writer before delegating,
// mirroring what a real adapter does with an upstream 429 header.
type retryAfterAdapter struct {
	inner      ports.UpstreamAdapter
	retryAfter string
}

func (a *retryAfterAdapter) Protocol() domain.Protocol { return a.inner.Protocol() }

func (a *retryAfterAdapter) Forward(ctx context.Context, t *domain.Target, req ports.ForwardRequest, w io.Writer) error {
	if a.retryAfter != "" {
		if rw, ok := w.(http.ResponseWriter); ok {
			rw.Header().Set("Retry-After", a.retryAfter)
		}
	}
	return a.inner.Forward(ctx, t, req, w)
}

// TestComboFailover_DirectModelUnaffected pins that the attempt loop is
// transparent for a model that is not a combo: exactly one upstream call and
// the response passed through untouched.
func TestComboFailover_DirectModelUnaffected(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {status: http.StatusInternalServerError, body: `{"e":1}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, nil, nil)

	deps, _, tenant := f.deps(t, adapter)
	rec := postChat(t, deps, tenant, "m-u1", false)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (a direct model must not fail over)", rec.Code)
	}
	if got := adapter.callNames(); len(got) != 1 {
		t.Fatalf("upstream calls = %v, want exactly one", got)
	}
}

// TestComboFailover_AllMembersFailReportsLast pins the error the client sees:
// the last attempt's, because that is the member that was still failing when
// the budget ran out.
func TestComboFailover_AllMembersFailReportsLast(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {status: http.StatusInternalServerError, body: `{"from":"u1"}`},
		"u2": {status: http.StatusBadGateway, body: `{"from":"u2"}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)
	rec := postChat(t, deps, tenant, "combo", false)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (the last member's status)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"from":"u2"`) {
		t.Fatalf("body = %q, want the last member's body", rec.Body.String())
	}
}

// TestComboFailover_AttemptBudgetCapsCalls pins the cost ceiling: a wide combo
// may not turn one client request into an unbounded number of provider calls.
func TestComboFailover_AttemptBudgetCapsCalls(t *testing.T) {
	members := []string{"u1", "u2", "u3", "u4", "u5"}
	reply := scriptedReply{status: http.StatusInternalServerError, body: `{"e":1}`}
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{}, fallback: reply}
	comboModels := make([]string, 0, len(members))
	for _, u := range members {
		comboModels = append(comboModels, "m-"+u)
	}
	f := newComboFixture(members, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: comboModels, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)
	postChat(t, deps, tenant, "combo", false)

	if got := adapter.callNames(); len(got) != domain.MaxComboFailoverAttempts {
		t.Fatalf("upstream calls = %d (%v), want %d", len(got), got, domain.MaxComboFailoverAttempts)
	}
}

// TestComboFailover_TransportErrorIsRetried covers the status-less failure: a
// dial error never reached a provider, so the next member is fair game.
func TestComboFailover_TransportErrorIsRetried(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {err: errors.New("dial tcp: connection refused")},
		"u2": {status: http.StatusOK, body: `{"ok":1}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)
	rec := postChat(t, deps, tenant, "combo", false)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := adapter.callNames(); len(got) != 2 {
		t.Fatalf("upstream calls = %v, want 2", got)
	}
}

// TestComboFailover_TTFBWatchdogFires proves a member that hangs before its
// first byte is abandoned so the next member can serve the client.
func TestComboFailover_TTFBWatchdogFires(t *testing.T) {
	prev := comboAttemptTTFB
	comboAttemptTTFB = 40 * time.Millisecond
	defer func() { comboAttemptTTFB = prev }()

	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {block: true},
		"u2": {status: http.StatusOK, body: `{"ok":"from-u2"}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- postChat(t, deps, tenant, "combo", false) }()

	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "from-u2") {
			t.Fatalf("body = %q, want the second member", rec.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish; the TTFB watchdog never fired")
	}
	if got := adapter.callNames(); len(got) != 2 {
		t.Fatalf("upstream calls = %v, want 2", got)
	}
}

// TestComboFailover_PromptGuardReappliedPerMember proves each member is called
// with its own directive: the failed member's system prompt must not leak into
// the request sent to its replacement.
func TestComboFailover_PromptGuardReappliedPerMember(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {status: http.StatusInternalServerError, body: `{"e":1}`},
		"u2": {status: http.StatusOK, body: `{"ok":1}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})
	f.systemPrompts = map[string]string{
		"m-u1": "DIRECTIVE-ONE",
		"m-u2": "DIRECTIVE-TWO",
	}

	deps, _, tenant := f.deps(t, adapter)
	rec := postChat(t, deps, tenant, "combo", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	bodies := adapter.callBodies()
	if len(bodies) != 2 {
		t.Fatalf("bodies = %d, want 2", len(bodies))
	}
	if !strings.Contains(bodies[0], "DIRECTIVE-ONE") {
		t.Fatalf("first attempt body = %q, want DIRECTIVE-ONE", bodies[0])
	}
	if strings.Contains(bodies[1], "DIRECTIVE-ONE") {
		t.Fatalf("second attempt body = %q, leaked the failed member's directive", bodies[1])
	}
	if !strings.Contains(bodies[1], "DIRECTIVE-TWO") {
		t.Fatalf("second attempt body = %q, want DIRECTIVE-TWO", bodies[1])
	}
}

// TestComboFailover_CredentialTicketReleasedBetweenAttempts proves the first
// attempt's credential reservation is gone before the second attempt asks for
// it. With MaxConcurrent 1 on a shared credential, a leaked ticket turns the
// retry into a 429 that no provider ever saw.
func TestComboFailover_CredentialTicketReleasedBetweenAttempts(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {status: http.StatusInternalServerError, body: `{"e":1}`},
		"u2": {status: http.StatusOK, body: `{"ok":1}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})
	// Both members share one credential: the second attempt must be able to
	// reserve it again.
	f.credentialMaxConcurrent = 1

	deps, snap, tenant := f.deps(t, adapter)
	for _, u := range []string{"u1", "u2"} {
		if up, ok := snap.Upstream(u); ok {
			up.CredentialRef = "shared-cred"
		}
	}

	rec := postChat(t, deps, tenant, "combo", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := adapter.callNames(); len(got) != 2 {
		t.Fatalf("upstream calls = %v, want 2", got)
	}
}

// TestComboFailover_ConsoleRowsKeepBothAttempts proves the operator sees the
// discarded member next to the one that answered: each attempt needs its own
// console row, and the final row must name the member it failed over from.
func TestComboFailover_ConsoleRowsKeepBothAttempts(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {status: http.StatusInternalServerError, body: `{"e":1}`},
		"u2": {status: http.StatusOK, body: `{"ok":1}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"combo","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(httpx.WithTenant(httpx.WithRequestID(req.Context(), "req-failover"), tenant))
	rec := httptest.NewRecorder()
	deps.forwardEndpoint("/chat/completions", openAIIngress{})(rec, req)

	rows := deps.LiveLogs.Snapshot()
	var attempted, succeeded []LiveLog
	for _, row := range rows {
		if !strings.HasPrefix(row.ID, "req-failover") {
			continue
		}
		switch row.Status {
		case http.StatusInternalServerError:
			attempted = append(attempted, row)
		case http.StatusOK:
			succeeded = append(succeeded, row)
		}
	}
	if len(attempted) != 1 {
		t.Fatalf("discarded attempt rows = %d, want 1 (rows: %+v)", len(attempted), rows)
	}
	if len(succeeded) != 1 {
		t.Fatalf("successful rows = %d, want 1", len(succeeded))
	}
	if attempted[0].ID == succeeded[0].ID {
		t.Fatalf("both attempts share console row id %q; the second would overwrite the first", attempted[0].ID)
	}
	if attempted[0].Attempt != 1 || attempted[0].Upstream != "u1" {
		t.Fatalf("discarded attempt = %+v, want attempt 1 on u1", attempted[0])
	}
	if succeeded[0].FallbackFrom != "m-u2" && succeeded[0].FallbackFrom != "m-u1" {
		// The stamp is the top-level member name; the fixture's combo members
		// are concrete models, so the first member's name is expected.
		t.Fatalf("FallbackFrom = %q, want the failed member's name", succeeded[0].FallbackFrom)
	}
	if succeeded[0].Attempt != 2 || succeeded[0].Upstream != "u2" {
		t.Fatalf("final row = %+v, want attempt 2 on u2", succeeded[0])
	}
}

// TestComboFailover_ResolveFailureKeepsRealResponse proves the attempt is only
// discarded once a replacement exists: a request whose remaining members are
// unusable must still return the real upstream response instead of an
// error envelope synthesized from a nil error.
func TestComboFailover_ResolveFailureKeepsRealResponse(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {status: http.StatusInternalServerError, body: `{"real":"u1"}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)
	// The second member's upstream is disabled, so no replacement can resolve.
	// Report u2 as circuit-open: resolution skips it, so no replacement member
	// exists and the first attempt's response must be published.
	deps.Breakers = breakerStub{open: "u2"}

	rec := postChat(t, deps, tenant, "combo", false)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want the first member's 500, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"real":"u1"`) {
		t.Fatalf("body = %q, want the first member's real response", rec.Body.String())
	}
	if got := adapter.callNames(); len(got) != 1 {
		t.Fatalf("upstream calls = %v, want 1", got)
	}
}

// TestComboFailover_DrainingPublishesCurrentAttempt proves a retry is not
// started once the host is shutting down.
func TestComboFailover_DrainingPublishesCurrentAttempt(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {status: http.StatusInternalServerError, body: `{"real":"u1"}`},
		"u2": {status: http.StatusOK, body: `{"ok":1}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)
	deps.Draining = func() bool { return true }

	rec := postChat(t, deps, tenant, "combo", false)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want the current attempt's 500", rec.Code)
	}
	if got := adapter.callNames(); len(got) != 1 {
		t.Fatalf("upstream calls = %v, want 1 while draining", got)
	}
}

// TestComboFailover_SyntheticErrorIsRetried proves an adapter that refuses
// before dialing (a synthetic ErrUpstream with no body) still lets the combo
// move on, and that the client is never handed an empty error envelope.
func TestComboFailover_SyntheticErrorIsRetried(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {err: &openai.ErrUpstream{Status: http.StatusServiceUnavailable, Cause: errors.New("no credential")}},
		"u2": {status: http.StatusOK, body: `{"ok":1}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)
	rec := postChat(t, deps, tenant, "combo", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// TestComboFailover_UsageOnlyCountsDeliveredAttempt proves a discarded attempt
// does not bill the tenant: only the response the client received is accounted.
func TestComboFailover_UsageOnlyCountsDeliveredAttempt(t *testing.T) {
	adapter := &scriptedAdapter{byName: map[string]scriptedReply{
		"u1": {status: http.StatusInternalServerError, body: `{"e":1}`},
		"u2": {status: http.StatusOK, body: `{"usage":{"prompt_tokens":11,"completion_tokens":7},"choices":[]}`},
	}}
	f := newComboFixture([]string{"u1", "u2"}, map[string]*domain.Combo{
		"combo": {Name: "combo", Strategy: domain.RoutingStrategyFailover, Models: []string{"m-u1", "m-u2"}, Enabled: true},
	}, []string{"combo"})

	deps, _, tenant := f.deps(t, adapter)
	before := tenant.UsedTokens.Load()

	rec := postChat(t, deps, tenant, "combo", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	after := tenant.UsedTokens.Load()
	if after-before != 18 {
		t.Fatalf("tenant tokens = %d, want 18 (only the delivered attempt)", after-before)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("bad body: %v", err)
	}
}

// breakerStub reports the named upstream as circuit-open and lets everything
// else through, so a test can remove a candidate without touching the catalog.
type breakerStub struct {
	open string
}

func (b breakerStub) Allow(name string) error {
	if name == b.open {
		return upstream.ErrCircuitOpen
	}
	return nil
}

func (b breakerStub) Report(string, bool) {}
