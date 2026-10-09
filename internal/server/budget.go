package server

import (
	"strconv"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/notify"
)

// This file holds the tenant budget guard's notification side. The guard
// itself lives inline in forwardEndpoint so the refusal happens before any
// upstream work; what belongs here is turning a threshold crossing into an
// event an operator can act on.

// emitTenantBudgetWarning reports a tenant crossing the soft-warning
// threshold. The threshold check and the once-per-period latch live on
// domain.Tenant, so this only fires on the genuine crossing.
func (deps RouterDeps) emitTenantBudgetWarning(tenant *domain.Tenant) {
	if deps.Notifier == nil || tenant == nil {
		return
	}
	deps.Notifier.Emit(notify.Event{
		Type:     "tenant.budget_warning",
		Severity: notify.SeverityWarning,
		Subject:  "Tenant budget at " + budgetPercent(tenant.BudgetRatio()),
		Body: "Tenant " + tenant.Name + " has spent " +
			microsToUSDString(tenant.SpentMicros.Load()) + " of its " +
			microsToUSDString(tenant.BudgetMicros) + " budget.",
		Data: map[string]any{
			"tenant":        tenant.Name,
			"budget_micros": tenant.BudgetMicros,
			"spent_micros":  tenant.SpentMicros.Load(),
			"ratio":         tenant.BudgetRatio(),
		},
		OccurredAt: time.Now().UTC(),
	})
}

// emitTenantBudgetExceeded reports a request refused because the tenant is out
// of budget. It is emitted from the refusal path, not from AddSpend, so the
// event describes a rejected request rather than a crossing.
func (deps RouterDeps) emitTenantBudgetExceeded(tenant *domain.Tenant) {
	if deps.Notifier == nil || tenant == nil {
		return
	}
	deps.Notifier.Emit(notify.Event{
		Type:     "tenant.budget_exceeded",
		Severity: notify.SeverityCritical,
		Subject:  "Tenant budget exhausted",
		Body: "Tenant " + tenant.Name + " exceeded its " +
			microsToUSDString(tenant.BudgetMicros) + " budget; requests are being refused.",
		Data: map[string]any{
			"tenant":        tenant.Name,
			"budget_micros": tenant.BudgetMicros,
			"spent_micros":  tenant.SpentMicros.Load(),
		},
		OccurredAt: time.Now().UTC(),
	})
}

// budgetPercent renders a ratio as a whole-percent string for a subject line.
func budgetPercent(ratio float64) string {
	pct := int(ratio * 100)
	if pct < 0 {
		pct = 0
	}
	return strconv.Itoa(pct) + "%"
}

// microsToUSDString renders micro-USD as a fixed 4-decimal dollar string. The
// notification body is prose, so a plain float would render as 1.2e-05.
func microsToUSDString(micros int64) string {
	return "$" + strconv.FormatFloat(microsToUSD(micros), 'f', 4, 64)
}
