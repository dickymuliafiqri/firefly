package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
)

// contextTestSnapshot builds a catalog whose models declare max_context, one
// model that does not, and three combos: one whose members all declare a
// window, one where a member is bare, and one where a member is missing from
// the catalog — the last two must publish nothing.
func contextTestSnapshot() *domain.CatalogSnapshot {
	hash := auth.HashKey(testKey)
	return domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"u": {Name: "u", Protocol: domain.ProtocolOpenAI, BaseURL: "https://x/v1", CredentialRef: "UP_KEY"}},
		[]string{"u"},
		map[string]*domain.ModelEntry{
			"ctx-128k":  {PublicName: "ctx-128k", Upstream: "u", UpstreamModel: "ctx-128k", MaxContext: 128000, Enabled: true},
			"ctx-512k":  {PublicName: "ctx-512k", Upstream: "u", UpstreamModel: "ctx-512k", MaxContext: 512000, Enabled: true},
			"ctx-unset": {PublicName: "ctx-unset", Upstream: "u", UpstreamModel: "ctx-unset", Enabled: true},
		},
		[]string{"ctx-128k", "ctx-512k", "ctx-unset"},
		map[string]*domain.Tenant{hash: {
			KeyHash:       hash,
			Name:          "alpha",
			Status:        domain.TenantStatusActive,
			AllowedModels: []string{"ctx-128k", "ctx-512k", "ctx-unset", "combo-full", "combo-mixed", "combo-missing"},
			RateLimit:     domain.RateLimit{RPS: 1000, Burst: 1000, MaxConcurrent: 100},
		}},
		[]string{hash},
		domain.WithCombos(
			map[string]*domain.Combo{
				"combo-full":    {Name: "combo-full", Strategy: domain.RoutingStrategyFailover, Models: []string{"ctx-512k", "ctx-128k"}, Enabled: true},
				"combo-mixed":   {Name: "combo-mixed", Strategy: domain.RoutingStrategyFailover, Models: []string{"ctx-512k", "ctx-unset"}, Enabled: true},
				"combo-missing": {Name: "combo-missing", Strategy: domain.RoutingStrategyFailover, Models: []string{"ctx-512k", "not-in-catalog"}, Enabled: true},
			},
			[]string{"combo-full", "combo-mixed", "combo-missing"},
		),
	)
}

func newContextTestServer(t *testing.T, snap *domain.CatalogSnapshot) *Server {
	t.Helper()
	deps := RouterDeps{
		Snapshots:   fakeProvider{snap},
		TenantStore: auth.NewStore(fakeProvider{snap}),
		Limiter:     limits.New(),
		Adapter:     &fakeAdapter{body: `{}`},
		Usage:       usage.NewCounters(),
		Logger:      discardLogger(),
	}
	return newTestServer(t, deps)
}

func intPtr(v int) *int { return &v }

// TestListModelsPublishesContextLength is the regression test for coding
// agents (Cline, Roo Code, Hermes, OpenRouter-style clients) that read model
// metadata to learn the context window: the catalog's max_context must reach
// the client as context_length, and a combo may only advertise the smallest
// window among its members.
func TestListModelsPublishesContextLength(t *testing.T) {
	s := newContextTestServer(t, contextTestSnapshot())
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp ModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}

	got := map[string]*int{}
	for _, m := range resp.Data {
		got[m.ID] = m.ContextLength
	}
	cases := []struct {
		id   string
		want *int
	}{
		{"ctx-128k", intPtr(128000)},
		{"ctx-512k", intPtr(512000)},
		{"ctx-unset", nil},
		{"combo-full", intPtr(128000)},
		{"combo-mixed", nil},
		{"combo-missing", nil},
	}
	for _, c := range cases {
		if (got[c.id] == nil) != (c.want == nil) {
			t.Fatalf("model %q context_length present=%v, want present=%v", c.id, got[c.id] != nil, c.want != nil)
		}
		if got[c.id] != nil && *got[c.id] != *c.want {
			t.Fatalf("model %q context_length = %d, want %d", c.id, *got[c.id], *c.want)
		}
	}
}

// TestListModelsOmitsContextLengthFromJSON proves the omitempty tag holds: a
// model without max_context emits no context_length key at all, so a client
// never mistakes a missing configuration for a zero-width window.
func TestListModelsOmitsContextLengthFromJSON(t *testing.T) {
	s := newContextTestServer(t, contextTestSnapshot())
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	var raw struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	for _, m := range raw.Data {
		id := m["id"]
		_, hasCtx := m["context_length"]
		switch string(id[1 : len(id)-1]) {
		case "ctx-128k":
			if !hasCtx {
				t.Fatalf("model with max_context must emit context_length: %v", m)
			}
		case "ctx-unset":
			if hasCtx {
				t.Fatalf("model with unset max_context must not emit context_length: %v", m)
			}
		}
	}
}

// TestRetrieveModelPublishesContextLength covers GET /v1/models/{id}, the
// single-model surface some agents use instead of the list.
func TestRetrieveModelPublishesContextLength(t *testing.T) {
	s := newContextTestServer(t, contextTestSnapshot())

	get := func(id string) (int, *ModelObj) {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/models/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+testKey)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			return rec.Code, nil
		}
		var m ModelObj
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatalf("decode: %v body=%s", err, rec.Body.String())
		}
		return rec.Code, &m
	}

	if code, m := get("ctx-128k"); code != http.StatusOK || m.ContextLength == nil || *m.ContextLength != 128000 {
		t.Fatalf("ctx-128k = %d %+v, want context_length 128000", code, m)
	}
	if code, m := get("combo-full"); code != http.StatusOK || m.ContextLength == nil || *m.ContextLength != 128000 {
		t.Fatalf("combo-full = %d %+v, want smallest member window 128000", code, m)
	}
	if code, m := get("ctx-unset"); code != http.StatusOK || m.ContextLength != nil {
		t.Fatalf("ctx-unset = %d %+v, want no context_length", code, m)
	}
	if code, m := get("combo-mixed"); code != http.StatusOK || m.ContextLength != nil {
		t.Fatalf("combo-mixed = %d %+v, want no context_length", code, m)
	}
	if code, _ := get("nope"); code != http.StatusNotFound {
		t.Fatalf("unknown model = %d, want 404", code)
	}
}
