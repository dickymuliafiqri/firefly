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

// handleAntigravityModels discovers models by verifying every candidate live and
// reports which ids Google actually served. Candidates are the curated seed
// plus every model already mapped to this upstream in the catalog, so an
// operator's own naming is always part of the sweep.
func (deps RouterDeps) handleAntigravityModels(
	w http.ResponseWriter,
	ctx context.Context,
	existingUp *domain.Upstream,
	baseURL, accessToken, keyRef string,
	timeout time.Duration,
	egressMode, proxyURL string,
) {
	if strings.TrimSpace(accessToken) == "" {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
			Message: "No Google credential available for this upstream — connect a Google account (OAuth) to discover models live. No list is served from memory.",
			KeyRef:  keyRef,
		})
		return
	}

	candidates := antigravity.CuratedModels()
	candidates = append(candidates, deps.antigravityCatalogModels(existingUp)...)

	projectID := deps.antigravityProjectID(ctx, accessToken, keyRef)
	client := deps.makeCheckClient(timeout, egressMode, proxyURL)
	results := antigravity.DiscoverModels(ctx, client, baseURL, accessToken, projectID, candidates, 0)

	var verified, unavailable []string
	var slowest time.Duration
	for _, result := range results {
		if result.OK {
			verified = append(verified, result.Model)
		} else {
			unavailable = append(unavailable, result.Model)
		}
		if result.Latency > slowest {
			slowest = result.Latency
		}
	}

	msg := fmt.Sprintf(
		"Verified %d/%d candidate models live against Google Cloud Code (slowest %dms). "+
			"Cloud Code serves no model-list route, so every id is confirmed with a one-token generation.",
		len(verified), len(results), slowest.Milliseconds(),
	)
	if len(verified) == 0 {
		msg = "No candidate model answered on Google Cloud Code. " + msg
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(UpstreamModelsResponse{
		Models:      verified,
		ModelCount:  len(verified),
		Unavailable: unavailable,
		LatencyMs:   slowest.Milliseconds(),
		Message:     msg,
		KeyRef:      keyRef,
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
