package notify

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsPublicIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"127.8.9.10", false},
		{"::1", false},
		{"10.0.0.1", false},
		{"172.16.4.4", false},
		{"172.31.255.255", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false},
		{"100.64.0.1", false},
		{"100.127.255.255", false},
		{"0.0.0.0", false},
		{"224.0.0.1", false},
		{"255.255.255.255", false},
		{"240.0.0.1", false},
		{"192.0.0.1", false},
		{"198.18.0.1", false},
		{"198.51.100.7", false},
		{"203.0.113.9", false},
		{"fc00::1", false},
		{"fe80::1", false},
		{"", false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if tc.ip == "" {
			if isPublicIP(nil) {
				t.Error("nil ip must not be public")
			}
			continue
		}
		if ip == nil {
			t.Fatalf("test fixture %q is not a valid ip", tc.ip)
		}
		if got := isPublicIP(ip); got != tc.want {
			t.Errorf("isPublicIP(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestSafeTransport_BlocksPrivateHosts(t *testing.T) {
	if safeTransport(false) == nil {
		t.Fatal("transport must not be nil")
	}

	blocked := []string{
		"127.0.0.1:80",
		"169.254.169.254:80",
		"10.1.2.3:443",
		"192.168.0.5:8080",
		"172.20.1.1:9000",
		"100.100.100.100:80",
	}
	tr := safeTransport(false)
	for _, addr := range blocked {
		_, err := tr.DialContext(context.Background(), "tcp", addr)
		if err == nil {
			t.Errorf("dial to %s should have been blocked", addr)
		}
	}
}

func TestSafeTransport_AllowsPrivateWhenConfigured(t *testing.T) {
	// A local test server is the only way to prove the allowlist path works
	// without touching the network.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	p := NewPoster(true)
	ch := Channel{URL: srv.URL, Format: "generic", Enabled: true}
	if err := p.Post(context.Background(), ch, sampleEvent()); err != nil {
		t.Fatalf("local delivery should succeed with AllowPrivate: %v", err)
	}
}

func TestSafeTransport_BlocksLoopbackByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request must never reach a loopback server when the guard is on")
	}))
	defer srv.Close()

	p := NewPoster(false)
	ch := Channel{URL: srv.URL, Format: "generic", Enabled: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Post(ctx, ch, sampleEvent()); err == nil {
		t.Fatal("loopback delivery must be refused")
	}
}
