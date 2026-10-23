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
	// PoolSize is the configured number of concurrent WARP slots.
	PoolSize int `json:"pool_size,omitempty"`
	// PoolSizeMin and PoolSizeMax bound the size an operator may choose from the
	// dashboard. They travel in the status so the UI renders the engine's real
	// window instead of mirroring a copy of it that could drift.
	PoolSizeMin int `json:"pool_size_min,omitempty"`
	PoolSizeMax int `json:"pool_size_max,omitempty"`
	// ActiveSessions is the number of slots currently usable for new dials.
	ActiveSessions int `json:"active_sessions,omitempty"`
	// EgressIPs lists the public egress address of each active slot.
	EgressIPs []string `json:"egress_ips,omitempty"`
	// Sessions describes the status and metrics of each active slot in the pool.
	Sessions []SessionStatus `json:"sessions,omitempty"`
	// omitzero, not omitempty: a time.Time is a struct and omitempty never
	// fires, which used to publish 0001-01-01 as the last rotation date.
	RotatedAt time.Time `json:"last_rotated_at,omitzero"`

	// AutoRotateIntervalSeconds reports the automated rotation schedule in seconds.
	// A value of 0 indicates that automated periodic rotation is disabled.
	AutoRotateIntervalSeconds int `json:"auto_rotate_interval_seconds,omitempty"`

	// NextRotationAt reports when the next automated rotation is expected.
	NextRotationAt time.Time `json:"next_rotation_at,omitzero"`

	// LicenseConfigured reports whether a Cloudflare WARP+ / Zero Trust license
	// key is configured. The raw license key is never exposed in the API.
	LicenseConfigured bool `json:"license_configured"`

	// PreferredColo reports the Cloudflare data center (COLO) preferred for egress traffic.
	PreferredColo string `json:"preferred_colo,omitempty"`

	// AvailableColos lists the distinct Cloudflare data centers (COLOs) detected
	// across the active and cached WARP sessions.
	AvailableColos []string `json:"available_colos,omitempty"`

	Error string `json:"error,omitempty"`
}

// SessionStatus describes the real-time operational status and latency of an
// individual session in the WARP egress pool.
type SessionStatus struct {
	ID           int       `json:"id"`
	PublicIP     string    `json:"public_ip"`
	InternalIPv4 string    `json:"internal_ip,omitempty"`
	Colo         string    `json:"colo,omitempty"`
	Endpoint     string    `json:"endpoint,omitempty"`
	LatencyMs    int64     `json:"latency_ms"`
	ActiveConns  int64     `json:"active_conns"`
	Status       string    `json:"status"` // "active", "cooling", "unreachable", "primary"
	CreatedAt    time.Time `json:"created_at,omitzero"`
	LastCheckAt  time.Time `json:"last_check_at,omitzero"`
	Primary      bool      `json:"primary,omitempty"`
}

// Dialer defines a context-aware network connection dialer interface.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}
