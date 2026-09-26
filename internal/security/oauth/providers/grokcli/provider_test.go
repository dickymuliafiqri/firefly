package grokcli

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/security/oauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testIDToken(email string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"` + email + `"}`))
	return "e30." + payload + ".c2ln"
}

func TestProvider_Metadata(t *testing.T) {
	t.Parallel()
	p := New()
	assert.Equal(t, "grok-cli", p.Name())
	assert.Equal(t, domain.FlowTypeDeviceCode, p.FlowType())
	assert.False(t, p.IsSensitive())
}

func TestProvider_PrepareAuth(t *testing.T) {
	t.Parallel()
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, r.ParseForm())
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"device_code": "dev-123",
			"user_code": "ABCD-EFGH",
			"verification_uri": "https://auth.x.ai/activate",
			"verification_uri_complete": "https://auth.x.ai/activate?user_code=ABCD-EFGH",
			"expires_in": 600,
			"interval": 5
		}`)
	}))
	defer srv.Close()

	p := New(WithEndpoints(srv.URL, "", ""))
	sess, err := p.PrepareAuth(context.Background(), "http://localhost:8080/unused")
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "grok-cli", sess.Provider)
	assert.NotEmpty(t, sess.State)
	assert.Equal(t, "dev-123", sess.DeviceCode)
	assert.Equal(t, "ABCD-EFGH", sess.UserCode)
	assert.Equal(t, "https://auth.x.ai/activate", sess.VerificationURI)
	assert.Equal(t, "https://auth.x.ai/activate?user_code=ABCD-EFGH", sess.AuthURL)

	// Official CLI wire shape: public client_id + full scope + referrer, no PKCE.
	assert.Equal(t, "b1a00492-073a-47ea-816f-4c329264a828", gotForm.Get("client_id"))
	assert.Contains(t, gotForm.Get("scope"), "offline_access")
	assert.Equal(t, "grok-build", gotForm.Get("referrer"))
	assert.Empty(t, gotForm.Get("code_challenge"))
}

func TestProvider_PrepareAuthError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"invalid_client"}`)
	}))
	defer srv.Close()

	p := New(WithEndpoints(srv.URL, "", ""))
	_, err := p.PrepareAuth(context.Background(), "")
	require.Error(t, err)
}

func TestProvider_PollTokenPending(t *testing.T) {
	t.Parallel()
	for _, oauthErr := range []string{"authorization_pending", "slow_down"} {
		t.Run(oauthErr, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, r.ParseForm())
				assert.Equal(t, "urn:ietf:params:oauth:grant-type:device_code", r.PostForm.Get("grant_type"))
				assert.Equal(t, "dev-123", r.PostForm.Get("device_code"))
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, `{"error":%q}`, oauthErr)
			}))
			defer srv.Close()

			p := New(WithEndpoints("", srv.URL, ""))
			conn, pending, err := p.PollToken(context.Background(), &ports.AuthSession{
				Provider:   "grok-cli",
				State:      "state-x",
				DeviceCode: "dev-123",
			})
			require.NoError(t, err)
			assert.True(t, pending)
			assert.Nil(t, conn)
		})
	}
}

func TestProvider_PollTokenInvalidSession(t *testing.T) {
	t.Parallel()
	p := New()
	_, _, err := p.PollToken(context.Background(), &ports.AuthSession{State: "x"})
	require.Error(t, err)
}

func TestProvider_PollTokenSuccess(t *testing.T) {
	t.Parallel()
	email := "grok-user@example.com"
	userSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer fresh-access", r.Header.Get("Authorization"))
		assert.Equal(t, "xai-grok-cli", r.Header.Get("x-xai-token-auth"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"email":%q,"userId":"u-42","subscriptionTier":"supergrok"}`, email)
	}))
	defer userSrv.Close()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"access_token": "fresh-access",
			"refresh_token": "fresh-refresh",
			"id_token": %q,
			"token_type": "Bearer",
			"expires_in": 2700,
			"scope": "openid profile email offline_access"
		}`, testIDToken("id-token-fallback@example.com"))
	}))
	defer tokenSrv.Close()

	p := New(WithEndpoints("", tokenSrv.URL, userSrv.URL))
	conn, pending, err := p.PollToken(context.Background(), &ports.AuthSession{
		Provider:   "grok-cli",
		State:      "state-x",
		DeviceCode: "dev-123",
	})
	require.NoError(t, err)
	assert.False(t, pending)
	require.NotNil(t, conn)
	assert.Equal(t, "grok-cli", conn.Provider)
	assert.Equal(t, "grok-cli-"+email, conn.ID)
	assert.Equal(t, email, conn.Email)
	assert.Equal(t, "fresh-access", conn.Token.AccessToken)
	assert.Equal(t, "fresh-refresh", conn.Token.RefreshToken)
	assert.Equal(t, "device_code", conn.ProviderSpecificData["auth_method"])
	assert.Equal(t, "u-42", conn.ProviderSpecificData["user_id"])
	assert.Equal(t, "supergrok", conn.ProviderSpecificData["subscription_tier"])
	assert.WithinDuration(t, time.Now().Add(2700*time.Second), conn.Token.ExpiresAt, 30*time.Second)
}

func TestProvider_PollTokenSuccessUserProfileDown(t *testing.T) {
	t.Parallel()
	// A failing /v1/user must not fail the login: identity falls back to id_token.
	userSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer userSrv.Close()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"access_token": "fresh-access",
			"refresh_token": "fresh-refresh",
			"id_token": %q,
			"expires_in": 2700
		}`, testIDToken("id-fallback@example.com"))
	}))
	defer tokenSrv.Close()

	p := New(WithEndpoints("", tokenSrv.URL, userSrv.URL))
	conn, pending, err := p.PollToken(context.Background(), &ports.AuthSession{
		Provider:   "grok-cli",
		State:      "state-x",
		DeviceCode: "dev-123",
	})
	require.NoError(t, err)
	assert.False(t, pending)
	require.NotNil(t, conn)
	assert.Equal(t, "id-fallback@example.com", conn.Email)
}

func TestProvider_ExchangeCodePending(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"authorization_pending"}`)
	}))
	defer srv.Close()

	p := New(WithEndpoints("", srv.URL, ""))
	_, err := p.ExchangeCode(context.Background(), "", &ports.AuthSession{DeviceCode: "dev-123"})
	require.ErrorContains(t, err, "pending")
}

func TestProvider_RefreshTokenRotates(t *testing.T) {
	t.Parallel()
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"access_token": "new-access",
			"refresh_token": "new-refresh",
			"expires_in": 2700
		}`)
	}))
	defer srv.Close()

	p := New(WithEndpoints("", srv.URL, ""))
	got, err := p.RefreshToken(context.Background(), &domain.OAuthConnection{
		ID:       "grok-cli-x",
		Provider: "grok-cli",
		Token: domain.OAuthToken{
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			ExpiresAt:    time.Now().Add(-time.Minute),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "refresh_token", gotForm.Get("grant_type"))
	assert.Equal(t, "old-refresh", gotForm.Get("refresh_token"))
	assert.Empty(t, gotForm.Get("client_secret"))
	assert.Equal(t, "new-access", got.AccessToken)
	assert.Equal(t, "new-refresh", got.RefreshToken)
	assert.WithinDuration(t, time.Now().Add(2700*time.Second), got.ExpiresAt, 30*time.Second)
}

func TestProvider_RefreshTokenPreservesOldRT(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token": "new-access", "expires_in": 2700}`)
	}))
	defer srv.Close()

	p := New(WithEndpoints("", srv.URL, ""))
	got, err := p.RefreshToken(context.Background(), &domain.OAuthConnection{
		ID:       "grok-cli-x",
		Provider: "grok-cli",
		Token:    domain.OAuthToken{AccessToken: "a", RefreshToken: "still-valid-rt"},
	})
	require.NoError(t, err)
	assert.Equal(t, "still-valid-rt", got.RefreshToken)
}

func TestProvider_RefreshTokenErrors(t *testing.T) {
	t.Parallel()
	p := New()
	_, err := p.RefreshToken(context.Background(), nil)
	require.Error(t, err)
	_, err = p.RefreshToken(context.Background(), &domain.OAuthConnection{})
	require.Error(t, err)

	// invalid_grant (revoked/rotated refresh token) must surface as an error so
	// the caller fails closed instead of wiping the stored credential.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"invalid_grant","error_description":"token revoked"}`)
	}))
	defer srv.Close()

	p = New(WithEndpoints("", srv.URL, ""))
	_, err = p.RefreshToken(context.Background(), &domain.OAuthConnection{
		Token: domain.OAuthToken{RefreshToken: "dead-rt"},
	})
	require.ErrorContains(t, err, "invalid_grant")
}

// memStore is a minimal in-memory ports.TokenStore for manager integration tests.
type memStore struct {
	mu    sync.Mutex
	conns map[string]*domain.OAuthConnection
}

func newMemStore() *memStore { return &memStore{conns: map[string]*domain.OAuthConnection{}} }

func (s *memStore) Get(_ context.Context, id string) (*domain.OAuthConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.conns[id]
	if !ok {
		return nil, fmt.Errorf("connection %q not found", id)
	}
	cp := *c
	return &cp, nil
}

func (s *memStore) Save(_ context.Context, conn *domain.OAuthConnection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *conn
	s.conns[conn.ID] = &cp
	return nil
}

func (s *memStore) List(_ context.Context) ([]*domain.OAuthConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*domain.OAuthConnection, 0, len(s.conns))
	for _, c := range s.conns {
		cp := *c
		out = append(out, &cp)
	}
	return out, nil
}

func (s *memStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, id)
	return nil
}

func TestManager_RefreshAndResolveGrokCLI(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"access_token": "rotated-access",
			"refresh_token": "rotated-refresh",
			"expires_in": 2700
		}`)
	}))
	defer srv.Close()

	p := New(WithEndpoints("", srv.URL, ""))
	store := newMemStore()
	mgr := oauth.NewManager(store)
	require.NoError(t, mgr.RegisterProvider(p))

	ctx := context.Background()
	seed := &domain.OAuthConnection{
		ID:       "grok-cli-int@example.com",
		Provider: "grok-cli",
		Email:    "int@example.com",
		Token: domain.OAuthToken{
			AccessToken:  "stale-access",
			RefreshToken: "seed-refresh",
			ExpiresAt:    time.Now().Add(-time.Minute),
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.Save(ctx, seed))

	refreshed, err := mgr.RefreshToken(ctx, seed.ID)
	require.NoError(t, err)
	require.NotNil(t, refreshed)
	assert.Equal(t, "rotated-access", refreshed.Token.AccessToken)
	assert.Equal(t, "rotated-refresh", refreshed.Token.RefreshToken)

	// The rotated credential is persisted and resolvable end to end.
	tok, err := mgr.ResolveToken(ctx, "oauth:"+seed.ID)
	require.NoError(t, err)
	assert.Equal(t, "rotated-access", tok)
}
