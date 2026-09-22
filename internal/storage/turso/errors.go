package turso

import "errors"

var (
	// ErrConflict is returned when an optimistic concurrency check fails (version mismatch).
	ErrConflict = errors.New("concurrent modification detected: version mismatch")
	// ErrNotFound is returned when a requested entity does not exist.
	ErrNotFound = errors.New("resource not found")
	// ErrInvalidSyncPayload is returned when a harvester sync batch is missing a
	// required field (provider name, key secret, or key provider). Maps to 400.
	ErrInvalidSyncPayload = errors.New("invalid sync payload")
	// ErrProviderUnknown is returned when a sync batch references a provider that
	// is neither declared in the same payload nor present in the database. Maps to 400.
	ErrProviderUnknown = errors.New("unknown provider")
	// ErrKeyProviderMismatch is returned when an existing api_key row (matched by
	// the secret itself) belongs to a different provider than the payload claims.
	// Because UNIQUE(api_key) is global, applying the move would hand one
	// provider's quota to another, so the batch is rejected instead. Maps to 409.
	ErrKeyProviderMismatch = errors.New("api key belongs to a different provider")
	// ErrInvalidPayload is returned when an operator CRUD payload is missing a
	// required field or carries an unsupported value. Maps to 400.
	ErrInvalidPayload = errors.New("invalid payload")
	// ErrProviderExists is returned when creating a provider whose name is already
	// taken. Providers are matched by name, so a duplicate insert would silently
	// capture the existing pool. Maps to 409.
	ErrProviderExists = errors.New("provider already exists")
	// ErrProviderInUse is returned when deleting a provider that upstreams still
	// reference: the delete would silently detach their credential pool. Maps to 409.
	ErrProviderInUse = errors.New("provider is still referenced by upstreams")
	// ErrAPIKeyTaken is returned when rotating a key's secret to a value another
	// row already holds. UNIQUE(api_key) is global, so applying it would either
	// fail the write or merge two providers' credentials into one row. Maps to 409.
	ErrAPIKeyTaken = errors.New("api key secret is already stored under a different key id")
)
