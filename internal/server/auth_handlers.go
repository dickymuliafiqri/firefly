package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/openai"
)

type LoginRequest struct {
	Password string `json:"password"`
}

type LoginResponse struct {
	Status    string `json:"status"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

type UpdatePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleOptionsAuth serves CORS preflight requests for authentication endpoints.
func (deps RouterDeps) handleOptionsAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthLogin verifies dashboard credentials and issues an active session token.
func (deps RouterDeps) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10) // 64 KB limit
	defer r.Body.Close()

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "invalid JSON payload: "+err.Error())
		return
	}

	if deps.Auth == nil {
		// Fallback if auth manager was not wired: check adminToken directly
		if deps.AdminToken != "" && req.Password == deps.AdminToken {
			_ = json.NewEncoder(w).Encode(LoginResponse{
				Status:    "ok",
				Token:     deps.AdminToken,
				ExpiresAt: time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			})
			return
		}
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "authentication service not available")
		return
	}

	sess, err := deps.Auth.Authenticate(ctx, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "incorrect dashboard password")
			return
		}
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, err.Error())
		return
	}

	_ = json.NewEncoder(w).Encode(LoginResponse{
		Status:    "ok",
		Token:     sess.Token,
		ExpiresAt: sess.ExpiresAt.Format(time.RFC3339),
	})
}

// handleAuthVerify checks whether the client's current session or admin token is valid.
func (deps RouterDeps) handleAuthVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthenticated or expired session")
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":        "ok",
		"authenticated": true,
	})
}

// handleAuthLogout revokes the presented session token.
func (deps RouterDeps) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	token, ok := auth.ExtractBearer(r.Header.Get("Authorization"))
	if ok && deps.Auth != nil {
		deps.Auth.RevokeSession(r.Context(), token)
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
	})
}

// handleAuthPassword updates the backend dashboard password with authorization.
func (deps RouterDeps) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized: valid session or admin token required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	defer r.Body.Close()

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "read body: "+err.Error())
		return
	}

	var req UpdatePasswordRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "parse JSON: "+err.Error())
		return
	}

	if deps.Auth == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "authentication service not available")
		return
	}

	if err := deps.Auth.UpdatePassword(r.Context(), req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "incorrect current password")
			return
		}
		if errors.Is(err, auth.ErrPasswordTooShort) {
			openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "password must be at least 4 characters")
			return
		}
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, err.Error())
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"message": "dashboard password updated successfully",
	})
}
