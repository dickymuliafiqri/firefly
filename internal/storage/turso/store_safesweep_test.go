package turso

import (
	"context"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/config"
)

// The existing-rows pre-reads inside SaveSettings feed the payload sweeps that
// decide what to DELETE, so they must fail closed. The earlier code discarded
// the QueryContext error with `if err == nil` (models, combos, tenants) and
// never checked rows.Err(), so a failed read produced an empty map and the save
// still reported success — leaving orphans behind and, on the upstream path,
// defeating the "still referenced by a surviving model" guard.

func TestLoadExistingIDs_ReadsEveryKey(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	isEn := true
	if err := store.SaveSettings(ctx, config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{
				Name: "atria", Protocol: "openai", BaseURL: "https://api.atria.example",
				KeyStrategy: "round_robin", Enabled: &isEn,
				CredentialPool: []config.CredentialKeyDTO{{Ref: "atria-key-1", Secret: "sk-atria-secret-123456"}},
			},
			{
				Name: "borea", Protocol: "openai", BaseURL: "https://api.borea.example",
				KeyStrategy: "round_robin", Enabled: &isEn,
				CredentialPool: []config.CredentialKeyDTO{{Ref: "borea-key-1", Secret: "sk-borea-secret-123456"}},
			},
		},
		Models: []config.ModelDTO{},
	}); err != nil {
		t.Fatalf("seed upstreams: %v", err)
	}

	tx, err := store.beginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	got, err := loadExistingIDs(ctx, tx, "SELECT id, name FROM upstreams",
		func(id *int64, scan rowScanner) ([]string, error) {
			var name string
			if err := scan.Scan(id, &name); err != nil {
				return nil, err
			}
			return []string{name}, nil
		})
	if err != nil {
		t.Fatalf("loadExistingIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d (%v)", len(got), got)
	}
	for _, name := range []string{"atria", "borea"} {
		if _, ok := got[name]; !ok {
			t.Errorf("missing key %q in %v", name, got)
		}
	}
}

func TestLoadExistingIDs_FailsInsteadOfReturningEmpty(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	tx, err := store.beginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	got, err := loadExistingIDs(ctx, tx, "SELECT id, name FROM no_such_table",
		func(id *int64, scan rowScanner) ([]string, error) {
			return nil, nil
		})
	if err == nil {
		t.Fatal("an unreadable lookup must be an error, not an empty map")
	}
	if got != nil {
		t.Fatalf("map must be nil on error, got %v", got)
	}
}

// SaveSettings must abort — and roll back — when a pre-read fails, instead of
// carrying on with an empty map under an authoritative payload.
func TestSaveSettings_FailsClosedWhenPreReadFails(t *testing.T) {
	ctx := context.Background()
	store, db := setupTestDB(t)

	isEn := true
	baseline := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{{
			Name:        "atria",
			Protocol:    "openai",
			BaseURL:     "https://api.atria.example",
			KeyStrategy: "round_robin",
			Enabled:     &isEn,
			CredentialPool: []config.CredentialKeyDTO{
				{Ref: "atria-key-1", Secret: "sk-atria-secret-123456"},
			},
		}},
		Models: []config.ModelDTO{},
	}
	if err := store.SaveSettings(ctx, baseline); err != nil {
		t.Fatalf("baseline save: %v", err)
	}

	// Break the combos lookup. Section 1 (upstreams) still runs and, under an
	// authoritative payload, sweeps the upstream away; section 3 is where the
	// pre-read now fails. The swallowed error used to let the transaction commit
	// with the upstream already deleted.
	if _, err := db.ExecContext(ctx, "DROP TABLE combos"); err != nil {
		t.Fatalf("drop combos: %v", err)
	}

	err := store.SaveSettings(ctx, config.SettingsDTO{
		Upstreams:       []config.UpstreamDTO{},
		Models:          []config.ModelDTO{},
		Combos:          []config.ComboDTO{},
		ManageUpstreams: true,
	})
	if err == nil {
		t.Fatal("SaveSettings must fail closed when an existing-rows pre-read fails")
	}

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM upstreams").Scan(&count); err != nil {
		t.Fatalf("count upstreams: %v", err)
	}
	if count != 1 {
		t.Fatalf("failed save must roll back, got %d upstream rows, want 1", count)
	}
}
