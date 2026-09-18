package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// typedNilStore is a *typedNilStore held in the ports.TenantStore interface.
type typedNilStore struct{}

func (typedNilStore) Lookup(_ context.Context, _ string) (*domain.Tenant, bool) { return nil, false }

// TestIsNil covers the boundary helper that distinguishes a plain nil from a
// typed-nil pointer inside an interface — the exact case a naive `== nil`
// misses and that later panics on first dereference.
func TestIsNil(t *testing.T) {
	var p *typedNilStore
	var i any = p // non-nil interface holding a nil pointer

	if !IsNil(nil) {
		t.Fatal("IsNil(nil) = false, want true")
	}
	if !IsNil(i) {
		t.Fatal("IsNil(typed-nil-in-interface) = false, want true")
	}
	if IsNil(typedNilStore{}) {
		t.Fatal("IsNil(concrete value) = true, want false")
	}
	if IsNil(&typedNilStore{}) {
		t.Fatal("IsNil(non-nil pointer) = true, want false")
	}
	// A nil map stored in an interface is also nil-like.
	var m map[string]int
	if !IsNil(m) {
		t.Fatal("IsNil(nil map) = false, want true")
	}
	if IsNil(map[string]int{}) {
		t.Fatal("IsNil(empty map) = true, want false")
	}
}

// TestAuthMiddlewareTypedNilStoreFailsClosed proves a typed-nil TenantStore
// yields a clean 401 instead of a nil-pointer panic mid-request.
func TestAuthMiddlewareTypedNilStoreFailsClosed(t *testing.T) {
	var store *typedNilStore // typed nil
	h := AuthMiddleware(store)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be reached with a nil store")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-gw-anything")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestAdmissionMiddlewareNilLimiterIsUnlimited proves a nil *limits.Limiter is
// tolerated (unlimited) rather than panicking on a nil receiver.
func TestAdmissionMiddlewareNilLimiterIsUnlimited(t *testing.T) {
	reached := false
	h := AdmissionMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req = req.WithContext(WithTenant(req.Context(), &domain.Tenant{Name: "alpha"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !reached {
		t.Fatal("handler not reached with nil limiter")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
