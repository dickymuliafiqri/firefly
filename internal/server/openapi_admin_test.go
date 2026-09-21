package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// loadAdminSpec parses the embedded admin OpenAPI document.
func loadAdminSpec(t *testing.T) *specDoc {
	t.Helper()
	var doc specDoc
	if err := yaml.Unmarshal(openAPISpecAdmin, &doc); err != nil {
		t.Fatalf("openapi-admin.yaml is not valid YAML: %v", err)
	}
	return &doc
}

func TestOpenAPIAdminSpec_Structure(t *testing.T) {
	doc := loadAdminSpec(t)

	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Errorf("openapi version = %q, want 3.1.x", doc.OpenAPI)
	}
	if doc.Info.Title == "" || doc.Info.Version == "" {
		t.Error("info.title and info.version are required")
	}
	if len(doc.Paths) == 0 {
		t.Fatal("spec has no paths")
	}
	if _, ok := doc.Components.SecuritySchemes["AdminAuth"]; !ok {
		t.Error("missing securityScheme AdminAuth")
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

// TestOpenAPIAdminSpec_RefsResolve ensures every local $ref points at a
// component that actually exists. The admin spec also references path
// parameters, so the section set is wider than the public spec's test.
func TestOpenAPIAdminSpec_RefsResolve(t *testing.T) {
	var raw map[string]any
	if err := yaml.Unmarshal(openAPISpecAdmin, &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
	refRe := regexp.MustCompile(`#/components/(schemas|responses|securitySchemes|parameters)/([A-Za-z0-9_]+)`)
	text := string(openAPISpecAdmin)
	for _, m := range refRe.FindAllStringSubmatch(text, -1) {
		section, name := m[1], m[2]
		comps, _ := raw["components"].(map[string]any)
		sec, _ := comps[section].(map[string]any)
		if _, ok := sec[name]; !ok {
			t.Errorf("$ref %s points at missing components/%s/%s", m[0], section, name)
		}
	}
}

// adminRoutesFromSource extracts the METHOD /api/tenants* routes registered in
// router.go so the admin spec is checked against the actual mux registrations
// rather than a hand-maintained list.
func adminRoutesFromSource(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	re := regexp.MustCompile(`"(GET|POST|PUT|DELETE) (/api/tenants[A-Za-z0-9/_{}]*)"`)
	routes := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		// Skip OPTIONS/preflight; the spec documents the functional verbs only.
		routes[m[1]+" "+m[2]] = true
	}
	return routes
}

// TestOpenAPIAdminSpec_NoDriftFromRoutes pins the admin spec to the registered
// tenant routes in both directions: every route documented, nothing phantom.
func TestOpenAPIAdminSpec_NoDriftFromRoutes(t *testing.T) {
	doc := loadAdminSpec(t)
	spec := specPaths(doc)
	routes := adminRoutesFromSource(t)

	var undocumented []string
	for r := range routes {
		if !spec[r] {
			undocumented = append(undocumented, r)
		}
	}
	var phantom []string
	for s := range spec {
		if !routes[s] {
			phantom = append(phantom, s)
		}
	}

	sort.Strings(undocumented)
	sort.Strings(phantom)
	if len(undocumented) > 0 {
		t.Errorf("tenant routes registered but missing from openapi-admin.yaml:\n  %s", strings.Join(undocumented, "\n  "))
	}
	if len(phantom) > 0 {
		t.Errorf("paths documented in openapi-admin.yaml but not registered as routes:\n  %s", strings.Join(phantom, "\n  "))
	}
}

// TestOpenAPIAdminSpec_SanityRouteCount guards against the drift test silently
// passing because it extracted zero routes (a regex/refactor regression).
func TestOpenAPIAdminSpec_SanityRouteCount(t *testing.T) {
	routes := adminRoutesFromSource(t)
	// GET+POST /api/tenants, GET+PUT+DELETE /api/tenants/{name}, POST /api/tenants/topup = 6
	if len(routes) != 6 {
		t.Fatalf("expected 6 tenant routes extracted from router.go, got %d: %v", len(routes), routes)
	}
}

// TestServeOpenAPIAdminSpec confirms the admin spec is served as YAML.
func TestServeOpenAPIAdminSpec(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/openapi-admin.yaml", nil)
	RouterDeps{}.handleOpenAPISpecAdmin(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("content-type = %q, want yaml", ct)
	}
	if !strings.Contains(rec.Body.String(), "openapi: 3.1") {
		t.Error("served admin spec does not contain the openapi version marker")
	}
}

// TestServeAPIDocsAdmin confirms the interactive admin docs page renders and
// references the admin spec URL (and not the public one).
func TestServeAPIDocsAdmin(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/docs/admin", nil)
	RouterDeps{}.handleAPIDocsAdmin(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/api/openapi-admin.yaml") {
		t.Error("admin docs page does not reference the admin spec URL")
	}
	if strings.Contains(body, `data-url="/api/openapi.yaml"`) {
		t.Error("admin docs page points at the public spec")
	}
}

// TestOpenAPIAdminSpec_VersionStampedFromBuild mirrors the public-spec guard:
// SetBuildVersion must override the placeholder in the admin document too.
func TestOpenAPIAdminSpec_VersionStampedFromBuild(t *testing.T) {
	const want = "v9.9.9-test"

	specMu.RLock()
	prev := servedSpecAdmin
	specMu.RUnlock()
	t.Cleanup(func() {
		specMu.Lock()
		servedSpecAdmin = prev
		specMu.Unlock()
	})

	SetBuildVersion(want)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/openapi-admin.yaml", nil)
	RouterDeps{}.handleOpenAPISpecAdmin(rec, req)

	var served specDoc
	if err := yaml.Unmarshal(rec.Body.Bytes(), &served); err != nil {
		t.Fatalf("served admin spec is not valid YAML: %v", err)
	}
	if served.Info.Version != want {
		t.Errorf("served admin info.version = %q, want %q", served.Info.Version, want)
	}

	var embedded specDoc
	if err := yaml.Unmarshal(openAPISpecAdmin, &embedded); err != nil {
		t.Fatalf("embedded admin spec: %v", err)
	}
	if len(served.Paths) != len(embedded.Paths) {
		t.Errorf("stamped admin spec has %d paths, embedded has %d", len(served.Paths), len(embedded.Paths))
	}
}

// TestOpenAPIAdminSpec_UsedTokensReadOnly pins the security-relevant contract
// that metered consumption is never writable through the CRUD surface.
func TestOpenAPIAdminSpec_UsedTokensReadOnly(t *testing.T) {
	var raw map[string]any
	if err := yaml.Unmarshal(openAPISpecAdmin, &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}

	tenant := dig(t, raw, "components", "schemas", "Tenant")
	props := tenant["properties"].(map[string]any)
	used, ok := props["used_tokens"].(map[string]any)
	if !ok {
		t.Fatal("Tenant.used_tokens missing")
	}
	if used["readOnly"] != true {
		t.Error("Tenant.used_tokens must be readOnly: true")
	}

	// The update payload must not even offer used_tokens.
	update := dig(t, raw, "components", "schemas", "TenantUpdate")
	upProps := update["properties"].(map[string]any)
	if _, ok := upProps["used_tokens"]; ok {
		t.Error("TenantUpdate exposes used_tokens; the handler ignores it by design")
	}
}
