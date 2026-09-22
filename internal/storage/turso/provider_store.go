package turso

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// errStoreNotInitialized is returned by store methods invoked without a database
// handle (nil Store, or a Store built before the client connected).
var errStoreNotInitialized = errors.New("turso store is not initialized")

// ensureReady rejects calls on a store with no database handle instead of
// panicking on a nil db.
func (s *Store) ensureReady() error {
	if s == nil || s.db == nil {
		return errStoreNotInitialized
	}
	return nil
}

// pushAfterWrite flushes a committed write to the cloud replica. The caller must
// hold the store lock (PushLocked runs on the single shared connection). A failed
// push is not fatal: the local replica holds the write and the next sync retries
// it, so the failure is logged and reported through the returned flag.
func (s *Store) pushAfterWrite(ctx context.Context, op string) bool {
	if s.client == nil {
		return false
	}
	if err := s.client.PushLocked(ctx); err != nil {
		s.getLogger().Warn("push to turso cloud failed; local replica holds the write", "op", op, "err", err)
		return false
	}
	return true
}

// keyUpsertOutcome classifies what one upsertKeyTx call did to the row.
type keyUpsertOutcome int

const (
	keyUpsertUnchanged keyUpsertOutcome = iota
	keyUpsertCreated
	keyUpsertUpdated
	// keyUpsertReassigned means the row existed under a different provider and was
	// moved (only reachable when allowReassign is set).
	keyUpsertReassigned
)

// upsertKeyTx inserts or updates one credential keyed by the secret itself
// (UNIQUE(api_key) is global), preserving the row id in both paths. A key that
// already belongs to another provider is rejected with ErrKeyProviderMismatch
// unless allowReassign is set (operator-only); a reassignment also drops the old
// provider's bound credential rows so the key cannot keep serving on both pools.
// On update, an omitted expires_at or account_metadata keeps the stored value;
// only an explicit null clears the expiry.
func (s *Store) upsertKeyTx(ctx context.Context, tx *sql.Tx, key ProviderKeySyncEntry, providerID, now int64, allowReassign bool) (keyUpsertOutcome, error) {
	status := key.Status
	if status == "" {
		status = "active"
	}
	activeInt := 0
	if status == "active" {
		activeInt = 1
	}

	var (
		id             int64
		existingPID    int64
		existingStatus string
		existingActive int
		existingExpiry sql.NullInt64
		existingMeta   sql.NullString
	)
	newExpiry, clearExpiry, err := expiryIntent(key.ExpiresAt)
	if err != nil {
		// Both callers validate before opening the transaction, so this is the
		// rollback path rather than the normal one; the sentinel keeps it a 400.
		return keyUpsertUnchanged, fmt.Errorf("%w: %v", ErrInvalidSyncPayload, err)
	}
	err = tx.QueryRowContext(ctx, `
		SELECT id, provider_id, status, is_active, expires_at, account_metadata
		FROM api_keys
		WHERE api_key = ?
	`, key.APIKey).Scan(&id, &existingPID, &existingStatus, &existingActive, &existingExpiry, &existingMeta)
	if errors.Is(err, sql.ErrNoRows) {
		var expiry any
		if newExpiry != nil {
			expiry = *newExpiry
		}
		var meta any
		if len(key.AccountMetadata) > 0 {
			meta = string(key.AccountMetadata)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO api_keys (provider_id, api_key, status, is_active, expires_at,
			                      last_used_at, total_requests, account_metadata, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 0, 0, ?, ?, ?)
		`, providerID, key.APIKey, status, activeInt, expiry, meta, now, now); err != nil {
			return keyUpsertUnchanged, fmt.Errorf("insert api key for provider %d: %w", providerID, err)
		}
		return keyUpsertCreated, nil
	}
	if err != nil {
		return keyUpsertUnchanged, fmt.Errorf("query existing api key: %w", err)
	}

	moved := existingPID != providerID
	if moved && !allowReassign {
		return keyUpsertUnchanged, fmt.Errorf("%w: key %d belongs to provider %d, payload says %d",
			ErrKeyProviderMismatch, id, existingPID, providerID)
	}

	// An omitted expires_at keeps the stored one: a writer that refreshes a row it
	// did not create must not silently un-expire it, because the key would then
	// stay routable past its real lifetime. JSON null is the explicit clear.
	resolved := existingExpiry
	switch {
	case clearExpiry:
		resolved = sql.NullInt64{}
	case newExpiry != nil:
		resolved = sql.NullInt64{Int64: *newExpiry, Valid: true}
	}
	expiryEqual := resolved == existingExpiry
	// An omitted account_metadata means "keep the stored vault blob": it is
	// large write-only data the client may legitimately not resend.
	metaEqual := len(key.AccountMetadata) == 0 ||
		bytes.Equal(bytes.TrimSpace([]byte(existingMeta.String)), bytes.TrimSpace(key.AccountMetadata))

	// Retiring a key is a lifecycle command this surface can issue by status alone,
	// so the mirror every other writer performs belongs here too. It runs before the
	// unchanged short-circuit, which is what lets a replay converge a bound row left
	// routable by an older write instead of reporting a hollow no-op.
	if status != "active" {
		if err := mirrorCredentialDeactivationTx(ctx, tx, id, status, now); err != nil {
			return keyUpsertUnchanged, err
		}
	}

	if !moved && expiryEqual && metaEqual && existingStatus == status && existingActive == activeInt {
		return keyUpsertUnchanged, nil
	}

	var expiry any
	if resolved.Valid {
		expiry = resolved.Int64
	}
	if len(key.AccountMetadata) > 0 {
		_, err = tx.ExecContext(ctx, `
			UPDATE api_keys
			SET provider_id = ?, status = ?, is_active = ?, expires_at = ?, account_metadata = ?, updated_at = ?
			WHERE id = ?
		`, providerID, status, activeInt, expiry, string(key.AccountMetadata), now, id)
	} else {
		_, err = tx.ExecContext(ctx, `
			UPDATE api_keys
			SET provider_id = ?, status = ?, is_active = ?, expires_at = ?, updated_at = ?
			WHERE id = ?
		`, providerID, status, activeInt, expiry, now, id)
	}
	if err != nil {
		return keyUpsertUnchanged, fmt.Errorf("update api key %d: %w", id, err)
	}

	if moved {
		// The old provider's upstreams must stop serving this key: the live join no
		// longer yields it, but a bound upstream_credentials row would keep it
		// routable. Rows of upstreams on the new provider are left alone.
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM upstream_credentials
			WHERE api_key_id = ? AND upstream_id IN (SELECT id FROM upstreams WHERE provider_id = ?)
		`, id, existingPID); err != nil {
			return keyUpsertUnchanged, fmt.Errorf("drop stale credentials for reassigned key %d: %w", id, err)
		}
		return keyUpsertReassigned, nil
	}
	return keyUpsertUpdated, nil
}

// activeKeyIDsTx lists the routable key ids of one provider, using the same
// predicate as the snapshot loader so the client's view matches routing.
func activeKeyIDsTx(ctx context.Context, tx *sql.Tx, providerID, now int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id FROM api_keys
		WHERE provider_id = ? AND is_active = 1 AND status = 'active'
		  AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY id ASC
	`, providerID, now)
	if err != nil {
		return nil, fmt.Errorf("list active keys for provider %d: %w", providerID, err)
	}
	defer rows.Close()

	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan active key id for provider %d: %w", providerID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active keys for provider %d: %w", providerID, err)
	}
	return ids, nil
}

// bumpCatalogRevisionTx increments the catalog revision inside the caller's
// transaction, so a committed sync is never left invisible to the reload check.
func bumpCatalogRevisionTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	if _, err := tx.ExecContext(ctx, `
		UPDATE catalog_revisions
		SET revision = revision + 1, updated_at = ?
		WHERE id = 1
	`, time.Now().UnixMilli()); err != nil {
		return 0, fmt.Errorf("increment catalog revision: %w", err)
	}
	var rev int64
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM catalog_revisions WHERE id = 1").Scan(&rev); err != nil {
		return 0, fmt.Errorf("read bumped revision: %w", err)
	}
	return rev, nil
}

// catalogRevisionTx reads the current revision inside the caller's transaction,
// for writers that changed nothing and so must report the revision they left in
// place rather than bump it.
func catalogRevisionTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var rev int64
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM catalog_revisions WHERE id = 1").Scan(&rev); err != nil {
		return 0, fmt.Errorf("read catalog revision: %w", err)
	}
	return rev, nil
}

// mirrorCredentialDeactivationTx retires the upstream_credentials rows of a key
// that is no longer routable. The snapshot loader reads those rows itself and
// prefers their own secret copy over the joined api_keys value, so a key retired
// in api_keys alone would keep serving the pool. Rows are matched by the foreign
// key and by the id embedded in a "<prefix>-key-<id>" ref, because a credential
// pool copied in by a settings save carries the ref without the binding. The
// WHERE guard makes a replay a no-op: a row already retired at that status is
// left untouched, so a retrying writer cannot churn its updated_at.
func mirrorCredentialDeactivationTx(ctx context.Context, tx *sql.Tx, keyID int64, status string, now int64) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE upstream_credentials
		SET status = ?, is_active = 0, updated_at = ?
		WHERE (is_active = 1 OR status <> ?)
		  AND (api_key_id = ? OR ref LIKE ?)
	`, status, now, status, keyID, "%-key-"+strconv.FormatInt(keyID, 10)); err != nil {
		return fmt.Errorf("deactivate credentials bound to key %d: %w", keyID, err)
	}
	return nil
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Shape limits taken from the adopted column definitions (Lampiran A.1 of
// docs/harvester-ownership-plan.md). SQLite does not enforce VARCHAR(n), so a
// writer that ignores them stores rows that every validating read path then
// refuses to touch; these are the schema's own widths, not invented limits.
const (
	maxProviderNameLen = 64
	maxProviderURLLen  = 255
	maxProviderDescLen = 255
	// maxKeySecretLen bounds one credential. api_keys.api_key is unbounded TEXT and
	// SQLite enforces nothing, so this is the writer's own bound: every stored
	// secret is copied into a KeySlot on each catalog rebuild and then sent as a
	// request header, so an oversized value costs memory on every reload and could
	// never be transmitted. It is far above any bearer token this pool holds.
	maxKeySecretLen = 4096
	// MaxKeyMetadataBytes bounds one vault blob. The largest observed value is
	// ~28 KiB, so this leaves room for a legitimate credential while stopping a
	// malformed client from writing a multi-megabyte row.
	MaxKeyMetadataBytes = 64 << 10
	// minExpiryMillis is 1973-03-03 in Unix milliseconds. Nothing legitimate sits
	// below it, so a smaller value can only be a second-scale timestamp — and
	// storing 1.7e9 in a column the routing predicate compares against
	// time.Now().UnixMilli() silently drops that key from rotation forever.
	minExpiryMillis = int64(100_000_000_000)
)

// validateKeySecret checks a credential the operator is writing. Unlike the
// batch shape check it also rejects padding and control characters, because a
// rotated secret becomes the row's natural key: a padded value can never be
// matched again by the harvester, and a control character can never be
// transmitted as a bearer token.
func validateKeySecret(secret string) error {
	switch {
	case secret == "":
		return errors.New("api_key must not be empty")
	case len(secret) > maxKeySecretLen:
		return fmt.Errorf("api_key exceeds %d bytes", maxKeySecretLen)
	case strings.TrimSpace(secret) != secret:
		return errors.New("api_key has leading or trailing whitespace")
	}
	for i := 0; i < len(secret); i++ {
		if secret[i] < 0x20 || secret[i] == 0x7f {
			return errors.New("api_key contains control characters")
		}
	}
	return nil
}

// validateExpiry rejects an expiry that cannot be a millisecond timestamp.
func validateExpiry(expiresAt *int64) error {
	if expiresAt == nil {
		return nil
	}
	if *expiresAt < minExpiryMillis {
		return fmt.Errorf("expires_at must be a Unix millisecond timestamp (>= %d); "+
			"a second-scale value reads as already expired", minExpiryMillis)
	}
	return nil
}

// expiryIntent decodes the three-way expires_at wire form: absent keeps the
// stored value, JSON null clears it, a number replaces it. Returning (value,
// clear) instead of a pointer is what makes "not sent" distinguishable from
// "sent as null", which a *int64 cannot express.
func expiryIntent(raw json.RawMessage) (*int64, bool, error) {
	value := bytes.TrimSpace(raw)
	if len(value) == 0 {
		return nil, false, nil
	}
	if bytes.Equal(value, []byte("null")) {
		return nil, true, nil
	}
	var ts int64
	if err := json.Unmarshal(value, &ts); err != nil {
		return nil, false, errors.New("expires_at must be a unix millisecond timestamp or null")
	}
	if err := validateExpiry(&ts); err != nil {
		return nil, false, err
	}
	return &ts, false, nil
}

// -----------------------------------------------------------------------------
// Operator CRUD (dashboard)
//
// These are the dashboard-facing writers for providers and api_keys. Every
// mutation runs in one transaction that bumps the catalog revision, so a
// committed operator write is published by the next sync instead of waiting on
// the MAX(updated_at) heuristic. Reads expose metadata only: account_metadata (a
// credential vault holding private keys, mnemonics, and OAuth tokens) is never
// read back, and a raw secret leaves the store solely for the handler to render a
// masked hint.
// -----------------------------------------------------------------------------

// rowScanner is implemented by *sql.Row and *sql.Rows, letting one scan helper
// serve both single-row and list queries.
type rowScanner interface{ Scan(dest ...any) error }

// ProviderRecord is the operator view of one providers row.
type ProviderRecord struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	BaseURL     string `json:"base_url"`
	Description string `json:"description,omitempty"`
	IsActive    bool   `json:"is_active"`
	ActiveKeys  int    `json:"active_keys"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

// ProviderKeyRecord is the operator view of one api_keys row. Secret carries the
// raw credential so the handler can render a masked hint; the json:"-" tag keeps
// it out of any serialized response even if a caller embeds the record directly.
type ProviderKeyRecord struct {
	ID            int64  `json:"id"`
	ProviderID    int64  `json:"provider_id"`
	Status        string `json:"status"`
	IsActive      bool   `json:"is_active"`
	ExpiresAt     *int64 `json:"expires_at,omitempty"`
	LastUsedAt    int64  `json:"last_used_at"`
	TotalRequests int64  `json:"total_requests"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`

	Secret string `json:"-"`
}

// ProviderUpdate is the operator-editable subset of a provider. Nil fields keep
// their stored value, so a partial edit cannot silently clear a column.
type ProviderUpdate struct {
	Name        *string `json:"name,omitempty"`
	BaseURL     *string `json:"base_url,omitempty"`
	Description *string `json:"description,omitempty"`
	IsActive    *bool   `json:"is_active,omitempty"`
}

// ProviderKeyPatch is a partial update of one api_keys row. Nil fields keep their
// stored value; ClearExpiry removes a stored expiry. APIKey rotates the secret in
// place: the row id survives, so "<upstream>-key-<id>" refs keep pointing at the
// same credential, which is the whole reason this path exists instead of a
// delete-and-reinsert.
type ProviderKeyPatch struct {
	APIKey      string
	Status      *string
	IsActive    *bool
	ExpiresAt   *int64
	ClearExpiry bool
}

// keyStatuses are the api_keys.status values the operator may set. 'active' is
// the only routable one; the others mirror the failure modes the data plane
// already produces (deactivated by the key error policy, expired by the sweep,
// revoked after repeated 401s).
var keyStatuses = map[string]bool{
	"active":      true,
	"deactivated": true,
	"expired":     true,
	"revoked":     true,
}

// ProviderKeyUpsertResult reports what an operator key batch changed. Updated
// counts rows written; Reassigned counts how many of those moved between
// providers.
type ProviderKeyUpsertResult struct {
	Created    int     `json:"created"`
	Updated    int     `json:"updated"`
	Unchanged  int     `json:"unchanged"`
	Reassigned int     `json:"reassigned"`
	KeyIDs     []int64 `json:"key_ids"`
	Revision   int64   `json:"revision"`
	Pushed     bool    `json:"pushed"`
}

// ProviderKeyPatchResult is the patched row plus the catalog bookkeeping the
// handler reports back.
type ProviderKeyPatchResult struct {
	Key      ProviderKeyRecord `json:"key"`
	Revision int64             `json:"revision"`
	Pushed   bool              `json:"pushed"`
}

// providerKeyColumns is the operator projection of api_keys, shared by the list
// and re-read queries.
const providerKeyColumns = `id, provider_id, api_key, status, is_active, expires_at,
	last_used_at, total_requests, created_at, updated_at`

// providerRecordSelect projects one providers row with its active key count: the
// same routability predicate the snapshot loader uses, so the count matches what
// actually serves traffic.
const providerRecordSelect = `
	SELECT p.id, p.name, p.base_url, COALESCE(p.description, ''), p.is_active,
	       COALESCE((SELECT COUNT(*) FROM api_keys ak
	                 WHERE ak.provider_id = p.id AND ak.is_active = 1 AND ak.status = 'active'
	                   AND (ak.expires_at IS NULL OR ak.expires_at > ?)), 0),
	       p.created_at, p.updated_at
	FROM providers p
`

// scanProviderRecord maps one projected providers row. sql.ErrNoRows passes
// through untouched so single-row callers can map it to ErrNotFound.
func scanProviderRecord(row rowScanner) (ProviderRecord, error) {
	var (
		rec      ProviderRecord
		isActive int
	)
	err := row.Scan(&rec.ID, &rec.Name, &rec.BaseURL, &rec.Description, &isActive,
		&rec.ActiveKeys, &rec.CreatedAt, &rec.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rec, err
		}
		return rec, fmt.Errorf("scan provider row: %w", err)
	}
	rec.IsActive = isActive == 1
	return rec, nil
}

// scanProviderKeyRecord maps one api_keys projection, keeping the raw secret for
// hint rendering only.
func scanProviderKeyRecord(row rowScanner) (ProviderKeyRecord, error) {
	var (
		rec       ProviderKeyRecord
		isActive  int
		expiresAt sql.NullInt64
	)
	err := row.Scan(&rec.ID, &rec.ProviderID, &rec.Secret, &rec.Status, &isActive,
		&expiresAt, &rec.LastUsedAt, &rec.TotalRequests, &rec.CreatedAt, &rec.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rec, err
		}
		return rec, fmt.Errorf("scan api key row: %w", err)
	}
	rec.IsActive = isActive == 1
	if expiresAt.Valid {
		v := expiresAt.Int64
		rec.ExpiresAt = &v
	}
	return rec, nil
}

// ListProviderRecords returns every provider, active or not, with its active key
// count. Unlike ListProviders it surfaces query failures instead of degrading to
// an empty list, so the dashboard can tell "no providers" from "store broken".
func (s *Store) ListProviderRecords(ctx context.Context) ([]ProviderRecord, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}
	s.rLock()
	defer s.rUnlock()

	rows, err := s.db.QueryContext(ctx, providerRecordSelect+" ORDER BY p.name ASC, p.id ASC", time.Now().UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer rows.Close()

	list := make([]ProviderRecord, 0)
	for rows.Next() {
		rec, err := scanProviderRecord(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate providers: %w", err)
	}
	return list, nil
}

// GetProviderRecord returns one provider by id, active or not.
func (s *Store) GetProviderRecord(ctx context.Context, providerID int64) (ProviderRecord, error) {
	if err := s.ensureReady(); err != nil {
		return ProviderRecord{}, err
	}
	if providerID <= 0 {
		return ProviderRecord{}, fmt.Errorf("%w: provider id must be positive", ErrInvalidPayload)
	}
	s.rLock()
	defer s.rUnlock()

	rec, err := scanProviderRecord(s.db.QueryRowContext(ctx, providerRecordSelect+" WHERE p.id = ?", time.Now().UnixMilli(), providerID))
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderRecord{}, fmt.Errorf("%w: provider %d", ErrNotFound, providerID)
	}
	return rec, err
}

// CreateProviderRecord inserts a new provider and returns it. A name that is
// already taken is refused rather than merged into: providers are matched by
// name, so a duplicate insert would silently capture the existing pool.
func (s *Store) CreateProviderRecord(ctx context.Context, in ProviderUpdate) (ProviderRecord, error) {
	if err := s.ensureReady(); err != nil {
		return ProviderRecord{}, err
	}
	if in.Name == nil || *in.Name == "" {
		return ProviderRecord{}, fmt.Errorf("%w: provider name is required", ErrInvalidPayload)
	}
	if in.BaseURL == nil || *in.BaseURL == "" {
		return ProviderRecord{}, fmt.Errorf("%w: provider base_url is required", ErrInvalidPayload)
	}
	name := *in.Name
	baseURL := *in.BaseURL
	description := ""
	if in.Description != nil {
		description = *in.Description
	}
	switch {
	case len(name) > maxProviderNameLen:
		return ProviderRecord{}, fmt.Errorf("%w: provider name exceeds %d characters", ErrInvalidPayload, maxProviderNameLen)
	case len(baseURL) > maxProviderURLLen:
		return ProviderRecord{}, fmt.Errorf("%w: provider base_url exceeds %d characters", ErrInvalidPayload, maxProviderURLLen)
	case len(description) > maxProviderDescLen:
		return ProviderRecord{}, fmt.Errorf("%w: provider description exceeds %d characters", ErrInvalidPayload, maxProviderDescLen)
	}
	active := true
	if in.IsActive != nil {
		active = *in.IsActive
	}

	s.lock()
	defer s.unlock()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return ProviderRecord{}, fmt.Errorf("begin provider create: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var existingID int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM providers WHERE name = ?", name).Scan(&existingID)
	if err == nil {
		return ProviderRecord{}, fmt.Errorf("%w: provider %q exists with id %d", ErrProviderExists, name, existingID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ProviderRecord{}, fmt.Errorf("query provider %q: %w", name, err)
	}

	now := time.Now().UnixMilli()
	activeInt := 0
	if active {
		activeInt = 1
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO providers (name, base_url, description, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, name, baseURL, nullableText(description), activeInt, now, now)
	if err != nil {
		return ProviderRecord{}, fmt.Errorf("insert provider %q: %w", name, err)
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return ProviderRecord{}, fmt.Errorf("read provider %q id: %w", name, err)
	}
	if _, err := bumpCatalogRevisionTx(ctx, tx); err != nil {
		return ProviderRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProviderRecord{}, fmt.Errorf("commit provider create: %w", err)
	}
	committed = true

	s.pushAfterWrite(ctx, "provider create")

	return ProviderRecord{
		ID: newID, Name: name, BaseURL: baseURL, Description: description,
		IsActive: active, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// UpdateProviderRecord applies operator edits to an existing provider. The name
// is not editable: providers are matched by name (the natural key the harvester
// syncs against), so a rename would orphan the pool instead of moving it.
func (s *Store) UpdateProviderRecord(ctx context.Context, providerID int64, in ProviderUpdate) (ProviderRecord, error) {
	if err := s.ensureReady(); err != nil {
		return ProviderRecord{}, err
	}
	if providerID <= 0 {
		return ProviderRecord{}, fmt.Errorf("%w: provider id must be positive", ErrInvalidPayload)
	}

	if in.BaseURL != nil && len(*in.BaseURL) > maxProviderURLLen {
		return ProviderRecord{}, fmt.Errorf("%w: provider base_url exceeds %d characters",
			ErrInvalidPayload, maxProviderURLLen)
	}
	if in.Description != nil && len(*in.Description) > maxProviderDescLen {
		return ProviderRecord{}, fmt.Errorf("%w: provider description exceeds %d characters",
			ErrInvalidPayload, maxProviderDescLen)
	}

	s.lock()
	defer s.unlock()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return ProviderRecord{}, fmt.Errorf("begin provider update: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var (
		name        string
		baseURL     string
		description sql.NullString
		isActive    int
		createdAt   int64
	)
	err = tx.QueryRowContext(ctx, `
		SELECT name, base_url, description, is_active, created_at
		FROM providers WHERE id = ?
	`, providerID).Scan(&name, &baseURL, &description, &isActive, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderRecord{}, fmt.Errorf("%w: provider %d", ErrNotFound, providerID)
	}
	if err != nil {
		return ProviderRecord{}, fmt.Errorf("query provider %d: %w", providerID, err)
	}

	if in.Name != nil && *in.Name != name {
		return ProviderRecord{}, fmt.Errorf("%w: provider name is not editable (id %d is %q)", ErrInvalidPayload, providerID, name)
	}
	if in.BaseURL != nil {
		if *in.BaseURL == "" {
			return ProviderRecord{}, fmt.Errorf("%w: provider base_url cannot be empty", ErrInvalidPayload)
		}
		baseURL = *in.BaseURL
	}
	if in.Description != nil {
		description = sql.NullString{String: *in.Description, Valid: *in.Description != ""}
	}
	if in.IsActive != nil {
		isActive = 0
		if *in.IsActive {
			isActive = 1
		}
	}

	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		UPDATE providers SET base_url = ?, description = ?, is_active = ?, updated_at = ? WHERE id = ?
	`, baseURL, description, isActive, now, providerID); err != nil {
		return ProviderRecord{}, fmt.Errorf("update provider %d: %w", providerID, err)
	}

	var activeKeys int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM api_keys
		WHERE provider_id = ? AND is_active = 1 AND status = 'active'
		  AND (expires_at IS NULL OR expires_at > ?)
	`, providerID, now).Scan(&activeKeys); err != nil {
		return ProviderRecord{}, fmt.Errorf("count active keys for provider %d: %w", providerID, err)
	}

	if _, err := bumpCatalogRevisionTx(ctx, tx); err != nil {
		return ProviderRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProviderRecord{}, fmt.Errorf("commit provider update: %w", err)
	}
	committed = true

	s.pushAfterWrite(ctx, "provider update")

	return ProviderRecord{
		ID: providerID, Name: name, BaseURL: baseURL, Description: description.String,
		IsActive: isActive == 1, ActiveKeys: activeKeys, CreatedAt: createdAt, UpdatedAt: now,
	}, nil
}

// DeleteProviderRecord removes a provider together with its keys (hard delete:
// the operator right per I7) and returns how many keys were removed. It refuses
// while any upstream still references the provider, because deleting would
// silently detach that upstream's credential pool.
func (s *Store) DeleteProviderRecord(ctx context.Context, providerID int64) (int, error) {
	if err := s.ensureReady(); err != nil {
		return 0, err
	}
	if providerID <= 0 {
		return 0, fmt.Errorf("%w: provider id must be positive", ErrInvalidPayload)
	}

	s.lock()
	defer s.unlock()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin provider delete: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var name string
	err = tx.QueryRowContext(ctx, "SELECT name FROM providers WHERE id = ?", providerID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: provider %d", ErrNotFound, providerID)
	}
	if err != nil {
		return 0, fmt.Errorf("query provider %d: %w", providerID, err)
	}

	var upstreamRefs int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM upstreams WHERE provider_id = ?", providerID).Scan(&upstreamRefs); err != nil {
		return 0, fmt.Errorf("count upstreams for provider %d: %w", providerID, err)
	}
	if upstreamRefs > 0 {
		return 0, fmt.Errorf("%w: provider %q is referenced by %d upstream(s); detach them first",
			ErrProviderInUse, name, upstreamRefs)
	}

	var keyCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys WHERE provider_id = ?", providerID).Scan(&keyCount); err != nil {
		return 0, fmt.Errorf("count keys for provider %d: %w", providerID, err)
	}

	// SQLite/libSQL does not enforce foreign keys unless PRAGMA foreign_keys is
	// enabled (it is not), so the children are removed explicitly instead of
	// relying on ON DELETE CASCADE.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM upstream_credentials
		WHERE api_key_id IN (SELECT id FROM api_keys WHERE provider_id = ?)
	`, providerID); err != nil {
		return 0, fmt.Errorf("delete credentials of provider %d: %w", providerID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_keys WHERE provider_id = ?`, providerID); err != nil {
		return 0, fmt.Errorf("delete keys of provider %d: %w", providerID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM providers WHERE id = ?`, providerID); err != nil {
		return 0, fmt.Errorf("delete provider %d: %w", providerID, err)
	}

	if _, err := bumpCatalogRevisionTx(ctx, tx); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit provider delete: %w", err)
	}
	committed = true

	s.pushAfterWrite(ctx, "provider delete")
	return keyCount, nil
}

// ListProviderKeyRecords returns every key of one provider, active or not.
func (s *Store) ListProviderKeyRecords(ctx context.Context, providerID int64) ([]ProviderKeyRecord, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}
	if providerID <= 0 {
		return nil, fmt.Errorf("%w: provider id must be positive", ErrInvalidPayload)
	}
	s.rLock()
	defer s.rUnlock()

	var exists int64
	err := s.db.QueryRowContext(ctx, "SELECT id FROM providers WHERE id = ?", providerID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: provider %d", ErrNotFound, providerID)
	}
	if err != nil {
		return nil, fmt.Errorf("query provider %d: %w", providerID, err)
	}

	rows, err := s.db.QueryContext(ctx, "SELECT "+providerKeyColumns+" FROM api_keys WHERE provider_id = ? ORDER BY id ASC", providerID)
	if err != nil {
		return nil, fmt.Errorf("list keys for provider %d: %w", providerID, err)
	}
	defer rows.Close()

	list := make([]ProviderKeyRecord, 0)
	for rows.Next() {
		rec, err := scanProviderKeyRecord(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate keys for provider %d: %w", providerID, err)
	}
	return list, nil
}

// UpsertProviderKeyRecords applies an operator key batch to one provider. Rows are
// matched by the secret itself (UNIQUE(api_key) is global) and keep their id, so a
// replay updates in place. A key already owned by another provider is refused
// unless reassign is set — the operator-only correction path O2b reserves —
// in which case it moves here and the old provider's bound credential rows are
// dropped so it cannot keep serving on both pools. The whole batch is one
// transaction, and a failed batch reports nothing: every count and id describes
// rows that are actually committed.
func (s *Store) UpsertProviderKeyRecords(ctx context.Context, providerID int64, entries []ProviderKeySyncEntry, reassign bool) (res ProviderKeyUpsertResult, err error) {
	res = ProviderKeyUpsertResult{KeyIDs: []int64{}}
	if err := s.ensureReady(); err != nil {
		return res, err
	}
	if providerID <= 0 {
		return res, fmt.Errorf("%w: provider id must be positive", ErrInvalidPayload)
	}
	if len(entries) == 0 {
		return res, fmt.Errorf("%w: at least one key is required", ErrInvalidPayload)
	}

	// The operator surface issues lifecycle commands, so it accepts only the
	// states Firefly's own writers produce; the whole batch is refused before
	// anything is written.
	for i, entry := range entries {
		if entry.APIKey == "" {
			return res, fmt.Errorf("%w: keys[%d] requires api_key", ErrInvalidPayload, i)
		}
		if len(entry.APIKey) > maxKeySecretLen {
			return res, fmt.Errorf("%w: keys[%d] api_key exceeds %d bytes",
				ErrInvalidPayload, i, maxKeySecretLen)
		}
		if entry.Status != "" && !keyStatuses[entry.Status] {
			return res, fmt.Errorf("%w: keys[%d] unsupported status (expected active, deactivated, expired, or revoked)",
				ErrInvalidPayload, i)
		}
		if _, _, err := expiryIntent(entry.ExpiresAt); err != nil {
			return res, fmt.Errorf("%w: keys[%d] %v", ErrInvalidPayload, i, err)
		}
		if len(entry.AccountMetadata) > MaxKeyMetadataBytes {
			return res, fmt.Errorf("%w: keys[%d] account_metadata exceeds %d bytes",
				ErrInvalidPayload, i, MaxKeyMetadataBytes)
		}
	}

	s.lock()
	defer s.unlock()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return res, fmt.Errorf("begin key upsert: %w", err)
	}
	committed := false
	// A batch that fails after its first write is rolled back whole, so the counts
	// and ids it gathered on the way are dropped with it: reporting them would
	// describe rows that no longer exist.
	defer func() {
		if !committed {
			_ = tx.Rollback()
			res = ProviderKeyUpsertResult{KeyIDs: []int64{}}
		}
	}()

	var providerName string
	err = tx.QueryRowContext(ctx, "SELECT name FROM providers WHERE id = ?", providerID).Scan(&providerName)
	if errors.Is(err, sql.ErrNoRows) {
		return res, fmt.Errorf("%w: provider %d", ErrNotFound, providerID)
	}
	if err != nil {
		return res, fmt.Errorf("query provider %d: %w", providerID, err)
	}

	now := time.Now().UnixMilli()
	for _, entry := range entries {
		// The route already fixes the provider; a body that names another one is a
		// client bug, and silently preferring either side would hide it.
		if entry.Provider != "" && entry.Provider != providerName {
			return res, fmt.Errorf("%w: key entry names provider %q but the route targets %q",
				ErrInvalidPayload, entry.Provider, providerName)
		}
		outcome, err := s.upsertKeyTx(ctx, tx, entry, providerID, now, reassign)
		if err != nil {
			return res, err
		}
		switch outcome {
		case keyUpsertCreated:
			res.Created++
		case keyUpsertUpdated:
			res.Updated++
		case keyUpsertReassigned:
			res.Updated++
			res.Reassigned++
		case keyUpsertUnchanged:
			res.Unchanged++
		}
	}

	ids, err := activeKeyIDsTx(ctx, tx, providerID, now)
	if err != nil {
		return res, err
	}
	res.KeyIDs = ids

	revision, err := bumpCatalogRevisionTx(ctx, tx)
	if err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("commit key upsert: %w", err)
	}
	committed = true

	res.Revision = revision
	res.Pushed = s.pushAfterWrite(ctx, "operator key upsert")
	return res, nil
}

// PatchProviderKeyRecord applies a partial update to one key and returns the row
// as stored. A status-only edit keeps is_active coherent with it, because the
// routing predicate requires both and a stale is_active would show a key as
// serving in the dashboard while the data plane skips it. An APIKey edit rotates
// the secret without changing the row id. A patch that resolves to the values
// already stored changes nothing: no write, no revision bump.
func (s *Store) PatchProviderKeyRecord(ctx context.Context, keyID int64, patch ProviderKeyPatch) (ProviderKeyPatchResult, error) {
	var out ProviderKeyPatchResult
	if err := s.ensureReady(); err != nil {
		return out, err
	}
	if keyID <= 0 {
		return out, fmt.Errorf("%w: key id must be positive", ErrInvalidPayload)
	}
	if patch.Status != nil && !keyStatuses[*patch.Status] {
		return out, fmt.Errorf("%w: unsupported status %q (expected active, deactivated, expired, or revoked)", ErrInvalidPayload, *patch.Status)
	}
	if err := validateExpiry(patch.ExpiresAt); err != nil {
		return out, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if patch.APIKey != "" {
		if err := validateKeySecret(patch.APIKey); err != nil {
			return out, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
		}
	}

	s.lock()
	defer s.unlock()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return out, fmt.Errorf("begin key patch: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var (
		status    string
		isActive  int
		expiresAt sql.NullInt64
		storedKey string
	)
	err = tx.QueryRowContext(ctx, "SELECT status, is_active, expires_at, api_key FROM api_keys WHERE id = ?", keyID).
		Scan(&status, &isActive, &expiresAt, &storedKey)
	if errors.Is(err, sql.ErrNoRows) {
		return out, fmt.Errorf("%w: key %d", ErrNotFound, keyID)
	}
	if err != nil {
		return out, fmt.Errorf("query api key %d: %w", keyID, err)
	}
	storedStatus, storedActive, storedExpiry := status, isActive, expiresAt

	if patch.Status != nil {
		status = *patch.Status
	}
	switch {
	case patch.IsActive != nil:
		isActive = 0
		if *patch.IsActive {
			isActive = 1
		}
	case patch.Status != nil:
		isActive = 0
		if status == "active" {
			isActive = 1
		}
	}
	if patch.ClearExpiry {
		expiresAt = sql.NullInt64{}
	} else if patch.ExpiresAt != nil {
		expiresAt = sql.NullInt64{Int64: *patch.ExpiresAt, Valid: true}
	}

	// A save that resolves to the stored values is not a change: writing it anyway
	// would advance updated_at and the catalog revision, and both are change signals
	// that make every instance rebuild the whole snapshot.
	rotating := patch.APIKey != "" && patch.APIKey != storedKey
	dirty := rotating || status != storedStatus || isActive != storedActive || expiresAt != storedExpiry

	now := time.Now().UnixMilli()
	if dirty {
		var expiryArg any
		if expiresAt.Valid {
			expiryArg = expiresAt.Int64
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE api_keys SET status = ?, is_active = ?, expires_at = ?, updated_at = ? WHERE id = ?
		`, status, isActive, expiryArg, now, keyID); err != nil {
			return out, fmt.Errorf("update api key %d: %w", keyID, err)
		}

		// A rotation is the one operator edit that replaces a row's identity material
		// while keeping its id — credential refs are derived from the id, which is why
		// this path exists instead of delete-and-reinsert. UNIQUE(api_key) is global, so
		// a value another row already holds is refused: applying it would merge two
		// providers' credentials into one row.
		if rotating {
			var takenID int64
			err := tx.QueryRowContext(ctx,
				"SELECT id FROM api_keys WHERE api_key = ? AND id <> ?", patch.APIKey, keyID).Scan(&takenID)
			if err == nil {
				return out, fmt.Errorf("%w: key %d", ErrAPIKeyTaken, takenID)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return out, fmt.Errorf("check rotated api key: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				"UPDATE api_keys SET api_key = ?, updated_at = ? WHERE id = ?", patch.APIKey, now, keyID); err != nil {
				return out, fmt.Errorf("rotate api key %d: %w", keyID, err)
			}
			// Bound credentials keep their own copy of the secret and the snapshot loader
			// prefers it over the joined api_keys value, so a rotation that stopped at
			// api_keys would leave the old credential serving traffic.
			if _, err := tx.ExecContext(ctx,
				"UPDATE upstream_credentials SET secret = ?, updated_at = ? WHERE api_key_id = ?",
				patch.APIKey, now, keyID); err != nil {
				return out, fmt.Errorf("rotate secret of credentials bound to key %d: %w", keyID, err)
			}
		}

		// A key that is no longer routable must not keep serving through a bound
		// upstream_credentials row, so the same mirror every other writer performs is
		// applied here. Reactivation is not mirrored back: the provider join reads
		// api_keys directly, and explicit-pool rows are restored by a settings save.
		if status != "active" || isActive != 1 {
			mirrorStatus := status
			if mirrorStatus == "active" {
				mirrorStatus = "deactivated"
			}
			if err := mirrorCredentialDeactivationTx(ctx, tx, keyID, mirrorStatus, now); err != nil {
				return out, err
			}
		}
	}

	rec, err := scanProviderKeyRecord(tx.QueryRowContext(ctx, "SELECT "+providerKeyColumns+" FROM api_keys WHERE id = ?", keyID))
	if err != nil {
		return out, fmt.Errorf("re-read api key %d: %w", keyID, err)
	}
	out.Key = rec

	var revision int64
	if dirty {
		revision, err = bumpCatalogRevisionTx(ctx, tx)
		if err != nil {
			return out, err
		}
	} else {
		revision, err = catalogRevisionTx(ctx, tx)
		if err != nil {
			return out, err
		}
	}
	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("commit key patch: %w", err)
	}
	committed = true

	out.Revision = revision
	if dirty {
		out.Pushed = s.pushAfterWrite(ctx, "operator key patch")
	}
	return out, nil
}

// DeleteProviderKeyRecord hard-deletes one key (the operator right per I7): the
// row and every credential bound to it are removed, so its refs disappear from
// all upstreams. Returns the new catalog revision.
func (s *Store) DeleteProviderKeyRecord(ctx context.Context, keyID int64) (int64, bool, error) {
	if err := s.ensureReady(); err != nil {
		return 0, false, err
	}
	if keyID <= 0 {
		return 0, false, fmt.Errorf("%w: key id must be positive", ErrInvalidPayload)
	}

	s.lock()
	defer s.unlock()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("begin key delete: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, keyID)
	if err != nil {
		return 0, false, fmt.Errorf("delete api key %d: %w", keyID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("delete api key %d: %w", keyID, err)
	}
	if affected == 0 {
		return 0, false, fmt.Errorf("%w: key %d", ErrNotFound, keyID)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM upstream_credentials WHERE api_key_id = ?`, keyID); err != nil {
		return 0, false, fmt.Errorf("delete credentials bound to key %d: %w", keyID, err)
	}

	revision, err := bumpCatalogRevisionTx(ctx, tx)
	if err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit key delete: %w", err)
	}
	committed = true

	return revision, s.pushAfterWrite(ctx, "operator key delete"), nil
}

// Legacy provider reads (kept for the pre-consolidation dashboard endpoints
// GET /api/turso/providers and GET /api/turso/providers/{id}/keys). Unlike the
// operator surface above they degrade to an empty list on query failure and
// expose the raw api_key, so new callers belong on the ProviderRecord /
// ProviderKeyRecord path instead.

// ProviderSummary represents a provider loaded from the Turso database.
type ProviderSummary struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	BaseURL     string `json:"base_url"`
	Description string `json:"description"`
	ActiveKeys  int    `json:"active_keys"`
}

// ListProviders retrieves the active providers and their available active key count.
func (s *Store) ListProviders(ctx context.Context) ([]ProviderSummary, error) {
	s.rLock()
	defer s.rUnlock()

	now := time.Now().UnixMilli()
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.name, p.base_url,
		       COALESCE((SELECT COUNT(*) FROM api_keys ak WHERE ak.provider_id = p.id AND ak.is_active = 1 AND ak.status = 'active' AND (ak.expires_at IS NULL OR ak.expires_at > ?)), 0) AS active_keys
		FROM providers p
		WHERE p.is_active = 1
		ORDER BY p.name ASC
	`, now)
	if err != nil {
		// If providers table does not exist or errors, return empty list gracefully
		return []ProviderSummary{}, nil
	}
	defer rows.Close()

	var list []ProviderSummary
	for rows.Next() {
		var ps ProviderSummary
		if err := rows.Scan(&ps.ID, &ps.Name, &ps.BaseURL, &ps.ActiveKeys); err != nil {
			continue
		}
		list = append(list, ps)
	}
	if list == nil {
		list = []ProviderSummary{}
	}
	return list, nil
}

// ProviderKeyDTO represents an active API key from the Turso database.
type ProviderKeyDTO struct {
	ID         int64  `json:"id"`
	ProviderID int64  `json:"provider_id"`
	APIKey     string `json:"api_key"`
	Status     string `json:"status"`
	IsActive   bool   `json:"is_active"`
}

// ListProviderKeys retrieves active API keys for a specific provider, or all active keys if providerID <= 0.
// The returned APIKey is the real secret: every HTTP surface must mask it before writing a response.
func (s *Store) ListProviderKeys(ctx context.Context, providerID int64) ([]ProviderKeyDTO, error) {
	s.rLock()
	defer s.rUnlock()

	now := time.Now().UnixMilli()
	var (
		rows *sql.Rows
		err  error
	)
	if providerID > 0 {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, provider_id, api_key, status, is_active
			FROM api_keys
			WHERE provider_id = ? AND is_active = 1 AND status = 'active'
			  AND (expires_at IS NULL OR expires_at > ?)
			ORDER BY id ASC
		`, providerID, now)
	} else {
		rows, err = s.db.QueryContext(ctx, `
			SELECT id, provider_id, api_key, status, is_active
			FROM api_keys
			WHERE is_active = 1 AND status = 'active'
			  AND (expires_at IS NULL OR expires_at > ?)
			ORDER BY id ASC
		`, now)
	}
	if err != nil {
		return []ProviderKeyDTO{}, nil
	}
	defer rows.Close()

	var list []ProviderKeyDTO
	for rows.Next() {
		var (
			k         ProviderKeyDTO
			activeInt int
		)
		if err := rows.Scan(&k.ID, &k.ProviderID, &k.APIKey, &k.Status, &activeInt); err != nil {
			continue
		}
		k.IsActive = (activeInt == 1)
		list = append(list, k)
	}
	if list == nil {
		list = []ProviderKeyDTO{}
	}
	return list, nil
}
