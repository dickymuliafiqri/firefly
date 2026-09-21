package warp

import (
	"context"
	"net"
	"time"
)

// Status describes the real-time operational status of the embedded Cloudflare WARP tunnel.
type Status struct {
	Enabled bool `json:"enabled"`
	// PublicIP is the egress address the Cloudflare edge reported for the
	// tunnel (v4 or v6, whichever the tunnel negotiated). It is empty until a
	// session is dialled and probed; the internal tunnel address is reported
	// separately and is never substituted for this one.
	PublicIP     string `json:"public_ip,omitempty"`
	InternalIPv4 string `json:"internal_ip,omitempty"`
	Colo         string `json:"colo,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`

	HandshakeLatencyMs int64 `json:"latency_ms"`
	// ActiveConnections counts live connections dialled through the tunnel.
	ActiveConnections int `json:"active_connections"`
	// DrainingSessions counts superseded tunnels kept open for their connections.
	DrainingSessions int `json:"draining_sessions,omitempty"`
	// omitzero, not omitempty: a time.Time is a struct and omitempty never
	// fires, which used to publish 0001-01-01 as the last rotation date.
	RotatedAt time.Time `json:"last_rotated_at,omitzero"`
	Error     string    `json:"error,omitempty"`
}

// Dialer defines a context-aware network connection dialer interface.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}
