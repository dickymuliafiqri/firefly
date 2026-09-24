package server

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"strconv"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/observability/metrics"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
)

// providerAdminStore is the store surface behind /api/providers*. *turso.Store
// implements it against the providers/api_keys tables. Without a Turso database
// the dashboard would lose its credential catalog entirely, so fileProviderStore
// serves the same reads from the live catalog snapshot and refuses every write:
// in file-config mode the definition of a credential lives in upstreams.json and
// the environment, which the settings surface — not this one — owns.
type providerAdminStore interface {
	ListProviderRecords(ctx context.Context) ([]turso.ProviderRecord, error)
	GetProviderRecord(ctx context.Context, id int64) (turso.ProviderRecord, error)
	CreateProviderRecord(ctx context.Context, in turso.ProviderUpdate) (turso.ProviderRecord, error)
	UpdateProviderRecord(ctx context.Context, id int64, in turso.ProviderUpdate) (turso.ProviderRecord, error)
	DeleteProviderRecord(ctx context.Context, id int64) (int, error)
	ListProviderKeyRecords(ctx context.Context, id int64) ([]turso.ProviderKeyRecord, error)
	UpsertProviderKeyRecords(ctx context.Context, id int64, keys []turso.ProviderKeySyncEntry, reassign bool) (turso.ProviderKeyUpsertResult, error)
	PatchProviderKeyRecord(ctx context.Context, id int64, patch turso.ProviderKeyPatch) (turso.ProviderKeyPatchResult, error)
	DeleteProviderKeyRecord(ctx context.Context, id int64) (int64, bool, error)
}

// errProviderReadOnly is returned by every fileProviderStore mutation and mapped
// to 501 by writeProvidersAdminError. The message names the editable surface so
// an operator who lands here from the dashboard knows where to go instead of
// reading a bare "operation failed".
var errProviderReadOnly = errors.New(
	"provider administration is read-only in file-config mode: " +
		"credentials come from upstreams.json and environment variables; " +
		"connect turso storage to manage keys here")

// fileProviderStore projects the active catalog snapshot onto the provider
// surface: one provider per upstream that carries a credential pool, one key per
// key ring slot.
//
// It is read-only by design, not by omission. A KeySlot in a file-built snapshot
// is a resolution of `credential_pool`/`credential_ref`/`api_keys` from
// upstreams.json and the environment; there is no row to update, and a mutation
// applied here would be discarded by the next reload, which rebuilds every ring
// from the files. Writes therefore fail loudly (501) rather than pretending.
//
// Usage counters come from the live metrics registry. LastUsedAt has no source
// there and stays zero; the dashboard renders that as "never used".
type fileProviderStore struct {
	snap    *domain.CatalogSnapshot
	metrics *metrics.Metrics
}

func newFileProviderStore(snap *domain.CatalogSnapshot, mx *metrics.Metrics) *fileProviderStore {
	return &fileProviderStore{snap: snap, metrics: mx}
}

// ListProviderRecords returns every credential-carrying upstream, in catalog
// order. Keyless upstreams (a free tier authenticated with a fixed public token,
// for example) have no credential pool to administer and are omitted.
func (s *fileProviderStore) ListProviderRecords(context.Context) ([]turso.ProviderRecord, error) {
	names := s.snap.UpstreamNames()
	records := make([]turso.ProviderRecord, 0, len(names))
	for _, name := range names {
		u, ok := s.snap.Upstream(name)
		if !ok || !hasCredentials(u) {
			continue
		}
		records = append(records, s.providerRecord(u))
	}
	return records, nil
}

// GetProviderRecord returns one provider by its derived id.
func (s *fileProviderStore) GetProviderRecord(_ context.Context, id int64) (turso.ProviderRecord, error) {
	u, ok := s.upstreamByID(id)
	if !ok {
		return turso.ProviderRecord{}, fmt.Errorf("%w: provider %d", turso.ErrNotFound, id)
	}
	return s.providerRecord(u), nil
}

// ListProviderKeyRecords returns every slot of one provider's key ring, in ring
// order, each with request counts from the live metrics registry.
func (s *fileProviderStore) ListProviderKeyRecords(ctx context.Context, id int64) ([]turso.ProviderKeyRecord, error) {
	u, ok := s.upstreamByID(id)
	if !ok {
		return nil, fmt.Errorf("%w: provider %d", turso.ErrNotFound, id)
	}

	var requests map[string]int64
	if s.metrics != nil {
		requests = s.metrics.Snapshot().KeyRequests
	}

	slots := u.KeyRing.AllSlots()
	records := make([]turso.ProviderKeyRecord, 0, len(slots))
	for _, slot := range slots {
		records = append(records, fileKeyRecord(u.Name, slot, requests[u.Name+"/"+slot.Ref]))
	}
	return records, nil
}

// CreateProviderRecord refuses: upstreams are defined in upstreams.json.
func (s *fileProviderStore) CreateProviderRecord(context.Context, turso.ProviderUpdate) (turso.ProviderRecord, error) {
	return turso.ProviderRecord{}, errProviderReadOnly
}

// UpdateProviderRecord refuses: upstreams are defined in upstreams.json.
func (s *fileProviderStore) UpdateProviderRecord(context.Context, int64, turso.ProviderUpdate) (turso.ProviderRecord, error) {
	return turso.ProviderRecord{}, errProviderReadOnly
}

// DeleteProviderRecord refuses: upstreams are defined in upstreams.json.
func (s *fileProviderStore) DeleteProviderRecord(context.Context, int64) (int, error) {
	return 0, errProviderReadOnly
}

// UpsertProviderKeyRecords refuses: credentials are declared in upstreams.json or
// the environment, and this store cannot write either.
func (s *fileProviderStore) UpsertProviderKeyRecords(context.Context, int64, []turso.ProviderKeySyncEntry, bool) (turso.ProviderKeyUpsertResult, error) {
	return turso.ProviderKeyUpsertResult{}, errProviderReadOnly
}

// PatchProviderKeyRecord refuses: a slot is rebuilt from its declaration on every
// reload, so a rotation applied here would not survive.
func (s *fileProviderStore) PatchProviderKeyRecord(context.Context, int64, turso.ProviderKeyPatch) (turso.ProviderKeyPatchResult, error) {
	return turso.ProviderKeyPatchResult{}, errProviderReadOnly
}

// DeleteProviderKeyRecord refuses: credentials are declared in upstreams.json or
// the environment, and this store cannot write either.
func (s *fileProviderStore) DeleteProviderKeyRecord(context.Context, int64) (int64, bool, error) {
	return 0, false, errProviderReadOnly
}

// providerRecord renders one upstream as a provider row. The description states
// where the row comes from, so the page never reads as a Turso-backed catalog.
func (s *fileProviderStore) providerRecord(u *domain.Upstream) turso.ProviderRecord {
	active := 0
	for _, slot := range u.KeyRing.AllSlots() {
		if !slot.Revoked.Load() {
			active++
		}
	}
	return turso.ProviderRecord{
		ID:          fileProviderID(u.Name),
		Name:        u.Name,
		BaseURL:     u.BaseURL,
		Description: "config-file upstream (" + string(u.Protocol) + " protocol)",
		IsActive:    !u.Disabled,
		ActiveKeys:  active,
	}
}

// upstreamByID resolves a derived provider id back to its upstream.
func (s *fileProviderStore) upstreamByID(id int64) (*domain.Upstream, bool) {
	for _, name := range s.snap.UpstreamNames() {
		if fileProviderID(name) != id {
			continue
		}
		u, ok := s.snap.Upstream(name)
		if !ok || !hasCredentials(u) {
			return nil, false
		}
		return u, true
	}
	return nil, false
}

// hasCredentials reports whether an upstream carries a credential pool.
func hasCredentials(u *domain.Upstream) bool {
	return u != nil && u.KeyRing != nil && u.KeyRing.SlotCount() > 0
}

// providerStoreProvenance names the backing store behind a providerAdminStore so
// the dashboard can label the page: "turso" for the database catalog, "file" for
// the read-only projection of the running configuration.
func providerStoreProvenance(store providerAdminStore) (storage string, readOnly bool) {
	if _, ok := store.(*fileProviderStore); ok {
		return "file", true
	}
	return "turso", false
}

// fileKeyRecord renders one key ring slot as an api_keys row. The record carries
// the value the handler will mask into api_key_hint:
//
//   - a database-managed credential (APIKeyID > 0, harvested or operator-added)
//     is identified by a fragment of its secret, exactly as in Turso mode;
//   - a config-declared credential has no stored secret to hint at, so its ref —
//     the ENV var name or pool ref an operator chose — is used instead. Refs are
//     identifiers, already visible on the admin telemetry surface, and are what
//     the operator recognises the credential by.
func fileKeyRecord(upstreamName string, slot *domain.KeySlot, requests int64) turso.ProviderKeyRecord {
	revoked := slot.Revoked.Load()
	status := "active"
	if revoked {
		status = "revoked"
	}

	hint := slot.Ref
	if slot.APIKeyID > 0 {
		hint = slot.Secret
	}

	providerID := fileProviderID(upstreamName)
	return turso.ProviderKeyRecord{
		ID:            fileKeyID(providerID, slot.Ref),
		ProviderID:    providerID,
		Status:        status,
		IsActive:      !revoked,
		TotalRequests: requests,
		Secret:        hint,
	}
}

// fileProviderID and fileKeyID derive stable positive ids for the read-only
// projection. Ids travel in dashboard URLs (/api/providers/{id}/keys) and in row
// labels, so they must not move between reloads — and a file-built snapshot is
// rebuilt from scratch on every reload, so there is no autoincrement counter to
// borrow. Hashing the natural keys (upstream name; provider id plus slot ref)
// keeps them stable across hot-swaps and identical across restarts.
func fileProviderID(name string) int64 {
	return stablePositiveID("provider", name)
}

func fileKeyID(providerID int64, ref string) int64 {
	return stablePositiveID("key", strconv.FormatInt(providerID, 10), ref)
}

// stablePositiveID hashes namespaced parts into the positive int64 range the
// path parsers accept (pathID rejects zero and negatives). The separator keeps
// adjacent parts from colliding ("ab"+"c" vs "a"+"bc"); the namespace keeps a
// provider id from colliding with a key id of the same value.
func stablePositiveID(namespace string, parts ...string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(namespace))
	for _, part := range parts {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(part))
	}
	id := int64(h.Sum64() & math.MaxInt64)
	if id == 0 {
		id = 1
	}
	return id
}
