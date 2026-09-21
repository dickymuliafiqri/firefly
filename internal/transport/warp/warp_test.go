package warp

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestGenerateKeyPair(t *testing.T) {
	t.Parallel()

	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair failed: %v", err)
	}

	if len(kp.PrivateKey) != 32 {
		t.Errorf("expected 32-byte private key, got %d", len(kp.PrivateKey))
	}
	if len(kp.PublicKey) != 32 {
		t.Errorf("expected 32-byte public key, got %d", len(kp.PublicKey))
	}

	// Verify Curve25519 clamping
	if kp.PrivateKey[0]&7 != 0 {
		t.Errorf("private key byte 0 not clamped: %08b", kp.PrivateKey[0])
	}
	if kp.PrivateKey[31]&128 != 0 || kp.PrivateKey[31]&64 == 0 {
		t.Errorf("private key byte 31 not clamped: %08b", kp.PrivateKey[31])
	}

	if kp.PrivateKeyB64 == "" || kp.PublicKeyB64 == "" {
		t.Error("expected non-empty base64 keys")
	}
	if len(kp.PrivateKeyHex) != 64 {
		t.Errorf("expected 64-char hex key, got %d", len(kp.PrivateKeyHex))
	}
}

func TestProxyDialer_Validation(t *testing.T) {
	t.Parallel()

	// Empty
	_, err := ProxyDialer("")
	if err == nil {
		t.Error("expected error for empty proxy URL")
	}

	// Invalid scheme
	_, err = ProxyDialer("ftp://127.0.0.1:21")
	if err == nil {
		t.Error("expected error for unsupported scheme ftp")
	}

	// Valid SOCKS5
	dialer, err := ProxyDialer("socks5://127.0.0.1:1080")
	if err != nil {
		t.Fatalf("unexpected error for socks5 proxy URL: %v", err)
	}
	if dialer == nil {
		t.Fatal("expected non-nil dialer")
	}
}

func TestConfigureTransportEgress(t *testing.T) {
	t.Parallel()

	// 1. Direct mode (nil dialer, proxy from environment)
	trDirect := &http.Transport{}
	ConfigureTransportEgress(trDirect, EgressConfig{
		Mode: "direct",
	})
	if trDirect.DialContext == nil {
		t.Error("expected valid DialContext for direct egress")
	}

	// 2. Warp mode
	mgr := NewManager(nil, "")
	trWarp := &http.Transport{}
	ConfigureTransportEgress(trWarp, EgressConfig{
		Mode:       "warp",
		WarpDialer: mgr,
	})
	if trWarp.DialContext == nil {
		t.Error("expected valid DialContext for warp egress")
	}
	if trWarp.Proxy != nil {
		t.Error("expected nil Proxy function for warp mode")
	}

	// 3. Proxy mode (SOCKS5)
	trProxy := &http.Transport{}
	ConfigureTransportEgress(trProxy, EgressConfig{
		Mode:     "proxy",
		ProxyURL: "socks5://127.0.0.1:1080",
	})
	if trProxy.DialContext == nil {
		t.Error("expected valid DialContext for socks5 egress")
	}
	if trProxy.Proxy != nil {
		t.Error("expected nil Proxy function for socks5 egress (handled via DialContext)")
	}

	// 4. Proxy mode (HTTP)
	trHTTPProxy := &http.Transport{}
	ConfigureTransportEgress(trHTTPProxy, EgressConfig{
		Mode:     "proxy",
		ProxyURL: "http://127.0.0.1:8080",
	})
	if trHTTPProxy.Proxy == nil {
		t.Error("expected non-nil Proxy function for http proxy egress")
	}
}

func TestManager_StatusInitial(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil, "")
	st := mgr.Status()

	if st.Enabled {
		t.Error("expected uninitialized manager to have Enabled = false")
	}
	if st.ActiveConnections != 0 {
		t.Errorf("expected 0 active connections, got %d", st.ActiveConnections)
	}
	if st.PublicIP != "" {
		t.Errorf("expected no public ip before a tunnel exists, got %q", st.PublicIP)
	}
}

func TestLiveCloudflareRegistration(t *testing.T) {
	if os.Getenv("FIREFLY_LIVE_TESTS") != "1" {
		t.Skip("skipping live test; set FIREFLY_LIVE_TESTS=1 to run")
	}

	// Exercises the real Cloudflare registration + trace probe, including TLS
	// verification of the probe endpoint.
	mgr := NewManager(quietLogger(), "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess, err := mgr.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	t.Logf("Session Endpoint: %s", sess.Endpoint)
	t.Logf("Session Public IP: %s", sess.PublicIP)
	t.Logf("Session Colo: %s", sess.Colo)
}
