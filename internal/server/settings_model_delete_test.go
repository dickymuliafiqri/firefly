package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
	"github.com/stretchr/testify/require"
	_ "turso.tech/database/tursogo"
)

func setupTestServerWithTurso(t *testing.T) (*Server, *turso.Store) {
	t.Helper()
	db, err := sql.Open("turso", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx := context.Background()
	require.NoError(t, turso.MigrateSchema(ctx, db))

	store := turso.NewStoreWithDB(db)
	t.Cleanup(func() {
		_ = db.Close()
	})

	reg := registry.New()
	deps := RouterDeps{
		Snapshots:   reg,
		Registry:    reg,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		AdminToken:  "test-secret",
		TursoStore:  store,
	}
	s := New(Config{Addr: "0.0.0.0:8080"}, deps, context.Background(), nil)
	return s, store
}

// TestSettingsModelDeletion_TursoStore reproduces and validates the exact bug
// where deleting models from the UI leaves them in the Turso store unless
// manage_models=true is sent.
func TestSettingsModelDeletion_TursoStore(t *testing.T) {
	s, _ := setupTestServerWithTurso(t)

	// Step 1: Seed two models
	seed := config.SettingsDTO{
		Upstreams: []config.UpstreamDTO{
			{Name: "openai-main", Protocol: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk-live-key-1"},
		},
		Models: []config.ModelDTO{
			{PublicName: "gpt-4o", Upstream: "openai-main", UpstreamModel: "gpt-4o"},
			{PublicName: "gpt-4o-mini", Upstream: "openai-main", UpstreamModel: "gpt-4o-mini"},
		},
	}
	bSeed, _ := json.Marshal(seed)
	reqSeed := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(bSeed))
	reqSeed.Header.Set("Authorization", "Bearer test-secret")
	reqSeed.Header.Set("Content-Type", "application/json")
	wSeed := httptest.NewRecorder()
	s.Handler().ServeHTTP(wSeed, reqSeed)
	require.Equal(t, http.StatusOK, wSeed.Code)

	// Verify both models exist via GET /api/settings
	get1 := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	get1.Header.Set("Authorization", "Bearer test-secret")
	wGet1 := httptest.NewRecorder()
	s.Handler().ServeHTTP(wGet1, get1)
	require.Equal(t, http.StatusOK, wGet1.Code)
	var res1 config.SettingsDTO
	require.NoError(t, json.Unmarshal(wGet1.Body.Bytes(), &res1))
	require.Len(t, res1.Models, 2)

	// Step 2: Delete gpt-4o-mini (leaving only gpt-4o), sending ManageModels: true (the fix)
	dropOne := config.SettingsDTO{
		Upstreams:    seed.Upstreams,
		Models:       []config.ModelDTO{{PublicName: "gpt-4o", Upstream: "openai-main", UpstreamModel: "gpt-4o"}},
		ManageModels: true,
	}
	bDropOne, _ := json.Marshal(dropOne)
	reqDropOne := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(bDropOne))
	reqDropOne.Header.Set("Authorization", "Bearer test-secret")
	reqDropOne.Header.Set("Content-Type", "application/json")
	wDropOne := httptest.NewRecorder()
	s.Handler().ServeHTTP(wDropOne, reqDropOne)
	require.Equal(t, http.StatusOK, wDropOne.Code)

	// Verify GET shows only 1 model
	get2 := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	get2.Header.Set("Authorization", "Bearer test-secret")
	wGet2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(wGet2, get2)
	require.Equal(t, http.StatusOK, wGet2.Code)
	var res2 config.SettingsDTO
	require.NoError(t, json.Unmarshal(wGet2.Body.Bytes(), &res2))
	require.Len(t, res2.Models, 1)
	require.Equal(t, "gpt-4o", res2.Models[0].PublicName)

	// Step 3 (The Bug): Try deleting the LAST model WITHOUT ManageModels.
	// The backend answers 200 "ok", but silently refuses to delete from Turso!
	dropLastWithoutFlag := config.SettingsDTO{
		Upstreams:    seed.Upstreams,
		Models:       []config.ModelDTO{},
		ManageModels: false, // old frontend behavior
	}
	bBug, _ := json.Marshal(dropLastWithoutFlag)
	reqBug := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(bBug))
	reqBug.Header.Set("Authorization", "Bearer test-secret")
	reqBug.Header.Set("Content-Type", "application/json")
	wBug := httptest.NewRecorder()
	s.Handler().ServeHTTP(wBug, reqBug)
	require.Equal(t, http.StatusOK, wBug.Code) // Answers 200!

	// The model is STILL THERE because ManageModels was false!
	get3 := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	get3.Header.Set("Authorization", "Bearer test-secret")
	wGet3 := httptest.NewRecorder()
	s.Handler().ServeHTTP(wGet3, get3)
	var res3 config.SettingsDTO
	require.NoError(t, json.Unmarshal(wGet3.Body.Bytes(), &res3))
	require.Len(t, res3.Models, 1, "unauthoritative empty models list must not delete (protects against stale payload)")

	// Step 4 (The Fix): Delete the LAST model WITH ManageModels: true (what withSettings now sends)
	dropLastWithFix := config.SettingsDTO{
		Upstreams:    seed.Upstreams,
		Models:       []config.ModelDTO{},
		ManageModels: true, // withSettings now sets this automatically!
	}
	bFix, _ := json.Marshal(dropLastWithFix)
	reqFix := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(bFix))
	reqFix.Header.Set("Authorization", "Bearer test-secret")
	reqFix.Header.Set("Content-Type", "application/json")
	wFix := httptest.NewRecorder()
	s.Handler().ServeHTTP(wFix, reqFix)
	require.Equal(t, http.StatusOK, wFix.Code)

	// The last model IS NOW GENUINELY DELETED!
	get4 := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	get4.Header.Set("Authorization", "Bearer test-secret")
	wGet4 := httptest.NewRecorder()
	s.Handler().ServeHTTP(wGet4, get4)
	var res4 config.SettingsDTO
	require.NoError(t, json.Unmarshal(wGet4.Body.Bytes(), &res4))
	require.Len(t, res4.Models, 0, "authoritative empty models list must delete all remaining models")
}
