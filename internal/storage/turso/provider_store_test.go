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

// syncCtx bundles the store under test with the context every sync call uses.
func syncCtx(t *testing.T) (*Store, context.Context) {
	t.Helper()
	store, _ := setupTestDB(t)
	return store, context.Background()
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
// columns the sync must never touch (metering) as well as on identity stability.
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

func TestSyncProviderKeys_CreatesProviderAndKey(t *testing.T) {
	store, ctx := syncCtx(t)
	before := readRevision(t, store)

	res, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "openai", BaseURL: "https://api.openai.com"}},
		Keys:      []ProviderKeySyncEntry{{Provider: "openai", APIKey: "sk-alpha"}},
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Created != 1 || res.Updated != 0 || res.Unchanged != 0 {
		t.Fatalf("counters = created:%d updated:%d unchanged:%d, want 1/0/0", res.Created, res.Updated, res.Unchanged)
	}
	if res.Revision != before+1 {
		t.Fatalf("revision = %d, want %d (one bump per sync)", res.Revision, before+1)
	}
	if res.Pushed {
		t.Error("Pushed = true without a turso client")
	}

	row := readKeyRow(t, store, "sk-alpha")
	if row.ProviderID != readProviderID(t, store, "openai") {
		t.Errorf("key bound to provider %d, want the provider created by this batch", row.ProviderID)
	}
	if row.Status != "active" || row.IsActive != 1 {
		t.Errorf("status/is_active = %q/%d, want active/1", row.Status, row.IsActive)
	}
	if row.LastUsedAt != 0 || row.TotalRequests != 0 {
		t.Errorf("metering = last_used_at:%d total_requests:%d, want 0/0 for a new key", row.LastUsedAt, row.TotalRequests)
	}
	if got := res.KeyIDs["openai"]; len(got) != 1 || got[0] != row.ID {
		t.Errorf("key_ids[openai] = %v, want [%d]", got, row.ID)
	}

	// A second sync of the same provider must reuse the row (no duplicate) and
	// must not have written a provider active flag of 0.
	var providers int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM providers WHERE name = 'openai'").Scan(&providers); err != nil {
		t.Fatalf("count providers: %v", err)
	}
	if providers != 1 {
		t.Errorf("providers named openai = %d, want 1", providers)
	}
}

func TestSyncProviderKeys_IdempotentReplayPreservesIdentityAndMetering(t *testing.T) {
	store, ctx := syncCtx(t)

	first, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "anthropic", BaseURL: "https://api.anthropic.com", Description: "claude"}},
		Keys:      []ProviderKeySyncEntry{{Provider: "anthropic", APIKey: "sk-beta"}},
	})
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	id := readKeyRow(t, store, "sk-beta").ID

	// Simulate live traffic metering: this is what the usage flusher writes.
	if _, err := store.DB().ExecContext(ctx,
		"UPDATE api_keys SET last_used_at = ?, total_requests = ? WHERE id = ?",
		int64(1_700_000_000_000), int64(42), id); err != nil {
		t.Fatalf("seed metering: %v", err)
	}
	metered := readKeyRow(t, store, "sk-beta")

	before := readRevision(t, store)
	replay, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "anthropic", BaseURL: "https://api.anthropic.com", Description: "claude"}},
		Keys:      []ProviderKeySyncEntry{{Provider: "anthropic", APIKey: "sk-beta"}},
	})
	if err != nil {
		t.Fatalf("replay sync: %v", err)
	}
	if replay.Unchanged != 1 || replay.Created != 0 || replay.Updated != 0 {
		t.Fatalf("replay counters = created:%d updated:%d unchanged:%d, want 0/0/1",
			replay.Created, replay.Updated, replay.Unchanged)
	}

	after := readKeyRow(t, store, "sk-beta")
	if after.ID != id {
		t.Fatalf("key id changed across replay: %d -> %d", id, after.ID)
	}
	if after.LastUsedAt != metered.LastUsedAt || after.TotalRequests != metered.TotalRequests {
		t.Errorf("metering rewritten by replay: last_used_at %d->%d total_requests %d->%d",
			metered.LastUsedAt, after.LastUsedAt, metered.TotalRequests, after.TotalRequests)
	}
	if after.CreatedAt != first.KeyIDs["anthropic"][0] && after.CreatedAt <= 0 {
		t.Errorf("created_at = %d, want the original insert timestamp", after.CreatedAt)
	}
	if after.UpdatedAt != metered.UpdatedAt {
		t.Errorf("updated_at advanced on an unchanged key: %d -> %d; MAX(updated_at) is a structural signal", metered.UpdatedAt, after.UpdatedAt)
	}
	// The revision still advances: the bump is unconditional so a replay can
	// never be mistaken for "nothing was applied" by the reload loop.
	if replay.Revision != before+1 {
		t.Errorf("revision = %d, want %d", replay.Revision, before+1)
	}
}

func TestSyncProviderKeys_UpdatesChangedFieldsOnly(t *testing.T) {
	store, ctx := syncCtx(t)

	active := false
	if _, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "grok", BaseURL: "https://api.x.ai", IsActive: &active}},
		Keys:      []ProviderKeySyncEntry{{Provider: "grok", APIKey: "sk-gamma"}},
	}); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	seed := readKeyRow(t, store, "sk-gamma")

	expiry := int64(4_102_444_800_000)
	res, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "grok", BaseURL: "https://api.x.ai", IsActive: &active}},
		Keys:      []ProviderKeySyncEntry{{Provider: "grok", APIKey: "sk-gamma", Status: "rate_limited", ExpiresAt: msExpiry(expiry)}},
	})
	if err != nil {
		t.Fatalf("update sync: %v", err)
	}
	if res.Updated != 1 || res.Unchanged != 0 {
		t.Fatalf("counters = updated:%d unchanged:%d, want 1/0", res.Updated, res.Unchanged)
	}

	updated := readKeyRow(t, store, "sk-gamma")
	if updated.ID != seed.ID || updated.CreatedAt != seed.CreatedAt {
		t.Errorf("identity changed: id %d->%d created_at %d->%d", seed.ID, updated.ID, seed.CreatedAt, updated.CreatedAt)
	}
	if updated.Status != "rate_limited" || updated.IsActive != 0 {
		t.Errorf("status/is_active = %q/%d, want rate_limited/0", updated.Status, updated.IsActive)
	}
	if updated.ExpiresAt == nil || *updated.ExpiresAt != expiry {
		t.Errorf("expires_at = %v, want %d", updated.ExpiresAt, expiry)
	}
	if updated.LastUsedAt != seed.LastUsedAt || updated.TotalRequests != seed.TotalRequests {
		t.Error("metering columns touched by a structural update")
	}
	// is_active is derived from status, so a non-active key is not routable.
	if got := res.KeyIDs["grok"]; len(got) != 0 {
		t.Errorf("key_ids[grok] = %v, want empty for a rate_limited key", got)
	}

	// A provider-only change must not touch key rows at all.
	activeTrue := true
	beforeRev := readRevision(t, store)
	res2, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "grok", BaseURL: "https://api.x.ai/v1", IsActive: &activeTrue}},
	})
	if err != nil {
		t.Fatalf("provider-only sync: %v", err)
	}
	if res2.Created != 0 || res2.Updated != 0 || res2.Unchanged != 0 {
		t.Errorf("key counters = %d/%d/%d, want all zero for a provider-only batch",
			res2.Created, res2.Updated, res2.Unchanged)
	}
	if res2.Revision != beforeRev+1 {
		t.Errorf("revision = %d, want %d", res2.Revision, beforeRev+1)
	}
}

func TestSyncProviderKeys_RejectsProviderMismatch(t *testing.T) {
	store, ctx := syncCtx(t)

	if _, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{
			{Name: "provider-a", BaseURL: "https://a.example.com"},
			{Name: "provider-b", BaseURL: "https://b.example.com"},
		},
		Keys: []ProviderKeySyncEntry{{Provider: "provider-a", APIKey: "sk-shared"}},
	}); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	seed := readKeyRow(t, store, "sk-shared")
	revBefore := readRevision(t, store)

	// UNIQUE(api_key) is global, so re-homing a secret would move provider-a's
	// quota to provider-b. The whole batch must be refused.
	_, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Keys: []ProviderKeySyncEntry{{Provider: "provider-b", APIKey: "sk-shared"}},
	})
	if !errors.Is(err, ErrKeyProviderMismatch) {
		t.Fatalf("err = %v, want ErrKeyProviderMismatch", err)
	}

	after := readKeyRow(t, store, "sk-shared")
	if after.ProviderID != seed.ProviderID {
		t.Errorf("key moved to provider %d despite the rejection (was %d)", after.ProviderID, seed.ProviderID)
	}
	if revAfter := readRevision(t, store); revAfter != revBefore {
		t.Errorf("revision advanced on a rejected batch: %d -> %d", revBefore, revAfter)
	}
}

func TestSyncProviderKeys_ValidatesRequiredFields(t *testing.T) {
	store, ctx := syncCtx(t)
	before := readRevision(t, store)

	cases := []struct {
		name    string
		req     ProviderSyncRequest
		wantErr error
	}{
		{
			name:    "provider without name",
			req:     ProviderSyncRequest{Providers: []ProviderSyncEntry{{BaseURL: "https://x.example.com"}}},
			wantErr: ErrInvalidSyncPayload,
		},
		{
			name:    "key without provider",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{APIKey: "sk-x"}}},
			wantErr: ErrInvalidSyncPayload,
		},
		{
			name: "key without secret",
			req: ProviderSyncRequest{
				Providers: []ProviderSyncEntry{{Name: "p", BaseURL: "https://p.example.com"}},
				Keys:      []ProviderKeySyncEntry{{Provider: "p"}},
			},
			wantErr: ErrInvalidSyncPayload,
		},
		{
			name:    "key for unknown provider",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{Provider: "ghost", APIKey: "sk-ghost"}}},
			wantErr: ErrProviderUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.SyncProviderKeys(ctx, tc.req); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}

	// Every rejected batch must roll back completely: no rows and no revision.
	var providers, keys int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM providers").Scan(&providers); err != nil {
		t.Fatalf("count providers: %v", err)
	}
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys").Scan(&keys); err != nil {
		t.Fatalf("count keys: %v", err)
	}
	if providers != 0 || keys != 0 {
		t.Errorf("rejected batches left data behind: providers=%d keys=%d", providers, keys)
	}
	if after := readRevision(t, store); after != before {
		t.Errorf("revision advanced by rejected batches: %d -> %d", before, after)
	}
}

func TestSyncProviderKeys_DeactivatesExplicitIDs(t *testing.T) {
	store, ctx := syncCtx(t)

	if _, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{
			{Name: "doomed", BaseURL: "https://doomed.example.com"},
			{Name: "kept", BaseURL: "https://kept.example.com"},
		},
		Keys: []ProviderKeySyncEntry{
			{Provider: "doomed", APIKey: "sk-doom-1"},
			{Provider: "doomed", APIKey: "sk-doom-2"},
			{Provider: "kept", APIKey: "sk-keep-1"},
		},
	}); err != nil {
		t.Fatalf("seed sync: %v", err)
	}

	doomed1 := readKeyRow(t, store, "sk-doom-1").ID
	doomed2 := readKeyRow(t, store, "sk-doom-2").ID
	kept := readKeyRow(t, store, "sk-keep-1").ID

	// Bind an upstream credential to the key being deactivated so the mirror is
	// observable (the error-policy path does the same).
	up := &UpstreamRecord{Name: "doomed-up", Protocol: "openai", BaseURL: "https://doomed.example.com", KeyStrategy: "round_robin", Enabled: true}
	if err := store.SaveUpstream(ctx, up); err != nil {
		t.Fatalf("save upstream: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `
		INSERT INTO upstream_credentials (upstream_id, api_key_id, ref, secret, status, is_active, created_at, updated_at)
		VALUES (?, ?, 'doomed-cred-1', 'sk-doom-1', 'active', 1, ?, ?)
	`, up.ID, doomed1, int64(1), int64(1)); err != nil {
		t.Fatalf("seed upstream credential: %v", err)
	}

	res, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		DeactivateKeys: []int64{doomed1, doomed2, doomed1, 0, -5, 999_999},
	})
	if err != nil {
		t.Fatalf("deactivate sync: %v", err)
	}
	if res.Deactivated != 2 {
		t.Errorf("deactivated = %d, want 2 (deduped, real transitions only)", res.Deactivated)
	}
	if res.Created != 0 || res.Updated != 0 || res.Unchanged != 0 {
		t.Errorf("key counters = %d/%d/%d, want all zero for a deactivate-only batch",
			res.Created, res.Updated, res.Unchanged)
	}
	if len(res.KeyIDs) != 0 {
		t.Errorf("key_ids = %v, want empty when no provider is touched", res.KeyIDs)
	}

	for _, secret := range []string{"sk-doom-1", "sk-doom-2"} {
		row := readKeyRow(t, store, secret)
		if row.IsActive != 0 || row.Status != "deactivated" {
			t.Errorf("%s: is_active/status = %d/%q, want 0/deactivated", secret, row.IsActive, row.Status)
		}
	}
	if row := readKeyRow(t, store, "sk-keep-1"); row.IsActive != 1 {
		t.Errorf("unlisted key was deactivated: is_active = %d", row.IsActive)
	}
	if row := readKeyRow(t, store, "sk-keep-1"); row.ID != kept {
		t.Errorf("unlisted key id changed: %d -> %d", kept, row.ID)
	}

	var credActive int
	var credStatus string
	if err := store.DB().QueryRowContext(ctx,
		"SELECT is_active, status FROM upstream_credentials WHERE api_key_id = ?", doomed1).
		Scan(&credActive, &credStatus); err != nil {
		t.Fatalf("read upstream credential: %v", err)
	}
	if credActive != 0 || credStatus != "deactivated" {
		t.Errorf("bound credential not mirrored: is_active/status = %d/%q", credActive, credStatus)
	}

	// Replaying a deactivation is a no-op and reports zero, so a retrying client
	// can resend its batch idempotently.
	replay, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{DeactivateKeys: []int64{doomed1, doomed2}})
	if err != nil {
		t.Fatalf("replay deactivate: %v", err)
	}
	if replay.Deactivated != 0 {
		t.Errorf("replayed deactivation counted %d, want 0", replay.Deactivated)
	}
}

func TestSyncProviderKeys_AccountMetadataVaultSemantics(t *testing.T) {
	store, ctx := syncCtx(t)

	const vault = `{"private_key":"0xdeadbeef","password":"hunter2"}`

	res, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "vault", BaseURL: "https://vault.example.com"}},
		Keys:      []ProviderKeySyncEntry{{Provider: "vault", APIKey: "sk-vault", AccountMetadata: json.RawMessage(vault)}},
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if row := readKeyRow(t, store, "sk-vault"); row.Metadata == nil || *row.Metadata != vault {
		t.Fatalf("account_metadata stored as %v, want the blob written verbatim", row.Metadata)
	}

	// The secret vault must never travel back out through the sync result.
	encoded, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	for _, forbidden := range []string{"sk-vault", "private_key", "0xdeadbeef", "hunter2"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("sync result leaked %q: %s", forbidden, encoded)
		}
	}

	// An omitted blob means "keep what is stored" — not "erase it".
	omitted, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Keys: []ProviderKeySyncEntry{{Provider: "vault", APIKey: "sk-vault"}},
	})
	if err != nil {
		t.Fatalf("omitted-metadata sync: %v", err)
	}
	if omitted.Unchanged != 1 {
		t.Errorf("counters = %+v, want the key unchanged when the blob is omitted", omitted)
	}
	if row := readKeyRow(t, store, "sk-vault"); row.Metadata == nil || *row.Metadata != vault {
		t.Errorf("omitted blob erased the stored vault: %v", row.Metadata)
	}

	// Whitespace-only differences are not a change; a different blob is.
	same, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Keys: []ProviderKeySyncEntry{{Provider: "vault", APIKey: "sk-vault", AccountMetadata: json.RawMessage("\n  " + vault + "  \n")}},
	})
	if err != nil {
		t.Fatalf("whitespace-metadata sync: %v", err)
	}
	if same.Unchanged != 1 {
		t.Errorf("whitespace-padded blob counted as a change: %+v", same)
	}

	rotated := `{"private_key":"0xfeedface"}`
	changed, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Keys: []ProviderKeySyncEntry{{Provider: "vault", APIKey: "sk-vault", AccountMetadata: json.RawMessage(rotated)}},
	})
	if err != nil {
		t.Fatalf("rotated-metadata sync: %v", err)
	}
	if changed.Updated != 1 {
		t.Errorf("counters = %+v, want the key updated for a rotated blob", changed)
	}
	if row := readKeyRow(t, store, "sk-vault"); row.Metadata == nil || *row.Metadata != rotated {
		t.Errorf("account_metadata = %v, want the rotated blob", row.Metadata)
	}
}

func TestSyncProviderKeys_ExpiryGateMatchesSnapshotLoader(t *testing.T) {
	store, ctx := syncCtx(t)

	expired := time.Now().Add(-24 * time.Hour).UnixMilli()
	future := int64(4_102_444_800_000)
	res, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "gated", BaseURL: "https://gated.example.com"}},
		Keys: []ProviderKeySyncEntry{
			{Provider: "gated", APIKey: "sk-live"},
			{Provider: "gated", APIKey: "sk-stale", ExpiresAt: msExpiry(expired)},
			{Provider: "gated", APIKey: "sk-future", ExpiresAt: msExpiry(future)},
		},
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	live := readKeyRow(t, store, "sk-live").ID
	futureID := readKeyRow(t, store, "sk-future").ID
	want := map[int64]bool{live: true, futureID: true}
	got := res.KeyIDs["gated"]
	if len(got) != len(want) {
		t.Fatalf("key_ids[gated] = %v, want ids for the two non-expired keys", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("key id %d is expired and must not be routable", id)
		}
	}

	// The expired key stays stored and active — expiry is a routing-time filter,
	// not a write — which is exactly what the snapshot loader re-evaluates.
	if row := readKeyRow(t, store, "sk-stale"); row.IsActive != 1 || row.Status != "active" {
		t.Errorf("expired key rewritten: is_active/status = %d/%q, want 1/active", row.IsActive, row.Status)
	}

	// A batch that says nothing about expires_at must not rewrite it: the writer
	// refreshing this row is not the one that set its lifetime, and a silent wipe
	// would put an expired credential back into rotation. It is also a no-op, so
	// the row must not be counted as updated.
	kept, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Keys: []ProviderKeySyncEntry{{Provider: "gated", APIKey: "sk-stale"}},
	})
	if err != nil {
		t.Fatalf("omit-expiry sync: %v", err)
	}
	if kept.Updated != 0 || kept.Unchanged != 1 {
		t.Errorf("counters = %+v, want unchanged when expires_at is omitted", kept)
	}
	if row := readKeyRow(t, store, "sk-stale"); row.ExpiresAt == nil || *row.ExpiresAt != expired {
		t.Errorf("expires_at = %v, want the stored %d preserved", row.ExpiresAt, expired)
	}

	// An explicit null is the documented way back: it returns a key whose lifetime
	// has passed to the pool once the underlying credential is genuinely renewed.
	cleared, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Keys: []ProviderKeySyncEntry{{Provider: "gated", APIKey: "sk-stale", ExpiresAt: clearExpiry}},
	})
	if err != nil {
		t.Fatalf("clear-expiry sync: %v", err)
	}
	if cleared.Updated != 1 {
		t.Errorf("counters = %+v, want the key updated when expires_at is cleared", cleared)
	}
	if row := readKeyRow(t, store, "sk-stale"); row.ExpiresAt != nil {
		t.Errorf("expires_at = %v, want NULL", row.ExpiresAt)
	}
	if ids := cleared.KeyIDs["gated"]; len(ids) != 3 {
		t.Errorf("key_ids[gated] = %v, want all three keys routable after the clear", ids)
	}
}

func TestSyncProviderKeys_KeyResolvesProviderDeclaredInSameBatch(t *testing.T) {
	store, ctx := syncCtx(t)

	// A batch that declares a provider and immediately uses it must not need a
	// prior round trip through the database.
	res, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "fresh", BaseURL: "https://fresh.example.com"}},
		Keys: []ProviderKeySyncEntry{
			{Provider: "fresh", APIKey: "sk-fresh-1"},
			{Provider: "fresh", APIKey: "sk-fresh-2"},
		},
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Created != 2 {
		t.Fatalf("created = %d, want 2", res.Created)
	}
	if len(res.KeyIDs["fresh"]) != 2 {
		t.Errorf("key_ids[fresh] = %v, want both keys", res.KeyIDs["fresh"])
	}

	// An existing key for an existing provider resolves from the database.
	if _, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Keys: []ProviderKeySyncEntry{{Provider: "fresh", APIKey: "sk-fresh-3"}},
	}); err != nil {
		t.Fatalf("db-resolved sync: %v", err)
	}
	if got := readKeyRow(t, store, "sk-fresh-3").ProviderID; got != readProviderID(t, store, "fresh") {
		t.Errorf("db-resolved key bound to provider %d, want %d", got, readProviderID(t, store, "fresh"))
	}
}

func TestSyncProviderKeys_RequiresInitializedStore(t *testing.T) {
	var nilStore *Store
	if _, err := nilStore.SyncProviderKeys(context.Background(), ProviderSyncRequest{}); err == nil {
		t.Error("nil store returned no error")
	}
	if _, err := (&Store{}).SyncProviderKeys(context.Background(), ProviderSyncRequest{}); err == nil {
		t.Error("store without a db handle returned no error")
	}
}

func TestSyncProviderKeys_RejectsMalformedBatch(t *testing.T) {
	repeat := func(n int, c byte) string { return strings.Repeat(string(c), n) }
	seconds := time.Now().Unix()
	negative := int64(-1)

	cases := []struct {
		name    string
		req     ProviderSyncRequest
		wantSub string
	}{
		{
			name:    "second-scale expiry",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{Provider: "gate", APIKey: "sk-bad", ExpiresAt: msExpiry(seconds)}}},
			wantSub: "Unix millisecond",
		},
		{
			name:    "negative expiry",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{Provider: "gate", APIKey: "sk-bad", ExpiresAt: msExpiry(negative)}}},
			wantSub: "Unix millisecond",
		},
		{
			name:    "expiry as a string",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{Provider: "gate", APIKey: "sk-bad", ExpiresAt: json.RawMessage(`"4102444800000"`)}}},
			wantSub: "unix millisecond timestamp or null",
		},
		{
			name:    "expiry as a fraction",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{Provider: "gate", APIKey: "sk-bad", ExpiresAt: json.RawMessage("4102444800.5")}}},
			wantSub: "unix millisecond timestamp or null",
		},
		{
			name:    "expiry as an object",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{Provider: "gate", APIKey: "sk-bad", ExpiresAt: json.RawMessage("{}")}}},
			wantSub: "unix millisecond timestamp or null",
		},
		{
			name:    "oversized status",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{Provider: "gate", APIKey: "sk-bad", Status: repeat(maxKeyStatusLen+1, 's')}}},
			wantSub: "status exceeds",
		},
		{
			name:    "padded status",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{Provider: "gate", APIKey: "sk-bad", Status: " active"}}},
			wantSub: "whitespace",
		},
		{
			name:    "control character in status",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{Provider: "gate", APIKey: "sk-bad", Status: "ac\x00tive"}}},
			wantSub: "control characters",
		},
		{
			name:    "oversized vault blob",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{Provider: "gate", APIKey: "sk-bad", AccountMetadata: json.RawMessage(repeat(MaxKeyMetadataBytes+1, 'v'))}}},
			wantSub: "account_metadata exceeds",
		},
		{
			name:    "oversized secret",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{Provider: "gate", APIKey: repeat(maxKeySecretLen+1, 'k')}}},
			wantSub: "api_key exceeds",
		},
		{
			name:    "missing secret",
			req:     ProviderSyncRequest{Keys: []ProviderKeySyncEntry{{Provider: "gate", APIKey: ""}}},
			wantSub: "requires both provider and api_key",
		},
		{
			name:    "empty provider name",
			req:     ProviderSyncRequest{Providers: []ProviderSyncEntry{{Name: "", BaseURL: "https://gate.example.com"}}},
			wantSub: "empty name",
		},
		{
			name:    "provider name too long",
			req:     ProviderSyncRequest{Providers: []ProviderSyncEntry{{Name: repeat(maxProviderNameLen+1, 'n'), BaseURL: "https://gate.example.com"}}},
			wantSub: "name exceeds",
		},
		{
			name:    "provider base_url too long",
			req:     ProviderSyncRequest{Providers: []ProviderSyncEntry{{Name: "gate", BaseURL: repeat(maxProviderURLLen+1, 'u')}}},
			wantSub: "base_url exceeds",
		},
		{
			name:    "provider description too long",
			req:     ProviderSyncRequest{Providers: []ProviderSyncEntry{{Name: "gate", BaseURL: "https://gate.example.com", Description: repeat(maxProviderDescLen+1, 'd')}}},
			wantSub: "description exceeds",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := syncCtx(t)
			revBefore, err := store.GetCatalogRevision(ctx)
			if err != nil {
				t.Fatalf("read catalog revision: %v", err)
			}

			// A well-formed sibling rides along with the malformed entry. If the
			// batch were applied partially, both would be found in the database.
			req := tc.req
			req.Providers = append([]ProviderSyncEntry{{Name: "gate", BaseURL: "https://gate.example.com"}}, req.Providers...)
			req.Keys = append([]ProviderKeySyncEntry{{Provider: "gate", APIKey: "sk-sibling"}}, req.Keys...)

			res, err := store.SyncProviderKeys(ctx, req)
			if !errors.Is(err, ErrInvalidSyncPayload) {
				t.Fatalf("error = %v, want ErrInvalidSyncPayload", err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
			if res.Created != 0 || res.Updated != 0 || res.Unchanged != 0 || res.Deactivated != 0 {
				t.Errorf("counters = %+v, want a rejected batch to report nothing", res)
			}

			var providers, keys int
			if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM providers").Scan(&providers); err != nil {
				t.Fatalf("count providers: %v", err)
			}
			if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys").Scan(&keys); err != nil {
				t.Fatalf("count api keys: %v", err)
			}
			if providers != 0 || keys != 0 {
				t.Errorf("rejected batch left rows behind: providers=%d api_keys=%d, want 0/0", providers, keys)
			}
			revAfter, err := store.GetCatalogRevision(ctx)
			if err != nil {
				t.Fatalf("read catalog revision: %v", err)
			}
			if revAfter != revBefore {
				t.Errorf("catalog revision moved %d -> %d for a rejected batch", revBefore, revAfter)
			}
		})
	}
}

// The status vocabulary belongs to the writer, so an unknown but well-formed
// state is stored verbatim instead of refused: rejecting it would freeze an
// entire batch over a state Firefly has simply never seen. What must hold is
// that any non-'active' status stays out of routing.
func TestSyncProviderKeys_StoresUnknownStatusVerbatim(t *testing.T) {
	store, ctx := syncCtx(t)
	if _, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "legacy", BaseURL: "https://legacy.example.com"}},
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}

	for _, status := range []string{"dead", "cooldown", "banned", "rate limited", "ACTIVE"} {
		secret := "sk-" + strings.NewReplacer(" ", "-", "_", "-").Replace(status)
		res, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
			Keys: []ProviderKeySyncEntry{{Provider: "legacy", APIKey: secret, Status: status}},
		})
		if err != nil {
			t.Fatalf("sync status %q: %v", status, err)
		}
		if res.Created != 1 {
			t.Fatalf("sync status %q counters = %+v, want one created key", status, res)
		}
		row := readKeyRow(t, store, secret)
		if row.Status != status {
			t.Errorf("stored status = %q, want %q", row.Status, status)
		}
		if row.IsActive != 0 {
			t.Errorf("is_active = %d for non-active status %q, want 0", row.IsActive, status)
		}
		if len(res.KeyIDs["legacy"]) != 0 {
			t.Errorf("key_ids[legacy] = %v with status %q, want no routable keys", res.KeyIDs["legacy"], status)
		}
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

// The operator batch carries expires_at in the same wire form as the sync, so it
// must carry the same three intents: a number sets, an explicit null clears, and an
// omitted field leaves the stored expiry alone. Wiping the lifetime as a side
// effect of editing something else would put an expired credential back in rotation.
func TestUpsertProviderKeyRecords_ExpiryIntentIsThreeWay(t *testing.T) {
	store, ctx := syncCtx(t)

	if _, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "operator", BaseURL: "https://operator.example.com"}},
	}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	pid := readProviderID(t, store, "operator")

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

// A rotation replaces a row's identity material while keeping its id, because the
// "<upstream>-key-<id>" refs are derived from that id. The bound upstream_credentials
// row holds its own copy of the secret and the loader prefers it over the joined
// api_keys value, so a rotation that stopped at api_keys would keep authenticating
// with the retired credential.
func TestPatchProviderKeyRecord_RotatesSecretInPlace(t *testing.T) {
	store, ctx := syncCtx(t)

	if _, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "rot", BaseURL: "https://rot.example.com"}},
		Keys:      []ProviderKeySyncEntry{{Provider: "rot", APIKey: "sk-rot-old"}},
	}); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
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
	if res.Key.ProviderID != readProviderID(t, store, "rot") {
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
	store, ctx := syncCtx(t)

	if _, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "taken", BaseURL: "https://taken.example.com"}},
		Keys: []ProviderKeySyncEntry{
			{Provider: "taken", APIKey: "sk-holder"},
			{Provider: "taken", APIKey: "sk-rotator"},
		},
	}); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
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

// Retiring a key by status is a lifecycle command the operator batch can issue
// without a settings save. The upstream_credentials rows carry their own copy of
// the secret and the snapshot loader prefers it over the joined api_keys value, so
// the mirror has to reach both binding shapes: the api_key_id foreign key, and the
// "<upstream>-key-<id>" ref a settings save copies into a pool with no binding.
// A key retired in api_keys alone would keep serving traffic through either.
func TestUpsertProviderKeyRecords_StatusChangeRetiresBoundCredentials(t *testing.T) {
	store, ctx := syncCtx(t)

	if _, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "ret", BaseURL: "https://ret.example.com"}},
		Keys: []ProviderKeySyncEntry{
			{Provider: "ret", APIKey: "sk-ret-live"},
			{Provider: "ret", APIKey: "sk-ret-keep"},
		},
	}); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	pid := readProviderID(t, store, "ret")
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

// A patch that resolves to the values already stored is not a change. Writing it
// anyway would advance api_keys.updated_at — which the syncer watches as its
// structural-change signal — and bump the catalog revision, making every instance
// rebuild the whole snapshot for nothing.
func TestPatchProviderKeyRecord_NoOpPatchLeavesChangeSignalsAlone(t *testing.T) {
	store, ctx := syncCtx(t)

	if _, err := store.SyncProviderKeys(ctx, ProviderSyncRequest{
		Providers: []ProviderSyncEntry{{Name: "noop", BaseURL: "https://noop.example.com"}},
		Keys:      []ProviderKeySyncEntry{{Provider: "noop", APIKey: "sk-noop-live"}},
	}); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
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
