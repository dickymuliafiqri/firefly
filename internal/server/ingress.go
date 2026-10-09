package server

import (
	"errors"
	"net/http"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/tidwall/gjson"
)

// ingress abstracts the client-facing wire protocol so a single forward
// pipeline serves both the OpenAI and the Anthropic surface. Everything after
// body parsing — routing, key selection, token saver, usage, cost — is
// protocol-agnostic and shared; only the edges differ.
type ingress interface {
	// Name identifies the surface in logs and traces.
	Name() string
	// ParseBody validates the raw request and returns the OpenAI-shaped body
	// the pipeline and every adapter expect, plus the routing fields.
	ParseBody(body []byte) (openAIBody []byte, model string, stream bool, tokensIn int, err error)
	// WriteError renders a failure in this surface's error envelope.
	WriteError(w http.ResponseWriter, status int, msg string)
	// WriteResolveError renders a routing failure in this surface's envelope.
	WriteResolveError(w http.ResponseWriter, err error)
	// WrapWriter wraps the response writer so upstream bytes are rewritten into
	// this surface's wire format. It may return w unchanged.
	WrapWriter(w http.ResponseWriter, model string, stream bool) http.ResponseWriter
}

// openAIIngress is the default surface: the client already speaks OpenAI, so
// the body passes through untouched and the response is relayed verbatim.
type openAIIngress struct{}

func (openAIIngress) Name() string { return "openai" }

func (openAIIngress) ParseBody(body []byte) ([]byte, string, bool, int, error) {
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		return nil, "", false, 0, errors.New("you must provide a model parameter")
	}
	return body, model, gjson.GetBytes(body, "stream").Bool(),
		estimateInputTokens(body), nil
}

func (openAIIngress) WriteError(w http.ResponseWriter, status int, msg string) {
	openai.WriteError(w, status, openai.StatusToType(status), msg)
}

func (openAIIngress) WriteResolveError(w http.ResponseWriter, err error) {
	writeResolveError(w, err)
}

func (openAIIngress) WrapWriter(w http.ResponseWriter, _ string, _ bool) http.ResponseWriter {
	return w
}
