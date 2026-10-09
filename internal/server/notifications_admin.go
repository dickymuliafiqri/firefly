package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/notify"
)

// This file owns the notification surface: masking the credentials a channel
// carries, validating a channel before it is persisted, and the test-send
// endpoint that proves a webhook works before an operator relies on it.

// maskNotificationSecret renders a write-only credential for the API. The
// settings round trip must never echo a bearer or signing secret back, so the
// stored value is replaced by a fixed marker and the real one is only ever
// accepted on write.
func maskNotificationSecret(secret string) string {
	if strings.TrimSpace(secret) == "" {
		return ""
	}
	return "***"
}

// sanitizeNotificationChannels strips credentials from a channel list for the
// API response.
func sanitizeNotificationChannels(channels []config.NotificationChannelDTO) []config.NotificationChannelDTO {
	out := make([]config.NotificationChannelDTO, 0, len(channels))
	for _, ch := range channels {
		ch.Bearer = maskNotificationSecret(ch.Bearer)
		ch.Secret = maskNotificationSecret(ch.Secret)
		out = append(out, ch)
	}
	return out
}

// mergeNotificationSecrets restores the stored credentials for channels whose
// incoming payload carries only the mask. Without this, saving settings from
// the dashboard — which never sees the real secret — would wipe every
// credential.
func mergeNotificationSecrets(incoming, stored []config.NotificationChannelDTO) []config.NotificationChannelDTO {
	if len(stored) == 0 {
		return incoming
	}
	byURL := make(map[string]config.NotificationChannelDTO, len(stored))
	for _, ch := range stored {
		byURL[ch.URL] = ch
	}
	for i := range incoming {
		prev, ok := byURL[incoming[i].URL]
		if !ok {
			continue
		}
		if incoming[i].Bearer == "***" {
			incoming[i].Bearer = prev.Bearer
		}
		if incoming[i].Secret == "***" {
			incoming[i].Secret = prev.Secret
		}
	}
	return incoming
}

// validateNotificationChannels rejects a channel list that cannot work, so a
// typo is caught at save time rather than silently dropping every event.
func validateNotificationChannels(channels []config.NotificationChannelDTO) error {
	for i, ch := range channels {
		if strings.TrimSpace(ch.URL) == "" {
			return &config.ValidationError{Field: fieldIdx("notifications.channels", i, "url"), Msg: "must not be empty"}
		}
		if !notify.IsKnownFormat(ch.Format) {
			return &config.ValidationError{
				Field: fieldIdx("notifications.channels", i, "format"),
				Msg:   "must be one of " + strings.Join(notify.KnownFormats, ", "),
			}
		}
		if ch.MinSeverity != "" {
			switch notify.Severity(ch.MinSeverity) {
			case notify.SeverityInfo, notify.SeverityWarning, notify.SeverityCritical:
			default:
				return &config.ValidationError{
					Field: fieldIdx("notifications.channels", i, "min_severity"),
					Msg:   "must be info, warning, or critical",
				}
			}
		}
	}
	return nil
}

// fieldIdx renders a nested field path for a validation error.
func fieldIdx(prefix string, i int, field string) string {
	return prefix + "[" + strconv.Itoa(i) + "]." + field
}

// notificationChannelsFromSnapshot reads the channel list off the live catalog
// snapshot, so a hot-reloaded settings file takes effect without a restart.
func (deps RouterDeps) notificationChannelsFromSnapshot() []config.NotificationChannelDTO {
	snap := deps.currentSnapshot()
	if snap == nil {
		return nil
	}
	channels := snap.Notifications()
	if len(channels) == 0 {
		return nil
	}
	out := make([]config.NotificationChannelDTO, 0, len(channels))
	for _, ch := range channels {
		out = append(out, config.NotificationChannelDTO{
			URL:         ch.URL,
			Format:      ch.Format,
			Bearer:      ch.Bearer,
			Secret:      ch.Secret,
			Enabled:     ch.Enabled,
			Events:      ch.Events,
			MinSeverity: ch.MinSeverity,
		})
	}
	return out
}

// handleNotificationsTest sends a synthetic event to one channel and reports
// the outcome. It is the operator's "does my webhook actually work" button, so
// the response names the failure rather than a bare status code.
func (deps RouterDeps) handleNotificationsTest(w http.ResponseWriter, r *http.Request) {
	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized admin access")
		return
	}

	var req config.NotificationChannelDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, "parse JSON request: "+err.Error())
		return
	}
	if err := validateNotificationChannels([]config.NotificationChannelDTO{req}); err != nil {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest, err.Error())
		return
	}

	// A test that carries only the mask must borrow the stored credential,
	// otherwise the button is useless right after a settings save.
	if req.Bearer == "***" || req.Secret == "***" {
		for _, stored := range deps.notificationChannelsFromSnapshot() {
			if stored.URL != req.URL {
				continue
			}
			if req.Bearer == "***" {
				req.Bearer = stored.Bearer
			}
			if req.Secret == "***" {
				req.Secret = stored.Secret
			}
			break
		}
	}

	ev := notify.Event{
		Type:     "notifications.test",
		Severity: notify.SeverityInfo,
		Subject:  "Firefly notification test",
		Body:     "This is a test event sent from the Firefly dashboard. If you can read it, the channel is wired correctly.",
		Data: map[string]any{
			"channel_format": req.Format,
			"sent_by":        "dashboard",
		},
		OccurredAt: time.Now().UTC(),
	}

	poster := notify.NewPoster(false)
	err := poster.Post(r.Context(), notify.Channel{
		URL:         req.URL,
		Format:      req.Format,
		Bearer:      req.Bearer,
		Secret:      req.Secret,
		Enabled:     true,
		Events:      req.Events,
		MinSeverity: notify.Severity(req.MinSeverity),
	}, ev)

	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "test event delivered",
	})
}

// notificationChannelsForDomain converts the DTO list into the domain shape the
// catalog snapshot carries.
func notificationChannelsForDomain(channels []config.NotificationChannelDTO) []domain.NotificationChannel {
	if len(channels) == 0 {
		return nil
	}
	out := make([]domain.NotificationChannel, 0, len(channels))
	for _, ch := range channels {
		out = append(out, domain.NotificationChannel{
			URL:         ch.URL,
			Format:      ch.Format,
			Bearer:      ch.Bearer,
			Secret:      ch.Secret,
			Enabled:     ch.Enabled,
			Events:      ch.Events,
			MinSeverity: ch.MinSeverity,
		})
	}
	return out
}
