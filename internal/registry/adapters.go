package registry

import (
	"errors"
	"sync"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

var (
	// ErrEmptyProtocol is returned when attempting to register with an empty protocol.
	ErrEmptyProtocol = errors.New("cannot register adapter with empty protocol")
	// ErrNilAdapter is returned when attempting to register a nil adapter.
	ErrNilAdapter = errors.New("cannot register nil adapter")
)

// AdapterRegistry is a thread-safe registry of UpstreamAdapter implementations
// keyed by domain.Protocol.
type AdapterRegistry struct {
	mu       sync.RWMutex
	adapters map[domain.Protocol]ports.UpstreamAdapter
}

// NewAdapterRegistry constructs an empty AdapterRegistry.
func NewAdapterRegistry() *AdapterRegistry {
	return &AdapterRegistry{
		adapters: make(map[domain.Protocol]ports.UpstreamAdapter),
	}
}

// Register associates an adapter with the specified protocol.
// It is thread-safe.
func (r *AdapterRegistry) Register(proto domain.Protocol, adapter ports.UpstreamAdapter) error {
	if proto == "" {
		return ErrEmptyProtocol
	}
	if adapter == nil {
		return ErrNilAdapter
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[proto] = adapter
	return nil
}

// Lookup returns the adapter registered for the protocol, or (nil, false) if not found.
// It is thread-safe.
func (r *AdapterRegistry) Lookup(proto domain.Protocol) (ports.UpstreamAdapter, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[proto]
	return a, ok
}
