package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
)

// ExtractBearer pulls the token from an Authorization header value. It accepts
// "Bearer <token>" (case-insensitive scheme). Returns ("", false) if absent or
// malformed.
func ExtractBearer(header string) (string, bool) {
	return auth.ExtractBearer(header)
}

// DescribeJSONError renders a decode failure without repeating any of the body.
// Go's syntax error quotes the offending byte, and bodies may carry credentials,
// so the position is reported instead of the raw character.
func DescribeJSONError(err error, what string) string {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Sprintf("parse JSON %s: malformed JSON at byte offset %d", what, syntaxErr.Offset)
	}
	return "parse JSON " + what + ": " + err.Error()
}

// ReadJSON decodes a request body bounded by maxBytes into T, writing HTTP 413
// for oversized bodies and HTTP 400 for malformed JSON or unreadable bodies.
// label names the payload in failure messages (e.g. "provider", "tenant").
func ReadJSON[T any](w http.ResponseWriter, r *http.Request, maxBytes int64, label string) (T, bool) {
	var v T

	if maxBytes <= 0 {
		maxBytes = 32 << 20 // 32 MB default
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	defer r.Body.Close()

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			openai.WriteError(w, http.StatusRequestEntityTooLarge, openai.TypeInvalidRequest, "request body too large")
			return v, false
		}
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "read request body: "+err.Error())
		return v, false
	}

	if err := json.Unmarshal(bodyBytes, &v); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, DescribeJSONError(err, label))
		return v, false
	}
	return v, true
}
