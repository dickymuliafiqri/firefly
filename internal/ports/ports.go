// Package ports defines the interfaces (contracts) that the gateway core
// depends on. Concrete implementations live in adapters; handlers depend only
// on these interfaces.
package ports

import (
	"context"
	"io"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// UpstreamAdapter translates a canonical gateway request into an upstream
// call and streams the response back. One adapter exists per protocol. The
// openai adapter is a thin passthrough today; other protocols may transform.
type UpstreamAdapter interface {
	// Protocol is the wire dialect this adapter speaks.
	Protocol() domain.Protocol

	// Forward sends the request to the upstream and writes the response to w.
	// For streaming requests it must relay incrementally and honor ctx cancel.
	// headers carries the client's request headers (already filtered).
	Forward(ctx context.Context, t *domain.Target, req ForwardRequest, w io.Writer) error
}

// AdapterRegistry manages adapters keyed by protocol.
type AdapterRegistry interface {
	// Register associates an adapter with a wire protocol.
	Register(proto domain.Protocol, adapter UpstreamAdapter) error
	// Lookup returns the adapter for a protocol, if registered.
	Lookup(proto domain.Protocol) (UpstreamAdapter, bool)
}

// ForwardRequest is the canonical, adapter-agnostic request envelope.
type ForwardRequest struct {
	// Method and Path are the upstream-relative HTTP method and path,
	// e.g. POST /chat/completions.
	Method string
	Path   string
	// Body is the ORIGINAL request body. Adapters that need to rewrite fields
	// (e.g. model) do so themselves; passthrough adapters relay it verbatim.
	Body io.Reader
	// BodyBytes is the fully read body, provided so adapters can rewrite
	// without re-reading a consumed stream. Nil-safe: Body is authoritative.
	BodyBytes []byte
	// Headers are client headers to forward (already stripped of hop-by-hop
	// and our own auth).
	Headers map[string][]string
	// Stream indicates an SSE response is expected.
	Stream bool
}

// ConfigSource loads the raw configuration set. The file-based implementation
// reads the three JSON files; a future implementation could read remotely.
type ConfigSource interface {
	// Load returns the raw bytes of each config file keyed by logical name
	// ("upstreams", "models", "tenants").
	Load(ctx context.Context) (map[string][]byte, error)
}

// TenantStore resolves a tenant from a presented gateway API key. The JSON
// implementation looks the hash up in the active snapshot; a future Redis or
// Postgres implementation would query externally.
type TenantStore interface {
	// Lookup returns the tenant for a plaintext gateway key, or (nil, false).
	Lookup(ctx context.Context, plaintextKey string) (*domain.Tenant, bool)
}

// UsageRecorder observes per-credential/per-key usage. Today it records
// counters only; it is the seam where a future QuotaEnforcer would plug in.
type UsageRecorder interface {
	// Record notes one request's usage for the given credential and model.
	Record(credentialRef, tenantName, model string, reqCount int64)

	// Snapshot returns current counters for metrics export. Implementations
	// must be safe for concurrent use and must not block callers.
	Snapshot() []UsageCounter
}

// UsageCounter is one observed counter row.
type UsageCounter struct {
	CredentialRef string
	TenantName    string
	Model         string
	Requests      int64
}

// KeyAction represents an automated lifecycle action triggered on a failing key.
type KeyAction string

const (
	KeyActionDeactivate KeyAction = "deactivate"
	KeyActionDelete     KeyAction = "delete"
	KeyActionCooldown   KeyAction = "cooldown"
)

// KeyActionNotifier receives automated key lifecycle events triggered by error thresholds.
type KeyActionNotifier interface {
	NotifyKeyAction(action KeyAction, upstreamName, ref string, keyID int64, reason string)
}
