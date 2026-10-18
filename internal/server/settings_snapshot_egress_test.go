package server

import (
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// TestSettingsDTOFromSnapshot_PreservesUpstreamEgress pins the round trip every
// full-settings re-persist path relies on (pricing CRUD, pricing import, tenant
// admin, notification admin all call settingsDTOFromSnapshot then persist the
// result authoritatively). The reconstruction must not drop upstream routing
// fields it did not touch, or editing an unrelated page silently resets every
// upstream's egress to direct.
func TestSettingsDTOFromSnapshot_PreservesUpstreamEgress(t *testing.T) {
	up := &domain.Upstream{
		Name:               "up",
		Protocol:           domain.ProtocolOpenAI,
		BaseURL:            "https://example.com/v1",
		EgressMode:         domain.EgressModeSingbox,
		ProxyURL:           "socks5://127.0.0.1:1080",
		SingboxOutboundTag: "node-hk-1",
		ProbeModel:         "gpt-4o-mini",
		KeyRing:            domain.NewKeyRing(domain.KeyStrategyRoundRobin, nil),
	}
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{"up": up},
		[]string{"up"},
		map[string]*domain.ModelEntry{},
		nil,
		map[string]*domain.Tenant{},
		nil,
	)

	settings := settingsDTOFromSnapshot(snap)
	if len(settings.Upstreams) != 1 {
		t.Fatalf("upstreams = %d, want 1", len(settings.Upstreams))
	}
	got := settings.Upstreams[0]
	if got.EgressMode != domain.EgressModeSingbox {
		t.Errorf("egress_mode = %q, want %q", got.EgressMode, domain.EgressModeSingbox)
	}
	if got.SingboxOutboundTag != "node-hk-1" {
		t.Errorf("singbox_outbound_tag = %q, want %q", got.SingboxOutboundTag, "node-hk-1")
	}
	if got.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Errorf("proxy_url = %q, want preserved", got.ProxyURL)
	}
	if got.ProbeModel != "gpt-4o-mini" {
		t.Errorf("probe_model = %q, want preserved", got.ProbeModel)
	}
	// The tri-state always-available field must stay unset here: the domain
	// value is already resolved, so emitting an explicit bool would turn a
	// NULL (let config.Build decide) into a hard "off" and disable the
	// keyless OpenCode free tier's default on the next reload.
	if got.AlwaysAvailable != nil {
		t.Errorf("always_available = %v, want nil (tri-state must survive)", *got.AlwaysAvailable)
	}
}
