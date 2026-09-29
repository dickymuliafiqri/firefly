package httpx

import (
	"context"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// TenantFrom returns the authenticated tenant stored in ctx, or nil.
func TenantFrom(ctx context.Context) *domain.Tenant {
	if v, ok := ctx.Value(ctxKeyTenant).(*domain.Tenant); ok {
		return v
	}
	return nil
}

// WithTenant stores the authenticated tenant in ctx.
func WithTenant(ctx context.Context, t *domain.Tenant) context.Context {
	return context.WithValue(ctx, ctxKeyTenant, t)
}
