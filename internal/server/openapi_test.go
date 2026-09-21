package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// --- Spec structural validation ----------------------------------------------

type specDoc struct {
	OpenAPI string `yaml:"openapi"`
	Info    struct {
		Title   string `yaml:"title"`
		Version string `yaml:"version"`
	} `yaml:"info"`
	Paths      map[string]map[string]specOp `yaml:"paths"`
	Components struct {
		Schemas         map[string]any `yaml:"schemas"`
		Responses       map[string]any `yaml:"responses"`
		SecuritySchemes map[string]any `yaml:"securitySchemes"`
	} `yaml:"components"`
}

type specOp struct {
	OperationID string         `yaml:"operationId"`
	Responses   map[string]any `yaml:"responses"`
	Summary     string         `yaml:"summary"`
}

// httpMethods are the operation keys we treat as HTTP verbs in a path item.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

func loadSpec(t *testing.T) *specDoc {
	t.Helper()
	var doc specDoc
	if err := yaml.Unmarshal(openAPISpec, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	return &doc
}

func TestOpenAPISpec_Structure(t *testing.T) {
	doc := loadSpec(t)

	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Errorf("openapi version = %q, want 3.1.x", doc.OpenAPI)
	}
	if doc.Info.Title == "" || doc.Info.Version == "" {
		t.Error("info.title and info.version are required")
	}
	if len(doc.Paths) == 0 {
		t.Fatal("spec has no paths")
	}
	if _, ok := doc.Components.SecuritySchemes["TenantKey"]; !ok {
		t.Error("missing securityScheme TenantKey")
	}

	// Every operation must declare at least one response, and any operationId
	// must be unique.
	seenOpIDs := map[string]string{}
	for path, item := range doc.Paths {
		for method, op := range item {
			if !httpMethods[strings.ToLower(method)] {
				continue
			}
			if len(op.Responses) == 0 {
				t.Errorf("%s %s: no responses declared", strings.ToUpper(method), path)
			}
			if op.OperationID != "" {
				if prev, dup := seenOpIDs[op.OperationID]; dup {
					t.Errorf("duplicate operationId %q on %s %s and %s", op.OperationID, method, path, prev)
				}
				seenOpIDs[op.OperationID] = method + " " + path
			}
		}
	}
}

// TestOpenAPISpec_RefsResolve ensures every local $ref points at a component
// that actually exists (a common source of silent spec rot).
func TestOpenAPISpec_RefsResolve(t *testing.T) {
	var raw map[string]any
	if err := yaml.Unmarshal(openAPISpec, &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
	refRe := regexp.MustCompile(`#/components/(schemas|responses|securitySchemes)/([A-Za-z0-9_]+)`)
	text := string(openAPISpec)
	for _, m := range refRe.FindAllStringSubmatch(text, -1) {
		section, name := m[1], m[2]
		comps, _ := raw["components"].(map[string]any)
		sec, _ := comps[section].(map[string]any)
		if _, ok := sec[name]; !ok {
			t.Errorf("$ref %s points at missing components/%s/%s", m[0], section, name)
		}
	}
}

// --- Anti-drift: spec paths <-> registered routes ----------------------------

// publicRoutesFromSource extracts the METHOD+/v1|/healthz routes registered in
// router.go so the spec is checked against the actual mux registrations rather
// than a hand-maintained list. It reads the source file so a newly added route
// (or a removed one) immediately shows up here.
func publicRoutesFromSource(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	// Matches: "GET /v1/models", "POST "+route.local (handled separately), etc.
	// We capture string literals of the form "METHOD /path" for /v1 and /healthz.
	re := regexp.MustCompile(`"(GET|POST|PUT|DELETE) (/v1/[A-Za-z0-9/_{}]*|/healthz)"`)
	routes := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		method, path := m[1], m[2]
		// Skip OPTIONS/preflight; the spec documents the functional verbs only.
		routes[method+" "+normalizePath(path)] = true
	}
	// The three inference endpoints are registered in a loop with string
	// concatenation ("POST "+route.local), so add them explicitly from the
	// same literals present in the loop body.
	loopRe := regexp.MustCompile(`\{"(/v1/[A-Za-z0-9/_]*)",\s*"`)
	for _, m := range loopRe.FindAllStringSubmatch(string(src), -1) {
		routes["POST "+m[1]] = true
	}
	return routes
}

// normalizePath converts Go 1.22 mux path params ({id}) — already the same
// syntax OpenAPI uses — and strips any trailing slash noise.
func normalizePath(p string) string {
	return p
}

func specPaths(doc *specDoc) map[string]bool {
	out := map[string]bool{}
	for path, item := range doc.Paths {
		for method := range item {
			if !httpMethods[strings.ToLower(method)] {
				continue
			}
			out[strings.ToUpper(method)+" "+path] = true
		}
	}
	return out
}

func TestOpenAPISpec_NoDriftFromRoutes(t *testing.T) {
	doc := loadSpec(t)
	spec := specPaths(doc)
	routes := publicRoutesFromSource(t)

	// Every registered public route must be documented.
	var undocumented []string
	for r := range routes {
		if !spec[r] {
			undocumented = append(undocumented, r)
		}
	}
	// Every documented path must correspond to a registered route (no phantom
	// endpoints in the docs).
	var phantom []string
	for s := range spec {
		if !routes[s] {
			phantom = append(phantom, s)
		}
	}

	sort.Strings(undocumented)
	sort.Strings(phantom)
	if len(undocumented) > 0 {
		t.Errorf("public routes registered but missing from openapi.yaml:\n  %s", strings.Join(undocumented, "\n  "))
	}
	if len(phantom) > 0 {
		t.Errorf("paths documented in openapi.yaml but not registered as routes:\n  %s", strings.Join(phantom, "\n  "))
	}
}

// TestOpenAPISpec_AdmissionDocumented guards the fifth drift class: every
// public route except /healthz sits behind per-tenant admission control (and
// the server-wide global limiter), so any of them can answer 429. The spec
// must say so, or client retry logic built from the docs will misbehave.
func TestOpenAPISpec_AdmissionDocumented(t *testing.T) {
	doc := loadSpec(t)

	var missing []string
	for route := range publicRoutesFromSource(t) {
		method, path, _ := strings.Cut(route, " ")
		if path == "/healthz" {
			continue // liveness probe bypasses admission by design
		}
		item, ok := doc.Paths[path]
		if !ok {
			continue // path-level drift is reported by TestOpenAPISpec_NoDriftFromRoutes
		}
		op, ok := item[strings.ToLower(method)]
		if !ok {
			continue
		}
		if _, ok := op.Responses["429"]; !ok {
			missing = append(missing, route)
		}
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("admission-protected routes missing a 429 response in openapi.yaml:\n  %s", strings.Join(missing, "\n  "))
	}
}

// TestOpenAPISpec_SanityRouteCount guards against the drift test silently
// passing because it extracted zero routes (a regex/refactor regression).
func TestOpenAPISpec_SanityRouteCount(t *testing.T) {
	routes := publicRoutesFromSource(t)
	// /healthz + /v1/models + /v1/models/{id} + /v1/usage + 3 inference + /v1/compress = 8
	if len(routes) < 8 {
		t.Fatalf("expected >=8 public routes extracted from router.go, got %d: %v", len(routes), routes)
	}
}

// TestServeOpenAPISpec confirms the spec is actually served as YAML.
func TestServeOpenAPISpec(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)
	RouterDeps{}.handleOpenAPISpec(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("content-type = %q, want yaml", ct)
	}
	if !strings.Contains(rec.Body.String(), "openapi: 3.1") {
		t.Error("served spec does not contain the openapi version marker")
	}
}

// TestServeAPIDocs confirms the interactive docs page renders and references the spec.
func TestServeAPIDocs(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/docs", nil)
	RouterDeps{}.handleAPIDocs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/api/openapi.yaml") {
		t.Error("docs page does not reference the spec URL")
	}
}

// TestOpenAPISpec_VersionStampedFromBuild guards against info.version drifting
// away from the binary that serves it: SetBuildVersion must override the
// placeholder baked into openapi.yaml.
func TestOpenAPISpec_VersionStampedFromBuild(t *testing.T) {
	const want = "v9.9.9-test"

	// The stamp mutates package state; restore it so later tests see the
	// embedded document.
	specMu.RLock()
	prev := servedSpec
	specMu.RUnlock()
	t.Cleanup(func() {
		specMu.Lock()
		servedSpec = prev
		specMu.Unlock()
	})

	SetBuildVersion(want)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)
	RouterDeps{}.handleOpenAPISpec(rec, req)

	var served specDoc
	if err := yaml.Unmarshal(rec.Body.Bytes(), &served); err != nil {
		t.Fatalf("served spec is not valid YAML: %v", err)
	}
	if served.Info.Version != want {
		t.Errorf("served info.version = %q, want %q", served.Info.Version, want)
	}

	// Stamping rewrites exactly one line; the rest of the document must survive.
	var embedded specDoc
	if err := yaml.Unmarshal(openAPISpec, &embedded); err != nil {
		t.Fatalf("embedded spec: %v", err)
	}
	if len(served.Paths) != len(embedded.Paths) {
		t.Errorf("stamped spec has %d paths, embedded has %d", len(served.Paths), len(embedded.Paths))
	}
	if len(served.Components.Schemas) != len(embedded.Components.Schemas) {
		t.Errorf("stamped spec has %d schemas, embedded has %d",
			len(served.Components.Schemas), len(embedded.Components.Schemas))
	}
}

// TestOpenAPISpec_VersionStampRejectsUnsafeValues ensures a version that cannot
// be embedded verbatim leaves the document untouched rather than producing
// malformed YAML.
func TestOpenAPISpec_VersionStampRejectsUnsafeValues(t *testing.T) {
	specMu.RLock()
	prev := servedSpec
	specMu.RUnlock()
	t.Cleanup(func() {
		specMu.Lock()
		servedSpec = prev
		specMu.Unlock()
	})

	for _, bad := range []string{"", "   ", `1.0" injected`, "a\nb"} {
		SetBuildVersion(bad)
		specMu.RLock()
		got := servedSpec
		specMu.RUnlock()
		if !bytes.Equal(got, prev) {
			t.Errorf("SetBuildVersion(%q) modified the served spec", bad)
		}
	}
}

// TestOpenAPISpec_CompressContract pins the two /v1/compress behaviours that
// previously drifted from the implementation: the 200 body is a union rather
// than always a CompressResult, and the request tolerates unknown fields.
func TestOpenAPISpec_CompressContract(t *testing.T) {
	var raw map[string]any
	if err := yaml.Unmarshal(openAPISpec, &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}

	op := dig(t, raw, "paths", "/v1/compress", "post")
	responses := op["responses"].(map[string]any)
	for _, code := range []string{"200", "400", "401", "413", "429"} {
		if _, ok := responses[code]; !ok {
			t.Errorf("/v1/compress does not document response %s", code)
		}
	}

	jsonContent := responses["200"].(map[string]any)["content"].(map[string]any)
	schema := jsonContent["application/json"].(map[string]any)["schema"].(map[string]any)
	branches, ok := schema["oneOf"].([]any)
	if !ok {
		t.Fatal("200 schema is not a oneOf, so the messages passthrough shape is undocumented")
	}
	if len(branches) != 2 {
		t.Errorf("200 oneOf has %d branches, want 2 (CompressResult and the messages passthrough)", len(branches))
	}

	// The handler reads fields with gjson and ignores the rest, so the request
	// must not claim to reject unknown fields.
	compressReq := dig(t, raw, "components", "schemas", "CompressRequest")
	if ap, ok := compressReq["additionalProperties"]; ok && ap == false {
		t.Error("CompressRequest declares additionalProperties: false but the handler ignores unknown fields")
	}
}

// dig walks nested YAML mappings and fails the test on a missing key.
func dig(t *testing.T, root map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := root
	for _, key := range path {
		next, ok := cur[key].(map[string]any)
		if !ok {
			t.Fatalf("spec is missing %s (want a mapping at %q)", strings.Join(path, "."), key)
		}
		cur = next
	}
	return cur
}
