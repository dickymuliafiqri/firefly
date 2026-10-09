package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/notify"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
)

// budgetTenant builds a tenant carrying a spend cap and a pre-loaded counter.
func budgetTenant(name string, budgetMicros, spentMicros int64) *domain.Tenant {
	t := domain.NewTenant()
	t.Name = name
	t.APIKey = "sk-gw-" + name
	t.Status = domain.TenantStatusActive
	t.AllowedModels = []string{"*"}
	t.BudgetMicros = budgetMicros
	t.SpentMicros.Store(spentMicros)
	return &t
}

func TestBudgetExceeded_UnlimitedNeverBlocks(t *testing.T) {
	tenant := budgetTenant("free", 0, 1_000_000_000)
	if tenant.BudgetExceeded() {
		t.Fatal("a zero budget must mean unlimited")
	}
	if got := tenant.BudgetRemainingMicros(); got != -1 {
		t.Fatalf("remaining = %d, want -1 for unlimited", got)
	}
	if got := tenant.BudgetRatio(); got != 0 {
		t.Fatalf("ratio = %v, want 0 for unlimited", got)
	}
}

func TestBudgetExceeded_AtAndAboveCap(t *testing.T) {
	if tenant := budgetTenant("under", 1000, 999); tenant.BudgetExceeded() {
		t.Fatal("999 of 1000 must not block")
	}
	if tenant := budgetTenant("at", 1000, 1000); !tenant.BudgetExceeded() {
		t.Fatal("exactly at the cap must block")
	}
	if tenant := budgetTenant("over", 1000, 5000); !tenant.BudgetExceeded() {
		t.Fatal("above the cap must block")
	}
}

func TestBudgetRemainingClampsAtZero(t *testing.T) {
	tenant := budgetTenant("over", 1000, 4000)
	if got := tenant.BudgetRemainingMicros(); got != 0 {
		t.Fatalf("remaining = %d, want 0 (clamped)", got)
	}
}

func TestAddSpend_SoftWarningFiresOnce(t *testing.T) {
	tenant := budgetTenant("warn", 1000, 0)

	// 79% then 81%: only the crossing counts.
	if tenant.AddSpend(790) {
		t.Fatal("79% must not warn")
	}
	if !tenant.AddSpend(20) {
		t.Fatal("crossing 80% must warn")
	}
	for i := 0; i < 5; i++ {
		if tenant.AddSpend(1) {
			t.Fatal("warning must fire exactly once per period")
		}
	}
	if got := tenant.SpentMicros.Load(); got != 815 {
		t.Fatalf("spent = %d, want 815", got)
	}
}

func TestAddSpend_NoWarningWithoutBudget(t *testing.T) {
	tenant := budgetTenant("free", 0, 0)
	for i := 0; i < 10; i++ {
		if tenant.AddSpend(1000) {
			t.Fatal("an unlimited tenant must never warn")
		}
	}
}

func TestResetSpend_RearmsWarning(t *testing.T) {
	tenant := budgetTenant("reset", 1000, 900)
	if !tenant.AddSpend(200) {
		t.Fatal("crossing 80% must warn")
	}
	if !tenant.BudgetExceeded() {
		t.Fatal("1100 of 1000 must block")
	}

	tenant.ResetSpend()
	if tenant.BudgetExceeded() {
		t.Fatal("reset must clear the block")
	}
	if got := tenant.SpentMicros.Load(); got != 0 {
		t.Fatalf("spent = %d, want 0", got)
	}
	if !tenant.AddSpend(850) {
		t.Fatal("warning must be re-armed after a reset")
	}
}

func TestEnsureCounters_OnBareLiteral(t *testing.T) {
	// A tenant decoded from JSON or built as a literal has nil counters; every
	// accessor must survive that rather than panicking mid-request.
	var tenant domain.Tenant
	tenant.Name = "bare"
	if tenant.BudgetExceeded() {
		t.Fatal("bare tenant must not block")
	}
	if tenant.AddSpend(10) {
		t.Fatal("bare tenant must not warn")
	}
	tenant.EnsureCounters()
	tenant.AddSpend(10)
	if got := tenant.SpentMicros.Load(); got != 10 {
		t.Fatalf("spent = %d, want 10", got)
	}
}

func TestBudgetGuard_ConcurrentSpendNeverExceedsByMoreThanOneRequest(t *testing.T) {
	const (
		budget     = 100_000
		goroutines = 100
		perRequest = 1_000
	)
	tenant := budgetTenant("race", budget, 0)

	var wg sync.WaitGroup
	var allowed atomic.Int64
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tenant.BudgetExceeded() {
				return
			}
			allowed.Add(1)
			tenant.AddSpend(perRequest)
		}()
	}
	wg.Wait()

	spent := tenant.SpentMicros.Load()
	if spent > budget+perRequest {
		t.Fatalf("spent %d overshot the budget by more than one in-flight request", spent)
	}
	if spent != allowed.Load()*perRequest {
		t.Fatalf("spent %d does not match %d admitted requests", spent, allowed.Load())
	}
}

// testDepsWithTenant builds a router wired to a snapshot containing exactly one
// tenant, so the budget guard can be exercised end to end through
// forwardEndpoint without touching the shared fixtures.
func testDepsWithTenant(t *testing.T, tenant *domain.Tenant) (RouterDeps, *domain.CatalogSnapshot) {
	t.Helper()
	tenant.EnsureCounters()
	hash := auth.HashKey(tenant.APIKey)
	tenant.KeyHash = hash

	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"u": {Name: "u", Protocol: domain.ProtocolOpenAI, BaseURL: "https://x/v1", CredentialRef: "UP_KEY"}},
		[]string{"u"},
		map[string]*domain.ModelEntry{
			"gpt-4o": {PublicName: "gpt-4o", Upstream: "u", UpstreamModel: "gpt-4o", Enabled: true},
		},
		[]string{"gpt-4o"},
		map[string]*domain.Tenant{hash: tenant},
		[]string{hash},
	)
	store := auth.NewStore(fakeProvider{snap})
	return RouterDeps{
		Snapshots:   fakeProvider{snap},
		TenantStore: store,
		Limiter:     limits.New(),
		Adapter:     &fakeAdapter{body: `{"ok":true}`},
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	}, snap
}

// recordingNotifier captures emitted events for assertion.
type recordingNotifier struct {
	mu     sync.Mutex
	events []notify.Event
}

func (n *recordingNotifier) Emit(ev notify.Event) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.events = append(n.events, ev)
}

func (n *recordingNotifier) count(eventType string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	found := 0
	for _, ev := range n.events {
		if ev.Type == eventType {
			found++
		}
	}
	return found
}

func TestEmitTenantBudgetWarning(t *testing.T) {
	notifier := &recordingNotifier{}
	deps := RouterDeps{Notifier: notifier}
	tenant := budgetTenant("warnme", 1000, 850)

	deps.emitTenantBudgetWarning(tenant)
	if got := notifier.count("tenant.budget_warning"); got != 1 {
		t.Fatalf("warning events = %d, want 1", got)
	}

	// A nil notifier must be a no-op, not a panic.
	RouterDeps{}.emitTenantBudgetWarning(tenant)
	RouterDeps{}.emitTenantBudgetExceeded(tenant)
}

func TestBudgetGuard_RejectsExhaustedTenant(t *testing.T) {
	tenant := budgetTenant("broke", 1000, 1000)
	deps, _ := testDepsWithTenant(t, tenant)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(httpx.WithTenant(req.Context(), tenant))
	rec := httptest.NewRecorder()

	deps.forwardEndpoint("/chat/completions", openAIIngress{})(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60", got)
	}
	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("bad error envelope: %v", err)
	}
	if payload.Error.Type != "rate_limit_error" {
		t.Fatalf("error type = %q", payload.Error.Type)
	}
}

func TestBudgetGuard_SpendUnchangedByRejection(t *testing.T) {
	tenant := budgetTenant("frozen", 1000, 1000)
	deps, _ := testDepsWithTenant(t, tenant)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewBufferString(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	req = req.WithContext(httpx.WithTenant(req.Context(), tenant))
	rec := httptest.NewRecorder()

	deps.forwardEndpoint("/chat/completions", openAIIngress{})(rec, req)

	if got := tenant.SpentMicros.Load(); got != 1000 {
		t.Fatalf("spent = %d, want 1000 (a rejection must not change the counter)", got)
	}
}
