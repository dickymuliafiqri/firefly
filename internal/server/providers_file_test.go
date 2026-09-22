package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/observability/metrics"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
)

// Secrets held by the file-mode fixture. They must never reach the wire, and the
// tests assert on their absence from every response body.
const (
	fileConfiguredSecret = "sk-file-aaaaaaaaaaaa"
	fileHarvestedSecret  = "sk-file-bbbbbbbbbbbb"
	fileRetiredSecret    = "sk-file-cccccccccccc"
	fileDisabledSecret   = "sk-file-eeeeeeeeeeee"
)

// fileProvidersListDTO mirrors the list envelope: rows plus the provenance the
// dashboard needs to label a read-only page.
type fileProvidersListDTO struct {
	Count     int                    `json:"count"`
	Providers []turso.ProviderRecord `json:"providers"`
	Storage   string                 `json:"storage"`
	ReadOnly  bool                   `json:"read_only"`
}

// fileProviderKeysDTO mirrors the key list envelope.
type fileProviderKeysDTO struct {
	ProviderID int64          `json:"provider_id"`
	Count      int            `json:"count"`
	Keys       []adminKeyView `json:"keys"`
}

// fileProvidersEnv serves the provider surface from a catalog snapshot alone:
// no Turso database, exactly the state of a file-config deployment.
type fileProvidersEnv struct {
	t *testing.T
	s *Server
}

// do issues one JSON request through the real server handler. No admin token is
// configured, so authorizeAdmin allows the request (the same posture as the
// other bare unit servers).
func (e *fileProvidersEnv) do(method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal %s %s body: %v", method, path, err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, req)
	return w
}

func (e *fileProvidersEnv) listProviders() fileProvidersListDTO {
	e.t.Helper()
	w := e.do(http.MethodGet, "/api/providers", nil)
	if w.Code != http.StatusOK {
		e.t.Fatalf("list providers: status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var res fileProvidersListDTO
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		e.t.Fatalf("decode provider list: %v; body = %s", err, w.Body.String())
	}
	return res
}

func (e *fileProvidersEnv) listKeys(providerID int64) (fileProviderKeysDTO, string) {
	e.t.Helper()
	w := e.do(http.MethodGet, fmt.Sprintf("/api/providers/%d/keys", providerID), nil)
	if w.Code != http.StatusOK {
		e.t.Fatalf("list keys for %d: status = %d, want 200; body = %s", providerID, w.Code, w.Body.String())
	}
	var res fileProviderKeysDTO
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		e.t.Fatalf("decode key list: %v; body = %s", err, w.Body.String())
	}
	return res, w.Body.String()
}

// newFileProvidersEnv builds a fixture covering every shape the projection has
// to render: a multi-key pool (one config-declared slot, one database-managed
// slot, one revoked slot), a disabled pool, and a keyless free-tier upstream
// that has no credential pool to administer.
func newFileProvidersEnv(t *testing.T) *fileProvidersEnv {
	t.Helper()

	configured := &domain.KeySlot{Ref: "OPENAI_KEY_1", Secret: fileConfiguredSecret}
	harvested := &domain.KeySlot{Ref: "pool-a-key-7", Secret: fileHarvestedSecret, APIKeyID: 7}
	retired := &domain.KeySlot{Ref: "OPENAI_KEY_RETIRED", Secret: fileRetiredSecret}
	retired.Revoked.Store(true)

	poolA := &domain.Upstream{
		Name:     "pool-a",
		Protocol: domain.ProtocolOpenAI,
		BaseURL:  "https://api.pool-a.test/v1",
		KeyRing:  domain.NewKeyRing(domain.KeyStrategyRoundRobin, []*domain.KeySlot{configured, harvested, retired}),
	}
	poolB := &domain.Upstream{
		Name:     "pool-b",
		Protocol: domain.ProtocolAnthropic,
		BaseURL:  "https://api.pool-b.test",
		Disabled: true,
		KeyRing: domain.NewKeyRing(domain.KeyStrategyLeastInflight, []*domain.KeySlot{
			{Ref: "ANTHROPIC_KEY_1", Secret: fileDisabledSecret},
		}),
	}
	freeTier := &domain.Upstream{
		Name:     "free-tier",
		Protocol: domain.ProtocolOpenCode,
		BaseURL:  "https://opencode.ai/zen/v1",
	}

	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"pool-a": poolA, "pool-b": poolB, "free-tier": freeTier},
		[]string{"pool-a", "pool-b", "free-tier"},
		map[string]*domain.ModelEntry{},
		nil,
		map[string]*domain.Tenant{},
		nil,
	)

	mx := metrics.New()
	// Counted exactly as the forward path counts them, under
	// "<upstream>/<ref>".
	mx.ObserveKeyRequest("pool-a", "pool-a-key-7", 200)
	mx.ObserveKeyRequest("pool-a", "pool-a-key-7", 200)

	deps := RouterDeps{Metrics: mx, Snapshots: fakeProvider{snap}}
	return &fileProvidersEnv{
		t: t,
		s: New(Config{Addr: "127.0.0.1:0"}, deps, context.Background(), nil),
	}
}

func TestFileProviderStore_ListsCredentialUpstreams(t *testing.T) {
	env := newFileProvidersEnv(t)

	w := env.do(http.MethodGet, "/api/providers", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var res fileProvidersListDTO
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v; body = %s", err, w.Body.String())
	}

	if res.Storage != "file" || !res.ReadOnly {
		t.Fatalf("provenance = (%q, read_only=%t), want (\"file\", true)", res.Storage, res.ReadOnly)
	}

	// Catalog order, and the keyless upstream is omitted rather than shown with
	// an empty pool.
	if res.Count != 2 || len(res.Providers) != 2 {
		t.Fatalf("count = %d, providers = %+v, want 2 rows", res.Count, res.Providers)
	}
	if got := res.Providers[0].Name; got != "pool-a" {
		t.Errorf("providers[0].name = %q, want pool-a", got)
	}
	if got := res.Providers[1].Name; got != "pool-b" {
		t.Errorf("providers[1].name = %q, want pool-b", got)
	}

	a := res.Providers[0]
	if a.ID != fileProviderID("pool-a") {
		t.Errorf("pool-a id = %d, want the derived %d", a.ID, fileProviderID("pool-a"))
	}
	if a.BaseURL != "https://api.pool-a.test/v1" || !a.IsActive {
		t.Errorf("pool-a row = %+v, want the live base URL and is_active", a)
	}
	if a.ActiveKeys != 2 {
		t.Errorf("pool-a active_keys = %d, want 2 (the revoked slot does not count)", a.ActiveKeys)
	}
	if !strings.Contains(a.Description, "config-file") {
		t.Errorf("pool-a description = %q, want it to name the file source", a.Description)
	}

	b := res.Providers[1]
	if b.IsActive {
		t.Error("pool-b is_active = true, want false for a disabled upstream")
	}
	if b.ActiveKeys != 1 {
		t.Errorf("pool-b active_keys = %d, want 1", b.ActiveKeys)
	}

	for _, secret := range []string{fileConfiguredSecret, fileHarvestedSecret, fileRetiredSecret, fileDisabledSecret} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("list response leaked %q: %s", secret, w.Body.String())
		}
	}
}

func TestFileProviderStore_KeysAreMaskedAndCounted(t *testing.T) {
	env := newFileProvidersEnv(t)
	const providerName = "pool-a"
	providerID := fileProviderID(providerName)

	// One provider row reads back on its own, by the same derived id.
	w := env.do(http.MethodGet, fmt.Sprintf("/api/providers/%d", providerID), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get provider: status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	res, body := env.listKeys(providerID)
	if res.ProviderID != providerID {
		t.Errorf("provider_id = %d, want %d", res.ProviderID, providerID)
	}
	if res.Count != 3 || len(res.Keys) != 3 {
		t.Fatalf("keys = %d (count %d), want 3: %+v", len(res.Keys), res.Count, res.Keys)
	}

	byID := make(map[int64]adminKeyView, len(res.Keys))
	for _, key := range res.Keys {
		byID[key.ID] = key
	}

	configured, ok := byID[fileKeyID(providerID, "OPENAI_KEY_1")]
	if !ok {
		t.Fatalf("no row for the config-declared slot; ids = %v", byID)
	}
	// A config-declared credential has no stored secret to hint at, so its ref
	// stands in — still masked, and still not a secret.
	if configured.APIKeyHint != "OPE...EY_1" {
		t.Errorf("configured hint = %q, want %q", configured.APIKeyHint, "OPE...EY_1")
	}
	if configured.Status != "active" || !configured.IsActive || configured.TotalRequests != 0 {
		t.Errorf("configured row = %+v, want active with no requests", configured)
	}

	harvested, ok := byID[fileKeyID(providerID, "pool-a-key-7")]
	if !ok {
		t.Fatalf("no row for the harvested slot; ids = %v", byID)
	}
	// A database-managed credential is hinted exactly as in Turso mode.
	if harvested.APIKeyHint != "sk-...bbbb" {
		t.Errorf("harvested hint = %q, want %q", harvested.APIKeyHint, "sk-...bbbb")
	}
	if harvested.TotalRequests != 2 {
		t.Errorf("harvested total_requests = %d, want the 2 recorded requests", harvested.TotalRequests)
	}
	// Usage counters come from the metrics registry, which carries no timestamp.
	if harvested.LastUsedAt != 0 {
		t.Errorf("last_used_at = %d, want 0 (no timestamp source)", harvested.LastUsedAt)
	}
	if harvested.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want nil (no expiry source)", *harvested.ExpiresAt)
	}

	retired, ok := byID[fileKeyID(providerID, "OPENAI_KEY_RETIRED")]
	if !ok {
		t.Fatalf("no row for the revoked slot; ids = %v", byID)
	}
	if retired.Status != "revoked" || retired.IsActive {
		t.Errorf("revoked row = %+v, want status revoked and is_active false", retired)
	}

	for _, key := range res.Keys {
		if key.Secret != "" {
			t.Fatalf("key %d serialized its secret", key.ID)
		}
	}
	for _, secret := range []string{fileConfiguredSecret, fileHarvestedSecret, fileRetiredSecret} {
		if strings.Contains(body, secret) {
			t.Fatalf("key list leaked %q: %s", secret, body)
		}
	}

	// Ids travel in dashboard URLs, so a second read must resolve the same rows:
	// a file-built snapshot is rebuilt on every reload and cannot hand out
	// autoincrement ids.
	again, _ := env.listKeys(providerID)
	for i := range again.Keys {
		if again.Keys[i].ID != res.Keys[i].ID {
			t.Fatalf("key ids moved between reads: %v -> %v", res.Keys, again.Keys)
		}
	}
}

func TestFileProviderStore_MutationsAreRefused(t *testing.T) {
	env := newFileProvidersEnv(t)
	providerID := fileProviderID("pool-a")
	keyID := fileKeyID(providerID, "OPENAI_KEY_1")

	const freshSecret = "sk-fresh-dddddddddddd"
	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"create provider", http.MethodPost, "/api/providers",
			map[string]any{"name": "pool-c", "base_url": "https://api.pool-c.test"}},
		{"update provider", http.MethodPut, fmt.Sprintf("/api/providers/%d", providerID),
			map[string]any{"is_active": false}},
		{"delete provider", http.MethodDelete, fmt.Sprintf("/api/providers/%d", providerID), nil},
		{"upsert keys", http.MethodPost, fmt.Sprintf("/api/providers/%d/keys", providerID),
			map[string]any{"keys": []map[string]any{{"api_key": freshSecret}}}},
		{"patch status", http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID),
			map[string]any{"status": "deactivated"}},
		{"rotate secret", http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID),
			map[string]any{"api_key": freshSecret}},
		{"delete key", http.MethodDelete, fmt.Sprintf("/api/keys/%d", keyID), nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := env.do(tc.method, tc.path, tc.body)
			if w.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want 501; body = %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			// The refusal has to name the editable surface: an operator who
			// landed here needs to know where the credential actually lives.
			for _, want := range []string{"upstreams.json", "turso"} {
				if !strings.Contains(body, want) {
					t.Errorf("501 body does not mention %q: %s", want, body)
				}
			}
			if strings.Contains(body, freshSecret) {
				t.Fatalf("501 body echoed the submitted credential: %s", body)
			}
		})
	}

	// Nothing was applied: the projection still reads exactly as before.
	res := env.listProviders()
	if res.Count != 2 || len(res.Providers) != 2 {
		t.Fatalf("after refused writes: %+v, want the same 2 rows", res.Providers)
	}
	if res.Storage != "file" || !res.ReadOnly {
		t.Fatalf("after refused writes: provenance = (%q, read_only=%t)", res.Storage, res.ReadOnly)
	}
}

func TestFileProviderStore_MaskedHintIsRejectedBeforeStore(t *testing.T) {
	env := newFileProvidersEnv(t)
	keyID := fileKeyID(fileProviderID("pool-a"), "OPENAI_KEY_1")

	// The dashboard sends back the hint it was shown; that is a client bug and
	// must be caught in the handler, not blamed on the read-only store.
	w := env.do(http.MethodPatch, fmt.Sprintf("/api/keys/%d", keyID), map[string]any{"api_key": "sk-...bbbb"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "masked hint") {
		t.Errorf("400 body does not explain the rejection: %s", w.Body.String())
	}
}

func TestFileProviderStore_UnknownIDsAreNotFound(t *testing.T) {
	env := newFileProvidersEnv(t)

	cases := []struct {
		name string
		path string
		want int
	}{
		{"keyless upstream has no provider row", fmt.Sprintf("/api/providers/%d", fileProviderID("free-tier")), http.StatusNotFound},
		{"unknown provider id", fmt.Sprintf("/api/providers/%d", fileProviderID("ghost")), http.StatusNotFound},
		{"malformed provider id", "/api/providers/abc", http.StatusBadRequest},
		{"zero provider id", "/api/providers/0", http.StatusBadRequest},
		{"keys of a keyless upstream", fmt.Sprintf("/api/providers/%d/keys", fileProviderID("free-tier")), http.StatusNotFound},
		{"keys of an unknown provider", fmt.Sprintf("/api/providers/%d/keys", fileProviderID("ghost")), http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := env.do(http.MethodGet, tc.path, nil)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestProviderStoreProvenance(t *testing.T) {
	cases := []struct {
		name         string
		store        providerAdminStore
		wantStorage  string
		wantReadOnly bool
	}{
		{"file projection", newFileProviderStore(nil, nil), "file", true},
		{"turso store", &turso.Store{}, "turso", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storage, readOnly := providerStoreProvenance(tc.store)
			if storage != tc.wantStorage || readOnly != tc.wantReadOnly {
				t.Fatalf("provenance = (%q, %t), want (%q, %t)", storage, readOnly, tc.wantStorage, tc.wantReadOnly)
			}
		})
	}
}

func TestStablePositiveID(t *testing.T) {
	id := fileProviderID("pool-a")
	if id <= 0 {
		t.Fatalf("provider id = %d, want positive", id)
	}
	if again := fileProviderID("pool-a"); again != id {
		t.Fatalf("provider id is not stable: %d -> %d", id, again)
	}

	// The namespace and the separator both have to prevent collisions: a key id
	// that collided with a provider id would let one URL read another row.
	if fileProviderID("x") == fileKeyID(fileProviderID("x"), "") {
		t.Error("provider and key ids collide in the shared namespace")
	}
	if stablePositiveID("ns", "ab", "c") == stablePositiveID("ns", "a", "bc") {
		t.Error("adjacent parts collide: the separator is not doing its job")
	}
	if fileProviderID("pool-a") == fileProviderID("pool-b") {
		t.Error("distinct names collide")
	}
}

// TestProvidersAdmin_TursoStoreTakesPrecedence pins the resolver's first branch:
// with a database attached (and no snapshot loaded at all) the catalog comes
// from Turso, not from the read-only file projection.
func TestProvidersAdmin_TursoStoreTakesPrecedence(t *testing.T) {
	env := newProvidersEnv(t, nil)
	env.createProvider("poolA", "https://api.pool-a.test")

	w := env.admin(http.MethodGet, "/api/providers", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var res fileProvidersListDTO
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v; body = %s", err, w.Body.String())
	}
	if res.Storage != "turso" || res.ReadOnly {
		t.Fatalf("provenance = (%q, read_only=%t), want (\"turso\", false)", res.Storage, res.ReadOnly)
	}
	if res.Count != 1 || len(res.Providers) != 1 || res.Providers[0].Name != "poolA" {
		t.Fatalf("providers = %+v, want the single store-backed poolA row", res.Providers)
	}
}
