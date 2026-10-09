package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/storage/analytics"
)

// defaultCostWindowDays is the span a cost query covers when the caller gives
// no since/until. A week is what the dashboard's overview panels expect.
const defaultCostWindowDays = 7

// handleUsageCosts serves GET /api/usage/costs, the aggregated cost report.
//
// Query parameters:
//
//	group_by  tenant | model | key   (default tenant)
//	since     unix seconds or RFC3339 (default: 7 days ago)
//	until     unix seconds or RFC3339 (default: now)
//
// The response carries USD floats for display alongside the integer micros the
// aggregation actually summed, so a client can render either without a second
// round trip.
func (deps RouterDeps) handleUsageCosts(w http.ResponseWriter, r *http.Request) {
	if !deps.authorizeAdmin(r) {
		openai.WriteError(w, http.StatusUnauthorized, openai.TypeAuthentication, "unauthorized admin access")
		return
	}
	if deps.Analytics == nil {
		openai.WriteError(w, http.StatusServiceUnavailable, openai.TypeAPI, "analytics store not configured")
		return
	}

	q := r.URL.Query()
	groupBy := analytics.ParseCostGroup(q.Get("group_by"))

	now := time.Now()
	until := now.UnixMilli()
	if raw := strings.TrimSpace(q.Get("until")); raw != "" {
		parsed, err := parseTimeParam(raw)
		if err != nil {
			openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
				"invalid until: "+err.Error())
			return
		}
		until = parsed
	}

	since := now.AddDate(0, 0, -defaultCostWindowDays).UnixMilli()
	if raw := strings.TrimSpace(q.Get("since")); raw != "" {
		parsed, err := parseTimeParam(raw)
		if err != nil {
			openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
				"invalid since: "+err.Error())
			return
		}
		since = parsed
	}

	if until > 0 && since > until {
		openai.WriteError(w, http.StatusBadRequest, openai.TypeInvalidRequest,
			"since must not be after until")
		return
	}

	report, err := deps.Analytics.CostReport(r.Context(), groupBy, since, until)
	if err != nil {
		openai.WriteError(w, http.StatusInternalServerError, openai.TypeAPI, "cost aggregation failed: "+err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(report)
}

// parseTimeParam accepts either a unix timestamp (seconds when the value is
// small enough to be seconds, milliseconds otherwise) or an RFC3339 string.
// Operators reach for both, and guessing wrong silently shifts the window.
func parseTimeParam(raw string) (int64, error) {
	if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
		// Values below 1e11 are seconds; anything larger is already
		// milliseconds. The boundary is the year 5138 in seconds, so no real
		// timestamp is ambiguous.
		if v > 0 && v < 100_000_000_000 {
			return v * 1000, nil
		}
		return v, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UnixMilli(), nil
	}
	return 0, errTimeParam
}

// errTimeParam is the sentinel returned for an unparseable time parameter.
var errTimeParam = &timeParamError{}

type timeParamError struct{}

func (e *timeParamError) Error() string {
	return "expected a unix timestamp or an RFC3339 date"
}
