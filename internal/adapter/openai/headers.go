// package openai (headers.go): request header scrubbing and context helpers.

package openai

import (
	"compress/flate"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/reqid"
	"github.com/dickymuliafiqri/firefly/internal/textx"
)

// hopByHopHeaders are connection-scoped and must never be forwarded upstream.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"proxy-connection":    true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// scrubbedHeaders are inbound headers we must NEVER forward: the client's auth
// (we substitute our own upstream credential) and headers that describe the
// client connection rather than the payload.
var scrubbedHeaders = map[string]bool{
	"authorization":   true, // replaced with the upstream secret
	"host":            true, // http.Client sets Host from the URL
	"content-length":  true, // recomputed after model rewrite
	"x-api-key":       true, // some OpenAI-compatible vendors use this; scrub it
	"api-key":         true, // Azure OpenAI header
	"accept-encoding": true, // let http.Transport negotiate + transparently decompress; forwarding the client's value (e.g. gzip) makes Go skip auto-decompression and relays raw compressed bytes
}

// copyUpstreamHeaders copies client headers to the upstream request, dropping
// hop-by-hop and credential headers. It lowercases for matching but preserves
// the original canonical key when setting.
func copyUpstreamHeaders(dst, src http.Header) {
	for k, vals := range src {
		lk := strings.ToLower(k)
		if hopByHopHeaders[lk] || scrubbedHeaders[lk] {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

// writeUpstreamError relays an upstream error response. If the body already
// looks like an OpenAI error envelope, it is passed through verbatim (preserving
// the upstream's message and code); otherwise the body is wrapped in our
// envelope so clients always see a consistent shape.
func writeUpstreamError(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	if looksLikeAPIError(body) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	// Cap message length to avoid relaying huge HTML error pages.
	msg := textx.Excerpt(body, 512)
	if msg == "" {
		msg = http.StatusText(status)
	}
	WriteError(w, status, StatusToType(status), msg)
}

// looksLikeAPIError reports whether body parses as {"error": {...}}.
func looksLikeAPIError(body []byte) bool {
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return len(probe.Error) > 0
}

// inferErrorStatus maps an OpenAI-shaped error body onto the HTTP status the
// upstream should have returned. Some OpenAI-compatible gateways answer a
// request for an unsupported model with HTTP 200 and an error envelope in the
// body (observed on bozzdrop: 200 {"error":{"message":"Model 'x' is not
// supported in this gateway catalog"}}). Relaying that verbatim reports success
// to the client even though no inference ran, so the adapter probes the body and
// reconstructs the status it should have carried.
//
// The mapping is deliberately conservative: the prose message is matched first
// (gateways routinely stamp every refusal with the generic type
// "invalid_request_error", so the message is the most specific signal), then an
// explicit "code", then a documented "type". Anything the probe cannot classify
// stays 502 so the caller's generic upstream-failure handling still applies
// rather than inventing a status the upstream never meant to send.
func inferErrorStatus(body []byte) int {
	var probe struct {
		Error struct {
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return http.StatusBadGateway
	}
	// The prose message is the most specific signal. Gateways frequently stamp
	// every refusal with the generic type "invalid_request_error", so trusting
	// type/code first would turn a "model is not supported" refusal into a 400.
	// Only fall back to the structured fields when the message is uninformative.
	if status := statusFromErrorMessage(probe.Error.Message); status != http.StatusBadGateway {
		return status
	}
	if status, ok := statusFromErrorCode(probe.Error.Code); ok {
		return status
	}
	if status, ok := statusFromErrorType(probe.Error.Type); ok {
		return status
	}
	return http.StatusBadGateway
}

// statusFromErrorCode maps the machine-readable error.code field. The code may
// be a string or a number on the wire, hence the fmt.Sprint normalization.
func statusFromErrorCode(code any) (int, bool) {
	if code == nil {
		return 0, false
	}
	switch strings.ToLower(fmt.Sprint(code)) {
	case "model_not_found":
		return http.StatusNotFound, true
	case "invalid_api_key", "authentication_error":
		return http.StatusUnauthorized, true
	case "permission_denied", "insufficient_quota":
		return http.StatusForbidden, true
	case "rate_limit_exceeded":
		return http.StatusTooManyRequests, true
	case "invalid_request_error", "invalid_request":
		return http.StatusBadRequest, true
	}
	return 0, false
}

// statusFromErrorType maps the documented OpenAI error.type taxonomy.
func statusFromErrorType(errType string) (int, bool) {
	switch strings.ToLower(errType) {
	case "not_found_error", "model_not_found_error":
		return http.StatusNotFound, true
	case "authentication_error":
		return http.StatusUnauthorized, true
	case "permission_error":
		return http.StatusForbidden, true
	case "rate_limit_error":
		return http.StatusTooManyRequests, true
	case "invalid_request_error":
		return http.StatusBadRequest, true
	}
	return 0, false
}

// statusFromErrorMessage is the last resort for gateways that send only a
// prose message. The keyword pairs are deliberately specific so a model-related
// refusal is not mistaken for a generic bad request.
func statusFromErrorMessage(message string) int {
	msg := strings.ToLower(message)
	switch {
	case strings.Contains(msg, "not supported") && strings.Contains(msg, "model"),
		strings.Contains(msg, "unknown model"),
		strings.Contains(msg, "model not found"),
		strings.Contains(msg, "does not exist") && strings.Contains(msg, "model"):
		return http.StatusNotFound
	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "too many requests"):
		return http.StatusTooManyRequests
	case strings.Contains(msg, "api key"), strings.Contains(msg, "unauthorized"), strings.Contains(msg, "authentication"):
		return http.StatusUnauthorized
	}
	return http.StatusBadGateway
}

// requestID pulls the gateway request id from ctx for upstream correlation.
func requestID(ctx context.Context) string { return reqid.From(ctx) }

// decodeResponseBody returns a reader over the response body, transparently
// decompressing it when the upstream set Content-Encoding to gzip/deflate and
// Go's transport did not already decompress it (which happens when the outbound
// request carried an explicit Accept-Encoding, e.g. from ExtraHeaders).
//
// When Go's transport auto-decompresses, it strips the Content-Encoding header,
// so this function correctly sees no encoding and returns the body unchanged.
// The caller remains responsible for closing resp.Body.
func decodeResponseBody(resp *http.Response) (io.ReadCloser, error) {
	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	switch enc {
	case "", "identity":
		return resp.Body, nil
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		return &wrappedBody{Reader: zr, closer: resp.Body, decomp: zr}, nil
	case "deflate":
		fr := flate.NewReader(resp.Body)
		return &wrappedBody{Reader: fr, closer: resp.Body, decomp: fr}, nil
	default:
		// Unknown encoding (e.g. br): we cannot decode it here. Return the body
		// as-is; scrubbing Accept-Encoding upstream normally prevents this.
		return resp.Body, nil
	}
}

// wrappedBody adapts a decompressing reader to io.ReadCloser, closing both the
// decompressor and the underlying transport body.
type wrappedBody struct {
	io.Reader
	closer io.Closer
	decomp io.Closer
}

func (b *wrappedBody) Close() error {
	if b.decomp != nil {
		_ = b.decomp.Close()
	}
	if b.closer != nil {
		return b.closer.Close()
	}
	return nil
}
