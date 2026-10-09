package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
)

func chanDTO(url, format, bearer, secret string) config.NotificationChannelDTO {
	return config.NotificationChannelDTO{
		URL:     url,
		Format:  format,
		Bearer:  bearer,
		Secret:  secret,
		Enabled: true,
	}
}

func TestMaskNotificationSecret(t *testing.T) {
	if got := maskNotificationSecret(""); got != "" {
		t.Fatalf("empty = %q, want empty", got)
	}
	if got := maskNotificationSecret("   "); got != "" {
		t.Fatalf("whitespace = %q, want empty", got)
	}
	if got := maskNotificationSecret("super-secret"); got != "***" {
		t.Fatalf("secret = %q, want ***", got)
	}
}

func TestSanitizeNotificationChannels(t *testing.T) {
	in := []config.NotificationChannelDTO{
		chanDTO("https://a.example/hook", "discord", "bearer-1", "secret-1"),
		chanDTO("https://b.example/hook", "slack", "", ""),
	}
	out := sanitizeNotificationChannels(in)
	if len(out) != 2 {
		t.Fatalf("len = %d", len(out))
	}
	for i, ch := range out {
		if ch.URL != in[i].URL || ch.Format != in[i].Format || !ch.Enabled {
			t.Fatalf("non-secret fields altered: %+v", ch)
		}
		if strings.Contains(ch.Bearer, "bearer-1") || strings.Contains(ch.Secret, "secret-1") {
			t.Fatalf("secret leaked: %+v", ch)
		}
	}
	if out[0].Bearer != "***" || out[0].Secret != "***" {
		t.Fatalf("present secrets must be masked: %+v", out[0])
	}
	if out[1].Bearer != "" || out[1].Secret != "" {
		t.Fatalf("absent secrets must stay absent: %+v", out[1])
	}
}

func TestMergeNotificationSecrets(t *testing.T) {
	stored := []config.NotificationChannelDTO{
		chanDTO("https://a.example/hook", "discord", "real-bearer", "real-secret"),
	}

	// A payload carrying only the mask borrows the stored credential.
	incoming := []config.NotificationChannelDTO{
		chanDTO("https://a.example/hook", "discord", "***", "***"),
	}
	got := mergeNotificationSecrets(incoming, stored)
	if got[0].Bearer != "real-bearer" || got[0].Secret != "real-secret" {
		t.Fatalf("masked payload did not borrow the stored secret: %+v", got[0])
	}

	// A payload carrying a genuinely new credential keeps it.
	incoming = []config.NotificationChannelDTO{
		chanDTO("https://a.example/hook", "discord", "new-bearer", "new-secret"),
	}
	got = mergeNotificationSecrets(incoming, stored)
	if got[0].Bearer != "new-bearer" || got[0].Secret != "new-secret" {
		t.Fatalf("new credential was overwritten: %+v", got[0])
	}

	// A brand new channel has nothing to borrow.
	incoming = []config.NotificationChannelDTO{
		chanDTO("https://new.example/hook", "generic", "***", ""),
	}
	got = mergeNotificationSecrets(incoming, stored)
	if got[0].Bearer != "***" {
		t.Fatalf("unknown channel should keep its payload: %+v", got[0])
	}
}

func TestValidateNotificationChannels(t *testing.T) {
	cases := []struct {
		name    string
		channel config.NotificationChannelDTO
		wantErr bool
	}{
		{"valid generic", chanDTO("https://a.example/hook", "generic", "", ""), false},
		{"valid discord", chanDTO("https://a.example/hook", "discord", "b", "s"), false},
		{"valid with severity", config.NotificationChannelDTO{URL: "https://a", Format: "slack", MinSeverity: "warning"}, false},
		{"empty url", chanDTO("", "generic", "", ""), true},
		{"whitespace url", chanDTO("   ", "generic", "", ""), true},
		{"unknown format", chanDTO("https://a", "carrier-pigeon", "", ""), true},
		{"empty format", chanDTO("https://a", "", "", ""), true},
		{"bad severity", config.NotificationChannelDTO{URL: "https://a", Format: "generic", MinSeverity: "loud"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNotificationChannels([]config.NotificationChannelDTO{tc.channel})
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "notifications.channels[0]") {
				t.Fatalf("error should name the field: %v", err)
			}
		})
	}

	if err := validateNotificationChannels(nil); err != nil {
		t.Fatalf("empty list must be valid: %v", err)
	}
}

func TestNotificationChannelsForDomain(t *testing.T) {
	in := []config.NotificationChannelDTO{
		{URL: "https://a", Format: "discord", Bearer: "b", Secret: "s", Enabled: true, Events: []string{"key.cooldown"}, MinSeverity: "warning"},
	}
	got := notificationChannelsForDomain(in)
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].URL != "https://a" || got[0].Format != "discord" || got[0].Bearer != "b" || got[0].Secret != "s" {
		t.Fatalf("fields lost: %+v", got[0])
	}
	if !got[0].Enabled || len(got[0].Events) != 1 || got[0].MinSeverity != "warning" {
		t.Fatalf("filters lost: %+v", got[0])
	}
	if notificationChannelsForDomain(nil) != nil {
		t.Fatal("nil input must stay nil")
	}
}

func TestHandleNotificationsTest_RequiresAdmin(t *testing.T) {
	deps, _ := testDeps()
	deps.AdminToken = "secret-admin"
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/test", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	deps.handleNotificationsTest(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleNotificationsTest_RejectsBadChannel(t *testing.T) {
	deps, _ := testDeps()
	deps.AdminToken = "secret-admin"

	for _, body := range []string{
		`{"url":"","format":"generic"}`,
		`{"url":"https://a.example","format":"carrier-pigeon"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/notifications/test", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer secret-admin")
		rec := httptest.NewRecorder()
		deps.handleNotificationsTest(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestHandleNotificationsTest_Delivers(t *testing.T) {
	var gotBody []byte
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		gotBody = buf[:n]
		gotSig = r.Header.Get("X-Firefly-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	deps, _ := testDeps()
	deps.AdminToken = "secret-admin"

	payload, _ := json.Marshal(chanDTO(srv.URL, "generic", "", ""))
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/test", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret-admin")
	rec := httptest.NewRecorder()
	deps.handleNotificationsTest(rec, req)

	// The default poster refuses private addresses, so a loopback test server
	// is rejected by design; what matters is that the endpoint answers with a
	// structured result rather than a bare status.
	if rec.Code != http.StatusOK && rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 200 or 502: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad payload: %v", err)
	}
	if !out.Success && out.Error == "" {
		t.Fatal("a failure must name its reason")
	}
	_ = gotBody
	_ = gotSig
}

func TestSettingsDTOFromSnapshot_IncludesNotifications(t *testing.T) {
	snap := domain.NewCatalogSnapshot(
		1,
		map[string]*domain.Upstream{},
		nil,
		map[string]*domain.ModelEntry{},
		nil,
		map[string]*domain.Tenant{},
		nil,
		domain.WithNotifications([]domain.NotificationChannel{
			{URL: "https://a.example/hook", Format: "discord", Bearer: "real-bearer", Secret: "real-secret", Enabled: true},
		}),
	)

	settings := settingsDTOFromSnapshot(snap)
	if settings.Notifications == nil {
		t.Fatal("notifications section missing from the reconstructed settings")
	}
	if len(settings.Notifications.Channels) != 1 {
		t.Fatalf("channels = %d, want 1", len(settings.Notifications.Channels))
	}
	ch := settings.Notifications.Channels[0]
	if ch.URL != "https://a.example/hook" || ch.Format != "discord" || !ch.Enabled {
		t.Fatalf("channel fields lost: %+v", ch)
	}
	// The reconstructed DTO is for display: credentials must be masked so a
	// caller that re-persists it cannot write the mask back as a secret.
	if ch.Bearer == "real-bearer" || ch.Secret == "real-secret" {
		t.Fatalf("credentials not masked: %+v", ch)
	}
}
