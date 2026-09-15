package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/oauth"
	cline "github.com/dickymuliafiqri/firefly/internal/oauth/providers/cline"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type mockOAuthProvider struct {
	name string
}

func (m *mockOAuthProvider) Name() string            { return m.name }
func (m *mockOAuthProvider) FlowType() domain.FlowType { return domain.FlowTypeStandardAuthCode }
func (m *mockOAuthProvider) IsSensitive() bool        { return true }

func (m *mockOAuthProvider) PrepareAuth(ctx context.Context, redirectURI string) (*ports.AuthSession, error) {
	return &ports.AuthSession{
		ID:          "session-123",
		Provider:    m.name,
		State:       "state-xyz",
		RedirectURI: redirectURI,
		AuthURL:     "https://accounts.google.com/o/oauth2/auth?state=state-xyz",
		CreatedAt:   time.Now(),
	}, nil
}

func (m *mockOAuthProvider) ExchangeCode(ctx context.Context, code string, session *ports.AuthSession) (*domain.OAuthConnection, error) {
	if code != "valid-code" {
		return nil, fmt.Errorf("invalid code")
	}
	return &domain.OAuthConnection{
		ID:       m.name + "-test-conn",
		Provider: m.name,
		Email:    "test@example.com",
		Token: domain.OAuthToken{
			AccessToken:  "secret-access-token",
			RefreshToken: "secret-refresh-token",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

func (m *mockOAuthProvider) RefreshToken(ctx context.Context, conn *domain.OAuthConnection) (*domain.OAuthToken, error) {
	return &domain.OAuthToken{
		AccessToken:  "new-access-token",
		RefreshToken: conn.Token.RefreshToken,
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}, nil
}

func setupOAuthServer(t *testing.T) (http.Handler, *oauth.Manager, *auth.Manager) {
	dir := t.TempDir()
	store, err := oauth.NewStore(filepath.Join(dir, "oauth.json"))
	require.NoError(t, err)

	mgr := oauth.NewManager(store)
	require.NoError(t, mgr.RegisterProvider(&mockOAuthProvider{name: "antigravity"}))

	authMgr := auth.NewManager(dir, "admin-secret-pass", "default-pass")

	srv := &Server{}
	deps := RouterDeps{
		OAuthManager:           mgr,
		Auth:                   authMgr,
		Logger:                 slog.Default(),
		DisableGlobalAdmission: true,
	}
	handler := srv.buildHandler(deps)
	return handler, mgr, authMgr
}

func TestServer_OAuthEndpoints(t *testing.T) {
	t.Parallel()

	handler, mgr, authMgr := setupOAuthServer(t)

	// Create valid admin session token
	session, err := authMgr.Authenticate(context.Background(), "admin-secret-pass")
	require.NoError(t, err)
	adminAuth := "Bearer " + session.Token

	// 1. GET /api/oauth/providers (Unauthorized)
	req := httptest.NewRequest(http.MethodGet, "/api/oauth/providers", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// 2. GET /api/oauth/providers (Authorized)
	req = httptest.NewRequest(http.MethodGet, "/api/oauth/providers", nil)
	req.Header.Set("Authorization", adminAuth)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.Bytes()
	require.True(t, gjson.ValidBytes(body))
	providers := gjson.ParseBytes(body).Array()
	require.Len(t, providers, 1)
	assert.Equal(t, "antigravity", providers[0].Get("id").String())
	assert.True(t, providers[0].Get("is_sensitive").Bool())

	// 3. POST /api/oauth/authorize
	authReqBody := []byte(`{"provider": "antigravity", "redirect_uri": "http://localhost:8080/callback"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/oauth/authorize", bytes.NewReader(authReqBody))
	req.Header.Set("Authorization", adminAuth)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	authRespBody := rec.Body.Bytes()
	state := gjson.GetBytes(authRespBody, "state").String()
	authURL := gjson.GetBytes(authRespBody, "auth_url").String()
	assert.NotEmpty(t, state)
	assert.Contains(t, authURL, "accounts.google.com")

	// 4. POST /api/oauth/callback
	cbReqBody := []byte(fmt.Sprintf(`{"state": "%s", "code": "valid-code"}`, state))
	req = httptest.NewRequest(http.MethodPost, "/api/oauth/callback", bytes.NewReader(cbReqBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	cbRespBody := rec.Body.Bytes()
	connID := gjson.GetBytes(cbRespBody, "connection.id").String()
	assert.Equal(t, "antigravity-test-conn", connID)
	assert.Equal(t, "test@example.com", gjson.GetBytes(cbRespBody, "connection.email").String())

	// 5. GET /api/oauth/connections (List)
	req = httptest.NewRequest(http.MethodGet, "/api/oauth/connections", nil)
	req.Header.Set("Authorization", adminAuth)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	listBody := rec.Body.Bytes()
	conns := gjson.ParseBytes(listBody).Array()
	require.Len(t, conns, 1)
	assert.Equal(t, connID, conns[0].Get("id").String())
	// Invariant: access_token and refresh_token MUST NOT be returned in JSON!
	assert.Empty(t, conns[0].Get("token").Raw)
	assert.Empty(t, conns[0].Get("access_token").String())

	// 6. DELETE /api/oauth/connections/{id}
	req = httptest.NewRequest(http.MethodDelete, "/api/oauth/connections/"+connID, nil)
	req.Header.Set("Authorization", adminAuth)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify connection was deleted
	connsAfter, err := mgr.ListConnections(context.Background())
	require.NoError(t, err)
	assert.Empty(t, connsAfter)
}

type mockDeviceOAuthProvider struct {
	mockOAuthProvider
	pollCount int
}

func (m *mockDeviceOAuthProvider) FlowType() domain.FlowType {
	return domain.FlowTypeDeviceCode
}

func (m *mockDeviceOAuthProvider) PollToken(ctx context.Context, session *ports.AuthSession) (*domain.OAuthConnection, bool, error) {
	m.pollCount++
	if m.pollCount == 1 {
		return nil, true, nil // pending
	}
	return &domain.OAuthConnection{
		ID:       "device-conn-123",
		Provider: m.name,
		Email:    "device@example.com",
		Token: domain.OAuthToken{
			AccessToken:  "device-access-token",
			RefreshToken: "device-refresh-token",
			ExpiresAt:    time.Now().Add(1 * time.Hour),
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, false, nil
}

func TestServer_OAuthPoll(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := oauth.NewStore(filepath.Join(dir, "oauth.json"))
	require.NoError(t, err)

	mgr := oauth.NewManager(store)
	devMock := &mockDeviceOAuthProvider{mockOAuthProvider: mockOAuthProvider{name: "codebuddy-cn"}}
	require.NoError(t, mgr.RegisterProvider(devMock))

	authMgr := auth.NewManager(dir, "admin-secret-pass", "default-pass")

	srv := &Server{}
	deps := RouterDeps{
		OAuthManager:           mgr,
		Auth:                   authMgr,
		Logger:                 slog.Default(),
		DisableGlobalAdmission: true,
	}
	handler := srv.buildHandler(deps)

	// Create valid admin session token
	session, err := authMgr.Authenticate(context.Background(), "admin-secret-pass")
	require.NoError(t, err)
	adminAuth := "Bearer " + session.Token

	// 1. Authorize to start session
	authReqBody := []byte(`{"provider": "codebuddy-cn"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/authorize", bytes.NewReader(authReqBody))
	req.Header.Set("Authorization", adminAuth)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	state := gjson.GetBytes(rec.Body.Bytes(), "state").String()
	require.NotEmpty(t, state)

	// 2. Poll 1: Pending
	pollBody := []byte(fmt.Sprintf(`{"state": "%s"}`, state))
	req = httptest.NewRequest(http.MethodPost, "/api/oauth/poll", bytes.NewReader(pollBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, gjson.GetBytes(rec.Body.Bytes(), "pending").Bool())
	assert.Equal(t, "pending", gjson.GetBytes(rec.Body.Bytes(), "status").String())

	// 3. Poll 2: Succeeded
	req = httptest.NewRequest(http.MethodPost, "/api/oauth/poll", bytes.NewReader(pollBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, gjson.GetBytes(rec.Body.Bytes(), "pending").Bool())
	assert.Equal(t, "ok", gjson.GetBytes(rec.Body.Bytes(), "status").String())
	assert.Equal(t, "device-conn-123", gjson.GetBytes(rec.Body.Bytes(), "connection.id").String())

	// 4. Poll 3: State no longer exists (already exchanged)
	req = httptest.NewRequest(http.MethodPost, "/api/oauth/poll", bytes.NewReader(pollBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestServer_OAuthCallback_StatelessCline(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := oauth.NewStore(filepath.Join(dir, "oauth.json"))
	require.NoError(t, err)

	mgr := oauth.NewManager(store)
	require.NoError(t, mgr.RegisterProvider(cline.New()))

	authMgr := auth.NewManager(dir, "admin-secret-pass", "default-pass")

	srv := &Server{}
	deps := RouterDeps{
		OAuthManager:           mgr,
		Auth:                   authMgr,
		Logger:                 slog.Default(),
		DisableGlobalAdmission: true,
	}
	handler := srv.buildHandler(deps)

	session, err := authMgr.Authenticate(context.Background(), "admin-secret-pass")
	require.NoError(t, err)
	adminAuth := "Bearer " + session.Token

	// 1. Initiate Cline auth flow
	authReqBody := []byte(`{"provider": "cline", "redirect_uri": "http://localhost:8080/api/oauth/callback"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/oauth/authorize", bytes.NewReader(authReqBody))
	req.Header.Set("Authorization", adminAuth)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	state := gjson.GetBytes(rec.Body.Bytes(), "state").String()
	require.NotEmpty(t, state)

	// 2. Poll before user authorizes in browser (Cline is non-device provider, should report pending)
	pollBody := []byte(fmt.Sprintf(`{"state": "%s"}`, state))
	req = httptest.NewRequest(http.MethodPost, "/api/oauth/poll", bytes.NewReader(pollBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, gjson.GetBytes(rec.Body.Bytes(), "pending").Bool())
	assert.Equal(t, "pending", gjson.GetBytes(rec.Body.Bytes(), "status").String())

	// 3. User finishes login and browser is redirected to callback WITHOUT state:
	// GET /api/oauth/callback?code=<base64Payload>
	userCode := "eyJhY2Nlc3NUb2tlbiI6ImV5SmhiR2NpT2lKU1V6STFOaUlzSW10cFpDSTZJbk56YjE5dmFXUmpYMnRsZVY5d1lXbHlYekF4U3pOQk5UUXhSRVpMUjFGYVJqRTFSMFk0VWtaTlVEQldJbjAuZXlKbGVIUmxjbTVoYkY5cFpDSTZJblZ6Y2kwd01VMHlSemxhUkRneVIxUlhNVFJFUWxwWFYwMDRTekZVVVNJc0ltWnBjbk4wVG1GdFpTSTZJbTExYkhsdmJtOXpZVzUwWVhraUxDSmxiV0ZwYkNJNkltMTFiSGx2Ym05ellXNTBZWGt4UUdkdFlXbHNMbU52YlNJc0ltbHpjeUk2SW1oMGRIQnpPaTh2WVhCcExuZHZjbXR2Y3k1amIyMHZkWE5sY2w5dFlXNWhaMlZ0Wlc1MEwyTnNhV1Z1ZEY4d01Vc3pRVFUwTVVaT09GUkJNMFZRVUVoVVJESXpNalZCVWlJc0luTjFZaUk2SW5WelpYSmZNREZOTWtjNVdrTkdRbFJHVURjeU9FNHhTRUZYUjFsYU0wWWlMQ0p6YVdRaU9pSnpaWE56YVc5dVh6QXhUVEpIT1ZwRU5GaE5SVEphTmpGYVZsbE9TME5GTjBnMElpd2lhblJwSWpvaU1ERk5Na2M1V2tSUVZEQlJSRWRCVjB0UU5qWlpUVFpUVmpFaUxDSmhkWFJvWDNScGJXVWlPakUzT0RrME1ERXlNVFlzSW1Oc2FXVnVkRjlwWkNJNkltTnNhV1Z1ZEY4d01Vc3pRVFUwTVVaT09GUkJNMFZRVUVoVVJESXpNalZCVWlJc0ltVjRjQ0k2TVRjNE9UUXdORGd4Tml3aWFXRjBJam94TnpnNU5EQXhNakUyZlEuZVFxeGJMVzBFQ1BXUUZlelc4VU9qVDdnOTFWU2txcnBNbHJMeVNCcUxKSVBlclljeVlqSDRNM1JheHMwX1hkWWRFNnZ2RUc4NjRzX01MTHFoUkxXY3ZEZE5wbUxLTUJmaWJoc2JLcDVDTlNBa1E5UUNQRWt3dTVDV21qWnoxMzNEcVE4QVV1RHNkTnZNeENnVXdydzA2dFoxLVY4NlpCOTZFUGlFYlViVXctRldqQ3g4YWhvcWU4YmhhdU1kU1huVDJfMXNkeGQyZG8yaXA2X196MGVhUWktc0I5bXgtNktIZ3pqTllUOFlPeF84NWNabTlXZGowT1lCdUdJYU9SY1hTMEthRVRveDZJdVVtQndfYXpnMEZvRmFKV0RjaXo0MjNBZllxOTNyQ19sYWcwUjRhOVg0dmItdHByVEZpY0VTUGExVmZhTlFhWEdOZDE0QzdqUzZRIiwicmVmcmVzaFRva2VuIjoiUjRyTjVnMXdlQzhTd0JzY3dnZ3l3Wk83byIsImVtYWlsIjoibXVseW9ub3NhbnRheTFAZ21haWwuY29tIiwibmFtZSI6IiIsImZpcnN0TmFtZSI6Im11bHlvbm9zYW50YXkiLCJsYXN0TmFtZSI6IiIsImV4cGlyZXNBdCI6IjIwMjYtMDktMTRUMTU6NTg6MzYuNzY1NjAyOTU3WiJ92Y978oc1pwge9DskLUsjqf9hxbzrIYRURqO2hu_5pWw="

	cbReq := httptest.NewRequest(http.MethodGet, "/api/oauth/callback?code="+userCode, nil)
	cbReq.Header.Set("Accept", "text/html,application/xhtml+xml")
	cbRec := httptest.NewRecorder()
	handler.ServeHTTP(cbRec, cbReq)

	assert.Equal(t, http.StatusOK, cbRec.Code)
	assert.Contains(t, cbRec.Header().Get("Content-Type"), "text/html")
	htmlBody := cbRec.Body.String()
	assert.Contains(t, htmlBody, "Account Connected Successfully!")
	assert.Contains(t, htmlBody, "mulyonosantay1@gmail.com")

	// 4. Poll again: Frontend should now immediately receive the completed connection!
	req = httptest.NewRequest(http.MethodPost, "/api/oauth/poll", bytes.NewReader(pollBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, gjson.GetBytes(rec.Body.Bytes(), "pending").Bool())
	assert.Equal(t, "ok", gjson.GetBytes(rec.Body.Bytes(), "status").String())
	assert.Equal(t, "cline-mulyonosantay1@gmail.com", gjson.GetBytes(rec.Body.Bytes(), "connection.id").String())
	assert.Equal(t, "mulyonosantay1@gmail.com", gjson.GetBytes(rec.Body.Bytes(), "connection.email").String())

	// 5. Subsequent poll fails because session was consumed
	req = httptest.NewRequest(http.MethodPost, "/api/oauth/poll", bytes.NewReader(pollBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

