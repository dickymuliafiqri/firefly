package turso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests drive the operator CRUD entry points of provider_store.go: the
// provider and api_keys rows the dashboard writes, and the catalog bookkeeping
// every write has to move. They run against a real in-memory database so the
// guarantees under test are the SQL-level ones — UNIQUE(api_key), one
// transaction per batch, the mirrors into upstream_credentials.
//
// Fixtures: storeCtx opens the store, seedProvider creates a provider row, and
// seedKeys writes a key batch at it.

// storeCtx bundles the store under test with the context every call uses.
func storeCtx(t *testing.T) (*Store, context.Context) {
	t.Helper()
	store, _ := setupTestDB(t)
	return store, context.Background()
}

// seedProvider creates one provider and returns its id.
func seedProvider(t *testing.T, store *Store, name string) int64 {
	t.Helper()
	baseURL := "https://" + name + ".example.com"
	rec, err := store.CreateProviderRecord(context.Background(), ProviderUpdate{Name: &name, BaseURL: &baseURL})
	if err != nil {
		t.Fatalf("seed provider %q: %v", name, err)
	}
	return rec.ID
}

// seedKeys applies one key batch to a seeded provider. Reassignment stays off: a
// fixture must never silently move another provider's key.
func seedKeys(t *testing.T, store *Store, providerID int64, entries ...ProviderKeySyncEntry) ProviderKeyUpsertResult {
	t.Helper()
	res, err := store.UpsertProviderKeyRecords(context.Background(), providerID, entries, false)
	if err != nil {
		t.Fatalf("seed keys for provider %d: %v", providerID, err)
	}
	return res
}

// msExpiry renders a millisecond timestamp the way the wire contract carries it,
// so tests exercise the same expires_at form the handlers decode.
func msExpiry(ms int64) json.RawMessage {
	return json.RawMessage(strconv.FormatInt(ms, 10))
}

// clearExpiry is the explicit "remove the stored expiry" intent. An omitted
// expires_at means the opposite (keep it), so the two must not be conflated.
var clearExpiry = json.RawMessage("null")

// keyRow reads the raw api_keys row for one secret so tests can assert on the
// columns the writers must never touch (metering) as well as on identity
// stability across replays and rotations.
type keyRow struct {
	ID            int64
	ProviderID    int64
	Status        string
	IsActive      int
	ExpiresAt     *int64
	LastUsedAt    int64
	TotalRequests int64
	CreatedAt     int64
	UpdatedAt     int64
	Metadata      *string
}

func readKeyRow(t *testing.T, store *Store, secret string) keyRow {
	t.Helper()
	var (
		r      keyRow
		expiry *int64
		meta   *string
	)
	err := store.DB().QueryRowContext(context.Background(), `
		SELECT id, provider_id, status, is_active, expires_at, last_used_at,
		       total_requests, created_at, updated_at, account_metadata
		FROM api_keys WHERE api_key = ?
	`, secret).Scan(&r.ID, &r.ProviderID, &r.Status, &r.IsActive, &expiry,
		&r.LastUsedAt, &r.TotalRequests, &r.CreatedAt, &r.UpdatedAt, &meta)
	if err != nil {
		t.Fatalf("read api key row: %v", err)
	}
	r.ExpiresAt = expiry
	r.Metadata = meta
	return r
}

func readProviderID(t *testing.T, store *Store, name string) int64 {
	t.Helper()
	var id int64
	if err := store.DB().QueryRowContext(context.Background(),
		"SELECT id FROM providers WHERE name = ?", name).Scan(&id); err != nil {
		t.Fatalf("read provider %q id: %v", name, err)
	}
	return id
}

func readRevision(t *testing.T, store *Store) int64 {
	t.Helper()
	rev, err := store.GetCatalogRevision(context.Background())
	if err != nil {
		t.Fatalf("read catalog revision: %v", err)
	}
	return rev
}

// A create is refused when the shape does not fit the columns the adopting
// migration declares, and when the name is already taken: providers are matched
// by name, so a duplicate insert would silently capture the existing pool. None
// of the refusals may publish a revision — a reload for nothing rebuilds every
// snapshot.
func TestCreateProviderRecord_RefusesDuplicateNameAndInvalidShape(t *testing.T) {
	store, ctx := storeCtx(t)

	txt := func(s string) *string { return &s }
	longName := strings.Repeat("n", maxProviderNameLen+1)
	longURL := strings.Repeat("u", maxProviderURLLen+1)
	longDesc := strings.Repeat("d", maxProviderDescLen+1)

	cases := []struct {
		name    string
		in      ProviderUpdate
		wantSub string
	}{
		{
			name:    "missing name",
			in:      ProviderUpdate{BaseURL: txt("https://gate.example.com")},
			wantSub: "name is required",
		},
		{
			name:    "empty name",
			in:      ProviderUpdate{Name: txt(""), BaseURL: txt("https://gate.example.com")},
			wantSub: "name is required",
		},
		{
			name:    "missing base_url",
			in:      ProviderUpdate{Name: txt("gate")},
			wantSub: "base_url is required",
		},
		{
			name:    "empty base_url",
			in:      ProviderUpdate{Name: txt("gate"), BaseURL: txt("")},
			wantSub: "base_url is required",
		},
		{
			name:    "name too long",
			in:      ProviderUpdate{Name: &longName, BaseURL: txt("https://gate.example.com")},
			wantSub: "name exceeds",
		},
		{
			name:    "base_url too long",
			in:      ProviderUpdate{Name: txt("gate"), BaseURL: &longURL},
			wantSub: "base_url exceeds",
		},
		{
			name:    "description too long",
			in:      ProviderUpdate{Name: txt("gate"), BaseURL: txt("https://gate.example.com"), Description: &longDesc},
			wantSub: "description exceeds",
		},
	}

	before := readRevision(t, store)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.CreateProviderRecord(ctx, tc.in); !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("err = %v, want ErrInvalidPayload", err)
			} else if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
	var providers int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM providers").Scan(&providers); err != nil {
		t.Fatalf("count providers: %v", err)
	}
	if providers != 0 {
		t.Errorf("refused creates left %d provider rows behind", providers)
	}
	if got := readRevision(t, store); got != before {
		t.Errorf("revision = %d, want %d: a refused create must not publish", got, before)
	}

	pid := seedProvider(t, store, "acme")
	if pid <= 0 {
		t.Fatalf("provider id = %d, want the inserted row id", pid)
	}
	if got := readRevision(t, store); got != before+1 {
		t.Errorf("revision = %d, want %d for one create", got, before+1)
	}

	// The name is the natural key, so a second create is refused whatever else it
	// carries: a different base_url must not be enough to fork the pool.
	for _, in := range []ProviderUpdate{
		{Name: txt("acme"), BaseURL: txt("https://acme.example.com")},
		{Name: txt("acme"), BaseURL: txt("https://elsewhere.example.com")},
	} {
		if _, err := store.CreateProviderRecord(ctx, in); !errors.Is(err, ErrProviderExists) {
			t.Fatalf("duplicate create = %v, want ErrProviderExists", err)
		}
	}
	rec, err := store.GetProviderRecord(ctx, pid)
	if err != nil {
		t.Fatalf("read provider back: %v", err)
	}
	if rec.Name != "acme" || rec.BaseURL != "https://acme.example.com" || !rec.IsActive {
		t.Errorf("provider = %+v, want the created row untouched by the refused duplicates", rec)
	}
	if rec.ActiveKeys != 0 {
		t.Errorf("active_keys = %d, want 0 for a provider with no keys", rec.ActiveKeys)
	}
	if got := readRevision(t, store); got != before+1 {
		t.Errorf("revision = %d, want %d: refused duplicates must not publish", got, before+1)
	}
}

// One batch creates its keys, binds them to the route's provider, and publishes
// the change. The revision bump is what tells every instance to rebuild, and the
// returned ids are the routable set the caller reconciles against.
func TestUpsertProviderKeyRecords_CreatesAndReportsRevision(t *testing.T) {
	store, ctx := storeCtx(t)
	pid := seedProvider(t, store, "openai")
	before := readRevision(t, store)

	res, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{{APIKey: "sk-alpha"}}, false)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if res.Created != 1 || res.Updated != 0 || res.Unchanged != 0 || res.Reassigned != 0 {
		t.Fatalf("counters = %+v, want one created key", res)
	}
	if res.Revision != before+1 {
		t.Errorf("revision = %d, want %d", res.Revision, before+1)
	}
	if res.Pushed {
		t.Error("Pushed = true without a turso client")
	}

	row := readKeyRow(t, store, "sk-alpha")
	if row.ProviderID != pid {
		t.Errorf("key bound to provider %d, want %d", row.ProviderID, pid)
	}
	if row.Status != "active" || row.IsActive != 1 {
		t.Errorf("status/is_active = %q/%d, want active/1", row.Status, row.IsActive)
	}
	if row.LastUsedAt != 0 || row.TotalRequests != 0 {
		t.Errorf("metering = last_used_at:%d total_requests:%d, want 0/0 for a new key", row.LastUsedAt, row.TotalRequests)
	}
	if len(res.KeyIDs) != 1 || res.KeyIDs[0] != row.ID {
		t.Errorf("key_ids = %v, want [%d]", res.KeyIDs, row.ID)
	}
	rec, err := store.GetProviderRecord(ctx, pid)
	if err != nil {
		t.Fatalf("read provider back: %v", err)
	}
	if rec.ActiveKeys != 1 {
		t.Errorf("active_keys = %d, want the count to follow the batch", rec.ActiveKeys)
	}
}

// A replayed batch converges on the same row: identity, created_at, and the
// traffic counters survive. updated_at must not move either — MAX(updated_at) is
// the syncer's structural-change signal, and a heartbeat on it would reload every
// instance per push. The revision still advances: the batch did run.
func TestUpsertProviderKeyRecords_IdempotentReplayPreservesIdentityAndMetering(t *testing.T) {
	store, ctx := storeCtx(t)
	pid := seedProvider(t, store, "anthropic")
	seedKeys(t, store, pid, ProviderKeySyncEntry{APIKey: "sk-beta"})
	id := readKeyRow(t, store, "sk-beta").ID

	// Simulate live traffic metering: this is what the usage flusher writes.
	if _, err := store.DB().ExecContext(ctx,
		"UPDATE api_keys SET last_used_at = ?, total_requests = ? WHERE id = ?",
		int64(1_700_000_000_000), int64(42), id); err != nil {
		t.Fatalf("seed metering: %v", err)
	}
	metered := readKeyRow(t, store, "sk-beta")

	before := readRevision(t, store)
	replay, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{{APIKey: "sk-beta"}}, false)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Unchanged != 1 || replay.Created != 0 || replay.Updated != 0 {
		t.Fatalf("replay counters = %+v, want the row unchanged", replay)
	}

	after := readKeyRow(t, store, "sk-beta")
	if after.ID != id {
		t.Fatalf("key id changed across replay: %d -> %d", id, after.ID)
	}
	if after.LastUsedAt != metered.LastUsedAt || after.TotalRequests != metered.TotalRequests {
		t.Errorf("metering rewritten by replay: last_used_at %d->%d total_requests %d->%d",
			metered.LastUsedAt, after.LastUsedAt, metered.TotalRequests, after.TotalRequests)
	}
	if after.CreatedAt != metered.CreatedAt {
		t.Errorf("created_at = %d, want the original insert timestamp %d", after.CreatedAt, metered.CreatedAt)
	}
	if after.UpdatedAt != metered.UpdatedAt {
		t.Errorf("updated_at advanced on an unchanged key: %d -> %d; MAX(updated_at) is a structural signal",
			metered.UpdatedAt, after.UpdatedAt)
	}
	if replay.Revision != before+1 {
		t.Errorf("revision = %d, want %d", replay.Revision, before+1)
	}
}

// expires_at is a routing-time filter, not a write: an expired key stays stored
// and active, and the ids a batch reports exclude it — the same predicate the
// snapshot loader re-evaluates on every rebuild.
func TestUpsertProviderKeyRecords_ExpiryGateMatchesSnapshotLoader(t *testing.T) {
	store, ctx := storeCtx(t)
	pid := seedProvider(t, store, "gated")

	expired := time.Now().Add(-24 * time.Hour).UnixMilli()
	future := int64(4_102_444_800_000)
	res, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{
		{APIKey: "sk-live"},
		{APIKey: "sk-stale", ExpiresAt: msExpiry(expired)},
		{APIKey: "sk-future", ExpiresAt: msExpiry(future)},
	}, false)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	live := readKeyRow(t, store, "sk-live").ID
	futureID := readKeyRow(t, store, "sk-future").ID
	want := map[int64]bool{live: true, futureID: true}
	if len(res.KeyIDs) != len(want) {
		t.Fatalf("key_ids = %v, want ids for the two non-expired keys", res.KeyIDs)
	}
	for _, id := range res.KeyIDs {
		if !want[id] {
			t.Errorf("key id %d is expired and must not be routable", id)
		}
	}
	if row := readKeyRow(t, store, "sk-stale"); row.IsActive != 1 || row.Status != "active" {
		t.Errorf("expired key rewritten: is_active/status = %d/%q, want 1/active", row.IsActive, row.Status)
	}

	// A batch that says nothing about expires_at must not rewrite it: the writer
	// refreshing this row is not the one that set its lifetime, and a silent wipe
	// would put an expired credential back into rotation. It is also a no-op, so
	// the row must not be counted as updated.
	kept, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{{APIKey: "sk-stale"}}, false)
	if err != nil {
		t.Fatalf("omit-expiry upsert: %v", err)
	}
	if kept.Updated != 0 || kept.Unchanged != 1 {
		t.Errorf("counters = %+v, want unchanged when expires_at is omitted", kept)
	}
	if row := readKeyRow(t, store, "sk-stale"); row.ExpiresAt == nil || *row.ExpiresAt != expired {
		t.Errorf("expires_at = %v, want the stored %d preserved", row.ExpiresAt, expired)
	}

	// An explicit null is the documented way back: it returns a key whose lifetime
	// has passed to the pool once the underlying credential is genuinely renewed.
	cleared, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{
		{APIKey: "sk-stale", ExpiresAt: clearExpiry},
	}, false)
	if err != nil {
		t.Fatalf("clear-expiry upsert: %v", err)
	}
	if cleared.Updated != 1 {
		t.Errorf("counters = %+v, want the key updated when expires_at is cleared", cleared)
	}
	if row := readKeyRow(t, store, "sk-stale"); row.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want NULL", row.ExpiresAt)
	}
	if len(cleared.KeyIDs) != 3 {
		t.Errorf("key_ids = %v, want all three keys routable after the clear", cleared.KeyIDs)
	}
}

// account_metadata is a credential vault: written verbatim, never read back. An
// omitted blob means "keep the stored one" — it is large write-only data a client
// may legitimately not resend — whitespace-only differences are not a change, and
// a different blob is.
func TestUpsertProviderKeyRecords_AccountMetadataVaultSemantics(t *testing.T) {
	store, ctx := storeCtx(t)
	pid := seedProvider(t, store, "vault")

	const vault = `{"private_key":"0xdeadbeef","password":"hunter2"}`
	res, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{
		{APIKey: "sk-vault", AccountMetadata: json.RawMessage(vault)},
	}, false)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if row := readKeyRow(t, store, "sk-vault"); row.Metadata == nil || *row.Metadata != vault {
		t.Fatalf("account_metadata stored as %v, want the blob written verbatim", row.Metadata)
	}

	// The vault must never travel back out through the result.
	encoded, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	for _, forbidden := range []string{"sk-vault", "private_key", "0xdeadbeef", "hunter2"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("upsert result leaked %q: %s", forbidden, encoded)
		}
	}

	omitted, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{{APIKey: "sk-vault"}}, false)
	if err != nil {
		t.Fatalf("omitted-metadata upsert: %v", err)
	}
	if omitted.Unchanged != 1 {
		t.Errorf("counters = %+v, want the key unchanged when the blob is omitted", omitted)
	}
	if row := readKeyRow(t, store, "sk-vault"); row.Metadata == nil || *row.Metadata != vault {
		t.Errorf("omitted blob erased the stored vault: %v", row.Metadata)
	}

	same, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{
		{APIKey: "sk-vault", AccountMetadata: json.RawMessage("\n  " + vault + "  \n")},
	}, false)
	if err != nil {
		t.Fatalf("whitespace-metadata upsert: %v", err)
	}
	if same.Unchanged != 1 {
		t.Errorf("whitespace-padded blob counted as a change: %+v", same)
	}

	rotated := `{"private_key":"0xfeedface"}`
	changed, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{
		{APIKey: "sk-vault", AccountMetadata: json.RawMessage(rotated)},
	}, false)
	if err != nil {
		t.Fatalf("rotated-metadata upsert: %v", err)
	}
	if changed.Updated != 1 {
		t.Errorf("counters = %+v, want the key updated for a rotated blob", changed)
	}
	if row := readKeyRow(t, store, "sk-vault"); row.Metadata == nil || *row.Metadata != rotated {
		t.Errorf("account_metadata = %v, want the rotated blob", row.Metadata)
	}
}

// UNIQUE(api_key) is global, so a key already owned by another provider is
// refused: applying the move would hand one provider's quota to another. The
// operator-only reassignment exists to do exactly that on purpose, and it drops
// the old provider's bound credential rows so the key cannot keep serving on both
// pools at once.
func TestUpsertProviderKeyRecords_RejectsCrossProviderKeyUnlessReassigned(t *testing.T) {
	store, ctx := storeCtx(t)
	from := seedProvider(t, store, "provider-a")
	to := seedProvider(t, store, "provider-b")
	seedKeys(t, store, from, ProviderKeySyncEntry{APIKey: "sk-shared"})
	key := readKeyRow(t, store, "sk-shared")

	// Bind a credential to the key on the owning provider, so the reassignment's
	// cleanup is observable rather than inferred from the join.
	up := &UpstreamRecord{
		Name: "a-up", Protocol: "openai", BaseURL: "https://provider-a.example.com",
		KeyStrategy: "round_robin", Enabled: true, ProviderID: &from,
	}
	if err := store.SaveUpstream(ctx, up); err != nil {
		t.Fatalf("save upstream: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `
		INSERT INTO upstream_credentials (upstream_id, api_key_id, ref, secret, status, is_active, created_at, updated_at)
		VALUES (?, ?, 'a-cred-1', 'sk-shared', 'active', 1, 1, 1)
	`, up.ID, key.ID); err != nil {
		t.Fatalf("seed bound credential: %v", err)
	}

	before := readRevision(t, store)
	_, err := store.UpsertProviderKeyRecords(ctx, to, []ProviderKeySyncEntry{{APIKey: "sk-shared"}}, false)
	if !errors.Is(err, ErrKeyProviderMismatch) {
		t.Fatalf("err = %v, want ErrKeyProviderMismatch", err)
	}
	if after := readKeyRow(t, store, "sk-shared"); after.ProviderID != from {
		t.Errorf("key moved to provider %d despite the rejection (was %d)", after.ProviderID, from)
	}
	if got := readRevision(t, store); got != before {
		t.Errorf("revision advanced on a rejected batch: %d -> %d", before, got)
	}
	if _, updatedAt := boundCredential(t, store, key.ID); updatedAt != 1 {
		t.Errorf("bound credential touched by a rejected batch: updated_at = %d", updatedAt)
	}

	// The correction path is explicit: reassign applies the move.
	res, err := store.UpsertProviderKeyRecords(ctx, to, []ProviderKeySyncEntry{{APIKey: "sk-shared"}}, true)
	if err != nil {
		t.Fatalf("reassign: %v", err)
	}
	if res.Updated != 1 || res.Reassigned != 1 {
		t.Errorf("counters = %+v, want one reassigned update", res)
	}
	if after := readKeyRow(t, store, "sk-shared"); after.ProviderID != to || after.ID != key.ID {
		t.Errorf("row = provider %d id %d, want provider %d with the row id preserved", after.ProviderID, after.ID, to)
	}
	if len(res.KeyIDs) != 1 || res.KeyIDs[0] != key.ID {
		t.Errorf("key_ids = %v, want [%d]: the key is routable on its new provider", res.KeyIDs, key.ID)
	}
	var left int
	if err := store.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM upstream_credentials WHERE api_key_id = ?", key.ID).Scan(&left); err != nil {
		t.Fatalf("count bound credentials: %v", err)
	}
	if left != 0 {
		t.Errorf("bound credentials left on the old provider = %d, want the key to stop serving there", left)
	}
}

// The route decides the provider, and the store validates before opening its
// transaction: a non-positive id and an empty batch are rejected payloads, an
// unseeded id is a 404, and none of them may write or publish anything.
func TestUpsertProviderKeyRecords_RejectsUnusableRouteTarget(t *testing.T) {
	store, ctx := storeCtx(t)
	pid := seedProvider(t, store, "route")
	before := readRevision(t, store)

	if _, err := store.UpsertProviderKeyRecords(ctx, 0, []ProviderKeySyncEntry{{APIKey: "sk-x"}}, false); !errors.Is(err, ErrInvalidPayload) {
		t.Errorf("provider id 0 = %v, want ErrInvalidPayload", err)
	}
	if _, err := store.UpsertProviderKeyRecords(ctx, pid, nil, false); !errors.Is(err, ErrInvalidPayload) {
		t.Errorf("empty batch = %v, want ErrInvalidPayload", err)
	}
	if _, err := store.UpsertProviderKeyRecords(ctx, 999_999, []ProviderKeySyncEntry{{APIKey: "sk-x"}}, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown provider = %v, want ErrNotFound", err)
	}

	var keys int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys").Scan(&keys); err != nil {
		t.Fatalf("count api keys: %v", err)
	}
	if keys != 0 {
		t.Errorf("rejected batches left %d key rows behind", keys)
	}
	if got := readRevision(t, store); got != before {
		t.Errorf("revision advanced by rejected batches: %d -> %d", before, got)
	}
}

// Every malformed entry refuses the whole batch: the well-formed sibling riding
// along in each case is what makes the rollback observable, and the revision must
// stay put so a refused batch cannot trigger a reload.
func TestUpsertProviderKeyRecords_RejectsMalformedEntries(t *testing.T) {
	repeat := func(n int, c byte) string { return strings.Repeat(string(c), n) }
	seconds := time.Now().Unix()

	cases := []struct {
		name    string
		entry   ProviderKeySyncEntry
		wantSub string
	}{
		{
			name:    "missing secret",
			entry:   ProviderKeySyncEntry{},
			wantSub: "requires api_key",
		},
		{
			name:    "oversized secret",
			entry:   ProviderKeySyncEntry{APIKey: repeat(maxKeySecretLen+1, 'k')},
			wantSub: "api_key exceeds",
		},
		{
			name:    "unsupported status",
			entry:   ProviderKeySyncEntry{APIKey: "sk-bad", Status: "dead"},
			wantSub: "unsupported status",
		},
		{
			name:    "status differing only in case",
			entry:   ProviderKeySyncEntry{APIKey: "sk-bad", Status: "ACTIVE"},
			wantSub: "unsupported status",
		},
		{
			name:    "second-scale expiry",
			entry:   ProviderKeySyncEntry{APIKey: "sk-bad", ExpiresAt: msExpiry(seconds)},
			wantSub: "Unix millisecond",
		},
		{
			name:    "negative expiry",
			entry:   ProviderKeySyncEntry{APIKey: "sk-bad", ExpiresAt: msExpiry(-1)},
			wantSub: "Unix millisecond",
		},
		{
			name:    "expiry as a string",
			entry:   ProviderKeySyncEntry{APIKey: "sk-bad", ExpiresAt: json.RawMessage(`"4102444800000"`)},
			wantSub: "unix millisecond timestamp or null",
		},
		{
			name:    "expiry as a fraction",
			entry:   ProviderKeySyncEntry{APIKey: "sk-bad", ExpiresAt: json.RawMessage("4102444800.5")},
			wantSub: "unix millisecond timestamp or null",
		},
		{
			name:    "expiry as an object",
			entry:   ProviderKeySyncEntry{APIKey: "sk-bad", ExpiresAt: json.RawMessage("{}")},
			wantSub: "unix millisecond timestamp or null",
		},
		{
			name: "oversized vault blob",
			entry: ProviderKeySyncEntry{
				APIKey:          "sk-bad",
				AccountMetadata: json.RawMessage(repeat(MaxKeyMetadataBytes+1, 'v')),
			},
			wantSub: "account_metadata exceeds",
		},
		{
			name:    "entry names another provider",
			entry:   ProviderKeySyncEntry{Provider: "elsewhere", APIKey: "sk-bad"},
			wantSub: "route targets",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := storeCtx(t)
			pid := seedProvider(t, store, "gate")
			before := readRevision(t, store)

			res, err := store.UpsertProviderKeyRecords(ctx, pid,
				[]ProviderKeySyncEntry{{APIKey: "sk-sibling"}, tc.entry}, false)
			if !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("error = %v, want ErrInvalidPayload", err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
			if res.Created != 0 || res.Updated != 0 || res.Unchanged != 0 ||
				res.Reassigned != 0 || len(res.KeyIDs) != 0 {
				t.Errorf("counters = %+v, want a rejected batch to report nothing", res)
			}

			var keys int
			if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys").Scan(&keys); err != nil {
				t.Fatalf("count api keys: %v", err)
			}
			if keys != 0 {
				t.Errorf("rejected batch left %d key rows behind", keys)
			}
			if got := readRevision(t, store); got != before {
				t.Errorf("catalog revision moved %d -> %d for a rejected batch", before, got)
			}
		})
	}
}

// Status is authoritative on every write, and an omitted status means "active" —
// which is what lets a batch both retire a key and bring it back. Every accepted
// status keeps the row exactly as written, and the ids a batch reports exclude
// anything that is not active.
func TestUpsertProviderKeyRecords_StatusDrivesRoutability(t *testing.T) {
	store, ctx := storeCtx(t)
	pid := seedProvider(t, store, "lifecycle")

	seedKeys(t, store, pid, ProviderKeySyncEntry{APIKey: "sk-life"})
	key := readKeyRow(t, store, "sk-life")
	if key.Status != "active" || key.IsActive != 1 {
		t.Fatalf("omitted status = %q/%d, want active/1", key.Status, key.IsActive)
	}

	for _, status := range []string{"deactivated", "expired", "revoked"} {
		retired, err := store.UpsertProviderKeyRecords(ctx, pid,
			[]ProviderKeySyncEntry{{APIKey: "sk-life", Status: status}}, false)
		if err != nil {
			t.Fatalf("retire as %q: %v", status, err)
		}
		if retired.Updated != 1 || retired.Unchanged != 0 {
			t.Errorf("retire as %q counters = %+v, want one update", status, retired)
		}
		if row := readKeyRow(t, store, "sk-life"); row.Status != status || row.IsActive != 0 {
			t.Errorf("row = %q/%d, want %q/0", row.Status, row.IsActive, status)
		}
		if len(retired.KeyIDs) != 0 {
			t.Errorf("key_ids = %v with status %q, want no routable keys", retired.KeyIDs, status)
		}

		// Replaying the retirement converges on the same state without churning
		// the row it already retired.
		replay, err := store.UpsertProviderKeyRecords(ctx, pid,
			[]ProviderKeySyncEntry{{APIKey: "sk-life", Status: status}}, false)
		if err != nil {
			t.Fatalf("replay %q: %v", status, err)
		}
		if replay.Unchanged != 1 {
			t.Errorf("replay of %q counters = %+v, want the row unchanged", status, replay)
		}

		// A re-push that no longer mentions the retirement reactivates the key.
		back, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{{APIKey: "sk-life"}}, false)
		if err != nil {
			t.Fatalf("reactivate from %q: %v", status, err)
		}
		if back.Updated != 1 || len(back.KeyIDs) != 1 || back.KeyIDs[0] != key.ID {
			t.Errorf("reactivation from %q = %+v, want the key routable again", status, back)
		}
		if row := readKeyRow(t, store, "sk-life"); row.Status != "active" || row.IsActive != 1 {
			t.Errorf("row = %q/%d, want active/1", row.Status, row.IsActive)
		}
	}
}

// The operator batch carries expires_at in the same three intents a lifecycle
// command has: a number sets, an explicit null clears, an omitted field leaves the
// stored expiry alone. Wiping the lifetime as a side effect of editing something
// else would put an expired credential back in rotation.
func TestUpsertProviderKeyRecords_ExpiryIntentIsThreeWay(t *testing.T) {
	store, ctx := storeCtx(t)
	pid := seedProvider(t, store, "operator")

	const (
		secret = "sk-op-expiry"
		stored = int64(4_102_444_800_000)
	)
	if _, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{
		{APIKey: secret, ExpiresAt: msExpiry(stored)},
	}, false); err != nil {
		t.Fatalf("set expiry: %v", err)
	}
	if row := readKeyRow(t, store, secret); row.ExpiresAt == nil || *row.ExpiresAt != stored {
		t.Fatalf("expires_at = %v, want %d", row.ExpiresAt, stored)
	}

	kept, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{{APIKey: secret}}, false)
	if err != nil {
		t.Fatalf("omit expiry: %v", err)
	}
	if kept.Updated != 0 || kept.Unchanged != 1 {
		t.Errorf("counters = %+v, want the row untouched when expires_at is omitted", kept)
	}
	if row := readKeyRow(t, store, secret); row.ExpiresAt == nil || *row.ExpiresAt != stored {
		t.Errorf("expires_at = %v, want the stored %d preserved", row.ExpiresAt, stored)
	}

	cleared, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{
		{APIKey: secret, ExpiresAt: clearExpiry},
	}, false)
	if err != nil {
		t.Fatalf("clear expiry: %v", err)
	}
	if cleared.Updated != 1 {
		t.Errorf("counters = %+v, want one update on an explicit clear", cleared)
	}
	if row := readKeyRow(t, store, secret); row.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want NULL", row.ExpiresAt)
	}

	// A malformed intent refuses the whole batch, so a client cannot end up with
	// half its keys written.
	if _, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{
		{APIKey: "sk-op-batchmate"}, {APIKey: secret, ExpiresAt: json.RawMessage(`"4102444800000"`)},
	}, false); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("string expires_at error = %v, want ErrInvalidPayload", err)
	}
	var rolledBack int
	if err := store.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM api_keys WHERE api_key = 'sk-op-batchmate'").Scan(&rolledBack); err != nil {
		t.Fatalf("count rolled-back key: %v", err)
	}
	if rolledBack != 0 {
		t.Error("the rejected batch wrote its first key")
	}
}

// poolSecret reads one credential out of the pool a catalog rebuild would hand to
// the data plane. That pool, not the api_keys row, is what authenticates upstream
// requests, so it is the only honest place to assert a rotation took effect.
func poolSecret(t *testing.T, store *Store, upstreamName, ref string) string {
	t.Helper()
	settings, err := store.LoadSettings(context.Background())
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	for _, up := range settings.Upstreams {
		if up.Name != upstreamName {
			continue
		}
		for _, cred := range up.CredentialPool {
			if cred.Ref == ref {
				return cred.Secret
			}
		}
		t.Fatalf("upstream %q has no pooled credential %q", upstreamName, ref)
	}
	t.Fatalf("settings have no upstream %q", upstreamName)
	return ""
}

func boundCredential(t *testing.T, store *Store, keyID int64) (string, int64) {
	t.Helper()
	var (
		secret    string
		updatedAt int64
	)
	if err := store.DB().QueryRowContext(context.Background(),
		"SELECT secret, updated_at FROM upstream_credentials WHERE api_key_id = ?", keyID).
		Scan(&secret, &updatedAt); err != nil {
		t.Fatalf("read credential bound to key %d: %v", keyID, err)
	}
	return secret, updatedAt
}

// Retiring a key by status is a lifecycle command the operator batch can issue
// without a settings save. The upstream_credentials rows carry their own copy of
// the secret and the snapshot loader prefers it over the joined api_keys value, so
// the mirror has to reach both binding shapes: the api_key_id foreign key, and the
// "<upstream>-key-<id>" ref a settings save copies into a pool with no binding.
// A key retired in api_keys alone would keep serving traffic through either.
func TestUpsertProviderKeyRecords_StatusChangeRetiresBoundCredentials(t *testing.T) {
	store, ctx := storeCtx(t)
	pid := seedProvider(t, store, "ret")
	seedKeys(t, store, pid,
		ProviderKeySyncEntry{APIKey: "sk-ret-live"},
		ProviderKeySyncEntry{APIKey: "sk-ret-keep"},
	)
	id := readKeyRow(t, store, "sk-ret-live").ID
	keepRef := fmt.Sprintf("ret-up-key-%d", readKeyRow(t, store, "sk-ret-keep").ID)

	up := &UpstreamRecord{
		Name: "ret-up", Protocol: "openai", BaseURL: "https://ret.example.com",
		KeyStrategy: "round_robin", Enabled: true, ProviderID: &pid,
	}
	if err := store.SaveUpstream(ctx, up); err != nil {
		t.Fatalf("save upstream: %v", err)
	}
	keyRef := fmt.Sprintf("ret-up-key-%d", id)
	// Both copies carry a secret of their own: the pool collapses slots that share
	// a secret, so a bound copy holding the same string as the api_keys row is
	// invisible to the pool and would hide what this test exists to prove.
	for _, bind := range []struct {
		ref, secret string
		keyID       any
	}{
		{"ret-cred-1", "sk-ret-copy-bound", id},
		{keyRef, "sk-ret-copy-ref", nil},
	} {
		if _, err := store.DB().ExecContext(ctx, `
			INSERT INTO upstream_credentials (upstream_id, api_key_id, ref, secret, status, is_active, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'active', 1, 1, 1)
		`, up.ID, bind.keyID, bind.ref, bind.secret); err != nil {
			t.Fatalf("seed credential %q: %v", bind.ref, err)
		}
	}

	// The pool the data plane is handed, which is the only place a retired
	// credential still serving would be visible.
	pool := func() map[string]string {
		t.Helper()
		snap, _, err := store.LoadCatalogSnapshot(ctx, func(string) (string, bool) { return "", false })
		if err != nil {
			t.Fatalf("LoadCatalogSnapshot: %v", err)
		}
		u, ok := snap.Upstream("ret-up")
		if !ok || u == nil || u.KeyRing == nil {
			t.Fatal("upstream ret-up is not resolved with a key ring")
		}
		out := make(map[string]string, u.KeyRing.SlotCount())
		for _, slot := range u.KeyRing.Slots {
			out[slot.Ref] = slot.Secret
		}
		return out
	}
	checkPool := func(what string, want map[string]string) {
		t.Helper()
		got := pool()
		if len(got) != len(want) {
			t.Fatalf("%s pool = %v, want %v", what, got, want)
		}
		for ref, secret := range want {
			if got[ref] != secret {
				t.Errorf("%s pool[%q] = %q, want %q", what, ref, got[ref], secret)
			}
		}
	}
	credState := func(ref string) (string, int, int64) {
		t.Helper()
		var (
			status    string
			isActive  int
			updatedAt int64
		)
		if err := store.DB().QueryRowContext(ctx,
			"SELECT status, is_active, updated_at FROM upstream_credentials WHERE upstream_id = ? AND ref = ?",
			up.ID, ref).Scan(&status, &isActive, &updatedAt); err != nil {
			t.Fatalf("read credential %q: %v", ref, err)
		}
		return status, isActive, updatedAt
	}

	// Both copies serve alongside the provider keys while the key is live. The
	// ref-only copy is shadowed by the slot the provider join mints for the same
	// key under the same ref, which is why the count is three and not four.
	checkPool("before retirement", map[string]string{
		keyRef:       "sk-ret-live",
		"ret-cred-1": "sk-ret-copy-bound",
		keepRef:      "sk-ret-keep",
	})

	res, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{
		{APIKey: "sk-ret-live", Status: "deactivated"},
	}, false)
	if err != nil {
		t.Fatalf("retire by status: %v", err)
	}
	if res.Updated != 1 {
		t.Fatalf("counters = %+v, want one update for the status change", res)
	}
	if row := readKeyRow(t, store, "sk-ret-live"); row.Status != "deactivated" || row.IsActive != 0 {
		t.Fatalf("key row = %q/%d, want deactivated/0", row.Status, row.IsActive)
	}
	retiredAt := make(map[string]int64, 2)
	for _, ref := range []string{"ret-cred-1", keyRef} {
		status, isActive, updatedAt := credState(ref)
		if status != "deactivated" || isActive != 0 {
			t.Errorf("credential %q = %q/%d, want deactivated/0", ref, status, isActive)
		}
		if updatedAt <= 1 {
			t.Errorf("credential %q updated_at = %d, want the mirror to rewrite the row", ref, updatedAt)
		}
		retiredAt[ref] = updatedAt
	}
	checkPool("after retirement", map[string]string{keepRef: "sk-ret-keep"})

	// Replaying the batch converges on the same state without churning the rows it
	// already retired: updated_at is a change signal, not a heartbeat.
	replay, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{
		{APIKey: "sk-ret-live", Status: "deactivated"},
	}, false)
	if err != nil {
		t.Fatalf("replay retirement: %v", err)
	}
	if replay.Unchanged != 1 {
		t.Errorf("replay counters = %+v, want the row unchanged", replay)
	}
	for _, ref := range []string{"ret-cred-1", keyRef} {
		if _, _, updatedAt := credState(ref); updatedAt != retiredAt[ref] {
			t.Errorf("replay rewrote credential %q: updated_at %d -> %d", ref, retiredAt[ref], updatedAt)
		}
	}

	// Reactivation restores the key through the provider join. The retired pool rows
	// stay retired: re-enabling a copied credential is a settings-save decision, not
	// a side effect of re-enabling a key.
	if _, err := store.UpsertProviderKeyRecords(ctx, pid, []ProviderKeySyncEntry{
		{APIKey: "sk-ret-live", Status: "active"},
	}, false); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	checkPool("after reactivation", map[string]string{
		keyRef:  "sk-ret-live",
		keepRef: "sk-ret-keep",
	})
	if _, isActive, _ := credState("ret-cred-1"); isActive != 0 {
		t.Error("reactivation resurrected a retired credential row")
	}
}

// A rotation replaces a row's identity material while keeping its id, because the
// "<upstream>-key-<id>" refs are derived from that id. The bound upstream_credentials
// row holds its own copy of the secret and the loader prefers it over the joined
// api_keys value, so a rotation that stopped at api_keys would keep authenticating
// with the retired credential.
func TestPatchProviderKeyRecord_RotatesSecretInPlace(t *testing.T) {
	store, ctx := storeCtx(t)
	pid := seedProvider(t, store, "rot")
	seedKeys(t, store, pid, ProviderKeySyncEntry{APIKey: "sk-rot-old"})
	id := readKeyRow(t, store, "sk-rot-old").ID

	// Metering plus a pinned updated_at: a rotation is structural, so it must
	// advance the column the syncer watches while the traffic counters stay put.
	if _, err := store.DB().ExecContext(ctx,
		"UPDATE api_keys SET last_used_at = ?, total_requests = ?, updated_at = ? WHERE id = ?",
		int64(1_700_000_000_000), int64(77), int64(1_000), id); err != nil {
		t.Fatalf("seed metering: %v", err)
	}

	up := &UpstreamRecord{Name: "rot-up", Protocol: "openai", BaseURL: "https://rot.example.com", KeyStrategy: "round_robin", Enabled: true}
	if err := store.SaveUpstream(ctx, up); err != nil {
		t.Fatalf("save upstream: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `
		INSERT INTO upstream_credentials (upstream_id, api_key_id, ref, secret, status, is_active, created_at, updated_at)
		VALUES (?, ?, 'rot-cred-1', 'sk-rot-old', 'active', 1, ?, ?)
	`, up.ID, id, int64(1), int64(1)); err != nil {
		t.Fatalf("seed bound credential: %v", err)
	}
	_, seededAt := boundCredential(t, store, id)
	if seededAt != 1 {
		t.Fatalf("precondition: bound credential updated_at = %d, want the seeded 1", seededAt)
	}
	if got := poolSecret(t, store, "rot-up", "rot-cred-1"); got != "sk-rot-old" {
		t.Fatalf("pre-rotation pool secret = %q, want the stale bound copy to be what serves", got)
	}
	res, err := store.PatchProviderKeyRecord(ctx, id, ProviderKeyPatch{APIKey: "sk-rot-new"})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if res.Key.ID != id || res.Key.Secret != "sk-rot-new" {
		t.Fatalf("patch returned id %d with secret %q, want id %d rotated", res.Key.ID, res.Key.Secret, id)
	}
	if res.Key.ProviderID != pid {
		t.Errorf("rotation moved the key to provider %d", res.Key.ProviderID)
	}

	row := readKeyRow(t, store, "sk-rot-new")
	if row.ID != id {
		t.Fatalf("rotated row id = %d, want %d", row.ID, id)
	}
	if row.LastUsedAt != 1_700_000_000_000 || row.TotalRequests != 77 {
		t.Errorf("rotation rewrote metering: last_used_at=%d total_requests=%d", row.LastUsedAt, row.TotalRequests)
	}
	if row.UpdatedAt <= 1_000 {
		t.Errorf("updated_at = %d, want a rotation to advance the structural signal", row.UpdatedAt)
	}
	var retired int
	if err := store.DB().QueryRowContext(ctx,
		"SELECT COUNT(*) FROM api_keys WHERE api_key = 'sk-rot-old'").Scan(&retired); err != nil {
		t.Fatalf("count retired key: %v", err)
	}
	if retired != 0 {
		t.Error("the retired secret is still stored as a credential")
	}

	rotatedSecret, mirroredAt := boundCredential(t, store, id)
	if rotatedSecret != "sk-rot-new" {
		t.Errorf("bound credential secret = %q, want the rotated value", rotatedSecret)
	}
	if mirroredAt <= seededAt {
		t.Errorf("bound credential updated_at = %d, want the mirror to rewrite the row", mirroredAt)
	}
	if got := poolSecret(t, store, "rot-up", "rot-cred-1"); got != "sk-rot-new" {
		t.Errorf("rebuilt snapshot serves %q, want the rotated secret", got)
	}

	// Replaying the same rotation must not churn the bound row: re-writing an
	// unchanged secret would move a write signal on every retry.
	if _, err := store.PatchProviderKeyRecord(ctx, id, ProviderKeyPatch{APIKey: "sk-rot-new"}); err != nil {
		t.Fatalf("replayed rotate: %v", err)
	}
	if _, replayedAt := boundCredential(t, store, id); replayedAt != mirroredAt {
		t.Errorf("replayed rotation rewrote bound credentials: updated_at %d -> %d", mirroredAt, replayedAt)
	}
}

// UNIQUE(api_key) is global, so rotating onto a secret another row already holds
// would merge two providers' credentials into one row. The request is refused, and
// refused without a trace; malformed secrets are refused before anything is written.
func TestPatchProviderKeyRecord_RefusesTakenOrMalformedSecret(t *testing.T) {
	store, ctx := storeCtx(t)
	pid := seedProvider(t, store, "taken")
	seedKeys(t, store, pid,
		ProviderKeySyncEntry{APIKey: "sk-holder"},
		ProviderKeySyncEntry{APIKey: "sk-rotator"},
	)
	holder := readKeyRow(t, store, "sk-holder")
	rotator := readKeyRow(t, store, "sk-rotator")

	before := readRevision(t, store)
	_, err := store.PatchProviderKeyRecord(ctx, rotator.ID, ProviderKeyPatch{APIKey: "sk-holder"})
	if !errors.Is(err, ErrAPIKeyTaken) {
		t.Fatalf("rotate onto a taken secret = %v, want ErrAPIKeyTaken", err)
	}
	// The conflict names the owning row so the operator can find it; it never
	// repeats the secret, which is what made it identifiable in the first place.
	if !strings.Contains(err.Error(), strconv.FormatInt(holder.ID, 10)) {
		t.Errorf("error %q does not name the holding key id %d", err, holder.ID)
	}
	if row := readKeyRow(t, store, "sk-rotator"); row.ID != rotator.ID || row.UpdatedAt != rotator.UpdatedAt {
		t.Errorf("rejected rotation changed the row: %+v", row)
	}
	if got := readRevision(t, store); got != before {
		t.Errorf("revision = %d, want %d: a rejected patch must not trigger a reload", got, before)
	}

	for _, bad := range []string{" sk-padded", "sk-inner\x00null", "sk-trailing\n", strings.Repeat("s", maxKeySecretLen+1)} {
		if _, err := store.PatchProviderKeyRecord(ctx, rotator.ID, ProviderKeyPatch{APIKey: bad}); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("secret %q error = %v, want ErrInvalidPayload", strings.ToValidUTF8(bad, "?"), err)
		}
	}
	if row := readKeyRow(t, store, "sk-rotator"); row.UpdatedAt != rotator.UpdatedAt {
		t.Error("a malformed secret patch reached the row")
	}
}

// A patch that resolves to the values already stored is not a change. Writing it
// anyway would advance api_keys.updated_at — which the syncer watches as its
// structural-change signal — and bump the catalog revision, making every instance
// rebuild the whole snapshot for nothing.
func TestPatchProviderKeyRecord_NoOpPatchLeavesChangeSignalsAlone(t *testing.T) {
	store, ctx := storeCtx(t)
	pid := seedProvider(t, store, "noop")
	seedKeys(t, store, pid, ProviderKeySyncEntry{APIKey: "sk-noop-live"})

	id := readKeyRow(t, store, "sk-noop-live").ID
	if _, err := store.DB().ExecContext(ctx, "UPDATE api_keys SET updated_at = 1000 WHERE id = ?", id); err != nil {
		t.Fatalf("pin updated_at: %v", err)
	}

	active, yes := "active", true
	revision := readRevision(t, store)
	for _, patch := range []ProviderKeyPatch{{Status: &active}, {IsActive: &yes}} {
		out, err := store.PatchProviderKeyRecord(ctx, id, patch)
		if err != nil {
			t.Fatalf("no-op patch: %v", err)
		}
		if out.Revision != revision {
			t.Errorf("no-op patch revision = %d, want the stored %d", out.Revision, revision)
		}
		if out.Pushed {
			t.Error("no-op patch pushed to the cloud")
		}
	}
	if row := readKeyRow(t, store, "sk-noop-live"); row.UpdatedAt != 1000 {
		t.Errorf("no-op patch advanced updated_at to %d, want the pinned 1000", row.UpdatedAt)
	}

	// A real transition still moves both signals.
	deactivated := "deactivated"
	out, err := store.PatchProviderKeyRecord(ctx, id, ProviderKeyPatch{Status: &deactivated})
	if err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if out.Revision != revision+1 {
		t.Errorf("revision = %d, want %d after a real transition", out.Revision, revision+1)
	}
	if row := readKeyRow(t, store, "sk-noop-live"); row.Status != "deactivated" || row.UpdatedAt <= 1000 {
		t.Errorf("row = %q/updated_at:%d, want the transition applied and signalled", row.Status, row.UpdatedAt)
	}
}

// Every entry point refuses a store that was never given a database handle
// instead of panicking on a nil one.
func TestProviderRecords_RequiresInitializedStore(t *testing.T) {
	ctx := context.Background()
	status := "active"
	patch := ProviderKeyPatch{Status: &status}
	batch := []ProviderKeySyncEntry{{APIKey: "sk-x"}}

	var nilStore *Store
	if _, err := nilStore.CreateProviderRecord(ctx, ProviderUpdate{}); err == nil {
		t.Error("nil store created a provider")
	}
	if _, err := nilStore.UpsertProviderKeyRecords(ctx, 1, batch, false); err == nil {
		t.Error("nil store upserted a key batch")
	}
	if _, err := nilStore.PatchProviderKeyRecord(ctx, 1, patch); err == nil {
		t.Error("nil store patched a key")
	}

	empty := &Store{}
	if _, err := empty.CreateProviderRecord(ctx, ProviderUpdate{}); err == nil {
		t.Error("store without a db handle created a provider")
	}
	if _, err := empty.UpsertProviderKeyRecords(ctx, 1, batch, false); err == nil {
		t.Error("store without a db handle upserted a key batch")
	}
	if _, err := empty.PatchProviderKeyRecord(ctx, 1, patch); err == nil {
		t.Error("store without a db handle patched a key")
	}
}
