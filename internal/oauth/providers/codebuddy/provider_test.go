package codebuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodeBuddyProvider_CN(t *testing.T) {
	t.Parallel()

	var pollCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "CLI/2.108.1 CodeBuddy/2.108.1", r.Header.Get("User-Agent"))
		assert.Equal(t, "copilot.tencent.com", r.Header.Get("X-Domain"))
		assert.Equal(t, "SaaS", r.Header.Get("X-Product"))

		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			assert.Equal(t, "POST", r.Method)
			assert.Equal(t, "CLI", r.URL.Query().Get("platform"))
			assert.Equal(t, "true", r.Header.Get("X-No-Authorization"))

			resp := stateResponse{
				Code: CodeSuccess,
				Data: struct {
					State   string `json:"state"`
					AuthURL string `json:"authUrl"`
				}{
					State:   "mock-state-12345678",
					AuthURL: "https://copilot.tencent.com/login?state=mock-state-12345678",
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/v2/plugin/auth/token":
			assert.Equal(t, "GET", r.Method)
			assert.Equal(t, "mock-state-12345678", r.URL.Query().Get("state"))

			pollCount++
			if pollCount == 1 {
				// Pending response
				resp := tokenResponse{
					Code: CodePending,
					Msg:  "RetryFetchToken",
				}
				_ = json.NewEncoder(w).Encode(resp)
				return
			}

			// Success response
			resp := tokenResponse{
				Code: CodeSuccess,
				Data: struct {
					AccessToken  string `json:"accessToken"`
					RefreshToken string `json:"refreshToken"`
					TokenType    string `json:"tokenType"`
					ExpiresIn    int64  `json:"expiresIn"`
				}{
					AccessToken:  "mock-access-token",
					RefreshToken: "mock-refresh-token",
					TokenType:    "Bearer",
					ExpiresIn:    86400,
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		case "/v2/plugin/auth/token/refresh":
			assert.Equal(t, "POST", r.Method)
			assert.Equal(t, "mock-refresh-token", r.Header.Get("X-Refresh-Token"))
			assert.Equal(t, "plugin", r.Header.Get("X-Auth-Refresh-Source"))

			resp := tokenResponse{
				Code: CodeSuccess,
				Data: struct {
					AccessToken  string `json:"accessToken"`
					RefreshToken string `json:"refreshToken"`
					TokenType    string `json:"tokenType"`
					ExpiresIn    int64  `json:"expiresIn"`
				}{
					AccessToken:  "refreshed-access-token",
					RefreshToken: "refreshed-refresh-token",
					TokenType:    "Bearer",
					ExpiresIn:    86400,
				},
			}
			_ = json.NewEncoder(w).Encode(resp)

		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := NewCN(WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	assert.Equal(t, "codebuddy-cn", provider.Name())
	assert.Equal(t, domain.FlowTypeDeviceCode, provider.FlowType())
	assert.False(t, provider.IsSensitive())

	ctx := context.Background()

	// 1. PrepareAuth
	sess, err := provider.PrepareAuth(ctx, "http://localhost/cb")
	require.NoError(t, err)
	assert.Equal(t, "mock-state-12345678", sess.State)
	assert.Equal(t, "https://copilot.tencent.com/login?state=mock-state-12345678", sess.AuthURL)
	assert.Equal(t, "codebuddy-cn", sess.Provider)

	// 2. PollToken: 1st time is Pending
	conn, pending, err := provider.PollToken(ctx, sess)
	require.NoError(t, err)
	assert.True(t, pending)
	assert.Nil(t, conn)

	// 3. PollToken: 2nd time is Success
	conn, pending, err = provider.PollToken(ctx, sess)
	require.NoError(t, err)
	assert.False(t, pending)
	require.NotNil(t, conn)
	assert.Equal(t, "codebuddy-cn-mock-sta", conn.ID)
	assert.Equal(t, "mock-access-token", conn.Token.AccessToken)
	assert.Equal(t, "mock-refresh-token", conn.Token.RefreshToken)
	assert.Equal(t, "cn", conn.ProviderSpecificData["region"])

	// 4. RefreshToken
	newToken, err := provider.RefreshToken(ctx, conn)
	require.NoError(t, err)
	require.NotNil(t, newToken)
	assert.Equal(t, "refreshed-access-token", newToken.AccessToken)
	assert.Equal(t, "refreshed-refresh-token", newToken.RefreshToken)
}

func TestCodeBuddyProvider_Intl(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "IDE/2.108.1 CodeBuddy/2.108.1", r.Header.Get("User-Agent"))
		assert.Equal(t, "www.codebuddy.ai", r.Header.Get("X-Domain"))
		assert.Equal(t, "ide", r.URL.Query().Get("platform"))

		resp := stateResponse{
			Code: CodeSuccess,
			Data: struct {
				State   string `json:"state"`
				AuthURL string `json:"authUrl"`
			}{
				State:   "intl-state-12345",
				AuthURL: "https://www.codebuddy.ai/login?state=intl-state-12345",
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewIntl(WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	assert.Equal(t, "codebuddy-intl", provider.Name())

	sess, err := provider.PrepareAuth(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "intl-state-12345", sess.State)
	assert.Equal(t, "https://www.codebuddy.ai/login?state=intl-state-12345", sess.AuthURL)
}

func TestCodeBuddyProvider_ExchangeCode(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := tokenResponse{
			Code: CodePending,
			Msg:  "RetryFetchToken",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewCN(WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	sess := &ports.AuthSession{
		State: "test-state",
	}

	_, err := provider.ExchangeCode(context.Background(), "any-code", sess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authorization pending")
}

func TestCodeBuddyProvider_Errors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code": 10001, "msg": "Invalid platform"}`))
	}))
	defer server.Close()

	provider := NewCN(WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	_, err := provider.PrepareAuth(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 400")

	_, _, err = provider.PollToken(context.Background(), &ports.AuthSession{State: "xyz"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 400")

	_, err = provider.RefreshToken(context.Background(), &domain.OAuthConnection{
		Token: domain.OAuthToken{RefreshToken: "dummy"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "status 400")
}
