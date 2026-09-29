package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/antigravity"
	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// Live Antigravity (Google Cloud Code) probe and model discovery.
//
// Cloud Code has no model-list route — the host only serves
// `v1internal:generateContent`, everything else is 404 — so both dashboard
// actions run real, one-token generations against Google and report exactly
// what came back. The previous behaviour answered from a hardcoded list with a
// fabricated 1ms latency, so an expired OAuth token or a retired model id still
// looked "healthy" and the Fetch models list was fiction.

// handleAntigravityCheck verifies one model (or the host, via the default probe
// model) with a live generation and reports the upstream's real status, latency
// and message.
func (deps RouterDeps) handleAntigravityCheck(
	w http.ResponseWriter,
	ctx context.Context,
	existingUp *domain.Upstream,
	targetSlot *domain.KeySlot,
	baseURL, accessToken, keyRef, model string,
	timeout time.Duration,
	egressMode, proxyURL string,
) {
	respond := func(res UpstreamCheckResponse) {
		res.KeyRef = keyRef
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(res)
	}

	if strings.TrimSpace(accessToken) == "" {
		respond(UpstreamCheckResponse{
			Healthy: false,
			Message: "No Google credential available for this upstream — connect a Google account (OAuth) before probing. Nothing is inferred locally.",
		})
		return
	}

	target := strings.TrimSpace(model)
	implicit := target == ""
	if implicit {
		target = antigravity.DefaultProbeModel
	}

	projectID := deps.antigravityProjectID(ctx, accessToken, keyRef)
	client := deps.makeCheckClient(timeout, egressMode, proxyURL)

	var result antigravity.ProbeResult
	tried := []string{}
	if implicit {
		// No model requested: walk the fallback chain so an account without the
		// newest Flash still reports the host as reachable.
		for _, candidate := range antigravity.DefaultProbeModels() {
			tried = append(tried, candidate)
			result = antigravity.ProbeModel(ctx, client, baseURL, accessToken, projectID, candidate)
			if result.OK {
				break
			}
		}
	} else {
		result = antigravity.ProbeModel(ctx, client, baseURL, accessToken, projectID, target)
	}
	applyProbeKeyStatus(existingUp, targetSlot, result.StatusCode, "", []byte(result.Message))

	subject := fmt.Sprintf("Model %q", target)
	if implicit {
		subject = fmt.Sprintf("Host (probed with %d candidate model(s) %s — Cloud Code has no model-list route)",
			len(tried), strings.Join(tried, ", "))
	}
	verb := "answered live"
	if !result.OK {
		verb = "failed live"
	}
	respond(UpstreamCheckResponse{
		Healthy:    result.OK,
		StatusCode: result.StatusCode,
		LatencyMs:  result.Latency.Milliseconds(),
		Message: fmt.Sprintf(
			"%s %s: %s (Cloud Code has no model-list route, so the check is a real one-token generation).",
			subject, verb, result.Message,
		),
	})
}

// handleAntigravityModels serves the built-in Antigravity catalog without any
// network call. Cloud Code has no model-list route, so a live sweep would need
// one authenticated one-token generation per candidate: slow, billable, and
// impossible before a Google account is connected. The built-in list is the
// documented id set (see antigravity.CuratedModels), and the honest way to know
// whether an id is usable is the per-model Check, which is a real call.
//
// The list is merged with every model already mapped to this upstream in the
// catalog, so an operator's own ids (added by hand or by a previous sync) are
// always part of the answer.
func (deps RouterDeps) handleAntigravityModels(
	w http.ResponseWriter,
	existingUp *domain.Upstream,
	keyRef string,
) {
	models := antigravity.CuratedModels()
	for _, id := range deps.antigravityCatalogModels(existingUp) {
		known := false
		for _, existing := range models {
			if strings.EqualFold(existing, id) {
				known = true
				break
			}
		}
		if !known {
			models = append(models, id)
		}
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
		Models:     models,
		ModelCount: len(models),
		Message: fmt.Sprintf(
			"Antigravity catalog: %d model ids (built-in, no network call — Cloud Code serves no model-list route). "+
				"Check an id to verify it with a real one-token generation, or add your own below.",
			len(models),
		),
		KeyRef: keyRef,
	})
}

// antigravityProjectID resolves the Google Cloud companion project bound to the
// OAuth connection behind the credential, falling back to a generated id (the
// same fallback the adapter uses when no connection metadata exists).
func (deps RouterDeps) antigravityProjectID(ctx context.Context, refs ...string) string {
	if deps.OAuthManager != nil {
		for _, ref := range refs {
			ref = strings.TrimSpace(ref)
			if !strings.HasPrefix(ref, "oauth:") {
				continue
			}
			conn, err := deps.OAuthManager.ResolveConnection(ctx, ref)
			if err != nil || conn == nil {
				continue
			}
			if pid := strings.TrimSpace(conn.ProviderSpecificData["project_id"]); pid != "" {
				return pid
			}
		}
	}
	return antigravity.GenerateProjectID()
}

// antigravityCatalogModels returns the upstream-side model ids already mapped to
// this upstream in the catalog, so a live sweep also verifies the operator's
// own routes instead of only the curated seed.
func (deps RouterDeps) antigravityCatalogModels(u *domain.Upstream) []string {
	if u == nil {
		return nil
	}
	snap := deps.currentSnapshot()
	if snap == nil {
		return nil
	}
	var out []string
	for _, publicName := range snap.SortedPublicModelIDs() {
		entry, ok := snap.Model(publicName)
		if !ok || entry == nil || entry.Upstream != u.Name {
			continue
		}
		id := strings.TrimSpace(entry.UpstreamModel)
		if id == "" {
			id = strings.TrimSpace(entry.PublicName)
		}
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}
