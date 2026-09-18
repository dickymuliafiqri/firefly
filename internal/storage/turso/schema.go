package turso

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const schemaDDL = `
CREATE TABLE IF NOT EXISTS upstreams (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    name                    VARCHAR(64) NOT NULL UNIQUE,
    protocol                VARCHAR(32) NOT NULL DEFAULT 'openai',
    base_url                VARCHAR(255) NOT NULL,
    fallback_base_urls      TEXT,
    key_strategy            VARCHAR(32) NOT NULL DEFAULT 'round_robin',
    provider_id             INTEGER REFERENCES providers(id) ON DELETE SET NULL,
    credential_ref          VARCHAR(128),
    timeout_ms              INTEGER DEFAULT 30000,
    idle_timeout_ms         INTEGER DEFAULT 90000,
    stream_idle_timeout_ms  INTEGER DEFAULT 120000,
    max_idle_conns_per_host INTEGER DEFAULT 1000,
    max_conns_per_host      INTEGER DEFAULT 1500,
    extra_headers           TEXT,
    allow_insecure          INTEGER NOT NULL DEFAULT 0,
    credential_rps          REAL,
    credential_max_concur   INTEGER,
    key_error_threshold     INTEGER DEFAULT 0,
    key_error_action        VARCHAR(32) DEFAULT 'deactivate',
    key_cooldown_duration_ms INTEGER DEFAULT 300000,
    probe_model             VARCHAR(128),
    egress_mode             VARCHAR(32) DEFAULT 'direct',
    proxy_url               VARCHAR(255),
    warp_auto_rotate_on_429 INTEGER DEFAULT 0,
    enabled                 INTEGER NOT NULL DEFAULT 1,
    version                 INTEGER NOT NULL DEFAULT 1,
    created_at              BIGINT NOT NULL,
    updated_at              BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_upstreams_name ON upstreams(name);
CREATE INDEX IF NOT EXISTS idx_upstreams_enabled ON upstreams(enabled);

CREATE TABLE IF NOT EXISTS upstream_credentials (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    upstream_id    INTEGER NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
    api_key_id     INTEGER REFERENCES api_keys(id) ON DELETE CASCADE,
    ref            VARCHAR(128) NOT NULL,
    secret         TEXT,
    rps            REAL,
    max_concurrent INTEGER,
    status         VARCHAR(20) NOT NULL DEFAULT 'active',
    is_active      INTEGER NOT NULL DEFAULT 1,
    created_at     BIGINT NOT NULL,
    updated_at     BIGINT NOT NULL,
    UNIQUE(upstream_id, ref)
);

CREATE INDEX IF NOT EXISTS idx_upstream_cred_lookup ON upstream_credentials(upstream_id, is_active, status);

CREATE TABLE IF NOT EXISTS models (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    public_name        VARCHAR(128) NOT NULL UNIQUE,
    upstream_id        INTEGER NOT NULL REFERENCES upstreams(id) ON DELETE RESTRICT,
    upstream_model     VARCHAR(128) NOT NULL,
    fallback_upstreams TEXT,
    capabilities       TEXT NOT NULL,
    max_context        INTEGER DEFAULT 128000,
    enabled            INTEGER NOT NULL DEFAULT 1,
    version            INTEGER NOT NULL DEFAULT 1,
    created_at         BIGINT NOT NULL,
    updated_at         BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_models_public_name ON models(public_name);
CREATE INDEX IF NOT EXISTS idx_models_upstream_id ON models(upstream_id);

CREATE TABLE IF NOT EXISTS combos (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       VARCHAR(128) NOT NULL UNIQUE,
    strategy   VARCHAR(32) NOT NULL DEFAULT 'least_inflight',
    models     TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    version    INTEGER NOT NULL DEFAULT 1,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_combos_name ON combos(name);

CREATE TABLE IF NOT EXISTS tenants (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           VARCHAR(128) NOT NULL,
    api_key        VARCHAR(128),
    key_hash       VARCHAR(128),
    key_hint       VARCHAR(32),
    status         VARCHAR(20) NOT NULL DEFAULT 'active',
    max_tokens     BIGINT NOT NULL DEFAULT 0,
    used_tokens    BIGINT NOT NULL DEFAULT 0,
    expires_at     BIGINT,
    rps            REAL,
    burst          INTEGER,
    max_concurrent INTEGER,
    allowed_models TEXT,
    metadata       TEXT,
    version        INTEGER NOT NULL DEFAULT 1,
    created_at     BIGINT NOT NULL,
    updated_at     BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tenants_api_key ON tenants(api_key);
CREATE INDEX IF NOT EXISTS idx_tenants_key_hash ON tenants(key_hash);

CREATE TABLE IF NOT EXISTS oauth_connections (
    id                     VARCHAR(128) PRIMARY KEY,
    provider               VARCHAR(32) NOT NULL,
    email                  VARCHAR(255),
    access_token           TEXT NOT NULL,
    refresh_token          TEXT,
    expires_at             BIGINT,
    provider_specific_data TEXT,
    version                INTEGER NOT NULL DEFAULT 1,
    created_at             BIGINT NOT NULL,
    updated_at             BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS system_settings (
    key        VARCHAR(64) PRIMARY KEY,
    value      TEXT NOT NULL,
    version    INTEGER NOT NULL DEFAULT 1,
    updated_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS catalog_revisions (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    revision   BIGINT NOT NULL DEFAULT 1,
    updated_at BIGINT NOT NULL
);
`

// MigrateSchema executes the DDL definitions and ensures the initial revision row exists.
func MigrateSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, schemaDDL); err != nil {
		return fmt.Errorf("execute schema DDL: %w", err)
	}

	// Upgrade existing schema if columns are not present
	_, _ = db.ExecContext(ctx, "ALTER TABLE upstreams ADD COLUMN key_error_threshold INTEGER DEFAULT 0")
	_, _ = db.ExecContext(ctx, "ALTER TABLE upstreams ADD COLUMN key_error_action VARCHAR(32) DEFAULT 'deactivate'")
	_, _ = db.ExecContext(ctx, "ALTER TABLE upstreams ADD COLUMN key_cooldown_duration_ms INTEGER DEFAULT 300000")
	_, _ = db.ExecContext(ctx, "ALTER TABLE upstreams ADD COLUMN probe_model VARCHAR(128)")
	_, _ = db.ExecContext(ctx, "ALTER TABLE upstreams ADD COLUMN egress_mode VARCHAR(32) DEFAULT 'direct'")
	_, _ = db.ExecContext(ctx, "ALTER TABLE upstreams ADD COLUMN proxy_url VARCHAR(255)")
	_, _ = db.ExecContext(ctx, "ALTER TABLE upstreams ADD COLUMN warp_auto_rotate_on_429 INTEGER DEFAULT 0")

	// Upgrade tenants table if columns are not present
	_, _ = db.ExecContext(ctx, "ALTER TABLE tenants ADD COLUMN api_key VARCHAR(128)")
	_, _ = db.ExecContext(ctx, "ALTER TABLE tenants ADD COLUMN max_tokens BIGINT DEFAULT 0")
	_, _ = db.ExecContext(ctx, "ALTER TABLE tenants ADD COLUMN used_tokens BIGINT DEFAULT 0")
	_, _ = db.ExecContext(ctx, "ALTER TABLE tenants ADD COLUMN expires_at BIGINT")
	_, _ = db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_tenants_api_key ON tenants(api_key)")

	now := time.Now().UnixMilli()
	_, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO catalog_revisions (id, revision, updated_at)
		VALUES (1, 1, ?)
	`, now)
	if err != nil {
		return fmt.Errorf("init catalog_revisions: %w", err)
	}

	return nil
}
