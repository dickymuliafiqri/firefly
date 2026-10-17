package turso

import (
	"context"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/config"
)

// TestSaveSettings_AlwaysAvailableTriStateRoundTrip pins that the column keeps
// three states apart. always_available is NULL-able on purpose: absent means
// "let config.Build decide" (it turns the flag on for the keyless OpenCode free
// tier), while an explicit false is a deliberate operator decision to keep
// closing that upstream. Storing absent as 0 would silently opt every upstream
// out of the default the moment the dashboard saved the page once — the exact
// bug this column exists to prevent.
func TestSaveSettings_AlwaysAvailableTriStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, _ := setupTestDB(t)

	explicitTrue := true
	explicitFalse := false
	seed := config.SettingsDTO{
		ManageUpstreams: true,
		Upstreams: []config.UpstreamDTO{
			{Name: "unset", Protocol: "openai", BaseURL: "https://a/v1"},
			{Name: "on", Protocol: "openai", BaseURL: "https://b/v1", AlwaysAvailable: &explicitTrue},
			{Name: "off", Protocol: "openai", BaseURL: "https://c/v1", AlwaysAvailable: &explicitFalse},
		},
	}
	if err := store.SaveSettings(ctx, seed); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	loaded, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}

	got := map[string]*bool{}
	for i := range loaded.Upstreams {
		got[loaded.Upstreams[i].Name] = loaded.Upstreams[i].AlwaysAvailable
	}
	if v, ok := got["unset"]; !ok {
		t.Fatalf("upstream \"unset\" is missing from the loaded settings: %+v", loaded.Upstreams)
	} else if v != nil {
		t.Errorf("\"unset\" must load as NULL/unset, got %v", *v)
	}
	if v := got["on"]; v == nil || !*v {
		t.Errorf("\"on\" must load as true, got %v", v)
	}
	if v := got["off"]; v == nil {
		t.Error("\"off\" must load as an explicit false, not NULL")
	} else if *v {
		t.Error("\"off\" must load as false")
	}

	// A second save of the loaded payload (what the dashboard does — it stages
	// what GET returned) must not turn the unset row into an explicit false.
	if err := store.SaveSettings(ctx, *loaded); err != nil {
		t.Fatalf("re-save loaded settings: %v", err)
	}
	again, err := store.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	for i := range again.Upstreams {
		if again.Upstreams[i].Name == "unset" && again.Upstreams[i].AlwaysAvailable != nil {
			t.Fatalf("the unset row became explicit after a round trip: %v", *again.Upstreams[i].AlwaysAvailable)
		}
	}
}
