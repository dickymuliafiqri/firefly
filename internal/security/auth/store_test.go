package auth

import (
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// fakeProvider returns a fixed snapshot.
type fakeProvider struct{ s *domain.CatalogSnapshot }

func (f fakeProvider) Current() *domain.CatalogSnapshot { return f.s }

func snapshotWith(hash, name string, status domain.TenantStatus) *domain.CatalogSnapshot {
	return domain.NewCatalogSnapshot(
		1, nil, nil, nil, nil,
		map[string]*domain.Tenant{hash: {KeyHash: hash, Name: name, Status: status}},
		[]string{hash},
	)
}

func TestHashKeyStable(t *testing.T) {
	// Known SHA-256 of "sk-gw-demo-000000000000000000000000".
	got := HashKey("sk-gw-demo-000000000000000000000000")
	want := "sha256:ef084336ee257cf5b084310d376f1cb5e3e7cf20742438623d445a15a6b5b2b9"
	if got != want {
		t.Fatalf("HashKey = %s, want %s", got, want)
	}
}

func TestExtractBearer(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Bearer sk-gw-abc", "sk-gw-abc", true},
		{"bearer sk-gw-abc", "sk-gw-abc", true},
		{"BEARER  sk-gw-abc ", "sk-gw-abc", true},
		{"Basic sk-gw-abc", "", false},
		{"", "", false},
		{"Bearer ", "", false},
		{"Bearer", "", false},
	}
	for _, c := range cases {
		got, ok := ExtractBearer(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("ExtractBearer(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestLookupActive(t *testing.T) {
	key := "sk-gw-abc"
	snap := snapshotWith(HashKey(key), "alpha", domain.TenantStatusActive)
	st := NewStore(fakeProvider{snap})

	got, ok := st.Lookup(nil, key)
	if !ok || got.Name != "alpha" {
		t.Fatalf("Lookup = (%v,%v), want alpha", got, ok)
	}
}

func TestLookupRejectsSuspendedAndUnknown(t *testing.T) {
	key := "sk-gw-abc"
	snap := snapshotWith(HashKey(key), "alpha", domain.TenantStatusSuspended)
	st := NewStore(fakeProvider{snap})

	if _, ok := st.Lookup(nil, key); ok {
		t.Fatal("suspended tenant must not authenticate")
	}
	if _, ok := st.Lookup(nil, "sk-gw-other"); ok {
		t.Fatal("unknown key must not authenticate")
	}
}

func TestLookupRejectsBadPrefixAndNilSnapshot(t *testing.T) {
	st := NewStore(fakeProvider{nil})
	if _, ok := st.Lookup(nil, "sk-gw-anything"); ok {
		t.Fatal("nil snapshot must not authenticate")
	}
	if _, ok := st.Lookup(nil, "not-a-gateway-key"); ok {
		t.Fatal("wrong prefix must not authenticate")
	}
}

func TestLookupNeverReturnsPlaintext(t *testing.T) {
	key := "sk-gw-secret"
	snap := snapshotWith(HashKey(key), "alpha", domain.TenantStatusActive)
	st := NewStore(fakeProvider{snap})
	got, _ := st.Lookup(nil, key)
	if strings.Contains(got.KeyHash, key) {
		t.Fatal("tenant must not carry the plaintext key")
	}
}

// typedNilProvider is a *fakeProviderP held in the SnapshotProvider interface.
type fakeProviderP struct{ s *domain.CatalogSnapshot }

func (f *fakeProviderP) Current() *domain.CatalogSnapshot { return f.s }

// TestLookupTypedNilProviderFailsClosed proves the typed-nil-interface trap is
// handled: an interface holding (*fakeProviderP)(nil) is NOT == nil, so a naive
// guard would call Current() on a nil pointer and panic. Lookup must instead
// fail closed with (nil, false).
func TestLookupTypedNilProviderFailsClosed(t *testing.T) {
	var p *fakeProviderP // nil pointer with a concrete type
	st := NewStore(p)    // interface != nil

	got, ok := st.Lookup(nil, "sk-gw-anything")
	if ok || got != nil {
		t.Fatalf("Lookup with typed-nil provider = (%v,%v), want (nil,false)", got, ok)
	}
}

// TestNilStoreLookupFailsClosed proves a nil *Store does not panic.
func TestNilStoreLookupFailsClosed(t *testing.T) {
	var st *Store
	if got, ok := st.Lookup(nil, "sk-gw-anything"); ok || got != nil {
		t.Fatalf("nil Store Lookup = (%v,%v), want (nil,false)", got, ok)
	}
}
