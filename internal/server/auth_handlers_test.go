package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/registry"
)

func TestAuthEndpoints_CompleteLifecycle(t *testing.T) {
	dir := t.TempDir()
	authMgr := auth.NewManager(dir, "", "12345678")
	reg := registry.New()

	deps := RouterDeps{
		Snapshots: reg,
		Registry:  reg,
		ConfigDir: dir,
		Auth:      authMgr,
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)

	// 1. CORS Preflight
	reqOptions := httptest.NewRequest("OPTIONS", "/api/auth/login", nil)
	wOptions := httptest.NewRecorder()
	s.Handler().ServeHTTP(wOptions, reqOptions)
	if wOptions.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS /api/auth/login got %d, want 204", wOptions.Code)
	}

	// 2. Login with wrong password -> 401
	badLoginBody, _ := json.Marshal(LoginRequest{Password: "wrong"})
	reqBadLogin := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(badLoginBody))
	wBadLogin := httptest.NewRecorder()
	s.Handler().ServeHTTP(wBadLogin, reqBadLogin)
	if wBadLogin.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password got status %d, want 401", wBadLogin.Code)
	}

	// 3. Login with correct password -> 200 + token
	goodLoginBody, _ := json.Marshal(LoginRequest{Password: "12345678"})
	reqLogin := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(goodLoginBody))
	wLogin := httptest.NewRecorder()
	s.Handler().ServeHTTP(wLogin, reqLogin)
	if wLogin.Code != http.StatusOK {
		t.Fatalf("login got status %d, want 200", wLogin.Code)
	}
	var loginResp LoginResponse
	if err := json.Unmarshal(wLogin.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	if loginResp.Token == "" {
		t.Fatal("expected non-empty session token")
	}

	// 4. Verify without token -> 401
	reqVerifyNoToken := httptest.NewRequest("GET", "/api/auth/verify", nil)
	wVerifyNoToken := httptest.NewRecorder()
	s.Handler().ServeHTTP(wVerifyNoToken, reqVerifyNoToken)
	if wVerifyNoToken.Code != http.StatusUnauthorized {
		t.Fatalf("verify without token got status %d, want 401", wVerifyNoToken.Code)
	}

	// 5. Verify with valid token -> 200
	reqVerify := httptest.NewRequest("GET", "/api/auth/verify", nil)
	reqVerify.Header.Set("Authorization", "Bearer "+loginResp.Token)
	wVerify := httptest.NewRecorder()
	s.Handler().ServeHTTP(wVerify, reqVerify)
	if wVerify.Code != http.StatusOK {
		t.Fatalf("verify with token got status %d, want 200", wVerify.Code)
	}

	// 6. Access settings WITHOUT token -> 200 OK with sanitized public data (tenants empty)
	reqSettingsNoAuth := httptest.NewRequest("GET", "/api/settings", nil)
	wSettingsNoAuth := httptest.NewRecorder()
	s.Handler().ServeHTTP(wSettingsNoAuth, reqSettingsNoAuth)
	if wSettingsNoAuth.Code != http.StatusOK {
		t.Fatalf("GET /api/settings without auth got status %d, want 200", wSettingsNoAuth.Code)
	}
	var publicSettings config.SettingsDTO
	if err := json.Unmarshal(wSettingsNoAuth.Body.Bytes(), &publicSettings); err != nil {
		t.Fatalf("unmarshal public settings: %v", err)
	}
	if len(publicSettings.Tenants) != 0 || publicSettings.Turso != nil {
		t.Fatalf("public settings leaked sensitive data: %+v", publicSettings)
	}

	// 6b. Mutating settings WITHOUT token -> 401 Unauthorized
	reqMutateNoAuth := httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{}`))
	wMutateNoAuth := httptest.NewRecorder()
	s.Handler().ServeHTTP(wMutateNoAuth, reqMutateNoAuth)
	if wMutateNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/settings without auth got status %d, want 401", wMutateNoAuth.Code)
	}

	// 7. Access credentials (/api/settings) WITH session token -> 200 OK
	reqSettingsAuth := httptest.NewRequest("GET", "/api/settings", nil)
	reqSettingsAuth.Header.Set("Authorization", "Bearer "+loginResp.Token)
	wSettingsAuth := httptest.NewRecorder()
	s.Handler().ServeHTTP(wSettingsAuth, reqSettingsAuth)
	if wSettingsAuth.Code != http.StatusOK {
		t.Fatalf("GET /api/settings with session token got status %d, want 200", wSettingsAuth.Code)
	}

	// 8. Update password with session token
	passBody, _ := json.Marshal(UpdatePasswordRequest{
		CurrentPassword: "12345678",
		NewPassword:     "secure-pass-2026",
	})
	reqPass := httptest.NewRequest("PUT", "/api/auth/password", bytes.NewReader(passBody))
	reqPass.Header.Set("Authorization", "Bearer "+loginResp.Token)
	wPass := httptest.NewRecorder()
	s.Handler().ServeHTTP(wPass, reqPass)
	if wPass.Code != http.StatusOK {
		t.Fatalf("PUT /api/auth/password got status %d, want 200", wPass.Code)
	}

	// 9. Old token is now revoked -> verify fails
	reqVerifyOld := httptest.NewRequest("GET", "/api/auth/verify", nil)
	reqVerifyOld.Header.Set("Authorization", "Bearer "+loginResp.Token)
	wVerifyOld := httptest.NewRecorder()
	s.Handler().ServeHTTP(wVerifyOld, reqVerifyOld)
	if wVerifyOld.Code != http.StatusUnauthorized {
		t.Fatalf("old token after password update got %d, want 401", wVerifyOld.Code)
	}

	// 10. Login with new password -> 200
	newLoginBody, _ := json.Marshal(LoginRequest{Password: "secure-pass-2026"})
	reqNewLogin := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(newLoginBody))
	wNewLogin := httptest.NewRecorder()
	s.Handler().ServeHTTP(wNewLogin, reqNewLogin)
	if wNewLogin.Code != http.StatusOK {
		t.Fatalf("login with new password got status %d, want 200", wNewLogin.Code)
	}
	var newLoginResp LoginResponse
	_ = json.Unmarshal(wNewLogin.Body.Bytes(), &newLoginResp)

	// 11. Logout revokes token
	reqLogout := httptest.NewRequest("POST", "/api/auth/logout", nil)
	reqLogout.Header.Set("Authorization", "Bearer "+newLoginResp.Token)
	wLogout := httptest.NewRecorder()
	s.Handler().ServeHTTP(wLogout, reqLogout)
	if wLogout.Code != http.StatusOK {
		t.Fatalf("logout got %d, want 200", wLogout.Code)
	}

	// 12. Verify after logout -> 401
	reqVerifyLoggedOut := httptest.NewRequest("GET", "/api/auth/verify", nil)
	reqVerifyLoggedOut.Header.Set("Authorization", "Bearer "+newLoginResp.Token)
	wVerifyLoggedOut := httptest.NewRecorder()
	s.Handler().ServeHTTP(wVerifyLoggedOut, reqVerifyLoggedOut)
	if wVerifyLoggedOut.Code != http.StatusUnauthorized {
		t.Fatalf("verify after logout got %d, want 401", wVerifyLoggedOut.Code)
	}
}
