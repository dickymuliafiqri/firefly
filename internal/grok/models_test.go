package grok

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseModelsResponse(t *testing.T) {
	t.Parallel()

	// OpenAI-style {data:[{id},...]}
	ids := parseModelsResponse([]byte(`{"data":[{"id":"grok-build"},{"id":"grok-4.5"}]}`))
	assert.Equal(t, []string{"grok-4.5", "grok-build"}, ids)

	// {models:[...]} with mixed id fields
	ids = parseModelsResponse([]byte(`{"models":[{"model_id":"a"},{"name":"b"}]}`))
	assert.Equal(t, []string{"a", "b"}, ids)

	// bare string array
	ids = parseModelsResponse([]byte(`["z","a","a"]`))
	assert.Equal(t, []string{"a", "z"}, ids) // sorted + deduped

	// object map: keys are ids
	ids = parseModelsResponse([]byte(`{"grok-build":{},"grok-4.5":{}}`))
	assert.Equal(t, []string{"grok-4.5", "grok-build"}, ids)

	// invalid json
	assert.Nil(t, parseModelsResponse([]byte(`nope`)))
}

func TestFetchModels_LiveDiscovery(t *testing.T) {
	t.Parallel()

	var gotAuth, gotPath string
	var gotTokenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTokenAuth = r.Header.Get("x-xai-token-auth")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-build"},{"id":"grok-4.5"},{"id":"grok-5-preview"}]}`))
	}))
	defer srv.Close()

	models, err := FetchModels(context.Background(), srv.Client(), srv.URL+"/v1", "ey-access-token")
	require.NoError(t, err)
	assert.Equal(t, []string{"grok-4.5", "grok-5-preview", "grok-build"}, models)
	assert.Equal(t, "Bearer ey-access-token", gotAuth)
	assert.Equal(t, "xai-grok-cli", gotTokenAuth)
	assert.Equal(t, "/v1/models", gotPath)
}

func TestFetchModels_StripsResponsesSuffix(t *testing.T) {
	t.Parallel()

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-build"}]}`))
	}))
	defer srv.Close()

	// A base that mistakenly includes /responses is normalized back to /models.
	_, err := FetchModels(context.Background(), srv.Client(), srv.URL+"/v1/responses", "tok")
	require.NoError(t, err)
	assert.Equal(t, "/v1/models", gotPath)
}

func TestFetchModels_Errors(t *testing.T) {
	t.Parallel()

	_, err := FetchModels(context.Background(), nil, "https://x", "tok")
	assert.Error(t, err)

	_, err = FetchModels(context.Background(), http.DefaultClient, "https://x", "")
	assert.Error(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"expired"}`))
	}))
	defer srv.Close()
	_, err = FetchModels(context.Background(), srv.Client(), srv.URL+"/v1", "tok")
	assert.Error(t, err)
}
