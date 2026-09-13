// Package openai contains OpenAI wire-format types and helpers: the error
// envelope, model list shape, and SSE framing. Phase 3 uses the error writer;
// Phase 4 extends this with the upstream adapter and streaming relay.
package openai

import (
	"encoding/json"
	"net/http"
)

// APIError is the OpenAI error envelope.
type APIError struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the inner error object.
type ErrorDetail struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}

// WriteError writes an OpenAI-shaped error response with the given status.
// It never panics on a partially-written header; callers should avoid calling
// it after streaming has begun.
func WriteError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIError{Error: ErrorDetail{
		Message: message,
		Type:    errType,
	}})
}

// Error type constants aligned with OpenAI's documented taxonomy.
const (
	TypeInvalidRequest = "invalid_request_error"
	TypeAuthentication = "authentication_error"
	TypePermission     = "permission_error"
	TypeNotFound       = "not_found_error"
	TypeRateLimit      = "rate_limit_error"
	TypeAPI            = "api_error"
)

// StatusToType maps an HTTP status to the conventional OpenAI error type.
func StatusToType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return TypeInvalidRequest
	case http.StatusUnauthorized:
		return TypeAuthentication
	case http.StatusForbidden:
		return TypePermission
	case http.StatusNotFound:
		return TypeNotFound
	case http.StatusTooManyRequests:
		return TypeRateLimit
	default:
		return TypeAPI
	}
}
