package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/registry"
)

func TestFrontendEmbeddedRoutes(t *testing.T) {
	reg := registry.New()
	deps := RouterDeps{
		Snapshots: reg,
		Registry:  reg,
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)
	handler := s.Handler()

	// 1. Test GET / (index.html)
	{
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200 for /, got %d", rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			t.Fatalf("expected text/html for /, got %s", ct)
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte("Firefly")) {
			t.Fatalf("expected html content containing Firefly, got %s", rec.Body.String())
		}
	}

	// 2. Test SPA tab route GET /telemetry (should serve index.html)
	{
		req := httptest.NewRequest(http.MethodGet, "/telemetry", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200 for /telemetry, got %d", rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			t.Fatalf("expected text/html for /telemetry, got %s", ct)
		}
	}

	// 3. Test GET /healthz (bypass frontend)
	{
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200 for /healthz, got %d", rec.Code)
		}
		if rec.Body.String() != "ok" {
			t.Fatalf("expected 'ok' for /healthz, got %s", rec.Body.String())
		}
	}
}
