// Package auth resolves a presented gateway API key into a tenant policy using
// the active catalog snapshot. The plaintext key is never stored; only its
// SHA-256 hash is looked up.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

// SnapshotProvider returns the current catalog snapshot (may be nil before the
// first successful load).
type SnapshotProvider interface {
	Current() *domain.CatalogSnapshot
}

// Store resolves gateway keys against snapshots. It satisfies ports.TenantStore.
type Store struct {
	snapshots   SnapshotProvider
	authManager *Manager
}

// NewStore builds a Store backed by the given snapshot provider.
func NewStore(s SnapshotProvider) *Store {
	return &Store{snapshots: s}
}

// SetAuthManager attaches an AuthManager to allow authenticated admin dashboard
// sessions to be recognized by TenantStore.
func (s *Store) SetAuthManager(m *Manager) {
	if s != nil {
		s.authManager = m
	}
}

// KeyPrefix is the expected prefix for gateway keys.
const KeyPrefix = "sk-gw-"

// HashKey returns the canonical `sha256:<hex>` hash for a plaintext key.
func HashKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ExtractBearer pulls the token from an Authorization header value. It accepts
// "Bearer <token>" (case-insensitive scheme). Returns ("", false) if absent or
// malformed.
func ExtractBearer(header string) (string, bool) {
	const scheme = "bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	tok := strings.TrimSpace(header[len(scheme):])
	if tok == "" {
		return "", false
	}
	return tok, true
}

// Lookup resolves a plaintext key to an active tenant. Suspended or unknown
// tenants, and malformed keys, all return (nil, false) so callers cannot
// distinguish "no such key" from "suspended" by timing/response of this layer.
//
// A nil store or a nil snapshot provider fails closed (nil, false) rather than
// panicking: a mis-wired provider must not take down the auth path.
func (s *Store) Lookup(ctx context.Context, plaintextKey string) (*domain.Tenant, bool) {
	if s == nil {
		return nil, false
	}
	// 1. Allow authenticated admin dashboard sessions (e.g. Playground)
	if s.authManager != nil && s.authManager.ValidateToken(ctx, plaintextKey) {
		return s.authManager.AdminTenant(ctx), true
	}

	if isNilProvider(s.snapshots) {
		return nil, false
	}
	if !strings.HasPrefix(plaintextKey, KeyPrefix) {
		return nil, false
	}
	snap := s.snapshots.Current()
	if snap == nil {
		return nil, false
	}
	// Direct O(1) plaintext API key lookup (zero crypto allocations on hot path)
	t, ok := snap.TenantByKey(plaintextKey)
	if !ok {
		t, ok = snap.TenantByHash(HashKey(plaintextKey))
	}
	if !ok || t.Status == domain.TenantStatusSuspended {
		return nil, false
	}
	return t, true
}

// isNilProvider reports whether p is nil, including a typed-nil pointer held in
// the interface. Kept local to auth so the package does not depend on httpx.
func isNilProvider(p SnapshotProvider) bool {
	if p == nil {
		return true
	}
	rv := reflect.ValueOf(p)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}

var _ ports.TenantStore = (*Store)(nil)
