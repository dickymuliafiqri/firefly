package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
	_ "turso.tech/database/tursogo"
)

const (
	harvesterTestToken = "svc-token-2f9a1c"
	harvesterTestAdmin = "admin-token-7d3b5e"
)

// harvesterEnv wires the harvester sync surface to an in-memory Turso store and
// to a registry whose reload cursor the test controls, so reload assertions are
// deterministic and no 15s ticker participates.
type harvesterEnv struct {
	t     *testing.T
	db    *sql.DB
	store *turso.Store
	reg   *registry.Registry
	sync  *turso.Syncer
	s     *Server
	logs  *bytes.Buffer
}

func newHarvesterEnv(t *testing.T, mutate func(*RouterDeps)) *harvesterEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	db, err := sql.Open("turso", ":memory:")
	if err != nil {
		cancel()
		t.Fatalf("open memory db: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() {
		cancel()
		_ = db.Close()
	})

	if err := turso.MigrateSchema(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}

	store := turso.NewStoreWithDB(db)
	reg := registry.New()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mgr := NewTursoManager("", store, logger)
	// A client-less syncer never Pulls, and AttachRegistry only rebuilds the
	// syncer when a cloud client is connected, so this hand-bound instance
	// survives server startup. SyncOnce still runs the real reload path:
	// revision check -> LoadCatalogSnapshot -> registry swap.
	syncer := turso.NewSyncer(nil, store, reg, turso.SyncerConfig{
		Interval:  time.Hour,
		EnvLookup: os.LookupEnv,
		Logger:    logger,
	})
	mgr.syncer = syncer

	deps := RouterDeps{
		Snapshots:    reg,
		Registry:     reg,
		TenantStore:  auth.NewStore(reg),
		Limiter:      limits.New(),
		TursoManager: mgr,
		ServiceToken: harvesterTestToken,
		Logger:       logger,
	}
	if mutate != nil {
		mutate(&deps)
	}

	env := &harvesterEnv{
		t:     t,
		db:    db,
		store: store,
		reg:   reg,
		sync:  syncer,
		s:     New(Config{Addr: "0.0.0.0:8080"}, deps, ctx, logger),
		logs:  logs,
	}
	env.reseedSyncCursor()
	return env
}

// reseedSyncCursor pins the reload cursor to the current revision: setup writes
// during a test bump the revision and must not be mistaken for the batch under
// test.
func (e *harvesterEnv) reseedSyncCursor() int64 {
	e.t.Helper()
	rev, err := e.store.GetCatalogRevision(context.Background())
	if err != nil {
		e.t.Fatalf("read catalog revision: %v", err)
	}
	e.sync.SetInitialRevision(rev)
	return rev
}

func (e *harvesterEnv) postReader(body io.Reader, token string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/harvester/sync", body)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, req)
	return w
}

func (e *harvesterEnv) post(body []byte, token string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.postReader(bytes.NewReader(body), token)
}

func (e *harvesterEnv) postJSON(payload any) *httptest.ResponseRecorder {
	e.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		e.t.Fatalf("marshal sync payload: %v", err)
	}
	return e.post(raw, harvesterTestToken)
}

// harvesterSyncResult mirrors the handler's flattened response envelope.
type harvesterSyncResult struct {
	Created     int                `json:"created"`
	Updated     int                `json:"updated"`
	Unchanged   int                `json:"unchanged"`
	Deactivated int                `json:"deactivated"`
	KeyIDs      map[string][]int64 `json:"key_ids"`
	Revision    int64              `json:"revision"`
	Pushed      bool               `json:"pushed"`
	Reloaded    bool               `json:"reloaded"`
}

func decodeHarvesterSync(t *testing.T, w *httptest.ResponseRecorder) harvesterSyncResult {
	t.Helper()
	var out harvesterSyncResult
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode sync response: %v; body = %s", err, w.Body.String())
	}
	return out
}

type harvesterKeyRow struct {
	ID         int64
	ProviderID int64
	Status     string
	IsActive   int
	CreatedAt  int64
	UpdatedAt  int64
	LastUsed   int64
	Requests   int64
	Meta       sql.NullString
}

func (e *harvesterEnv) keyRow(apiKey string) harvesterKeyRow {
	e.t.Helper()
	var row harvesterKeyRow
	err := e.db.QueryRowContext(context.Background(), `
		SELECT id, provider_id, status, is_active, created_at, updated_at,
		       last_used_at, total_requests, account_metadata
		FROM api_keys WHERE api_key = ?
	`, apiKey).Scan(&row.ID, &row.ProviderID, &row.Status, &row.IsActive,
		&row.CreatedAt, &row.UpdatedAt, &row.LastUsed, &row.Requests, &row.Meta)
	if err != nil {
		e.t.Fatalf("read api key row: %v", err)
	}
	return row
}

func (e *harvesterEnv) countProviders() int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM providers").Scan(&n); err != nil {
		e.t.Fatalf("count providers: %v", err)
	}
	return n
}

func (e *harvesterEnv) revision() int64 {
	e.t.Helper()
	rev, err := e.store.GetCatalogRevision(context.Background())
	if err != nil {
		e.t.Fatalf("read catalog revision: %v", err)
	}
	return rev
}

func (e *harvesterEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.db.ExecContext(context.Background(), query, args...); err != nil {
		e.t.Fatalf("exec %q: %v", query, err)
	}
}

// seedLinkedUpstream creates a provider with the given keys and links an
// upstream to it through upstreams.provider_id — the very join the snapshot
// loader reads — so a synced key becomes routable after the reload.
func (e *harvesterEnv) seedLinkedUpstream(providerName, upstreamName string, apiKeys ...string) []int64 {
	e.t.Helper()
	ctx := context.Background()

	keys := make([]turso.ProviderKeySyncEntry, 0, len(apiKeys))
	for _, k := range apiKeys {
		keys = append(keys, turso.ProviderKeySyncEntry{Provider: providerName, APIKey: k})
	}
	res, err := e.store.SyncProviderKeys(ctx, turso.ProviderSyncRequest{
		Providers: []turso.ProviderSyncEntry{{Name: providerName, BaseURL: "https://api." + providerName + ".test"}},
		Keys:      keys,
	})
	if err != nil {
		e.t.Fatalf("seed provider %q: %v", providerName, err)
	}

	var providerID int64
	if err := e.db.QueryRowContext(ctx, "SELECT id FROM providers WHERE name = ?", providerName).Scan(&providerID); err != nil {
		e.t.Fatalf("read provider %q id: %v", providerName, err)
	}
	enabled := true
	if err := e.store.SaveSettings(ctx, config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{{
			Name:        upstreamName,
			Protocol:    "openai",
			BaseURL:     "https://api." + providerName + ".test",
			ProviderID:  &providerID,
			KeyStrategy: "round_robin",
			Enabled:     &enabled,
		}},
	}); err != nil {
		e.t.Fatalf("link upstream %q to provider %q: %v", upstreamName, providerName, err)
	}
	return res.KeyIDs[providerName]
}

func TestHarvesterSync_AuthMatrix(t *testing.T) {
	batch := map[string]any{
		"providers": []map[string]any{{"name": "poolA", "base_url": "https://api.pool-a.test"}},
		"keys":      []map[string]any{{"provider": "poolA", "api_key": "sk-auth-1"}},
	}

	cases := []struct {
		name   string
		token  string
		mutate func(*RouterDeps)
		want   int
	}{
		{"valid service token", harvesterTestToken, nil, http.StatusOK},
		{"missing authorization header", "", nil, http.StatusUnauthorized},
		{"wrong token", "not-the-service-token", nil, http.StatusUnauthorized},
		{
			"admin token is not a service token",
			harvesterTestAdmin,
			func(d *RouterDeps) { d.AdminToken = harvesterTestAdmin },
			http.StatusUnauthorized,
		},
		{
			"unconfigured service token fails closed",
			harvesterTestToken,
			func(d *RouterDeps) { d.ServiceToken = "" },
			http.StatusServiceUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newHarvesterEnv(t, tc.mutate)
			before := env.revision()

			raw, err := json.Marshal(batch)
			if err != nil {
				t.Fatalf("marshal batch: %v", err)
			}
			w := env.post(raw, tc.token)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tc.want, w.Body.String())
			}

			if tc.want == http.StatusOK {
				if got := env.countProviders(); got != 1 {
					t.Fatalf("providers after accepted batch = %d, want 1", got)
				}
				return
			}

			// Rejected requests must not touch the database and must not echo
			// either credential back to the caller.
			if got := env.revision(); got != before {
				t.Fatalf("rejected request bumped revision: %d -> %d", before, got)
			}
			if got := env.countProviders(); got != 0 {
				t.Fatalf("rejected request created %d provider(s)", got)
			}
			body := w.Body.String()
			for _, secret := range []string{harvesterTestToken, harvesterTestAdmin, "sk-auth-1", tc.token} {
				if secret != "" && strings.Contains(body, secret) {
					t.Fatalf("response leaked %q: %s", secret, body)
				}
			}
		})
	}
}

func TestHarvesterSync_AppliesBatchAndReloadsCatalog(t *testing.T) {
	env := newHarvesterEnv(t, nil)

	const (
		provider     = "poolA"
		upstreamName = "sync-up"
		seedKey      = "sk-seed-1"
		staleKey     = "sk-stale-1"
		liveKey      = "sk-live-2"
		vaultMarker  = "0xVAULT-MARKER-1"
	)

	seeded := env.seedLinkedUpstream(provider, upstreamName, seedKey, staleKey)
	if len(seeded) != 2 {
		t.Fatalf("seeded active keys = %v, want 2", seeded)
	}
	seedID, staleID := seeded[0], seeded[1]

	// Publish a pre-sync snapshot so the reload has a before state.
	pre, _, err := env.store.LoadCatalogSnapshot(context.Background(), os.LookupEnv)
	if err != nil {
		t.Fatalf("load pre-sync snapshot: %v", err)
	}
	env.reg.Store(pre)
	preGen := env.reg.Current().Generation()
	env.reseedSyncCursor()

	w := env.postJSON(map[string]any{
		"providers": []map[string]any{{
			"name":        provider,
			"base_url":    "https://api.pool-a.test",
			"description": "primary pool",
		}},
		"keys": []map[string]any{{
			"provider":         provider,
			"api_key":          liveKey,
			"account_metadata": map[string]any{"private_key": vaultMarker, "mnemonic": "hunter2-MARKER"},
		}},
		"deactivate_keys": []int64{staleID},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	res := decodeHarvesterSync(t, w)

	if res.Created != 1 || res.Updated != 0 || res.Unchanged != 0 || res.Deactivated != 1 {
		t.Fatalf("counters = created:%d updated:%d unchanged:%d deactivated:%d, want 1/0/0/1",
			res.Created, res.Updated, res.Unchanged, res.Deactivated)
	}
	if res.Pushed {
		t.Fatal("pushed = true, but the store has no cloud client")
	}
	if !res.Reloaded {
		t.Fatal("reloaded = false after a committed batch with an attached syncer")
	}

	liveRow := env.keyRow(liveKey)
	staleRow := env.keyRow(staleKey)
	if staleRow.IsActive != 0 || staleRow.Status != "deactivated" {
		t.Fatalf("stale key row = active:%d status:%q, want deactivated", staleRow.IsActive, staleRow.Status)
	}
	ids := res.KeyIDs[provider]
	if len(ids) != 2 || ids[0] != seedID || ids[1] != liveRow.ID {
		t.Fatalf("key_ids[%s] = %v, want [%d %d] (active keys only, ordered by id)", provider, ids, seedID, liveRow.ID)
	}

	// The reload must have published the batch: the previously seeded snapshot
	// becomes the one carrying the new key and hiding the deactivated one.
	cur := env.reg.Current()
	if cur == nil {
		t.Fatal("registry has no snapshot after a successful sync")
	}
	if cur.Generation() != uint64(res.Revision) {
		t.Fatalf("snapshot generation = %d, want the batch revision %d", cur.Generation(), res.Revision)
	}
	if cur.Generation() <= preGen {
		t.Fatalf("snapshot generation did not advance: %d -> %d", preGen, cur.Generation())
	}
	up, ok := cur.Upstream(upstreamName)
	if !ok || up == nil || up.KeyRing == nil {
		t.Fatalf("upstream %q missing or keyless in the reloaded snapshot", upstreamName)
	}
	routable := make(map[int64]string, len(up.KeyRing.Slots))
	for _, slot := range up.KeyRing.Slots {
		routable[slot.APIKeyID] = slot.Secret
	}
	if len(routable) != 2 {
		t.Fatalf("routable keys = %d, want 2", len(routable))
	}
	if routable[seedID] != seedKey || routable[liveRow.ID] != liveKey {
		t.Fatalf("routable secrets = %v, want the seeded key plus the synced key", routable)
	}
	if _, present := routable[staleID]; present {
		t.Fatalf("deactivated key %d is still routable", staleID)
	}

	// The vault blob is written verbatim and never echoed anywhere.
	if !strings.Contains(liveRow.Meta.String, vaultMarker) {
		t.Fatalf("account_metadata was not stored verbatim: %q", liveRow.Meta.String)
	}
	for _, secret := range []string{seedKey, staleKey, liveKey, vaultMarker, "hunter2-MARKER"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("response leaked %q: %s", secret, w.Body.String())
		}
		if strings.Contains(env.logs.String(), secret) {
			t.Fatalf("logs leaked %q", secret)
		}
	}
}

func TestHarvesterSync_ReplayIsIdempotent(t *testing.T) {
	env := newHarvesterEnv(t, nil)

	const (
		provider = "poolA"
		apiKey   = "sk-replay-1"
	)
	payload := map[string]any{
		"providers": []map[string]any{{"name": provider, "base_url": "https://api.pool-a.test"}},
		"keys":      []map[string]any{{"provider": provider, "api_key": apiKey}},
	}

	first := decodeHarvesterSync(t, env.postJSON(payload))
	if first.Created != 1 {
		t.Fatalf("first call created = %d, want 1", first.Created)
	}
	ids := first.KeyIDs[provider]
	if len(ids) != 1 {
		t.Fatalf("first call key_ids = %v, want one id", ids)
	}
	liveID := ids[0]

	// Usage metering is owned by the traffic pipeline; a replay must not reset it.
	env.exec("UPDATE api_keys SET total_requests = 7, last_used_at = 4242 WHERE id = ?", liveID)
	before := env.keyRow(apiKey)
	env.reseedSyncCursor()

	second := decodeHarvesterSync(t, env.postJSON(payload))
	if second.Created != 0 || second.Updated != 0 || second.Unchanged != 1 || second.Deactivated != 0 {
		t.Fatalf("replay counters = created:%d updated:%d unchanged:%d deactivated:%d, want 0/0/1/0",
			second.Created, second.Updated, second.Unchanged, second.Deactivated)
	}
	if got := second.KeyIDs[provider]; len(got) != 1 || got[0] != liveID {
		t.Fatalf("replay key_ids = %v, want the stable id %d", got, liveID)
	}
	after := env.keyRow(apiKey)
	if after.ID != before.ID || after.CreatedAt != before.CreatedAt ||
		after.UpdatedAt != before.UpdatedAt || after.LastUsed != before.LastUsed ||
		after.Requests != before.Requests {
		t.Fatalf("replay mutated the row: before=%+v after=%+v", before, after)
	}
}

func TestHarvesterSync_RejectsInvalidBatches(t *testing.T) {
	tooManyProviders := make([]map[string]any, maxHarvesterSyncProviders+1)
	for i := range tooManyProviders {
		tooManyProviders[i] = map[string]any{
			"name":     "pool-" + strconv.Itoa(i),
			"base_url": "https://api.example.test",
		}
	}
	tooManyKeys := make([]map[string]any, maxHarvesterSyncKeys+1)
	for i := range tooManyKeys {
		tooManyKeys[i] = map[string]any{"provider": "poolA", "api_key": "sk-bulk-" + strconv.Itoa(i)}
	}
	tooManyDeactivations := make([]int64, maxHarvesterSyncDeactivations+1)
	for i := range tooManyDeactivations {
		tooManyDeactivations[i] = int64(i + 1)
	}

	cases := []struct {
		name    string
		payload any
		wantMsg string
	}{
		{"empty batch", map[string]any{}, "sync payload is empty"},
		{
			"provider without a name",
			map[string]any{"providers": []map[string]any{{"name": "", "base_url": "https://api.pool-a.test"}}},
			"empty name",
		},
		{
			"key without a provider",
			map[string]any{"keys": []map[string]any{{"api_key": "sk-orphan-1"}}},
			"requires both provider and api_key",
		},
		{
			"key without a secret",
			map[string]any{
				"providers": []map[string]any{{"name": "poolA", "base_url": "https://api.pool-a.test"}},
				"keys":      []map[string]any{{"provider": "poolA", "api_key": ""}},
			},
			"requires both provider and api_key",
		},
		{
			"unknown provider",
			map[string]any{"keys": []map[string]any{{"provider": "ghost", "api_key": "sk-ghost-1"}}},
			"neither declared in this batch nor present in the database",
		},
		{"too many providers", map[string]any{"providers": tooManyProviders}, "too many providers"},
		{"too many keys", map[string]any{"keys": tooManyKeys}, "too many keys"},
		{"too many deactivations", map[string]any{"deactivate_keys": tooManyDeactivations}, "too many deactivation ids"},
		{
			"oversized account_metadata",
			map[string]any{
				"providers": []map[string]any{{"name": "poolA", "base_url": "https://api.pool-a.test"}},
				"keys": []map[string]any{{
					"provider":         "poolA",
					"api_key":          "sk-meta-1",
					"account_metadata": strings.Repeat("v", maxHarvesterMetadataBytes+1),
				}},
			},
			"account_metadata exceeds",
		},
		{
			// A second-scale expiry would read as "expired in 1970" against the
			// millisecond column the routing predicate compares to.
			"second-scale expires_at",
			map[string]any{
				"providers": []map[string]any{{"name": "poolA", "base_url": "https://api.pool-a.test"}},
				"keys": []map[string]any{{
					"provider":   "poolA",
					"api_key":    "sk-units-1",
					"expires_at": time.Now().Unix(),
				}},
			},
			"Unix millisecond",
		},
		{
			"status longer than the column",
			map[string]any{
				"providers": []map[string]any{{"name": "poolA", "base_url": "https://api.pool-a.test"}},
				"keys": []map[string]any{{
					"provider": "poolA",
					"api_key":  "sk-status-1",
					"status":   strings.Repeat("s", 21),
				}},
			},
			"status exceeds",
		},
		{
			"padded status",
			map[string]any{
				"providers": []map[string]any{{"name": "poolA", "base_url": "https://api.pool-a.test"}},
				"keys": []map[string]any{{
					"provider": "poolA",
					"api_key":  "sk-status-2",
					"status":   " active",
				}},
			},
			"whitespace",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newHarvesterEnv(t, nil)
			before := env.revision()

			w := env.postJSON(tc.payload)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantMsg) {
				t.Fatalf("body %q does not mention %q", w.Body.String(), tc.wantMsg)
			}
			if got := env.revision(); got != before {
				t.Fatalf("rejected batch bumped revision: %d -> %d", before, got)
			}
			if got := env.countProviders(); got != 0 {
				t.Fatalf("rejected batch created %d provider(s)", got)
			}
		})
	}

	t.Run("malformed json", func(t *testing.T) {
		env := newHarvesterEnv(t, nil)
		w := env.post([]byte(`{"providers":`), harvesterTestToken)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "parse JSON sync payload") {
			t.Fatalf("body %q does not name the JSON parse failure", w.Body.String())
		}
	})

	t.Run("oversized body", func(t *testing.T) {
		env := newHarvesterEnv(t, nil)
		oversize := io.LimitReader(zeroReader{}, maxRequestBodyBytes+1)
		w := env.postReader(oversize, harvesterTestToken)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413; body = %s", w.Code, w.Body.String())
		}
	})
}

// An unrecognised but well-formed status is the writer's lifecycle state, so it
// is stored verbatim rather than refused — rejecting it would freeze an entire
// batch over a value Firefly has never seen. What must hold is that it routes
// as nothing.
func TestHarvesterSync_StoresUnknownStatusAsInert(t *testing.T) {
	env := newHarvesterEnv(t, nil)

	const (
		provider = "poolA"
		seedKey  = "sk-seed-inert"
		legacy   = "sk-legacy-dead"
	)
	seeded := env.seedLinkedUpstream(provider, "sync-up", seedKey)
	env.reseedSyncCursor()

	w := env.postJSON(map[string]any{
		"providers": []map[string]any{{"name": provider, "base_url": "https://api.pool-a.test"}},
		"keys":      []map[string]any{{"provider": provider, "api_key": legacy, "status": "dead"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	res := decodeHarvesterSync(t, w)
	if res.Created != 1 {
		t.Fatalf("counters = %+v, want one key created", res)
	}
	if ids := res.KeyIDs[provider]; len(ids) != 1 || ids[0] != seeded[0] {
		t.Fatalf("key_ids[%s] = %v, want only the seeded active key", provider, ids)
	}
	row := env.keyRow(legacy)
	if row.Status != "dead" || row.IsActive != 0 {
		t.Fatalf("stored row = status:%q is_active:%d, want dead/0", row.Status, row.IsActive)
	}
}

func TestHarvesterSync_RejectsKeyProviderMismatch(t *testing.T) {
	env := newHarvesterEnv(t, nil)

	const (
		owner    = "poolA"
		intruder = "poolB"
		apiKey   = "sk-owned-1"
	)
	env.seedLinkedUpstream(owner, "sync-up", apiKey)

	w := env.postJSON(map[string]any{
		"providers": []map[string]any{
			{"name": owner, "base_url": "https://api.pool-a.test"},
			{"name": intruder, "base_url": "https://api.pool-b.test"},
		},
		"keys": []map[string]any{{"provider": intruder, "api_key": apiKey}},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), apiKey) {
		t.Fatalf("response leaked the credential: %s", w.Body.String())
	}

	// The whole batch rolls back: the intruder provider must not survive, and
	// the key must still belong to its original provider.
	if got := env.countProviders(); got != 1 {
		t.Fatalf("providers after a rejected batch = %d, want 1 (rollback)", got)
	}
	var ownerID int64
	if err := env.db.QueryRowContext(context.Background(),
		"SELECT id FROM providers WHERE name = ?", owner).Scan(&ownerID); err != nil {
		t.Fatalf("read owner provider: %v", err)
	}
	if row := env.keyRow(apiKey); row.ProviderID != ownerID {
		t.Fatalf("key provider = %d, want the original owner %d", row.ProviderID, ownerID)
	}
}

func TestHarvesterSync_StoreUnavailable(t *testing.T) {
	// A bare deps literal: no manager and no store, which is how file-storage
	// deployments reach the handler. It must fail closed without consulting the
	// environment for Turso credentials.
	deps := RouterDeps{ServiceToken: harvesterTestToken}
	body, err := json.Marshal(map[string]any{
		"providers": []map[string]any{{"name": "poolA", "base_url": "https://api.pool-a.test"}},
	})
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/harvester/sync", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+harvesterTestToken)
	w := httptest.NewRecorder()
	deps.handleHarvesterSync(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not configured") {
		t.Fatalf("body %q does not explain the missing store", w.Body.String())
	}
}

func TestHarvesterSync_MethodNotAllowed(t *testing.T) {
	env := newHarvesterEnv(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/harvester/sync", nil)
	w := httptest.NewRecorder()
	env.s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", w.Code)
	}
}
