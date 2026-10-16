package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/transport/warp"
)

func TestHandleGetWarpStatus(t *testing.T) {
	t.Parallel()

	mgr := warp.NewManager(nil, "")
	deps := RouterDeps{
		WarpManager: mgr,
	}

	req := httptest.NewRequest(http.MethodGet, "/api/warp/status", nil)
	rr := httptest.NewRecorder()

	deps.handleGetWarpStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var st warp.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal status error: %v", err)
	}

	if st.Enabled {
		t.Errorf("expected initial status to have Enabled = false")
	}
}

func TestHandleRotateWarp_Unauthorized(t *testing.T) {
	t.Parallel()

	mgr := warp.NewManager(nil, "")
	deps := RouterDeps{
		AdminToken:  "secret-admin-token",
		WarpManager: mgr,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/warp/rotate", nil)
	rr := httptest.NewRecorder()

	deps.handleRotateWarp(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}
}

// newWarpPoolRequest builds an authorized pool-size request with the given body.
func newWarpPoolRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/warp/pool-size", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-admin-token")
	return req
}

func TestHandleSetWarpPoolSize_PersistsAndApplies(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr := warp.NewManager(nil, "")
	deps := RouterDeps{
		AdminToken:  "secret-admin-token",
		WarpManager: mgr,
		ConfigDir:   dir,
	}

	rr := httptest.NewRecorder()
	deps.handleSetWarpPoolSize(rr, newWarpPoolRequest(`{"pool_size":42}`))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	var st warp.Status
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if st.PoolSize != 42 {
		t.Fatalf("status pool_size = %d, want 42", st.PoolSize)
	}

	// The engine must actually carry the new ceiling, not just report it.
	if got := mgr.Status().PoolSize; got != 42 {
		t.Fatalf("engine pool_size = %d, want 42", got)
	}

	cfg, err := config.LoadWarp(dir)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if cfg.PoolSize != 42 {
		t.Fatalf("persisted pool_size = %d, want 42", cfg.PoolSize)
	}
}

func TestHandleSetWarpPoolSize_Unauthorized(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	deps := RouterDeps{
		AdminToken:  "secret-admin-token",
		WarpManager: warp.NewManager(nil, ""),
		ConfigDir:   dir,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/warp/pool-size", strings.NewReader(`{"pool_size":30}`))
	rr := httptest.NewRecorder()
	deps.handleSetWarpPoolSize(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}

	// A rejected caller must not be able to change persisted state.
	if _, err := os.Stat(filepath.Join(dir, config.FileNameWarp)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("warp.json should not exist after a rejected request (err=%v)", err)
	}
}

func TestHandleSetWarpPoolSize_RejectsOutOfRange(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{"pool_size":0}`, `{"pool_size":-1}`, `{"pool_size":65}`, `{"pool_size":1000000000}`} {
		dir := t.TempDir()
		mgr := warp.NewManager(nil, "")
		deps := RouterDeps{
			AdminToken:  "secret-admin-token",
			WarpManager: mgr,
			ConfigDir:   dir,
		}

		rr := httptest.NewRecorder()
		deps.handleSetWarpPoolSize(rr, newWarpPoolRequest(body))

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %s: expected status 400, got %d (%s)", body, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "pool_size must be between") {
			t.Fatalf("body %s: unexpected error message %s", body, rr.Body.String())
		}
		// The engine keeps its previous ceiling and nothing is persisted.
		if got := mgr.Status().PoolSize; got != warp.DefaultPoolSize {
			t.Fatalf("body %s: engine pool_size = %d, want untouched %d", body, got, warp.DefaultPoolSize)
		}
		if _, err := os.Stat(filepath.Join(dir, config.FileNameWarp)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("body %s: warp.json must not be written for a rejected value", body)
		}
	}
}

func TestHandleSetWarpPoolSize_RejectsMalformedBody(t *testing.T) {
	t.Parallel()

	deps := RouterDeps{
		AdminToken:  "secret-admin-token",
		WarpManager: warp.NewManager(nil, ""),
	}

	rr := httptest.NewRecorder()
	deps.handleSetWarpPoolSize(rr, newWarpPoolRequest(`{"pool_size":`))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "invalid JSON body") {
		t.Fatalf("unexpected error message %s", rr.Body.String())
	}
}

func TestHandleSetWarpPoolSize_RejectsUninitializedEngine(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	deps := RouterDeps{
		AdminToken: "secret-admin-token",
		ConfigDir:  dir,
	}

	rr := httptest.NewRecorder()
	deps.handleSetWarpPoolSize(rr, newWarpPoolRequest(`{"pool_size":30}`))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
	// The engine check runs before the write, so a refused request cannot leave
	// a file behind that claims a size nothing applied.
	if _, err := os.Stat(filepath.Join(dir, config.FileNameWarp)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("warp.json must not be written when the engine is missing")
	}
}
