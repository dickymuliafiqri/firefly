package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
)

// ProviderInfoDTO describes an available OAuth provider for frontend display.
type ProviderInfoDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	FlowType    string `json:"flow_type"`
	IsSensitive bool   `json:"is_sensitive"`
}

// AuthorizeRequestDTO is the payload sent to initiate an OAuth authorization session.
type AuthorizeRequestDTO struct {
	Provider    string `json:"provider"`
	RedirectURI string `json:"redirect_uri,omitempty"`
}

// AuthorizeResponseDTO contains the generated authorization URL and session ID.
type AuthorizeResponseDTO struct {
	SessionID string `json:"session_id"`
	AuthURL   string `json:"auth_url"`
	State     string `json:"state"`
}

// CallbackRequestDTO allows exchanging an OAuth code via POST JSON.
type CallbackRequestDTO struct {
	State string `json:"state"`
	Code  string `json:"code"`
}

// PollRequestDTO is the payload sent to poll an ongoing device/browser OAuth session.
type PollRequestDTO struct {
	State      string `json:"state"`
	DeviceCode string `json:"device_code,omitempty"`
}

// PollResponseDTO communicates polling status or completion back to caller.
type PollResponseDTO struct {
	Status     string         `json:"status"` // "ok" or "pending"
	Pending    bool           `json:"pending"`
	Connection *ConnectionDTO `json:"connection,omitempty"`
}

// ConnectionDTO represents a connected account for dashboard display with masked secrets.
type ConnectionDTO struct {
	ID                   string            `json:"id"`
	Provider             string            `json:"provider"`
	Email                string            `json:"email,omitempty"`
	ExpiresAt            string            `json:"expires_at,omitempty"`
	IsExpired            bool              `json:"is_expired"`
	ProviderSpecificData map[string]string `json:"provider_specific_data,omitempty"`
	CreatedAt            string            `json:"created_at"`
	UpdatedAt            string            `json:"updated_at"`
}

func (deps RouterDeps) handleOptionsOAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	w.WriteHeader(http.StatusNoContent)
}

func (deps RouterDeps) handleListOAuthProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized")
		return
	}

	if deps.OAuthManager == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "oauth subsystem is not configured")
		return
	}

	providers := deps.OAuthManager.ListProviders()
	dtoList := make([]ProviderInfoDTO, 0, len(providers))
	for _, p := range providers {
		dtoList = append(dtoList, ProviderInfoDTO{
			ID:          p.Name,
			Name:        p.Name,
			FlowType:    p.FlowType,
			IsSensitive: p.IsSensitive,
		})
	}

	_ = json.NewEncoder(w).Encode(dtoList)
}

func (deps RouterDeps) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized")
		return
	}

	if deps.OAuthManager == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "oauth subsystem is not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()

	var req AuthorizeRequestDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "invalid json payload: "+err.Error())
		return
	}

	if req.Provider == "" {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "provider is required")
		return
	}

	redirectURI := req.RedirectURI
	if redirectURI == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		redirectURI = fmt.Sprintf("%s://%s/api/oauth/callback", scheme, r.Host)
	}

	sess, err := deps.OAuthManager.PrepareAuth(r.Context(), req.Provider, redirectURI)
	if err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "failed to start auth: "+err.Error())
		return
	}

	_ = json.NewEncoder(w).Encode(AuthorizeResponseDTO{
		SessionID: sess.ID,
		AuthURL:   sess.AuthURL,
		State:     sess.State,
	})
}

func (deps RouterDeps) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if deps.OAuthManager == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "oauth subsystem is not configured")
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	if r.Method == http.MethodPost && (state == "" || code == "") {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		defer r.Body.Close()

		var req CallbackRequestDTO
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			if state == "" {
				state = req.State
			}
			if code == "" {
				code = req.Code
			}
		}
	}

	if code == "" {
		if r.Method == http.MethodGet && !strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "<html><body><h2>OAuth Connection Failed</h2><p>Missing authorization code</p></body></html>")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "code is required")
		return
	}

	conn, err := deps.OAuthManager.ExchangeCallback(r.Context(), state, code)
	if err != nil {
		if r.Method == http.MethodGet && !strings.Contains(r.Header.Get("Accept"), "application/json") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "<html><body><h2>OAuth Connection Failed</h2><p>%s</p></body></html>", err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "oauth exchange failed: "+err.Error())
		return
	}

	// If browser redirect GET request, render success HTML that communicates back to opener window
	if r.Method == http.MethodGet && !strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>OAuth Connection Successful</title></head>
<body style="font-family:system-ui,sans-serif;text-align:center;padding:50px;">
  <h2>Account Connected Successfully!</h2>
  <p>Provider: <strong>%s</strong> (%s)</p>
  <p>You may close this window.</p>
  <script>
    if (window.opener) {
      window.opener.postMessage({ type: 'oauth_complete', provider: %q, id: %q }, '*');
      setTimeout(() => window.close(), 1200);
    }
  </script>
</body>
</html>`, conn.Provider, conn.Email, conn.Provider, conn.ID)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"connection": ConnectionDTO{
			ID:                   conn.ID,
			Provider:             conn.Provider,
			Email:                conn.Email,
			ExpiresAt:            conn.Token.ExpiresAt.Format(time.RFC3339),
			IsExpired:            conn.Token.IsExpired(0),
			ProviderSpecificData: conn.ProviderSpecificData,
			CreatedAt:            conn.CreatedAt.Format(time.RFC3339),
			UpdatedAt:            conn.UpdatedAt.Format(time.RFC3339),
		},
	})
}

func (deps RouterDeps) handleOAuthPoll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if deps.OAuthManager == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "oauth subsystem is not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()

	var req PollRequestDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "invalid json payload: "+err.Error())
		return
	}

	state := req.State
	if state == "" {
		state = req.DeviceCode
	}
	if state == "" {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "state or device_code is required")
		return
	}

	conn, pending, err := deps.OAuthManager.PollAuth(r.Context(), state)
	if err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "oauth poll failed: "+err.Error())
		return
	}

	if pending {
		_ = json.NewEncoder(w).Encode(PollResponseDTO{
			Status:  "pending",
			Pending: true,
		})
		return
	}

	_ = json.NewEncoder(w).Encode(PollResponseDTO{
		Status:  "ok",
		Pending: false,
		Connection: &ConnectionDTO{
			ID:                   conn.ID,
			Provider:             conn.Provider,
			Email:                conn.Email,
			ExpiresAt:            conn.Token.ExpiresAt.Format(time.RFC3339),
			IsExpired:            conn.Token.IsExpired(0),
			ProviderSpecificData: conn.ProviderSpecificData,
			CreatedAt:            conn.CreatedAt.Format(time.RFC3339),
			UpdatedAt:            conn.UpdatedAt.Format(time.RFC3339),
		},
	})
}

func (deps RouterDeps) handleListOAuthConnections(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized")
		return
	}

	if deps.OAuthManager == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "oauth subsystem is not configured")
		return
	}

	connections, err := deps.OAuthManager.ListConnections(r.Context())
	if err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "failed to list connections: "+err.Error())
		return
	}

	dtos := make([]ConnectionDTO, 0, len(connections))
	for _, c := range connections {
		if c == nil {
			continue
		}
		dtos = append(dtos, ConnectionDTO{
			ID:                   c.ID,
			Provider:             c.Provider,
			Email:                c.Email,
			ExpiresAt:            c.Token.ExpiresAt.Format(time.RFC3339),
			IsExpired:            c.Token.IsExpired(0),
			ProviderSpecificData: c.ProviderSpecificData,
			CreatedAt:            c.CreatedAt.Format(time.RFC3339),
			UpdatedAt:            c.UpdatedAt.Format(time.RFC3339),
		})
	}

	_ = json.NewEncoder(w).Encode(dtos)
}

func (deps RouterDeps) handleDeleteOAuthConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized")
		return
	}

	if deps.OAuthManager == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "oauth subsystem is not configured")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "connection id is required")
		return
	}

	if err := deps.OAuthManager.DeleteConnection(r.Context(), id); err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "failed to delete connection: "+err.Error())
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
