package server

import (
	"crypto/subtle"
	"net/http"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
)

// authorizeService gates machine-to-machine endpoints (the harvester sync
// surface) with a dedicated shared secret, separate from the admin token and
// dashboard sessions so the harvester identity cannot reach admin routes and
// vice versa.
//
// It writes the failure response itself and returns false, because the
// distinction between "not configured" and "wrong token" must not be
// re-derived at each call site:
//   - 503 when ServiceToken is unset: the endpoint is unusable and must fail
//     closed rather than fall open like authorizeAdmin does for bare tests.
//   - 401 when the bearer credential is absent or does not match.
//
// Comparison is constant-time so a caller cannot recover the token byte by
// byte. The token is never logged, never echoed, and never included in an
// error body.
func (deps RouterDeps) authorizeService(w http.ResponseWriter, r *http.Request) bool {
	if deps.ServiceToken == "" {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI,
			"harvester service token is not configured")
		return false
	}
	token, ok := auth.ExtractBearer(r.Header.Get("Authorization"))
	if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(deps.ServiceToken)) != 1 {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication,
			"invalid or missing service credential")
		return false
	}
	return true
}
