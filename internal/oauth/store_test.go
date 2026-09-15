package oauth

import (
	"context"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

func TestStore_SaveAndGet(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}

	ctx := context.Background()
	conn := &domain.OAuthConnection{
		ID:       "ag-1",
		Provider: "antigravity",
		Email:    "dev@example.com",
		Token: domain.OAuthToken{
			AccessToken:  "access-123",
			RefreshToken: "refresh-123",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
		},
		ProviderSpecificData: map[string]string{
			"project_id": "test-project",
		},
	}

	if err := store.Save(ctx, conn); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	got, err := store.Get(ctx, "ag-1")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.ID != "ag-1" || got.Email != "dev@example.com" || got.Token.AccessToken != "access-123" {
		t.Fatalf("unexpected connection retrieved: %+v", got)
	}
	if got.ProviderSpecificData["project_id"] != "test-project" {
		t.Fatalf("missing provider specific data: %+v", got)
	}

	// Verify persistence across new store instance
	store2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reload store error: %v", err)
	}
	got2, err := store2.Get(ctx, "ag-1")
	if err != nil {
		t.Fatalf("reloaded store Get error: %v", err)
	}
	if got2.Token.AccessToken != "access-123" {
		t.Fatalf("reloaded store mismatch: %+v", got2)
	}

	// List
	list, err := store2.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List error or count mismatch: len=%d, err=%v", len(list), err)
	}

	// Delete
	if err := store2.Delete(ctx, "ag-1"); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if _, err := store2.Get(ctx, "ag-1"); err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}
