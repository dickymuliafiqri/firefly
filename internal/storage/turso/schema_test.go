package turso

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "turso.tech/database/tursogo"
)

func openSchemaTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("turso", filepath.Join(t.TempDir(), "schema.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func objectExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sqlite_master WHERE name = ?", name).Scan(&count); err != nil {
		t.Fatalf("query sqlite_master for %s: %v", name, err)
	}
	return count > 0
}

func columnNames(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate table_info(%s): %v", table, err)
	}
	return names
}

func TestMigrateSchema_AdoptsHarvesterPoolTables(t *testing.T) {
	ctx := context.Background()
	db := openSchemaTestDB(t)

	if err := MigrateSchema(ctx, db); err != nil {
		t.Fatalf("migrate fresh db: %v", err)
	}

	for _, tbl := range []string{"providers", "api_keys"} {
		if !objectExists(t, db, tbl) {
			t.Fatalf("table %s missing on fresh deploy", tbl)
		}
	}
	if !objectExists(t, db, "idx_provider_active_keys") {
		t.Fatal("index idx_provider_active_keys missing on fresh deploy")
	}

	// Index column order must match the live database.
	rows, err := db.QueryContext(ctx, "PRAGMA index_info(idx_provider_active_keys)")
	if err != nil {
		t.Fatalf("pragma index_info: %v", err)
	}
	var idxCols []string
	for rows.Next() {
		var seqno, cid int
		var name string
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			rows.Close()
			t.Fatalf("scan index_info: %v", err)
		}
		idxCols = append(idxCols, name)
	}
	rows.Close()
	wantIdx := []string{"provider_id", "is_active", "status", "last_used_at"}
	if len(idxCols) != len(wantIdx) {
		t.Fatalf("index columns = %v, want %v", idxCols, wantIdx)
	}
	for i := range wantIdx {
		if idxCols[i] != wantIdx[i] {
			t.Fatalf("index columns = %v, want %v", idxCols, wantIdx)
		}
	}

	// Columns adopted verbatim from the live schema.
	wantProviders := []string{"id", "name", "base_url", "description", "is_active", "created_at", "updated_at"}
	if got := columnNames(t, db, "providers"); !equalStrings(got, wantProviders) {
		t.Fatalf("providers columns = %v, want %v", got, wantProviders)
	}
	wantKeys := []string{
		"id", "provider_id", "api_key", "status", "is_active", "expires_at",
		"last_used_at", "total_requests", "account_metadata", "created_at", "updated_at",
	}
	if got := columnNames(t, db, "api_keys"); !equalStrings(got, wantKeys) {
		t.Fatalf("api_keys columns = %v, want %v", got, wantKeys)
	}

	// Foreign keys: api_keys -> providers (CASCADE) and upstreams -> providers.
	assertForeignKey(t, db, "api_keys", "providers", "provider_id", "id", "CASCADE")
	assertForeignKey(t, db, "upstreams", "providers", "provider_id", "id", "SET NULL")
	assertForeignKey(t, db, "upstream_credentials", "api_keys", "api_key_id", "id", "CASCADE")

	// Behavioral checks on the constraints the harvester relies on.
	res, err := db.ExecContext(ctx, `
		INSERT INTO providers (name, base_url, is_active, created_at, updated_at)
		VALUES ('bai', 'https://api.bai.org', 1, 1, 1)
	`)
	if err != nil {
		t.Fatalf("insert provider without explicit id: %v", err)
	}
	providerID, err := res.LastInsertId()
	if err != nil || providerID <= 0 {
		t.Fatalf("provider id not auto-assigned: id=%d err=%v", providerID, err)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO api_keys (provider_id, api_key, status, is_active, last_used_at, total_requests, created_at, updated_at)
		VALUES (?, 'sk-key-a', 'active', 1, 0, 0, 1, 1)
	`, providerID); err != nil {
		t.Fatalf("insert key without explicit id: %v", err)
	}

	// UNIQUE(api_key) is global in the live schema, not scoped per provider.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO api_keys (provider_id, api_key, status, is_active, last_used_at, total_requests, created_at, updated_at)
		VALUES (?, 'sk-key-a', 'active', 1, 0, 0, 1, 1)
	`, providerID); err == nil {
		t.Fatal("duplicate api_key accepted, want UNIQUE(api_key) violation")
	}

	if _, err := db.ExecContext(ctx,
		"INSERT INTO providers (name, base_url, is_active, created_at, updated_at) VALUES ('bai', 'https://dup', 1, 1, 1)"); err == nil {
		t.Fatal("duplicate provider name accepted, want UNIQUE(name) violation")
	}
}

func TestMigrateSchema_IdempotentAndPreservesRows(t *testing.T) {
	ctx := context.Background()
	db := openSchemaTestDB(t)

	if err := MigrateSchema(ctx, db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	before := countSchemaObjects(t, db)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO providers (id, name, base_url, is_active, created_at, updated_at)
		VALUES (7, 'grok', 'https://api.grok.com', 1, 1, 1)
	`); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO api_keys (id, provider_id, api_key, status, is_active, last_used_at, total_requests, created_at, updated_at)
		VALUES (99, 7, 'sk-stable', 'active', 1, 0, 0, 1, 1)
	`); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	if err := MigrateSchema(ctx, db); err != nil {
		t.Fatalf("second migrate over existing tables: %v", err)
	}

	if after := countSchemaObjects(t, db); after != before {
		t.Fatalf("schema objects changed across re-run: before=%d after=%d", before, after)
	}

	var name string
	var keyID int64
	if err := db.QueryRowContext(ctx, "SELECT name FROM providers WHERE id = 7").Scan(&name); err != nil {
		t.Fatalf("provider row lost after re-run: %v", err)
	}
	if name != "grok" {
		t.Fatalf("provider name = %q, want grok", name)
	}
	if err := db.QueryRowContext(ctx, "SELECT id FROM api_keys WHERE api_key = 'sk-stable'").Scan(&keyID); err != nil {
		t.Fatalf("key row lost after re-run: %v", err)
	}
	if keyID != 99 {
		t.Fatalf("key id = %d, want 99 (adoption must never rewrite ids)", keyID)
	}
}

func TestMigrateSchema_LeavesForeignHarvesterTablesUntouched(t *testing.T) {
	ctx := context.Background()
	db := openSchemaTestDB(t)

	// Simulate tables created by the harvester with an older/leaner shape.
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE providers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name VARCHAR(64) NOT NULL,
			base_url VARCHAR(255) NOT NULL,
			is_active INTEGER NOT NULL DEFAULT 1,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		);
		CREATE TABLE api_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_id INTEGER NOT NULL,
			api_key TEXT NOT NULL,
			status VARCHAR(32) NOT NULL DEFAULT 'active',
			is_active INTEGER NOT NULL DEFAULT 1,
			last_used_at BIGINT NOT NULL DEFAULT 0,
			total_requests BIGINT NOT NULL DEFAULT 0,
			created_at BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		);
		INSERT INTO providers (name, base_url, created_at, updated_at) VALUES ('legacy', 'https://legacy', 1, 1);
		INSERT INTO api_keys (provider_id, api_key, created_at, updated_at) VALUES (1, 'sk-legacy', 1, 1);
	`); err != nil {
		t.Fatalf("create legacy tables: %v", err)
	}

	if err := MigrateSchema(ctx, db); err != nil {
		t.Fatalf("migrate over pre-existing harvester tables: %v", err)
	}

	// CREATE TABLE IF NOT EXISTS must be a no-op: no ALTER, no backfill.
	got := columnNames(t, db, "providers")
	want := []string{"id", "name", "base_url", "is_active", "created_at", "updated_at"}
	if !equalStrings(got, want) {
		t.Fatalf("pre-existing providers table was modified: columns = %v, want %v", got, want)
	}

	// The index is the one additive object: an index-less legacy table converges
	// to the live shape, without touching any row.
	if !objectExists(t, db, "idx_provider_active_keys") {
		t.Fatal("index idx_provider_active_keys not adopted over pre-existing api_keys table")
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys").Scan(&count); err != nil {
		t.Fatalf("count legacy keys: %v", err)
	}
	if count != 1 {
		t.Fatalf("legacy api_keys rows = %d, want 1", count)
	}
}

func assertForeignKey(t *testing.T, db *sql.DB, table, wantTable, wantFrom, wantTo, wantOnDelete string) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "PRAGMA foreign_key_list("+table+")")
	if err != nil {
		t.Fatalf("pragma foreign_key_list(%s): %v", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, seq int
		var refTable, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &refTable, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan foreign_key_list(%s): %v", table, err)
		}
		if refTable == wantTable && from == wantFrom && to == wantTo && onDelete == wantOnDelete {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign_key_list(%s): %v", table, err)
	}
	t.Fatalf("%s has no FK %s(%s) -> %s(%s) ON DELETE %s", table, wantFrom, table, wantTable, wantTo, wantOnDelete)
}

func countSchemaObjects(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
		t.Fatalf("count schema objects: %v", err)
	}
	return count
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
