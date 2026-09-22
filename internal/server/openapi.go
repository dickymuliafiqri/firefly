package server

import (
	"bytes"
	_ "embed"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

// openAPISpec is the canonical OpenAPI 3.1 document for the public /v1 API.
// It is the single source of truth; openapi_test.go validates it against the
// routes actually registered in the mux so the two can never silently diverge.
//
//go:embed openapi/openapi.yaml
var openAPISpec []byte

// openAPISpecAdmin is the OpenAPI 3.1 document for the admin /api surface
// (tenant lifecycle management). It is validated against the registered
// /api/tenants* routes by openapi_admin_test.go. Like the public spec it is
// static documentation and leaks no secrets, so it is served unauthenticated;
// the endpoints it describes remain admin-gated.
//
//go:embed openapi/openapi-admin.yaml
var openAPISpecAdmin []byte

// openAPISpecHarvester is the OpenAPI 3.1 document for the harvester
// credential-pool sync surface. It is validated against the registered
// /api/harvester* routes by openapi_harvester_test.go. Like the other two
// documents it is static description and leaks no credentials, so it is served
// unauthenticated; the endpoint it describes is service-token gated.
//
//go:embed openapi/openapi-harvester.yaml
var openAPISpecHarvester []byte

// servedSpec, servedSpecAdmin, and servedSpecHarvester are the document bodies
// actually returned to clients. Each begins as the embedded bytes and is swapped
// by SetBuildVersion for a copy whose info.version matches the running build.
// specMu lets handlers read concurrently with a stamp applied at startup.
var (
	specMu              sync.RWMutex
	servedSpec          = openAPISpec
	servedSpecAdmin     = openAPISpecAdmin
	servedSpecHarvester = openAPISpecHarvester
)

// infoVersionRe matches the info.version scalar. Two-space indent keeps it
// from colliding with `openapi: 3.1.0`, which is unquoted and unindented.
var infoVersionRe = regexp.MustCompile(`(?m)^  version: "[^"]*"$`)

// safeVersionRe restricts a stamped version to characters that need no YAML
// escaping, so it can be spliced into a double-quoted scalar verbatim. Release
// tags and the "dev" default both satisfy it.
var safeVersionRe = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

// stampVersion splices v into the info.version scalar of doc. When the
// pattern does not match, the embedded document is returned unchanged so a
// malformed doc surfaces as a stamping no-op instead of a corrupted spec.
func stampVersion(doc []byte, v string) []byte {
	stamped := infoVersionRe.ReplaceAllFunc(doc, func([]byte) []byte {
		return []byte(`  version: "` + v + `"`)
	})
	if bytes.Equal(stamped, doc) {
		return doc
	}
	return stamped
}

// SetBuildVersion stamps the running build's version into every served OpenAPI
// document so info.version cannot drift from the release that ships them. The
// entrypoint calls this once at startup from its ldflags-injected Version
// variable; the embedded placeholders are served when it is never called or
// called with a value that cannot be safely embedded.
func SetBuildVersion(v string) {
	v = strings.TrimSpace(v)
	if v == "" || !safeVersionRe.MatchString(v) {
		return
	}
	stamped := stampVersion(openAPISpec, v)
	stampedAdmin := stampVersion(openAPISpecAdmin, v)
	stampedHarvester := stampVersion(openAPISpecHarvester, v)
	specMu.Lock()
	servedSpec = stamped
	servedSpecAdmin = stampedAdmin
	servedSpecHarvester = stampedHarvester
	specMu.Unlock()
}

// scalarDocsTemplate renders an embedded spec with Scalar, a zero-build API
// reference UI loaded from a CDN. The __SPEC_URL__ and __TITLE__ placeholders
// are replaced per document; everything else is byte-identical so both pages
// stay in sync. The spec itself is served locally, so the docs work offline
// except for the Scalar bundle.
const scalarDocsTemplate = `<!doctype html>
<html>
  <head>
    <title>__TITLE__</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <script id="api-reference" data-url="__SPEC_URL__"></script>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
  </body>
</html>`

// scalarDocsPage renders the Scalar reference page pointing at specURL.
func scalarDocsPage(specURL, title string) string {
	replacer := strings.NewReplacer(
		"__SPEC_URL__", specURL,
		"__TITLE__", title,
	)
	return replacer.Replace(scalarDocsTemplate)
}

// handleOpenAPISpec serves the raw public OpenAPI YAML document.
func (deps RouterDeps) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	specMu.RLock()
	body := servedSpec
	specMu.RUnlock()

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// handleOpenAPISpecAdmin serves the raw admin OpenAPI YAML document.
func (deps RouterDeps) handleOpenAPISpecAdmin(w http.ResponseWriter, r *http.Request) {
	specMu.RLock()
	body := servedSpecAdmin
	specMu.RUnlock()

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// handleOpenAPISpecHarvester serves the raw harvester OpenAPI YAML document.
func (deps RouterDeps) handleOpenAPISpecHarvester(w http.ResponseWriter, r *http.Request) {
	specMu.RLock()
	body := servedSpecHarvester
	specMu.RUnlock()

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// handleAPIDocs serves an interactive API reference page for the public API.
func (deps RouterDeps) handleAPIDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(scalarDocsPage("/api/openapi.yaml", "Firefly API Reference")))
}

// handleAPIDocsAdmin serves an interactive API reference page for the admin API.
func (deps RouterDeps) handleAPIDocsAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(scalarDocsPage("/api/openapi-admin.yaml", "Firefly Admin API Reference")))
}
