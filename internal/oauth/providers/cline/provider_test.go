package cline

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProvider_Metadata(t *testing.T) {
	t.Parallel()
	p := New()
	assert.Equal(t, "cline", p.Name())
	assert.Equal(t, domain.FlowTypeStandardAuthCode, p.FlowType())
	assert.False(t, p.IsSensitive())
}

func TestProvider_PrepareAuth(t *testing.T) {
	t.Parallel()
	p := New()

	sess, err := p.PrepareAuth(context.Background(), "http://localhost:8080/callback")
	require.NoError(t, err)
	require.NotNil(t, sess)

	assert.Equal(t, "cline", sess.Provider)
	assert.NotEmpty(t, sess.State)
	assert.Contains(t, sess.AuthURL, "https://api.cline.bot/api/v1/auth/authorize")
	assert.Contains(t, sess.AuthURL, "client_type=extension")
	assert.Contains(t, sess.AuthURL, "state="+sess.State)
}

func TestProvider_ExchangeCode_Base64FastPath(t *testing.T) {
	t.Parallel()
	p := New()

	// Simulate Cline Base64 JSON payload encoded inside code parameter
	rawJSON := `{"accessToken":"test-access-token-123","refreshToken":"test-refresh-token-456","email":"user@example.com","firstName":"John","lastName":"Doe","expiresAt":"2026-10-01T12:00:00Z"}`
	encodedCode := base64.StdEncoding.EncodeToString([]byte(rawJSON))

	sess := &ports.AuthSession{
		Provider:    "cline",
		RedirectURI: "http://localhost:8080/callback",
	}

	conn, err := p.ExchangeCode(context.Background(), encodedCode, sess)
	require.NoError(t, err)
	require.NotNil(t, conn)

	assert.Equal(t, "cline-user@example.com", conn.ID)
	assert.Equal(t, "cline", conn.Provider)
	assert.Equal(t, "user@example.com", conn.Email)
	assert.Equal(t, "test-access-token-123", conn.Token.AccessToken)
	assert.Equal(t, "test-refresh-token-456", conn.Token.RefreshToken)
	assert.Equal(t, "Bearer", conn.Token.TokenType)
	assert.Equal(t, "John", conn.ProviderSpecificData["firstName"])
	assert.Equal(t, "Doe", conn.ProviderSpecificData["lastName"])
}

func TestProvider_ExchangeCode_UserPayload(t *testing.T) {
	t.Parallel()
	p := New()

	userCode := "eyJhY2Nlc3NUb2tlbiI6ImV5SmhiR2NpT2lKU1V6STFOaUlzSW10cFpDSTZJbk56YjE5dmFXUmpYMnRsZVY5d1lXbHlYekF4U3pOQk5UUXhSRVpMUjFGYVJqRTFSMFk0VWtaTlVEQldJbjAuZXlKbGVIUmxjbTVoYkY5cFpDSTZJblZ6Y2kwd01VMHlSemxhUkRneVIxUlhNVFJFUWxwWFYwMDRTekZVVVNJc0ltWnBjbk4wVG1GdFpTSTZJbTExYkhsdmJtOXpZVzUwWVhraUxDSmxiV0ZwYkNJNkltMTFiSGx2Ym05ellXNTBZWGt4UUdkdFlXbHNMbU52YlNJc0ltbHpjeUk2SW1oMGRIQnpPaTh2WVhCcExuZHZjbXR2Y3k1amIyMHZkWE5sY2w5dFlXNWhaMlZ0Wlc1MEwyTnNhV1Z1ZEY4d01Vc3pRVFUwTVVaT09GUkJNMFZRVUVoVVJESXpNalZCVWlJc0luTjFZaUk2SW5WelpYSmZNREZOTWtjNVdrTkdRbFJHVURjeU9FNHhTRUZYUjFsYU0wWWlMQ0p6YVdRaU9pSnpaWE56YVc5dVh6QXhUVEpIT1ZwRU5GaE5SVEphTmpGYVZsbE9TME5GTjBnMElpd2lhblJwSWpvaU1ERk5Na2M1V2tSUVZEQlJSRWRCVjB0UU5qWlpUVFpUVmpFaUxDSmhkWFJvWDNScGJXVWlPakUzT0RrME1ERXlNVFlzSW1Oc2FXVnVkRjlwWkNJNkltTnNhV1Z1ZEY4d01Vc3pRVFUwTVVaT09GUkJNMFZRVUVoVVJESXpNalZCVWlJc0ltVjRjQ0k2TVRjNE9UUXdORGd4Tml3aWFXRjBJam94TnpnNU5EQXhNakUyZlEuZVFxeGJMVzBFQ1BXUUZlelc4VU9qVDdnOTFWU2txcnBNbHJMeVNCcUxKSVBlclljeVlqSDRNM1JheHMwX1hkWWRFNnZ2RUc4NjRzX01MTHFoUkxXY3ZEZE5wbUxLTUJmaWJoc2JLcDVDTlNBa1E5UUNQRWt3dTVDV21qWnoxMzNEcVE4QVV1RHNkTnZNeENnVXdydzA2dFoxLVY4NlpCOTZFUGlFYlViVXctRldqQ3g4YWhvcWU4YmhhdU1kU1huVDJfMXNkeGQyZG8yaXA2X196MGVhUWktc0I5bXgtNktIZ3pqTllUOFlPeF84NWNabTlXZGowT1lCdUdJYU9SY1hTMEthRVRveDZJdVVtQndfYXpnMEZvRmFKV0RjaXo0MjNBZllxOTNyQ19sYWcwUjRhOVg0dmItdHByVEZpY0VTUGExVmZhTlFhWEdOZDE0QzdqUzZRIiwicmVmcmVzaFRva2VuIjoiUjRyTjVnMXdlQzhTd0JzY3dnZ3l3Wk83byIsImVtYWlsIjoibXVseW9ub3NhbnRheTFAZ21haWwuY29tIiwibmFtZSI6IiIsImZpcnN0TmFtZSI6Im11bHlvbm9zYW50YXkiLCJsYXN0TmFtZSI6IiIsImV4cGlyZXNBdCI6IjIwMjYtMDktMTRUMTU6NTg6MzYuNzY1NjAyOTU3WiJ92Y978oc1pwge9DskLUsjqf9hxbzrIYRURqO2hu_5pWw="

	conn, err := p.ExchangeCode(context.Background(), userCode, nil)
	require.NoError(t, err)
	require.NotNil(t, conn)

	assert.Equal(t, "cline-mulyonosantay1@gmail.com", conn.ID)
	assert.Equal(t, "mulyonosantay1@gmail.com", conn.Email)
	assert.Equal(t, "R4rN5g1weC8SwBscwggywZO7o", conn.Token.RefreshToken)
	assert.Equal(t, "mulyonosantay", conn.ProviderSpecificData["firstName"])
}

func TestProvider_ExchangeCode_HTTPFallback(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/auth/token", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"accessToken": "http-access-token",
				"refreshToken": "http-refresh-token",
				"userInfo": {
					"email": "http-user@example.com"
				},
				"expiresAt": "2026-12-31T23:59:59Z"
			}
		}`))
	}))
	defer server.Close()

	p := New(
		WithEndpoints("", server.URL+"/auth/token", ""),
		WithHTTPClient(server.Client()),
	)

	sess := &ports.AuthSession{
		Provider:    "cline",
		RedirectURI: "http://localhost:8080/callback",
	}

	conn, err := p.ExchangeCode(context.Background(), "standard-oauth-code", sess)
	require.NoError(t, err)
	require.NotNil(t, conn)

	assert.Equal(t, "cline-http-user@example.com", conn.ID)
	assert.Equal(t, "http-access-token", conn.Token.AccessToken)
	assert.Equal(t, "http-refresh-token", conn.Token.RefreshToken)
}

func TestProvider_RefreshToken(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/auth/refresh", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"accessToken": "refreshed-access-token",
				"refreshToken": "new-refresh-token",
				"expiresAt": 1790000000
			}
		}`))
	}))
	defer server.Close()

	p := New(
		WithEndpoints("", "", server.URL+"/auth/refresh"),
		WithHTTPClient(server.Client()),
	)

	conn := &domain.OAuthConnection{
		ID:       "cline-user@example.com",
		Provider: "cline",
		Token: domain.OAuthToken{
			AccessToken:  "old-access-token",
			RefreshToken: "old-refresh-token",
			ExpiresAt:    time.Now().Add(-10 * time.Minute),
		},
	}

	tok, err := p.RefreshToken(context.Background(), conn)
	require.NoError(t, err)
	require.NotNil(t, tok)

	assert.Equal(t, "refreshed-access-token", tok.AccessToken)
	assert.Equal(t, "new-refresh-token", tok.RefreshToken)
	assert.False(t, tok.IsExpired(0))
}
