package warp

import (
	"context"
	"net"
	"time"
)

// Status describes the real-time operational status of the embedded Cloudflare WARP tunnel.
type Status struct {
	Enabled            bool      `json:"enabled"`
	PublicIPv4         string    `json:"public_ip"`
	Colo               string    `json:"colo,omitempty"`
	Endpoint           string    `json:"endpoint,omitempty"`
	HandshakeLatencyMs int64     `json:"latency_ms"`
	ActiveSessions     int       `json:"active_sessions"`
	RotatedAt          time.Time `json:"last_rotated_at,omitempty"`
	Error              string    `json:"error,omitempty"`
}

// Dialer defines a context-aware network connection dialer interface.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}
