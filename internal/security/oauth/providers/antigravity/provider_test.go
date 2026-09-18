package antigravity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/ports"
)

func TestAntigravityProvider_EndToEnd(t *testing.T) {
	mux := http.NewServeMux()

	// 1. Mock Token URL (Exchange + Refresh)
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		grantType := r.Form.Get("grant_type")
		w.Header().Set("Content-Type", "application/json")

		if grantType == "authorization_code" {
			if r.Form.Get("code") != "valid-google-code" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "mock-ya29-token",
				"refresh_token": "mock-1//refresh",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"scope":         "email profile",
			})
			return
		}

		if grantType == "refresh_token" {
			if r.Form.Get("refresh_token") != "mock-1//refresh" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "mock-ya29-refreshed",
				"expires_in":   3600,
				"token_type":   "Bearer",
			})
			return
		}

		w.WriteHeader(http.StatusBadRequest)
	})

	// 2. Mock UserInfo URL
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Bearer mock-ya29") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"developer@gmail.com","name":"Dev User"}`))
	})

	// 3. Mock loadCodeAssist
	mux.HandleFunc("POST /v1internal:loadCodeAssist", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != AntigravityIDEUserAgent {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"cloudaicompanionProject": {"id": "companion-proj-999"},
			"allowedTiers": [{"id": "tier-free", "isDefault": true}]
		}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	prov := New(
		WithCredentials("client-id-xyz", "client-secret-abc"),
		WithAuthURLs(server.URL+"/auth", server.URL+"/token", server.URL+"/userinfo"),
		WithProdBaseURL(server.URL),
		WithHTTPClient(server.Client()),
	)

	if prov.Name() != "antigravity" {
		t.Fatalf("unexpected name: %s", prov.Name())
	}
	if !prov.IsSensitive() {
		t.Fatal("expected IsSensitive to be true")
	}

	ctx := context.Background()

	// 1. PrepareAuth
	session, err := prov.PrepareAuth(ctx, "http://localhost:8080/callback")
	if err != nil {
		t.Fatalf("PrepareAuth error: %v", err)
	}
	if !strings.Contains(session.AuthURL, "client_id=client-id-xyz") {
		t.Fatalf("AuthURL missing client id: %s", session.AuthURL)
	}

	// 2. ExchangeCode
	conn, err := prov.ExchangeCode(ctx, "valid-google-code", session)
	if err != nil {
		t.Fatalf("ExchangeCode error: %v", err)
	}

	if conn.ID != "antigravity-developer@gmail.com" {
		t.Fatalf("unexpected connection ID: %s", conn.ID)
	}
	if conn.Token.AccessToken != "mock-ya29-token" {
		t.Fatalf("unexpected access token: %s", conn.Token.AccessToken)
	}
	if conn.ProviderSpecificData["project_id"] != "companion-proj-999" {
		t.Fatalf("unexpected project ID: %s", conn.ProviderSpecificData["project_id"])
	}
	if conn.ProviderSpecificData["tier_id"] != "tier-free" {
		t.Fatalf("unexpected tier ID: %s", conn.ProviderSpecificData["tier_id"])
	}

	// 3. RefreshToken
	newTok, err := prov.RefreshToken(ctx, conn)
	if err != nil {
		t.Fatalf("RefreshToken error: %v", err)
	}
	if newTok.AccessToken != "mock-ya29-refreshed" {
		t.Fatalf("unexpected refreshed access token: %s", newTok.AccessToken)
	}
	if newTok.RefreshToken != "mock-1//refresh" {
		t.Fatalf("expected preserved refresh token, got: %s", newTok.RefreshToken)
	}
}

var _ ports.OAuthProvider = (*Provider)(nil)
