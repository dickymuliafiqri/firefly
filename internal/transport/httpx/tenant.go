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

// TargetFrom returns the resolved routing target stored in ctx, or nil.
func TargetFrom(ctx context.Context) *domain.Target {
	if v, ok := ctx.Value(ctxKeyTarget).(*domain.Target); ok {
		return v
	}
	return nil
}

// WithTarget stores the resolved routing target in ctx.
func WithTarget(ctx context.Context, t *domain.Target) context.Context {
	return context.WithValue(ctx, ctxKeyTarget, t)
}
