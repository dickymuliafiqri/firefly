package httpx

import (
	"net/http"

	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

// AuthMiddleware authenticates the gateway key and injects the tenant into the
// request context. On failure it responds 401 with an OpenAI-shaped error.
// A missing Authorization header and an unknown key produce identical
// responses to avoid key-enumeration.
func AuthMiddleware(store ports.TenantStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := auth.ExtractBearer(r.Header.Get("Authorization"))
			if !ok {
				openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication,
					"missing or malformed API key; expected 'Authorization: Bearer sk-gw-...'")
				return
			}
			// A nil store cannot authenticate anyone: fail closed (401) rather
			// than panic on a nil dereference. This also covers the typed-nil
			// interface case, where `store == nil` alone would be false.
			if IsNil(store) {
				openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication,
					"invalid API key")
				return
			}
			tenant, ok := store.Lookup(r.Context(), token)
			if !ok {
				openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication,
					"invalid API key")
				return
			}
			next.ServeHTTP(w, r.WithContext(WithTenant(r.Context(), tenant)))
		})
	}
}

// RequireTenant returns the tenant from the context or writes 401 and returns
// false. Handlers use it as a guard.
func RequireTenant(w http.ResponseWriter, r *http.Request) (*domain.Tenant, bool) {
	t := TenantFrom(r.Context())
	if t == nil {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthenticated")
		return nil, false
	}
	return t, true
}
