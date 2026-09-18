package oauth

import (
	"context"
	"os"
	"path/filepath"
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

func TestStore_FilePathAndNestedSupport(t *testing.T) {
	ctx := context.Background()

	// 1. Direct file path test
	dir := t.TempDir()
	filePath := filepath.Join(dir, "oauth.json")
	storeFile, err := NewStore(filePath)
	if err != nil {
		t.Fatalf("NewStore(filePath) error: %v", err)
	}

	conn := &domain.OAuthConnection{
		ID:       "c-1",
		Provider: "cline",
		Email:    "test@example.com",
	}
	if err := storeFile.Save(ctx, conn); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Verify file was written directly to filePath (not nested in dir/oauth.json/oauth.json)
	fi, err := os.Stat(filePath)
	if err != nil || fi.IsDir() {
		t.Fatalf("expected regular file at %s, got err=%v, isDir=%v", filePath, err, fi != nil && fi.IsDir())
	}

	// 2. Nested directory backward-compatibility test
	nestedDir := t.TempDir()
	nestedOauthDir := filepath.Join(nestedDir, "oauth.json")
	if err := os.MkdirAll(nestedOauthDir, 0o755); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}

	// Case 2a: Passing nested directory directly
	storeNestedDir, err := NewStore(nestedOauthDir)
	if err != nil {
		t.Fatalf("NewStore(nestedOauthDir) error: %v", err)
	}
	if err := storeNestedDir.Save(ctx, conn); err != nil {
		t.Fatalf("Save in nested dir error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nestedOauthDir, "oauth.json")); err != nil {
		t.Fatalf("expected file in nested directory: %v", err)
	}

	// Case 2b: Passing parent dir where oauth.json is a directory
	storeParent, err := NewStore(nestedDir)
	if err != nil {
		t.Fatalf("NewStore(nestedDir) error: %v", err)
	}
	got, err := storeParent.Get(ctx, "c-1")
	if err != nil || got == nil {
		t.Fatalf("expected to read from nested directory via parent, got: %v", err)
	}
}
