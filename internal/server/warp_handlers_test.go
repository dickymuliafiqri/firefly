package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/warp"
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
