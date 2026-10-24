package mihomo

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

func TestManagerLifecycleAndFailOpen(t *testing.T) {
	m := NewManager(nil)
	if m.Ready() {
		t.Fatal("new manager should not be ready")
	}

	// Fail-open direct dial when not ready
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	addr := strings.TrimPrefix(ts.URL, "http://")
	conn, err := m.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("fail-open direct dial should succeed: %v", err)
	}
	conn.Close()

	// Check status
	st := m.Status()
	if st.Ready || st.NodeCount != 0 {
		t.Fatalf("expected unready with 0 nodes, got ready=%v nodes=%d", st.Ready, st.NodeCount)
	}
}

func TestManagerSubscriptionRefreshAndStrategies(t *testing.T) {
	// Sample node links (ss, trojan, hysteria2, vless)
	nodes := []string{
		"ss://YWVzLTEyOC1nY206cGFzc3dvcmQ=@1.1.1.1:8388#SS-1",
		"ss://YWVzLTEyOC1nY206cGFzc3dvcmQ=@2.2.2.2:8388#SS-2",
		"trojan://password@3.3.3.3:443#Trojan-1",
	}
	body := strings.Join(nodes, "\n")
	b64 := base64.StdEncoding.EncodeToString([]byte(body))

	subServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(b64))
	}))
	defer subServer.Close()

	m := NewManager(nil)
	m.SetStrategy(domain.SingboxStrategyFallback)
	m.SetSubscriptionURL(subServer.URL, 10*time.Minute)

	if err := m.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh failed: %v", err)
	}

	if !m.Ready() {
		t.Fatal("manager should be ready after refresh")
	}

	st := m.Status()
	if st.NodeCount != 3 {
		t.Fatalf("expected 3 nodes, got %d", st.NodeCount)
	}

	m.mu.RLock()
	autoGroup := m.proxies[domain.SingboxDefaultOutboundTag]
	fbGroup := m.proxies[domain.SingboxFallbackOutboundTag]
	rrGroup := m.proxies[domain.SingboxRoundRobinOutboundTag]
	m.mu.RUnlock()

	if autoGroup == nil || autoGroup.Type() != C.Fallback {
		t.Fatalf("expected auto group Fallback, got %v", autoGroup)
	}
	if fbGroup == nil || fbGroup.Type() != C.Fallback {
		t.Fatalf("expected fb group Fallback, got %v", fbGroup)
	}
	if rrGroup == nil || rrGroup.Type() != C.LoadBalance {
		t.Fatalf("expected rr group LoadBalance, got %v", rrGroup)
	}

	// Test TaggedDialer on "direct"
	td := m.TaggedDialer("direct")
	directConn, err := td.DialContext(context.Background(), "tcp", "127.0.0.1:0")
	// Dialing port 0 fails, but verifies DialTag logic reached direct
	if err == nil {
		directConn.Close()
	}

	// Test probe
	m.SetNodeProbeForTest(func(ctx context.Context, p C.Proxy) (time.Duration, error) {
		return 42 * time.Millisecond, nil
	})
	m.CheckHealth(context.Background())

	st = m.Status()
	if len(st.Nodes) != 3 {
		t.Fatalf("expected 3 node statuses, got %d", len(st.Nodes))
	}
	for _, n := range st.Nodes {
		if n.Status != "active" {
			t.Errorf("node %s status = %q, want active", n.Tag, n.Status)
		}
		if n.LatencyMs == nil || *n.LatencyMs != 42 {
			t.Errorf("node %s latency = %v, want 42", n.Tag, n.LatencyMs)
		}
	}

	// Stop manager
	m.Stop()
	if m.Ready() {
		t.Fatal("manager should not be ready after stop")
	}
}

func TestTaggedDialerInterface(t *testing.T) {
	m := NewManager(nil)
	var _ Dialer = m
	var _ Dialer = m.TaggedDialer("auto")
	var _ Dialer = m.TaggedDialer("direct")
	var _ Dialer = m.TaggedDialer("round-robin")
}
