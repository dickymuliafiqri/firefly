package server

import (
	_ "embed"
	"net/http"
)

// openAPISpec is the canonical OpenAPI 3.1 document for the public /v1 API.
// It is the single source of truth; openapi_test.go validates it against the
// routes actually registered in the mux so the two can never silently diverge.
//
//go:embed openapi/openapi.yaml
var openAPISpec []byte

// scalarDocsHTML renders the embedded spec with Scalar, a zero-build API
// reference UI loaded from a CDN. The spec itself is served locally at
// /api/openapi.yaml, so the docs work offline except for the Scalar bundle.
const scalarDocsHTML = `<!doctype html>
<html>
  <head>
    <title>Firefly API Reference</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <script id="api-reference" data-url="/api/openapi.yaml"></script>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
  </body>
</html>`

// handleOpenAPISpec serves the raw OpenAPI YAML document.
func (deps RouterDeps) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openAPISpec)
}

// handleAPIDocs serves an interactive API reference page for the public API.
func (deps RouterDeps) handleAPIDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(scalarDocsHTML))
}
