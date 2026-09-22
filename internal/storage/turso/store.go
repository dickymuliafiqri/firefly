package turso

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// Store handles database operations on the local Turso replica.
type Store struct {
	client *Client
	db     *sql.DB
	logger *slog.Logger
	mu     sync.RWMutex
}

// NewStore initializes a new Store with the provided Turso Client.
func NewStore(client *Client) *Store {
	logger := slog.Default()
	if client != nil && client.logger != nil {
		logger = client.logger
	}
	var db *sql.DB
	if client != nil {
		db = client.DB()
	}
	return &Store{
		client: client,
		db:     db,
		logger: logger,
	}
}

// NewStoreWithDB initializes a Store backed directly by an existing *sql.DB,
// primarily for in-memory test harnesses and local embedded usage.
func NewStoreWithDB(db *sql.DB) *Store {
	return &Store{
		db: db,
	}
}

// Client returns the underlying Turso Client.
func (s *Store) Client() *Client {
	return s.client
}

// DB returns the underlying sql.DB handle.
func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) rLock() {
	if s.client != nil {
		s.client.RLock()
	} else {
		s.mu.RLock()
	}
}

func (s *Store) rUnlock() {
	if s.client != nil {
		s.client.RUnlock()
	} else {
		s.mu.RUnlock()
	}
}

func (s *Store) lock() {
	if s.client != nil {
		s.client.Lock()
	} else {
		s.mu.Lock()
	}
}

func (s *Store) unlock() {
	if s.client != nil {
		s.client.Unlock()
	} else {
		s.mu.Unlock()
	}
}

// Lock acquires the underlying store lock.
func (s *Store) Lock() {
	s.lock()
}

// Unlock releases the underlying store lock.
func (s *Store) Unlock() {
	s.unlock()
}

// RLock acquires the underlying store read lock.
func (s *Store) RLock() {
	s.rLock()
}

// RUnlock releases the underlying store read lock.
func (s *Store) RUnlock() {
	s.rUnlock()
}

func isTransactionInProgressError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "transaction within a transaction") ||
		strings.Contains(msg, "cannot start a transaction within a transaction")
}

func (s *Store) getLogger() *slog.Logger {
	if s != nil && s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// beginTx starts a new transaction. If a previous transaction was left uncommitted
// or unrolled-back on the SQLite connection (yielding "cannot start a transaction
// within a transaction"), it recovers by issuing ROLLBACK and retrying once.
func (s *Store) beginTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		if isTransactionInProgressError(err) {
			s.getLogger().Warn("detected stale transaction on database connection; issuing rollback recovery", "err", err)
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_, rollbackErr := s.db.ExecContext(cleanupCtx, "ROLLBACK")
			cancel()
			if rollbackErr != nil {
				s.getLogger().Warn("turso rollback recovery encountered warning", "err", rollbackErr)
			}
			return s.db.BeginTx(ctx, nil)
		}
		return nil, err
	}
	return tx, nil
}

// BeginTx starts a transaction with automatic rollback recovery for stale transactions.
func (s *Store) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return s.beginTx(ctx)
}

// -----------------------------------------------------------------------------
// Catalog Revision & Snapshot Loading
// -----------------------------------------------------------------------------

// GetCatalogRevision fetches the current catalog revision from the database.
func (s *Store) GetCatalogRevision(ctx context.Context) (int64, error) {
	s.rLock()
	defer s.rUnlock()
	return s.getCatalogRevisionLocked(ctx)
}

func (s *Store) getCatalogRevisionLocked(ctx context.Context) (int64, error) {
	var rev int64
	err := s.db.QueryRowContext(ctx, "SELECT revision FROM catalog_revisions WHERE id = 1").Scan(&rev)
	if err != nil {
		return 0, fmt.Errorf("query catalog revision: %w", err)
	}
	return rev, nil
}

// BumpCatalogRevision increments the catalog revision and returns the new value.
func (s *Store) BumpCatalogRevision(ctx context.Context) (int64, error) {
	s.lock()
	defer s.unlock()
	return s.bumpCatalogRevisionLocked(ctx)
}

func (s *Store) bumpCatalogRevisionLocked(ctx context.Context) (int64, error) {
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `
		UPDATE catalog_revisions
		SET revision = revision + 1, updated_at = ?
		WHERE id = 1
	`, now)
	if err != nil {
		return 0, fmt.Errorf("increment catalog revision: %w", err)
	}

	var rev int64
	if err := s.db.QueryRowContext(ctx, "SELECT revision FROM catalog_revisions WHERE id = 1").Scan(&rev); err != nil {
		return 0, fmt.Errorf("read bumped revision: %w", err)
	}
	return rev, nil
}

// LoadCatalogSnapshot reads configuration from the database tables,
// resolves dynamic keys from providers/api_keys, builds a validated
// CatalogSnapshot, and returns any validation warnings.
func (s *Store) LoadCatalogSnapshot(ctx context.Context, envLookup func(string) (string, bool)) (*domain.CatalogSnapshot, []string, error) {
	s.rLock()
	defer s.rUnlock()

	settings, err := s.loadSettingsInternal(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("load settings from database: %w", err)
	}

	upRaw, err := json.Marshal(config.UpstreamsFile{Upstreams: settings.Upstreams})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal upstreams: %w", err)
	}
	modRaw, err := json.Marshal(config.ModelsFile{Models: settings.Models})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal models: %w", err)
	}
	tenRaw, err := json.Marshal(config.TenantsFile{Tenants: settings.Tenants})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal tenants: %w", err)
	}
	combRaw, err := json.Marshal(config.CombosFile{Combos: settings.Combos})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal combos: %w", err)
	}

	// Token Saver lives in the settings blob, not in a table, so it has to be
	// marshaled back into the file set: without it every snapshot rebuilt from
	// the database (startup and each sync) would carry the disabled default and
	// silently drop the configured compression.
	var tsRaw []byte
	if settings.TokenSaver != nil {
		tsRaw, err = json.Marshal(settings.TokenSaver)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal tokensaver: %w", err)
		}
	}

	fs := config.FileSet{
		Upstreams:  upRaw,
		Models:     modRaw,
		Tenants:    tenRaw,
		Combos:     combRaw,
		TokenSaver: tsRaw,
	}

	res, err := config.Build(fs, envLookup)
	if err != nil {
		return nil, nil, fmt.Errorf("build snapshot from turso data: %w", err)
	}

	for _, u := range res.Upstreams {
		if u != nil && u.KeyRing != nil {
			for _, slot := range u.KeyRing.Slots {
				if slot != nil && slot.APIKeyID == 0 {
					slot.APIKeyID = extractAPIKeyID(slot.Ref)
				}
			}
		}
	}

	rev, _ := s.getCatalogRevisionLocked(ctx)
	snap := domain.NewCatalogSnapshot(
		uint64(rev),
		res.Upstreams,
		res.UpstreamOrder,
		res.Models,
		res.EnabledModelIDs,
		res.TenantsByHash,
		res.TenantOrder,
		domain.WithCombos(res.Combos, res.ComboOrder),
		domain.WithTokenSaver(res.TokenSaver),
	)

	return snap, res.Warnings, nil
}

// LoadSettings retrieves the full settings DTO from the database.
func (s *Store) LoadSettings(ctx context.Context) (*config.SettingsDTO, error) {
	s.rLock()
	defer s.rUnlock()
	return s.loadSettingsInternal(ctx)
}

func (s *Store) loadSettingsInternal(ctx context.Context) (*config.SettingsDTO, error) {
	now := time.Now().UnixMilli()

	// 1. Load Upstreams
	upRows, err := s.db.QueryContext(ctx, `
		SELECT id, name, protocol, base_url, fallback_base_urls, key_strategy,
		       provider_id, credential_ref, timeout_ms, idle_timeout_ms, stream_idle_timeout_ms,
		       max_idle_conns_per_host, max_conns_per_host, extra_headers, allow_insecure,
		       credential_rps, credential_max_concur,
		       COALESCE(key_error_threshold, 0),
		       COALESCE(key_error_action, 'deactivate'),
		       COALESCE(key_cooldown_duration_ms, 300000),
		       COALESCE(probe_model, ''),
		       COALESCE(egress_mode, 'direct'),
		       COALESCE(proxy_url, ''),
		       COALESCE(warp_auto_rotate_on_429, 0),
		       enabled
		FROM upstreams
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query upstreams: %w", err)
	}
	defer upRows.Close()

	var upstreams []config.UpstreamDTO
	upstreamIDToName := make(map[int64]string)

	type upMeta struct {
		id                  int64
		name                string
		providerID          *int64
		credentialRPS       *float64
		credentialMaxConcur *int
	}
	var upstreamMetas []upMeta

	for upRows.Next() {
		var (
			id                                            int64
			name, protocol, baseURL, keyStrategy          string
			fallbackURLsJSON, credRef, extraHeadersJSON   sql.NullString
			providerID                                    sql.NullInt64
			timeoutMs, idleTimeoutMs, streamIdleTimeoutMs sql.NullInt64
			maxIdleConns, maxConns                        sql.NullInt64
			allowInsecure, enabled                        int
			credRPS                                       sql.NullFloat64
			credMaxConcur                                 sql.NullInt64
			keyErrorThreshold, keyCooldownMs              sql.NullInt64
			keyErrorAction                                sql.NullString
			probeModel                                    sql.NullString
			egressMode, proxyURL                          sql.NullString
			warpAutoRotateOn429Int                        sql.NullInt64
		)

		err := upRows.Scan(
			&id, &name, &protocol, &baseURL, &fallbackURLsJSON, &keyStrategy,
			&providerID, &credRef, &timeoutMs, &idleTimeoutMs, &streamIdleTimeoutMs,
			&maxIdleConns, &maxConns, &extraHeadersJSON, &allowInsecure,
			&credRPS, &credMaxConcur,
			&keyErrorThreshold, &keyErrorAction, &keyCooldownMs, &probeModel,
			&egressMode, &proxyURL, &warpAutoRotateOn429Int,
			&enabled,
		)
		if err != nil {
			return nil, fmt.Errorf("scan upstream: %w", err)
		}

		upstreamIDToName[id] = name

		var fallbacks []string
		if fallbackURLsJSON.Valid && fallbackURLsJSON.String != "" {
			_ = json.Unmarshal([]byte(fallbackURLsJSON.String), &fallbacks)
		}

		var extraHeaders map[string]string
		if extraHeadersJSON.Valid && extraHeadersJSON.String != "" {
			_ = json.Unmarshal([]byte(extraHeadersJSON.String), &extraHeaders)
		}

		var (
			tMs       *int
			iMs       *int
			sMs       *int
			maxIdle   *int
			maxC      *int
			cRPS      *float64
			cMaxC     *int
			pID       *int64
			isAllowIn = allowInsecure != 0
			isEn      = enabled != 0
		)

		if timeoutMs.Valid {
			v := int(timeoutMs.Int64)
			tMs = &v
		}
		if idleTimeoutMs.Valid {
			v := int(idleTimeoutMs.Int64)
			iMs = &v
		}
		if streamIdleTimeoutMs.Valid {
			v := int(streamIdleTimeoutMs.Int64)
			sMs = &v
		}
		if maxIdleConns.Valid {
			v := int(maxIdleConns.Int64)
			maxIdle = &v
		}
		if maxConns.Valid {
			v := int(maxConns.Int64)
			maxC = &v
		}
		if credRPS.Valid {
			v := credRPS.Float64
			cRPS = &v
		}
		if credMaxConcur.Valid {
			v := int(credMaxConcur.Int64)
			cMaxC = &v
		}
		if providerID.Valid {
			v := providerID.Int64
			pID = &v
		}

		allBaseURLs := []string{baseURL}
		if len(fallbacks) > 0 {
			allBaseURLs = append(allBaseURLs, fallbacks...)
		}

		var (
			kErrThresh *int
			kErrAct    string
			kCoolMs    *int
		)
		if keyErrorThreshold.Valid {
			v := int(keyErrorThreshold.Int64)
			kErrThresh = &v
		}
		if keyErrorAction.Valid && keyErrorAction.String != "" {
			kErrAct = keyErrorAction.String
		} else {
			kErrAct = "deactivate"
		}
		if keyCooldownMs.Valid {
			v := int(keyCooldownMs.Int64)
			kCoolMs = &v
		}

		dto := config.UpstreamDTO{
			Name:                    name,
			Protocol:                protocol,
			BaseURL:                 baseURL,
			BaseURLs:                allBaseURLs,
			ProviderID:              pID,
			CredentialRef:           credRef.String,
			KeyStrategy:             keyStrategy,
			TimeoutMs:               tMs,
			IdleTimeoutMs:           iMs,
			StreamIdleTimeoutMs:     sMs,
			MaxIdleConnsPerHost:     maxIdle,
			MaxConnsPerHost:         maxC,
			ExtraHeaders:            extraHeaders,
			AllowInsecure:           &isAllowIn,
			Enabled:                 &isEn,
			CredentialRPS:           cRPS,
			CredentialMaxConcurrent: cMaxC,
			KeyErrorThreshold:       kErrThresh,
			KeyErrorAction:          kErrAct,
			KeyCooldownDurationMs:   kCoolMs,
			ProbeModel:              probeModel.String,
			EgressMode:              egressMode.String,
			ProxyURL:                proxyURL.String,
			WarpAutoRotateOn429: func() *bool {
				if warpAutoRotateOn429Int.Valid {
					v := warpAutoRotateOn429Int.Int64 != 0
					return &v
				}
				return nil
			}(),
		}

		upstreams = append(upstreams, dto)
		upstreamMetas = append(upstreamMetas, upMeta{
			id:                  id,
			name:                name,
			providerID:          pID,
			credentialRPS:       cRPS,
			credentialMaxConcur: cMaxC,
		})
	}

	// 2. Resolve CredentialPool for each upstream
	for i, meta := range upstreamMetas {
		var pool []config.CredentialKeyDTO
		seenRefs := make(map[string]bool)

		// 2a. Check if attached to harvester provider_id
		if meta.providerID != nil {
			kRows, err := s.db.QueryContext(ctx, `
				SELECT id, api_key
				FROM api_keys
				WHERE provider_id = ? AND is_active = 1 AND status = 'active'
				  AND (expires_at IS NULL OR expires_at > ?)
				ORDER BY id ASC
			`, *meta.providerID, now)
			if err == nil {
				for kRows.Next() {
					var kid int64
					var keySecret string
					if err := kRows.Scan(&kid, &keySecret); err == nil && keySecret != "" {
						ref := fmt.Sprintf("%s-key-%d", meta.name, kid)
						if !seenRefs[ref] {
							seenRefs[ref] = true
							pool = append(pool, config.CredentialKeyDTO{
								Ref:           ref,
								Secret:        keySecret,
								RPS:           meta.credentialRPS,
								MaxConcurrent: meta.credentialMaxConcur,
							})
						}
					}
				}
				kRows.Close()
			}
		}

		// 2b. Check explicit upstream_credentials
		ucRows, err := s.db.QueryContext(ctx, `
			SELECT uc.id, uc.ref, uc.secret, uc.rps, uc.max_concurrent, ak.api_key
			FROM upstream_credentials uc
			LEFT JOIN api_keys ak ON uc.api_key_id = ak.id
			WHERE uc.upstream_id = ? AND uc.is_active = 1 AND uc.status = 'active'
			ORDER BY uc.id ASC
		`, meta.id)
		if err == nil {
			for ucRows.Next() {
				var (
					cid        int64
					ref        string
					secret     sql.NullString
					rps        sql.NullFloat64
					maxConcur  sql.NullInt64
					joinedKey  sql.NullString
					credRPSVal *float64
					credMaxVal *int
				)
				if err := ucRows.Scan(&cid, &ref, &secret, &rps, &maxConcur, &joinedKey); err == nil {
					keySec := secret.String
					if keySec == "" && joinedKey.Valid {
						keySec = joinedKey.String
					}
					// Skip credentials whose secret has not (yet) replicated
					// into the local Turso replica. Emitting a slot with an
					// empty secret would either fail the whole snapshot build
					// (empty secret -> ref reinterpreted as an ENV var name in
					// config.translateUpstream) or produce a KeySlot that gets
					// revoked on the first 401 ("Invalid API Key"), skewing
					// load balancing. Mirrors the keySecret != "" filter in the
					// harvester-provider path (block 2a) above.
					if strings.TrimSpace(keySec) == "" && !strings.HasPrefix(ref, "oauth:") {
						continue
					}
					if rps.Valid {
						v := rps.Float64
						credRPSVal = &v
					} else {
						credRPSVal = meta.credentialRPS
					}
					if maxConcur.Valid {
						v := int(maxConcur.Int64)
						credMaxVal = &v
					} else {
						credMaxVal = meta.credentialMaxConcur
					}
					if ref == "" {
						ref = fmt.Sprintf("%s-cred-%d", meta.name, cid)
					}
					if !seenRefs[ref] {
						seenRefs[ref] = true
						pool = append(pool, config.CredentialKeyDTO{
							Ref:           ref,
							Secret:        keySec,
							RPS:           credRPSVal,
							MaxConcurrent: credMaxVal,
						})
					}
				}
			}
			ucRows.Close()
		}

		if len(pool) > 0 {
			upstreams[i].CredentialPool = pool
		}
	}

	// 3. Load Models
	modRows, err := s.db.QueryContext(ctx, `
		SELECT id, public_name, upstream_id, upstream_model, fallback_upstreams,
		       capabilities, max_context, enabled
		FROM models
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query models: %w", err)
	}
	defer modRows.Close()

	var models []config.ModelDTO
	for modRows.Next() {
		var (
			id                              int64
			publicName, upstreamModel       string
			upstreamID                      int64
			fallbackUpstreamsJSON, capsJSON sql.NullString
			maxContext                      sql.NullInt64
			enabled                         int
		)

		if err := modRows.Scan(&id, &publicName, &upstreamID, &upstreamModel, &fallbackUpstreamsJSON, &capsJSON, &maxContext, &enabled); err != nil {
			return nil, fmt.Errorf("scan model: %w", err)
		}

		upName, ok := upstreamIDToName[upstreamID]
		if !ok {
			upName = fmt.Sprintf("upstream-%d", upstreamID)
		}

		var fallbacks []string
		if fallbackUpstreamsJSON.Valid && fallbackUpstreamsJSON.String != "" {
			_ = json.Unmarshal([]byte(fallbackUpstreamsJSON.String), &fallbacks)
		}

		var caps config.CapabilitiesDTO
		if capsJSON.Valid && capsJSON.String != "" {
			_ = json.Unmarshal([]byte(capsJSON.String), &caps)
		}

		var maxCtx *int
		if maxContext.Valid {
			v := int(maxContext.Int64)
			maxCtx = &v
		}

		isEn := enabled != 0
		models = append(models, config.ModelDTO{
			PublicName:        publicName,
			Upstream:          upName,
			FallbackUpstreams: fallbacks,
			UpstreamModel:     upstreamModel,
			Capabilities:      &caps,
			MaxContext:        maxCtx,
			Enabled:           &isEn,
		})
	}

	// 4. Load Combos
	comRows, err := s.db.QueryContext(ctx, `
		SELECT id, name, strategy, models, enabled
		FROM combos
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query combos: %w", err)
	}
	defer comRows.Close()

	var combos []config.ComboDTO
	for comRows.Next() {
		var (
			id             int64
			name, strategy string
			modelsJSON     string
			enabled        int
		)
		if err := comRows.Scan(&id, &name, &strategy, &modelsJSON, &enabled); err != nil {
			return nil, fmt.Errorf("scan combo: %w", err)
		}

		var comboModels []string
		if modelsJSON != "" {
			_ = json.Unmarshal([]byte(modelsJSON), &comboModels)
		}

		isEn := enabled != 0
		combos = append(combos, config.ComboDTO{
			Name:     name,
			Strategy: strategy,
			Models:   comboModels,
			Enabled:  &isEn,
		})
	}

	// 5. Load Tenants
	tenRows, err := s.db.QueryContext(ctx, `
		SELECT id, name, api_key, key_hash, key_hint, status, max_tokens, used_tokens, expires_at,
		       rps, burst, max_concurrent, allowed_models, metadata
		FROM tenants
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query tenants: %w", err)
	}
	defer tenRows.Close()

	var tenants []config.TenantDTO
	for tenRows.Next() {
		var (
			id                               int64
			name, status                     string
			apiKey, keyHash, keyHint         sql.NullString
			maxTokens, usedTokens, expiresAt sql.NullInt64
			rps                              sql.NullFloat64
			burst, maxConcurrent             sql.NullInt64
			allowedModelsJSON, metadataJSON  sql.NullString
		)

		if err := tenRows.Scan(&id, &name, &apiKey, &keyHash, &keyHint, &status, &maxTokens, &usedTokens, &expiresAt, &rps, &burst, &maxConcurrent, &allowedModelsJSON, &metadataJSON); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}

		var allowedModels []string
		if allowedModelsJSON.Valid && allowedModelsJSON.String != "" {
			_ = json.Unmarshal([]byte(allowedModelsJSON.String), &allowedModels)
		}

		var metadata map[string]string
		if metadataJSON.Valid && metadataJSON.String != "" {
			_ = json.Unmarshal([]byte(metadataJSON.String), &metadata)
		}

		var rl *config.RateLimitDTO
		if rps.Valid || burst.Valid || maxConcurrent.Valid {
			rl = &config.RateLimitDTO{}
			if rps.Valid {
				v := rps.Float64
				rl.RPS = &v
			}
			if burst.Valid {
				v := int(burst.Int64)
				rl.Burst = &v
			}
			if maxConcurrent.Valid {
				v := int(maxConcurrent.Int64)
				rl.MaxConcurrent = &v
			}
		}

		var expPtr *int64
		if expiresAt.Valid && expiresAt.Int64 > 0 {
			v := expiresAt.Int64
			expPtr = &v
		}

		key := apiKey.String
		if key == "" {
			key = keyHash.String
		}

		tenants = append(tenants, config.TenantDTO{
			APIKey:        key,
			KeyHash:       keyHash.String,
			Name:          name,
			Status:        status,
			MaxTokens:     maxTokens.Int64,
			UsedTokens:    usedTokens.Int64,
			ExpiresAt:     expPtr,
			AllowedModels: allowedModels,
			RateLimit:     rl,
			Metadata:      metadata,
		})
	}

	var tokenSaver *config.TokenSaverDTO
	var tsVal string
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM system_settings WHERE key = 'token_saver'").Scan(&tsVal); err == nil && tsVal != "" {
		var ts config.TokenSaverDTO
		if err := json.Unmarshal([]byte(tsVal), &ts); err == nil {
			tokenSaver = &ts
		}
	}

	return &config.SettingsDTO{
		Upstreams:  upstreams,
		Models:     models,
		Combos:     combos,
		Tenants:    tenants,
		TokenSaver: tokenSaver,
	}, nil
}

// pruneOrphanCredentials removes active upstream_credentials rows of upstreamID
// whose ref is absent from keep. Only rows the loader would have surfaced are
// eligible: deactivated rows are invisible to the credential pool by design, so
// the payload is authoritative for active credentials only and must never delete
// a tombstone. Credentials without a usable secret (empty `secret` and no joined
// api_keys row) are preserved too — they are invisible to the loaded pool, so a
// save issued while their secret was still replicating must not delete them.
// Rows are collected before deleting so the result set is fully drained on the
// single-connection pool used by the Turso client.
//
// The `api_keys` table came from the external harvester originally, and a
// replica that predates Firefly's own DDL may still lack it, so the join is
// best-effort: a failure falls back to reading the credentials table alone.
func pruneOrphanCredentials(ctx context.Context, tx *sql.Tx, upstreamID int64, keep map[string]bool) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT uc.id, uc.ref, uc.secret, ak.api_key
		FROM upstream_credentials uc
		LEFT JOIN api_keys ak ON uc.api_key_id = ak.id
		WHERE uc.upstream_id = ? AND uc.is_active = 1 AND uc.status = 'active'
	`, upstreamID)
	if err != nil {
		rows, err = tx.QueryContext(ctx, `
			SELECT id, ref, secret, NULL FROM upstream_credentials
			WHERE upstream_id = ? AND is_active = 1 AND status = 'active'
		`, upstreamID)
		if err != nil {
			return err
		}
	}

	var staleIDs []int64
	for rows.Next() {
		var (
			id        int64
			ref       string
			secret    sql.NullString
			joinedKey sql.NullString
		)
		if err := rows.Scan(&id, &ref, &secret, &joinedKey); err != nil {
			_ = rows.Close()
			return err
		}
		if keep[ref] {
			continue
		}
		if strings.TrimSpace(secret.String) == "" && !joinedKey.Valid {
			continue
		}
		staleIDs = append(staleIDs, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range staleIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM upstream_credentials WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// SaveSettings writes the full SettingsDTO into the Turso database in an atomic
// transaction, updates catalog revisions, and pushes changes to the cloud.
func (s *Store) SaveSettings(ctx context.Context, settings config.SettingsDTO) error {
	s.lock()
	defer s.unlock()

	tx, err := s.beginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_, _ = s.db.ExecContext(cleanupCtx, "ROLLBACK")
			cancel()
		}
	}()

	now := time.Now().UnixMilli()

	// 1. Process upstreams
	upstreamNameToID := make(map[string]int64)
	existingUpstreamIDs := make(map[int64]bool)

	rows, err := tx.QueryContext(ctx, "SELECT id, name FROM upstreams")
	if err != nil {
		return fmt.Errorf("query existing upstreams: %w", err)
	}
	for rows.Next() {
		var id int64
		var name string
		_ = rows.Scan(&id, &name)
		upstreamNameToID[name] = id
		existingUpstreamIDs[id] = true
	}
	rows.Close()

	activeUpstreamIDs := make(map[int64]bool)

	for _, u := range settings.Upstreams {
		fallbacksJSON := "[]"
		if len(u.BaseURLs) > 1 {
			raw, _ := json.Marshal(u.BaseURLs[1:])
			fallbacksJSON = string(raw)
		}
		headersJSON := "{}"
		if len(u.ExtraHeaders) > 0 {
			raw, _ := json.Marshal(u.ExtraHeaders)
			headersJSON = string(raw)
		}

		enabledInt := 1
		if u.Enabled != nil && !*u.Enabled {
			enabledInt = 0
		}
		allowInsecureInt := 0
		if u.AllowInsecure != nil && *u.AllowInsecure {
			allowInsecureInt = 1
		}

		keyErrorThresholdVal := 0
		if u.KeyErrorThreshold != nil {
			keyErrorThresholdVal = *u.KeyErrorThreshold
		}
		keyErrorActionVal := u.KeyErrorAction
		if keyErrorActionVal == "" {
			keyErrorActionVal = "deactivate"
		}
		keyCooldownMsVal := 300000
		if u.KeyCooldownDurationMs != nil && *u.KeyCooldownDurationMs > 0 {
			keyCooldownMsVal = *u.KeyCooldownDurationMs
		}

		egressModeVal := strings.ToLower(strings.TrimSpace(u.EgressMode))
		if egressModeVal == "" {
			egressModeVal = "direct"
		}
		proxyURLVal := strings.TrimSpace(u.ProxyURL)
		warpAutoRotateVal := 0
		if u.WarpAutoRotateOn429 != nil && *u.WarpAutoRotateOn429 {
			warpAutoRotateVal = 1
		}

		existingID, exists := upstreamNameToID[u.Name]
		if exists {
			activeUpstreamIDs[existingID] = true
			_, err = tx.ExecContext(ctx, `
				UPDATE upstreams SET
					protocol = ?, base_url = ?, fallback_base_urls = ?, key_strategy = ?,
					provider_id = ?, credential_ref = ?, timeout_ms = ?, idle_timeout_ms = ?, stream_idle_timeout_ms = ?,
					max_idle_conns_per_host = ?, max_conns_per_host = ?, extra_headers = ?,
					allow_insecure = ?, credential_rps = ?, credential_max_concur = ?,
					key_error_threshold = ?, key_error_action = ?, key_cooldown_duration_ms = ?,
					probe_model = ?,
					egress_mode = ?, proxy_url = ?, warp_auto_rotate_on_429 = ?,
					enabled = ?, version = version + 1, updated_at = ?
				WHERE id = ?
			`, u.Protocol, u.BaseURL, fallbacksJSON, u.KeyStrategy,
				u.ProviderID, u.CredentialRef, u.TimeoutMs, u.IdleTimeoutMs, u.StreamIdleTimeoutMs,
				u.MaxIdleConnsPerHost, u.MaxConnsPerHost, headersJSON,
				allowInsecureInt, u.CredentialRPS, u.CredentialMaxConcurrent,
				keyErrorThresholdVal, keyErrorActionVal, keyCooldownMsVal,
				u.ProbeModel,
				egressModeVal, proxyURLVal, warpAutoRotateVal,
				enabledInt, now, existingID)
			if err != nil {
				return fmt.Errorf("update upstream %q: %w", u.Name, err)
			}
		} else {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO upstreams (
					name, protocol, base_url, fallback_base_urls, key_strategy,
					provider_id, credential_ref, timeout_ms, idle_timeout_ms, stream_idle_timeout_ms,
					max_idle_conns_per_host, max_conns_per_host, extra_headers,
					allow_insecure, credential_rps, credential_max_concur,
					key_error_threshold, key_error_action, key_cooldown_duration_ms,
					probe_model, egress_mode, proxy_url, warp_auto_rotate_on_429,
					enabled, version, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
			`, u.Name, u.Protocol, u.BaseURL, fallbacksJSON, u.KeyStrategy,
				u.ProviderID, u.CredentialRef, u.TimeoutMs, u.IdleTimeoutMs, u.StreamIdleTimeoutMs,
				u.MaxIdleConnsPerHost, u.MaxConnsPerHost, headersJSON,
				allowInsecureInt, u.CredentialRPS, u.CredentialMaxConcurrent,
				keyErrorThresholdVal, keyErrorActionVal, keyCooldownMsVal,
				u.ProbeModel,
				egressModeVal, proxyURLVal, warpAutoRotateVal,
				enabledInt, now, now)
			if err != nil {
				return fmt.Errorf("insert upstream %q: %w", u.Name, err)
			}
			newID, _ := res.LastInsertId()
			upstreamNameToID[u.Name] = newID
			activeUpstreamIDs[newID] = true
			existingID = newID
		}

		// Update upstream_credentials if explicit CredentialPool is provided.
		// The payload is authoritative for this upstream's active credentials:
		// rows it no longer carries are orphans and must be pruned. Older
		// dashboard save flows minted a brand new ref family on every import
		// (positional index or timestamp), so stale rows never collided on
		// UNIQUE(upstream_id, ref) and accumulated without bound —
		// LoadCatalogSnapshot then re-read them on every load. Saves that do not
		// carry a pool (empty slice) never prune.
		if len(u.CredentialPool) > 0 {
			keepRefs := make(map[string]bool, len(u.CredentialPool))
			for _, k := range u.CredentialPool {
				if k.Ref == "" {
					continue
				}
				keepRefs[k.Ref] = true
				_, _ = tx.ExecContext(ctx, `
					INSERT INTO upstream_credentials (
						upstream_id, ref, secret, rps, max_concurrent, status, is_active, created_at, updated_at
					) VALUES (?, ?, ?, ?, ?, 'active', 1, ?, ?)
					ON CONFLICT(upstream_id, ref) DO UPDATE SET
						secret = excluded.secret,
						rps = excluded.rps,
						max_concurrent = excluded.max_concurrent,
						updated_at = excluded.updated_at
				`, existingID, k.Ref, k.Secret, k.RPS, k.MaxConcurrent, now, now)
			}
			if err := pruneOrphanCredentials(ctx, tx, existingID, keepRefs); err != nil {
				return fmt.Errorf("prune credentials for upstream %q: %w", u.Name, err)
			}
		}
	}

	// Delete removed upstreams. This is intentionally conservative: an upstream
	// is only removed when the incoming payload is authoritative for upstreams
	// (non-empty list) AND no surviving model in this same payload still targets
	// it. Deleting an upstream cascades to its models, so a stale or partial
	// payload (e.g. a save issued from the Models page while the store's
	// upstream list was momentarily incomplete) must never be able to wipe
	// models for an upstream that is still in use.
	modelUpstreamsInUse := make(map[string]bool)
	for _, m := range settings.Models {
		modelUpstreamsInUse[m.Upstream] = true
		for _, fu := range m.FallbackUpstreams {
			modelUpstreamsInUse[fu] = true
		}
	}
	if len(settings.Upstreams) > 0 || settings.ManageUpstreams {
		idToName := make(map[int64]string, len(upstreamNameToID))
		for name, id := range upstreamNameToID {
			idToName[id] = name
		}
		for id := range existingUpstreamIDs {
			if activeUpstreamIDs[id] {
				continue
			}
			// Do not delete an upstream (and its models) that surviving models
			// still reference; the payload is likely stale/partial.
			if name, ok := idToName[id]; ok && modelUpstreamsInUse[name] {
				continue
			}
			_, _ = tx.ExecContext(ctx, "DELETE FROM models WHERE upstream_id = ?", id)
			_, _ = tx.ExecContext(ctx, "DELETE FROM upstreams WHERE id = ?", id)
		}
	}

	// 2. Process models
	existingModels := make(map[string]int64)
	mRows, err := tx.QueryContext(ctx, "SELECT id, public_name FROM models")
	if err == nil {
		for mRows.Next() {
			var id int64
			var pubName string
			_ = mRows.Scan(&id, &pubName)
			existingModels[pubName] = id
		}
		mRows.Close()
	}

	activeModels := make(map[string]bool)
	for _, m := range settings.Models {
		upID, ok := upstreamNameToID[m.Upstream]
		if !ok {
			return fmt.Errorf("model %q references non-existent upstream %q", m.PublicName, m.Upstream)
		}
		activeModels[m.PublicName] = true

		fallbacksJSON := "[]"
		if len(m.FallbackUpstreams) > 0 {
			raw, _ := json.Marshal(m.FallbackUpstreams)
			fallbacksJSON = string(raw)
		}
		capsJSON := "{}"
		if m.Capabilities != nil {
			raw, _ := json.Marshal(m.Capabilities)
			capsJSON = string(raw)
		}
		enabledInt := 1
		if m.Enabled != nil && !*m.Enabled {
			enabledInt = 0
		}

		if existingID, ok := existingModels[m.PublicName]; ok {
			_, err = tx.ExecContext(ctx, `
				UPDATE models SET
					upstream_id = ?, upstream_model = ?, fallback_upstreams = ?,
					capabilities = ?, max_context = ?, enabled = ?,
					version = version + 1, updated_at = ?
				WHERE id = ?
			`, upID, m.UpstreamModel, fallbacksJSON, capsJSON, m.MaxContext, enabledInt, now, existingID)
			if err != nil {
				return fmt.Errorf("update model %q: %w", m.PublicName, err)
			}
		} else {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO models (
					public_name, upstream_id, upstream_model, fallback_upstreams,
					capabilities, max_context, enabled, version, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
			`, m.PublicName, upID, m.UpstreamModel, fallbacksJSON, capsJSON, m.MaxContext, enabledInt, now, now)
			if err != nil {
				return fmt.Errorf("insert model %q: %w", m.PublicName, err)
			}
		}
	}

	// Delete models that are genuinely absent from an authoritative payload.
	// An empty Models list is only honored as "delete everything" when the
	// caller explicitly marks the payload authoritative (ManageModels). Without
	// that flag, an empty list is treated as a partial/stale payload and ignored,
	// preventing catastrophic loss from a race between the 5s settings poll and a
	// delete action.
	if len(settings.Models) > 0 || settings.ManageModels {
		for pubName, id := range existingModels {
			if !activeModels[pubName] {
				_, _ = tx.ExecContext(ctx, "DELETE FROM models WHERE id = ?", id)
			}
		}
	}

	// 3. Process Combos
	existingCombos := make(map[string]int64)
	cRows, err := tx.QueryContext(ctx, "SELECT id, name FROM combos")
	if err == nil {
		for cRows.Next() {
			var id int64
			var cname string
			_ = cRows.Scan(&id, &cname)
			existingCombos[cname] = id
		}
		cRows.Close()
	}

	activeCombos := make(map[string]bool)
	for _, c := range settings.Combos {
		activeCombos[c.Name] = true
		modelsJSON := "[]"
		if len(c.Models) > 0 {
			raw, _ := json.Marshal(c.Models)
			modelsJSON = string(raw)
		}
		enabledInt := 1
		if c.Enabled != nil && !*c.Enabled {
			enabledInt = 0
		}
		strategy := c.Strategy
		if strategy == "" {
			strategy = "least_inflight"
		}

		if existingID, ok := existingCombos[c.Name]; ok {
			_, err = tx.ExecContext(ctx, `
				UPDATE combos SET
					strategy = ?, models = ?, enabled = ?,
					version = version + 1, updated_at = ?
				WHERE id = ?
			`, strategy, modelsJSON, enabledInt, now, existingID)
			if err != nil {
				return fmt.Errorf("update combo %q: %w", c.Name, err)
			}
		} else {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO combos (
					name, strategy, models, enabled, version, created_at, updated_at
				) VALUES (?, ?, ?, ?, 1, ?, ?)
			`, c.Name, strategy, modelsJSON, enabledInt, now, now)
			if err != nil {
				return fmt.Errorf("insert combo %q: %w", c.Name, err)
			}
		}
	}

	// Delete combos absent from an authoritative payload. An empty Combos list
	// only deletes everything when ManageCombos is set; otherwise it is treated
	// as a partial/stale payload and ignored (same race protection as models).
	if len(settings.Combos) > 0 || settings.ManageCombos {
		for cname, id := range existingCombos {
			if !activeCombos[cname] {
				_, _ = tx.ExecContext(ctx, "DELETE FROM combos WHERE id = ?", id)
			}
		}
	}

	// 4. Process Tenants
	existingTenants := make(map[string]int64)
	tRows, err := tx.QueryContext(ctx, "SELECT id, COALESCE(api_key, ''), COALESCE(key_hash, '') FROM tenants")
	if err == nil {
		for tRows.Next() {
			var id int64
			var ak, kh string
			_ = tRows.Scan(&id, &ak, &kh)
			if ak != "" {
				existingTenants[ak] = id
			}
			if kh != "" {
				existingTenants[kh] = id
			}
		}
		tRows.Close()
	}

	activeTenants := make(map[string]bool)
	for _, t := range settings.Tenants {
		key := t.APIKey
		if key == "" {
			key = t.KeyHash
		}
		if key == "" {
			continue
		}
		activeTenants[key] = true
		if t.KeyHash != "" {
			activeTenants[t.KeyHash] = true
		}

		allowedJSON := "[]"
		if len(t.AllowedModels) > 0 {
			raw, _ := json.Marshal(t.AllowedModels)
			allowedJSON = string(raw)
		}
		metaJSON := "{}"
		if len(t.Metadata) > 0 {
			raw, _ := json.Marshal(t.Metadata)
			metaJSON = string(raw)
		}

		keyHint := ""
		if len(key) >= 12 {
			keyHint = key[:12]
		}

		var (
			rpsVal       *float64
			burstVal     *int
			maxConcurVal *int
		)
		if t.RateLimit != nil {
			rpsVal = t.RateLimit.RPS
			burstVal = t.RateLimit.Burst
			maxConcurVal = t.RateLimit.MaxConcurrent
		}

		status := t.Status
		if status == "" {
			status = "active"
		}

		var expVal *int64
		if t.ExpiresAt != nil && *t.ExpiresAt > 0 {
			expVal = t.ExpiresAt
		}

		keyHash := t.KeyHash
		if keyHash == "" && strings.HasPrefix(key, "sk-gw-") {
			sum := sha256.Sum256([]byte(key))
			keyHash = "sha256:" + hex.EncodeToString(sum[:])
		}

		existingID, ok := existingTenants[key]
		if !ok && keyHash != "" {
			existingID, ok = existingTenants[keyHash]
		}

		if ok {
			_, err = tx.ExecContext(ctx, `
				UPDATE tenants SET
					name = ?, api_key = ?, key_hash = ?, key_hint = ?, status = ?,
					max_tokens = ?, used_tokens = ?, expires_at = ?,
					rps = ?, burst = ?, max_concurrent = ?,
					allowed_models = ?, metadata = ?,
					version = version + 1, updated_at = ?
				WHERE id = ?
			`, t.Name, key, keyHash, keyHint, status,
				t.MaxTokens, t.UsedTokens, expVal,
				rpsVal, burstVal, maxConcurVal,
				allowedJSON, metaJSON, now, existingID)
			if err != nil {
				return fmt.Errorf("update tenant %q: %w", t.Name, err)
			}
		} else {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO tenants (
					name, api_key, key_hash, key_hint, status,
					max_tokens, used_tokens, expires_at,
					rps, burst, max_concurrent, allowed_models, metadata,
					version, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
			`, t.Name, key, keyHash, keyHint, status,
				t.MaxTokens, t.UsedTokens, expVal,
				rpsVal, burstVal, maxConcurVal,
				allowedJSON, metaJSON, now, now)
			if err != nil {
				return fmt.Errorf("insert tenant %q: %w", t.Name, err)
			}
		}
	}

	// Delete tenants absent from an authoritative payload. An empty Tenants list
	// only deletes everything when ManageTenants is set; otherwise it is treated
	// as a partial/stale payload and ignored.
	if len(settings.Tenants) > 0 || settings.ManageTenants {
		for kh, id := range existingTenants {
			if !activeTenants[kh] {
				_, _ = tx.ExecContext(ctx, "DELETE FROM tenants WHERE id = ?", id)
			}
		}
	}

	// 4b. Upsert TokenSaver configuration if provided
	if settings.TokenSaver != nil {
		if tsRaw, err := json.Marshal(settings.TokenSaver); err == nil {
			_, _ = tx.ExecContext(ctx, `
				INSERT INTO system_settings (key, value, version, updated_at)
				VALUES ('token_saver', ?, 1, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
			`, string(tsRaw), now)
		}
	}

	// 5. Bump revision inside the transaction
	_, err = tx.ExecContext(ctx, `
		UPDATE catalog_revisions
		SET revision = revision + 1, updated_at = ?
		WHERE id = 1
	`, now)
	if err != nil {
		return fmt.Errorf("bump revision in tx: %w", err)
	}

	if err := tx.Commit(); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_, _ = s.db.ExecContext(cleanupCtx, "ROLLBACK")
		cancel()
		return fmt.Errorf("commit settings tx: %w", err)
	}
	committed = true

	// 6. Push local changes up to Turso Cloud primary
	if s.client != nil {
		if err := s.client.PushLocked(ctx); err != nil {
			s.getLogger().Warn("turso push encountered error after save settings", "err", err)
		}
	}

	return nil
}

// -----------------------------------------------------------------------------
// Granular CRUD Operations with Optimistic Concurrency Control (OCC)
// -----------------------------------------------------------------------------

// SaveUpstream inserts or updates an upstream using OCC version verification.
func (s *Store) SaveUpstream(ctx context.Context, u *UpstreamRecord) error {
	s.lock()
	defer s.unlock()

	now := time.Now().UnixMilli()
	fallbacksJSON, _ := json.Marshal(u.FallbackBaseURLs)
	extraHeadersJSON, _ := json.Marshal(u.ExtraHeaders)

	allowInsecureInt := 0
	if u.AllowInsecure {
		allowInsecureInt = 1
	}
	enabledInt := 0
	if u.Enabled {
		enabledInt = 1
	}

	if u.ID <= 0 {
		// INSERT
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO upstreams (
				name, protocol, base_url, fallback_base_urls, key_strategy,
				provider_id, credential_ref, timeout_ms, idle_timeout_ms, stream_idle_timeout_ms,
				max_idle_conns_per_host, max_conns_per_host, extra_headers,
				allow_insecure, credential_rps, credential_max_concur, enabled,
				version, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		`, u.Name, u.Protocol, u.BaseURL, string(fallbacksJSON), u.KeyStrategy,
			u.ProviderID, u.CredentialRef, u.TimeoutMs, u.IdleTimeoutMs, u.StreamIdleTimeoutMs,
			u.MaxIdleConnsPerHost, u.MaxConnsPerHost, string(extraHeadersJSON),
			allowInsecureInt, u.CredentialRPS, u.CredentialMaxConcur, enabledInt,
			now, now)
		if err != nil {
			return fmt.Errorf("insert upstream: %w", err)
		}
		id, _ := res.LastInsertId()
		u.ID = id
		u.Version = 1
		u.CreatedAt = now
		u.UpdatedAt = now
	} else {
		// UPDATE with OCC
		res, err := s.db.ExecContext(ctx, `
			UPDATE upstreams SET
				name = ?, protocol = ?, base_url = ?, fallback_base_urls = ?, key_strategy = ?,
				provider_id = ?, credential_ref = ?, timeout_ms = ?, idle_timeout_ms = ?, stream_idle_timeout_ms = ?,
				max_idle_conns_per_host = ?, max_conns_per_host = ?, extra_headers = ?,
				allow_insecure = ?, credential_rps = ?, credential_max_concur = ?, enabled = ?,
				version = version + 1, updated_at = ?
			WHERE id = ? AND version = ?
		`, u.Name, u.Protocol, u.BaseURL, string(fallbacksJSON), u.KeyStrategy,
			u.ProviderID, u.CredentialRef, u.TimeoutMs, u.IdleTimeoutMs, u.StreamIdleTimeoutMs,
			u.MaxIdleConnsPerHost, u.MaxConnsPerHost, string(extraHeadersJSON),
			allowInsecureInt, u.CredentialRPS, u.CredentialMaxConcur, enabledInt,
			now, u.ID, u.Version)
		if err != nil {
			return fmt.Errorf("update upstream: %w", err)
		}
		rows, _ := res.RowsAffected()
		if rows == 0 {
			var exists int
			_ = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM upstreams WHERE id = ?", u.ID).Scan(&exists)
			if exists == 0 {
				return ErrNotFound
			}
			return ErrConflict
		}
		u.Version++
		u.UpdatedAt = now
	}

	_, _ = s.bumpCatalogRevisionLocked(ctx)
	if s.client != nil {
		_ = s.client.PushLocked(ctx)
	}
	return nil
}

// DeleteUpstream deletes an upstream by ID.
func (s *Store) DeleteUpstream(ctx context.Context, id int64) error {
	s.lock()
	defer s.unlock()

	res, err := s.db.ExecContext(ctx, "DELETE FROM upstreams WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete upstream: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}

	_, _ = s.bumpCatalogRevisionLocked(ctx)
	if s.client != nil {
		_ = s.client.PushLocked(ctx)
	}
	return nil
}

// SaveModel inserts or updates a model with OCC.
func (s *Store) SaveModel(ctx context.Context, m *ModelRecord) error {
	s.lock()
	defer s.unlock()

	now := time.Now().UnixMilli()
	fallbacksJSON, _ := json.Marshal(m.FallbackUpstreams)
	capsJSON, _ := json.Marshal(m.Capabilities)

	enabledInt := 0
	if m.Enabled {
		enabledInt = 1
	}

	if m.ID <= 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO models (
				public_name, upstream_id, upstream_model, fallback_upstreams,
				capabilities, max_context, enabled, version, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		`, m.PublicName, m.UpstreamID, m.UpstreamModel, string(fallbacksJSON),
			string(capsJSON), m.MaxContext, enabledInt, now, now)
		if err != nil {
			return fmt.Errorf("insert model: %w", err)
		}
		id, _ := res.LastInsertId()
		m.ID = id
		m.Version = 1
		m.CreatedAt = now
		m.UpdatedAt = now
	} else {
		res, err := s.db.ExecContext(ctx, `
			UPDATE models SET
				public_name = ?, upstream_id = ?, upstream_model = ?, fallback_upstreams = ?,
				capabilities = ?, max_context = ?, enabled = ?,
				version = version + 1, updated_at = ?
			WHERE id = ? AND version = ?
		`, m.PublicName, m.UpstreamID, m.UpstreamModel, string(fallbacksJSON),
			string(capsJSON), m.MaxContext, enabledInt, now, m.ID, m.Version)
		if err != nil {
			return fmt.Errorf("update model: %w", err)
		}
		rows, _ := res.RowsAffected()
		if rows == 0 {
			var exists int
			_ = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM models WHERE id = ?", m.ID).Scan(&exists)
			if exists == 0 {
				return ErrNotFound
			}
			return ErrConflict
		}
		m.Version++
		m.UpdatedAt = now
	}

	_, _ = s.bumpCatalogRevisionLocked(ctx)
	if s.client != nil {
		_ = s.client.PushLocked(ctx)
	}
	return nil
}

// DeleteModel deletes a model by ID.
func (s *Store) DeleteModel(ctx context.Context, id int64) error {
	s.lock()
	defer s.unlock()

	res, err := s.db.ExecContext(ctx, "DELETE FROM models WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete model: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}

	_, _ = s.bumpCatalogRevisionLocked(ctx)
	if s.client != nil {
		_ = s.client.PushLocked(ctx)
	}
	return nil
}

// SaveCombo inserts or updates a combo with OCC.
func (s *Store) SaveCombo(ctx context.Context, c *ComboRecord) error {
	s.lock()
	defer s.unlock()

	now := time.Now().UnixMilli()
	modelsJSON, _ := json.Marshal(c.Models)

	enabledInt := 0
	if c.Enabled {
		enabledInt = 1
	}

	if c.ID <= 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO combos (
				name, strategy, models, enabled, version, created_at, updated_at
			) VALUES (?, ?, ?, ?, 1, ?, ?)
		`, c.Name, c.Strategy, string(modelsJSON), enabledInt, now, now)
		if err != nil {
			return fmt.Errorf("insert combo: %w", err)
		}
		id, _ := res.LastInsertId()
		c.ID = id
		c.Version = 1
		c.CreatedAt = now
		c.UpdatedAt = now
	} else {
		res, err := s.db.ExecContext(ctx, `
			UPDATE combos SET
				name = ?, strategy = ?, models = ?, enabled = ?,
				version = version + 1, updated_at = ?
			WHERE id = ? AND version = ?
		`, c.Name, c.Strategy, string(modelsJSON), enabledInt, now, c.ID, c.Version)
		if err != nil {
			return fmt.Errorf("update combo: %w", err)
		}
		rows, _ := res.RowsAffected()
		if rows == 0 {
			var exists int
			_ = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM combos WHERE id = ?", c.ID).Scan(&exists)
			if exists == 0 {
				return ErrNotFound
			}
			return ErrConflict
		}
		c.Version++
		c.UpdatedAt = now
	}

	_, _ = s.bumpCatalogRevisionLocked(ctx)
	if s.client != nil {
		_ = s.client.PushLocked(ctx)
	}
	return nil
}

// DeleteCombo deletes a combo by ID.
func (s *Store) DeleteCombo(ctx context.Context, id int64) error {
	s.lock()
	defer s.unlock()

	res, err := s.db.ExecContext(ctx, "DELETE FROM combos WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete combo: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}

	_, _ = s.bumpCatalogRevisionLocked(ctx)
	if s.client != nil {
		_ = s.client.PushLocked(ctx)
	}
	return nil
}

// SaveTenant inserts or updates a tenant with OCC.
func (s *Store) SaveTenant(ctx context.Context, t *TenantRecord) error {
	s.lock()
	defer s.unlock()

	now := time.Now().UnixMilli()
	allowedJSON, _ := json.Marshal(t.AllowedModels)
	metadataJSON, _ := json.Marshal(t.Metadata)

	key := t.APIKey
	if key == "" {
		key = t.KeyHash
	}
	keyHint := t.KeyHint
	if keyHint == "" && len(key) >= 12 {
		keyHint = key[:12]
	}

	if t.ID <= 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO tenants (
				name, api_key, key_hash, key_hint, status,
				max_tokens, used_tokens, expires_at,
				rps, burst, max_concurrent,
				allowed_models, metadata, version, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
		`, t.Name, key, t.KeyHash, keyHint, t.Status,
			t.MaxTokens, t.UsedTokens, t.ExpiresAt,
			t.RPS, t.Burst, t.MaxConcurrent,
			string(allowedJSON), string(metadataJSON), now, now)
		if err != nil {
			return fmt.Errorf("insert tenant: %w", err)
		}
		id, _ := res.LastInsertId()
		t.ID = id
		t.Version = 1
		t.CreatedAt = now
		t.UpdatedAt = now
	} else {
		res, err := s.db.ExecContext(ctx, `
			UPDATE tenants SET
				name = ?, api_key = ?, key_hash = ?, key_hint = ?, status = ?,
				max_tokens = ?, used_tokens = ?, expires_at = ?,
				rps = ?, burst = ?, max_concurrent = ?,
				allowed_models = ?, metadata = ?,
				version = version + 1, updated_at = ?
			WHERE id = ? AND version = ?
		`, t.Name, key, t.KeyHash, keyHint, t.Status,
			t.MaxTokens, t.UsedTokens, t.ExpiresAt,
			t.RPS, t.Burst, t.MaxConcurrent,
			string(allowedJSON), string(metadataJSON), now, t.ID, t.Version)
		if err != nil {
			return fmt.Errorf("update tenant: %w", err)
		}
		rows, _ := res.RowsAffected()
		if rows == 0 {
			var exists int
			_ = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tenants WHERE id = ?", t.ID).Scan(&exists)
			if exists == 0 {
				return ErrNotFound
			}
			return ErrConflict
		}
		t.Version++
		t.UpdatedAt = now
	}

	_, _ = s.bumpCatalogRevisionLocked(ctx)
	if s.client != nil {
		_ = s.client.PushLocked(ctx)
	}
	return nil
}

// DeleteTenant deletes a tenant by ID.
func (s *Store) DeleteTenant(ctx context.Context, id int64) error {
	s.lock()
	defer s.unlock()

	res, err := s.db.ExecContext(ctx, "DELETE FROM tenants WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}

	_, _ = s.bumpCatalogRevisionLocked(ctx)
	if s.client != nil {
		_ = s.client.PushLocked(ctx)
	}
	return nil
}

// DeactivateExpiredKeys marks keys whose expires_at timestamp has passed as expired.
func (s *Store) DeactivateExpiredKeys(ctx context.Context) (int64, error) {
	s.lock()
	defer s.unlock()

	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `
		UPDATE api_keys
		SET is_active = 0, status = 'expired', updated_at = ?
		WHERE is_active = 1 AND expires_at IS NOT NULL AND expires_at <= ?
	`, now, now)
	if err != nil {
		return 0, fmt.Errorf("deactivate expired keys: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return affected, nil
}

// GetKeysState returns the latest updated_at timestamp and active count of api_keys.
func (s *Store) GetKeysState(ctx context.Context) (int64, int64, error) {
	s.rLock()
	defer s.rUnlock()

	var (
		maxUpdated sql.NullInt64
		count      int64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(updated_at), COUNT(*)
		FROM api_keys
		WHERE is_active = 1
	`).Scan(&maxUpdated, &count)
	if err != nil {
		return 0, 0, err
	}
	return maxUpdated.Int64, count, nil
}

// DeactivateKey marks an API key as deactivated (is_active = 0, status = 'deactivated').
func (s *Store) DeactivateKey(ctx context.Context, keyID int64, reason string) error {
	s.lock()
	defer s.unlock()

	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `
		UPDATE api_keys
		SET is_active = 0, status = 'deactivated', updated_at = ?
		WHERE id = ?
	`, now, keyID)
	if err != nil {
		return fmt.Errorf("deactivate key %d: %w", keyID, err)
	}
	// Also mark matching upstream_credentials
	_, _ = s.db.ExecContext(ctx, `
		UPDATE upstream_credentials
		SET is_active = 0, status = 'deactivated', updated_at = ?
		WHERE api_key_id = ?
	`, now, keyID)

	if s.client != nil {
		_ = s.client.PushLocked(ctx)
	}
	return nil
}

// DeleteKey removes an API key and cascading upstream credentials from the database (hard delete).
func (s *Store) DeleteKey(ctx context.Context, keyID int64) error {
	s.lock()
	defer s.unlock()

	_, _ = s.db.ExecContext(ctx, `DELETE FROM upstream_credentials WHERE api_key_id = ?`, keyID)
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, keyID)
	if err != nil {
		return fmt.Errorf("delete key %d: %w", keyID, err)
	}

	if s.client != nil {
		_ = s.client.PushLocked(ctx)
	}
	return nil
}
