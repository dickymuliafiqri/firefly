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

// loadHarvesterSpec parses the embedded harvester OpenAPI document.
func loadHarvesterSpec(t *testing.T) *specDoc {
	t.Helper()
	var doc specDoc
	if err := yaml.Unmarshal(openAPISpecHarvester, &doc); err != nil {
		t.Fatalf("openapi-harvester.yaml is not valid YAML: %v", err)
	}
	return &doc
}

func TestOpenAPIHarvesterSpec_Structure(t *testing.T) {
	doc := loadHarvesterSpec(t)

	if !strings.HasPrefix(doc.OpenAPI, "3.1") {
		t.Errorf("openapi version = %q, want 3.1.x", doc.OpenAPI)
	}
	if doc.Info.Title == "" || doc.Info.Version == "" {
		t.Error("info.title and info.version are required")
	}
	if len(doc.Paths) == 0 {
		t.Fatal("spec has no paths")
	}
	if _, ok := doc.Components.SecuritySchemes["ServiceAuth"]; !ok {
		t.Error("missing securityScheme ServiceAuth")
	}

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

// TestOpenAPIHarvesterSpec_RefsResolve ensures every local $ref points at a
// component that actually exists.
func TestOpenAPIHarvesterSpec_RefsResolve(t *testing.T) {
	var raw map[string]any
	if err := yaml.Unmarshal(openAPISpecHarvester, &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
	refRe := regexp.MustCompile(`#/components/(schemas|responses|securitySchemes|parameters)/([A-Za-z0-9_]+)`)
	text := string(openAPISpecHarvester)
	for _, m := range refRe.FindAllStringSubmatch(text, -1) {
		section, name := m[1], m[2]
		comps, _ := raw["components"].(map[string]any)
		sec, _ := comps[section].(map[string]any)
		if _, ok := sec[name]; !ok {
			t.Errorf("$ref %s points at missing components/%s/%s", m[0], section, name)
		}
	}
}

// harvesterRoutesFromSource extracts the METHOD /api/harvester* routes registered
// in router.go so the spec is checked against the actual mux registrations.
func harvesterRoutesFromSource(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}
	re := regexp.MustCompile(`"(GET|POST|PUT|DELETE) (/api/harvester[A-Za-z0-9/_{}]*)"`)
	routes := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		routes[m[1]+" "+m[2]] = true
	}
	return routes
}

// TestOpenAPIHarvesterSpec_NoDriftFromRoutes pins the spec to the registered
// harvester routes in both directions: every route documented, nothing phantom.
func TestOpenAPIHarvesterSpec_NoDriftFromRoutes(t *testing.T) {
	doc := loadHarvesterSpec(t)
	spec := specPaths(doc)
	routes := harvesterRoutesFromSource(t)

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
		t.Errorf("harvester routes registered but missing from openapi-harvester.yaml:\n  %s", strings.Join(undocumented, "\n  "))
	}
	if len(phantom) > 0 {
		t.Errorf("paths documented in openapi-harvester.yaml but not registered as routes:\n  %s", strings.Join(phantom, "\n  "))
	}
}

// TestOpenAPIHarvesterSpec_SanityRouteCount guards against the drift test
// silently passing because it extracted zero routes (a regex/refactor regression).
func TestOpenAPIHarvesterSpec_SanityRouteCount(t *testing.T) {
	routes := harvesterRoutesFromSource(t)
	// POST /api/harvester/sync is the whole surface: no CORS preflight route.
	if len(routes) != 1 || !routes["POST /api/harvester/sync"] {
		t.Fatalf("expected exactly POST /api/harvester/sync in router.go, got %v", routes)
	}
}

// TestServeOpenAPIHarvesterSpec confirms the harvester spec is served as YAML.
func TestServeOpenAPIHarvesterSpec(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/openapi-harvester.yaml", nil)
	RouterDeps{}.handleOpenAPISpecHarvester(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("content-type = %q, want yaml", ct)
	}
	if !strings.Contains(rec.Body.String(), "openapi: 3.1") {
		t.Error("served harvester spec does not contain the openapi version marker")
	}
}

// TestOpenAPIHarvesterSpec_VersionStampedFromBuild mirrors the public-spec guard:
// SetBuildVersion must override the placeholder in the harvester document too.
func TestOpenAPIHarvesterSpec_VersionStampedFromBuild(t *testing.T) {
	const want = "v9.9.9-test"

	specMu.RLock()
	prev := servedSpecHarvester
	specMu.RUnlock()
	t.Cleanup(func() {
		specMu.Lock()
		servedSpecHarvester = prev
		specMu.Unlock()
	})

	SetBuildVersion(want)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/openapi-harvester.yaml", nil)
	RouterDeps{}.handleOpenAPISpecHarvester(rec, req)

	var served specDoc
	if err := yaml.Unmarshal(rec.Body.Bytes(), &served); err != nil {
		t.Fatalf("served harvester spec is not valid YAML: %v", err)
	}
	if served.Info.Version != want {
		t.Errorf("served harvester info.version = %q, want %q", served.Info.Version, want)
	}

	var embedded specDoc
	if err := yaml.Unmarshal(openAPISpecHarvester, &embedded); err != nil {
		t.Fatalf("embedded harvester spec: %v", err)
	}
	if len(served.Paths) != len(embedded.Paths) {
		t.Errorf("stamped harvester spec has %d paths, embedded has %d", len(served.Paths), len(embedded.Paths))
	}
}

// TestOpenAPIHarvesterSpec_DocumentsOnlyNonSecretFields pins the security
// contract: the sync response schema exposes ids and counters, and no schema
// offers a field that would echo a credential or the vault blob back.
func TestOpenAPIHarvesterSpec_DocumentsOnlyNonSecretFields(t *testing.T) {
	var raw map[string]any
	if err := yaml.Unmarshal(openAPISpecHarvester, &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}

	resp := dig(t, raw, "components", "schemas", "SyncResponse")
	props := resp["properties"].(map[string]any)
	if _, ok := props["api_key"]; ok {
		t.Error("SyncResponse documents an api_key field; responses must never echo secrets")
	}
	if _, ok := props["account_metadata"]; ok {
		t.Error("SyncResponse documents account_metadata; the vault blob is write-only")
	}
	for _, required := range []string{"created", "updated", "unchanged", "deactivated", "key_ids", "revision", "pushed", "reloaded"} {
		if _, ok := props[required]; !ok {
			t.Errorf("SyncResponse is missing the %q property the handler returns", required)
		}
	}

	// A key entry accepts account_metadata but must label it write-only, since a
	// generated client otherwise assumes it is a readable field.
	entry := dig(t, raw, "components", "schemas", "KeyEntry")
	entryProps := entry["properties"].(map[string]any)
	meta, ok := entryProps["account_metadata"].(map[string]any)
	if !ok {
		t.Fatal("KeyEntry.account_metadata missing")
	}
	desc, _ := meta["description"].(string)
	if !strings.Contains(desc, "Never returned") {
		t.Error("KeyEntry.account_metadata must document that it is never returned")
	}
}

// TestOpenAPIHarvesterSpec_ExpiryIntentIsDocumented pins the expires_at
// three-way contract in prose. The harvester is the only writer of this field
// for a running pool, so a spec that claims "omitted clears" would teach a
// client to un-expire keys with every refresh batch.
func TestOpenAPIHarvesterSpec_ExpiryIntentIsDocumented(t *testing.T) {
	var raw map[string]any
	if err := yaml.Unmarshal(openAPISpecHarvester, &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}

	entry := dig(t, raw, "components", "schemas", "KeyEntry")
	entryProps := entry["properties"].(map[string]any)
	expiry, ok := entryProps["expires_at"].(map[string]any)
	if !ok {
		t.Fatal("KeyEntry.expires_at missing")
	}
	desc, _ := expiry["description"].(string)
	if !strings.Contains(desc, "omitted") || !strings.Contains(desc, "keeps") {
		t.Errorf("KeyEntry.expires_at must document that an omitted value keeps the stored expiry: %q", desc)
	}
	if !strings.Contains(desc, "null") {
		t.Error("KeyEntry.expires_at must document that an explicit null clears the expiry")
	}
}

// TestOpenAPIHarvesterSpec_DeleteNotOffered guards the explicit-id-only
// deactivation contract: the surface must not grow a DELETE verb that would let
// a caller remove rows implicitly.
func TestOpenAPIHarvesterSpec_DeleteNotOffered(t *testing.T) {
	doc := loadHarvesterSpec(t)
	for path, item := range doc.Paths {
		if _, ok := item["delete"]; ok {
			t.Errorf("DELETE %s is documented; deactivation is explicit-id only", path)
		}
	}
}
