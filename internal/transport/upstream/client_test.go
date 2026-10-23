package upstream

import (
	"net/http"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

func TestPool_Client_ReusesClient(t *testing.T) {
	pool := NewPool(nil)
	u := &domain.Upstream{
		Name:       "test-up",
		BaseURL:    "https://api.openai.com/v1",
		EgressMode: "direct",
	}

	c1 := pool.Client(u)
	if c1 == nil {
		t.Fatal("expected non-nil client")
	}

	c2 := pool.Client(u)
	if c1 != c2 {
		t.Errorf("expected client to be reused, got %p vs %p", c1, c2)
	}
}

func TestPool_Client_RebuildsOnConfigChange(t *testing.T) {
	pool := NewPool(nil)
	u1 := &domain.Upstream{
		Name:       "test-up",
		BaseURL:    "https://api.openai.com/v1",
		EgressMode: "direct",
	}

	c1 := pool.Client(u1)
	if c1 == nil {
		t.Fatal("expected non-nil client")
	}

	// Change egress mode to proxy
	u2 := &domain.Upstream{
		Name:       "test-up",
		BaseURL:    "https://api.openai.com/v1",
		EgressMode: "proxy",
		ProxyURL:   "http://127.0.0.1:8080",
	}

	c2 := pool.Client(u2)
	if c2 == nil {
		t.Fatal("expected non-nil client after config change")
	}
	if c1 == c2 {
		t.Errorf("expected new client after config change, but got same instance %p", c1)
	}

	// Change to singbox with outbound tag
	u3 := &domain.Upstream{
		Name:               "test-up",
		BaseURL:            "https://api.openai.com/v1",
		EgressMode:         "singbox",
		SingboxOutboundTag: "node-1",
	}

	c3 := pool.Client(u3)
	if c3 == nil {
		t.Fatal("expected non-nil client after singbox config change")
	}
	if c2 == c3 {
		t.Errorf("expected new client after singbox config change, but got same instance %p", c2)
	}

	// Identical config should reuse c3
	c3Same := pool.Client(u3)
	if c3 != c3Same {
		t.Errorf("expected c3 to be reused, got %p vs %p", c3, c3Same)
	}
}

func TestPool_Invalidate(t *testing.T) {
	pool := NewPool(nil)
	u := &domain.Upstream{
		Name:       "test-up",
		BaseURL:    "https://api.openai.com/v1",
		EgressMode: "direct",
	}

	c1 := pool.Client(u)
	pool.Invalidate("test-up")

	c2 := pool.Client(u)
	if c1 == c2 {
		t.Errorf("expected new client after Invalidate, but got same %p", c1)
	}
}

func TestPool_Client_NilUpstream(t *testing.T) {
	pool := NewPool(nil)
	c := pool.Client(nil)
	if c != http.DefaultClient {
		t.Errorf("expected http.DefaultClient for nil upstream, got %v", c)
	}
}
