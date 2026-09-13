package registry

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

type dummyAdapter struct {
	proto domain.Protocol
}

func (d *dummyAdapter) Protocol() domain.Protocol { return d.proto }
func (d *dummyAdapter) Forward(_ context.Context, _ *domain.Target, _ ports.ForwardRequest, _ io.Writer) error {
	return nil
}

func TestAdapterRegistry_Basic(t *testing.T) {
	r := NewAdapterRegistry()

	// Empty protocol rejected
	if err := r.Register("", &dummyAdapter{proto: "test"}); err == nil {
		t.Fatal("expected error registering empty protocol")
	}

	// Nil adapter rejected
	if err := r.Register("test", nil); err == nil {
		t.Fatal("expected error registering nil adapter")
	}

	// Lookup non-existent
	if a, ok := r.Lookup("non-existent"); ok || a != nil {
		t.Fatalf("expected not found, got %v", a)
	}

	// Register valid
	adapter := &dummyAdapter{proto: domain.ProtocolOpenAI}
	if err := r.Register(domain.ProtocolOpenAI, adapter); err != nil {
		t.Fatalf("unexpected error registering adapter: %v", err)
	}

	// Lookup valid
	got, ok := r.Lookup(domain.ProtocolOpenAI)
	if !ok || got != adapter {
		t.Fatalf("expected adapter %v, got %v (ok=%v)", adapter, got, ok)
	}
}

func TestAdapterRegistry_Concurrent(t *testing.T) {
	r := NewAdapterRegistry()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(2)
		proto := domain.Protocol("proto")
		go func() {
			defer wg.Done()
			_ = r.Register(proto, &dummyAdapter{proto: proto})
		}()
		go func() {
			defer wg.Done()
			_, _ = r.Lookup(proto)
		}()
	}

	wg.Wait()

	got, ok := r.Lookup(domain.Protocol("proto"))
	if !ok || got == nil {
		t.Fatal("expected adapter to be present after concurrent execution")
	}
}
