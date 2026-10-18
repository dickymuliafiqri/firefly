package singbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

func TestDecodeBase64StringVariants(t *testing.T) {
	payload := "hello:world"
	std := base64.StdEncoding.EncodeToString([]byte(payload))
	urlSafe := base64.URLEncoding.EncodeToString([]byte(payload))
	rawURLSafe := base64.RawURLEncoding.EncodeToString([]byte(payload))
	for _, encoded := range []string{std, urlSafe, rawURLSafe} {
		got, err := decodeBase64String(encoded)
		if err != nil {
			t.Fatalf("decodeBase64String(%q): %v", encoded, err)
		}
		if got != payload {
			t.Errorf("decodeBase64String(%q) = %q, want %q", encoded, got, payload)
		}
	}
}

func TestDecodeBase64StringIgnoresWhitespace(t *testing.T) {
	payload := "method:password"
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))
	withSpace := encoded[:4] + "\n  " + encoded[4:]
	got, err := decodeBase64String(withSpace)
	if err != nil {
		t.Fatal(err)
	}
	if got != payload {
		t.Fatalf("got %q, want %q", got, payload)
	}
}

func TestParseShadowsocksSIP002(t *testing.T) {
	// ss://base64(method:password)@host:port#name
	userinfo := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:secret"))
	line := "ss://" + userinfo + "@hk.example.com:8388#Hong%20Kong"
	res := ParseSubscription([]byte(line))
	if len(res.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1 (skipped=%d)", len(res.Nodes), res.SkippedLines)
	}
	n := res.Nodes[0]
	if n.Type != constant.TypeShadowsocks {
		t.Errorf("type = %q", n.Type)
	}
	if n.Server != "hk.example.com" || n.Port != 8388 {
		t.Errorf("server = %s:%d", n.Server, n.Port)
	}
	if n.Tag != "Hong-Kong" {
		t.Errorf("tag = %q, want %q", n.Tag, "Hong-Kong")
	}
	opts, ok := n.Outbound.Options.(*option.ShadowsocksOutboundOptions)
	if !ok {
		t.Fatalf("options type = %T", n.Outbound.Options)
	}
	if opts.Method != "aes-256-gcm" || opts.Password != "secret" {
		t.Errorf("method/password = %q/%q", opts.Method, opts.Password)
	}
}

func TestParseShadowsocksLegacyBase64(t *testing.T) {
	// Legacy: whole `method:password@host:port` is base64.
	blob := base64.RawURLEncoding.EncodeToString([]byte("chacha20-poly1305:pw@sg.example.com:443"))
	res := ParseSubscription([]byte("ss://" + blob + "#SG"))
	if len(res.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(res.Nodes))
	}
	n := res.Nodes[0]
	if n.Server != "sg.example.com" || n.Port != 443 {
		t.Fatalf("server = %s:%d", n.Server, n.Port)
	}
}

func TestParseVLESSRealityPreserved(t *testing.T) {
	line := "vless://11111111-2222-3333-4444-555555555555@jp.example.com:443" +
		"?security=reality&pbk=PUBKEY123&sid=abcd&fp=chrome&type=tcp&sni=www.example.com#JP-Reality"
	res := ParseSubscription([]byte(line))
	if len(res.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1 (skipped=%d)", len(res.Nodes), res.SkippedLines)
	}
	opts, ok := res.Nodes[0].Outbound.Options.(*option.VLESSOutboundOptions)
	if !ok {
		t.Fatalf("options type = %T", res.Nodes[0].Outbound.Options)
	}
	if opts.UUID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("uuid = %q", opts.UUID)
	}
	if opts.TLS == nil {
		t.Fatal("TLS is nil; reality must not be dropped")
	}
	if opts.TLS.Reality == nil || !opts.TLS.Reality.Enabled {
		t.Fatal("reality options missing/disabled")
	}
	if opts.TLS.Reality.PublicKey != "PUBKEY123" || opts.TLS.Reality.ShortID != "abcd" {
		t.Errorf("reality = %+v", opts.TLS.Reality)
	}
	if opts.TLS.UTLS == nil || opts.TLS.UTLS.Fingerprint != "chrome" {
		t.Errorf("utls fingerprint = %+v", opts.TLS.UTLS)
	}
	// Certificate verification must stay ON because the link never asked to skip it.
	if opts.TLS.Insecure {
		t.Error("insecure must default to false")
	}
}

func TestParseVLESSInsecureOnlyWhenRequested(t *testing.T) {
	base := "vless://uuid-1@host.example.com:443?security=tls"
	plain := ParseSubscription([]byte(base + "#a"))
	if len(plain.Nodes) != 1 {
		t.Fatal("expected one node")
	}
	plainOpts := plain.Nodes[0].Outbound.Options.(*option.VLESSOutboundOptions)
	if plainOpts.TLS == nil || plainOpts.TLS.Insecure {
		t.Fatal("insecure must be false by default")
	}

	asked := ParseSubscription([]byte(base + "&allowInsecure=1#b"))
	if len(asked.Nodes) != 1 {
		t.Fatal("expected one node")
	}
	askedOpts := asked.Nodes[0].Outbound.Options.(*option.VLESSOutboundOptions)
	if askedOpts.TLS == nil || !askedOpts.TLS.Insecure {
		t.Fatal("allowInsecure=1 must disable verification")
	}
}

func TestParseTrojanDefaultsToTLS(t *testing.T) {
	line := "trojan://mypassword@trojan.example.com:8443#Trojan"
	res := ParseSubscription([]byte(line))
	if len(res.Nodes) != 1 {
		t.Fatalf("nodes = %d", len(res.Nodes))
	}
	opts, ok := res.Nodes[0].Outbound.Options.(*option.TrojanOutboundOptions)
	if !ok {
		t.Fatalf("options type = %T", res.Nodes[0].Outbound.Options)
	}
	if opts.Password != "mypassword" {
		t.Errorf("password = %q", opts.Password)
	}
	if opts.TLS == nil || !opts.TLS.Enabled {
		t.Fatal("trojan must default to TLS")
	}
}

func TestParseVMessJSON(t *testing.T) {
	link := vmessShareLink{
		Name:     "VMess Node",
		Address:  "vm.example.com",
		Port:     flexiblePort(10086),
		UUID:     "aaaa-bbbb",
		Security: "auto",
		Network:  "ws",
		Path:     "/ws",
		Host:     "vm.example.com",
		TLS:      "tls",
	}
	raw, err := json.Marshal(link)
	if err != nil {
		t.Fatal(err)
	}
	line := "vmess://" + base64.RawURLEncoding.EncodeToString(raw)
	res := ParseSubscription([]byte(line))
	if len(res.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1 (skipped=%d)", len(res.Nodes), res.SkippedLines)
	}
	opts, ok := res.Nodes[0].Outbound.Options.(*option.VMessOutboundOptions)
	if !ok {
		t.Fatalf("options type = %T", res.Nodes[0].Outbound.Options)
	}
	if opts.UUID != "aaaa-bbbb" {
		t.Errorf("uuid = %q", opts.UUID)
	}
	// alter_id is deprecated in sing-box 1.14; it must always be zero.
	if opts.AlterId != 0 {
		t.Errorf("alterId = %d, want 0", opts.AlterId)
	}
	if opts.Transport == nil || opts.Transport.Type != constant.V2RayTransportTypeWebsocket {
		t.Fatalf("transport = %+v", opts.Transport)
	}
	if opts.TLS == nil || !opts.TLS.Enabled {
		t.Fatal("vmess tls should be enabled")
	}
}

func TestParseVMessFlexiblePortString(t *testing.T) {
	// Some clients emit port as a quoted string.
	payload := `{"ps":"x","add":"h.example.com","port":"443","id":"uuid-x","scy":"auto"}`
	line := "vmess://" + base64.RawURLEncoding.EncodeToString([]byte(payload))
	res := ParseSubscription([]byte(line))
	if len(res.Nodes) != 1 {
		t.Fatalf("nodes = %d (skipped=%d)", len(res.Nodes), res.SkippedLines)
	}
	if res.Nodes[0].Port != 443 {
		t.Errorf("port = %d, want 443", res.Nodes[0].Port)
	}
}

func TestParseHysteria2(t *testing.T) {
	line := "hysteria2://pass123@hy.example.com:443,8443?obfs=salamander&obfs-password=obfspw&sni=hy.example.com#HY2"
	res := ParseSubscription([]byte(line))
	if len(res.Nodes) != 1 {
		t.Fatalf("nodes = %d (skipped=%d)", len(res.Nodes), res.SkippedLines)
	}
	n := res.Nodes[0]
	if n.Server != "hy.example.com" || n.Port != 443 {
		t.Fatalf("server = %s:%d", n.Server, n.Port)
	}
	opts, ok := n.Outbound.Options.(*option.Hysteria2OutboundOptions)
	if !ok {
		t.Fatalf("options type = %T", n.Outbound.Options)
	}
	if opts.Password != "pass123" {
		t.Errorf("password = %q", opts.Password)
	}
	if len(opts.ServerPorts) != 1 || opts.ServerPorts[0] != "8443" {
		t.Errorf("server ports = %v", opts.ServerPorts)
	}
	if opts.Obfs == nil || opts.Obfs.Type != "salamander" || opts.Obfs.Password != "obfspw" {
		t.Errorf("obfs = %+v", opts.Obfs)
	}
	if opts.TLS == nil || !opts.TLS.Enabled || opts.TLS.Insecure {
		t.Errorf("tls = %+v", opts.TLS)
	}
}

func TestParseTUICCongestionControl(t *testing.T) {
	line := "tuic://uuid-t:tuicpw@tuic.example.com:443?congestion_control=bbr&udp_relay_mode=native&zero_rtt_handshake=1#TUIC"
	res := ParseSubscription([]byte(line))
	if len(res.Nodes) != 1 {
		t.Fatalf("nodes = %d (skipped=%d)", len(res.Nodes), res.SkippedLines)
	}
	opts, ok := res.Nodes[0].Outbound.Options.(*option.TUICOutboundOptions)
	if !ok {
		t.Fatalf("options type = %T", res.Nodes[0].Outbound.Options)
	}
	if opts.UUID != "uuid-t" || opts.Password != "tuicpw" {
		t.Errorf("uuid/password = %q/%q", opts.UUID, opts.Password)
	}
	if opts.CongestionControl != "bbr" {
		t.Errorf("cc = %q", opts.CongestionControl)
	}
	if !opts.ZeroRTTHandshake {
		t.Error("zero rtt handshake should be enabled")
	}
}

func TestParseSOCKSAndHTTP(t *testing.T) {
	lines := []string{
		"socks5://user:pass@socks.example.com:1080#S",
		"http://user:pass@http.example.com:8080#H",
	}
	res := ParseSubscription([]byte(strings.Join(lines, "\n")))
	if len(res.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (skipped=%d)", len(res.Nodes), res.SkippedLines)
	}
	socks, ok := res.Nodes[0].Outbound.Options.(*option.SOCKSOutboundOptions)
	if !ok {
		t.Fatalf("socks options type = %T", res.Nodes[0].Outbound.Options)
	}
	if socks.Version != "5" || socks.Username != "user" || socks.Password != "pass" {
		t.Errorf("socks = %+v", socks)
	}
	httpOpts, ok := res.Nodes[1].Outbound.Options.(*option.HTTPOutboundOptions)
	if !ok {
		t.Fatalf("http options type = %T", res.Nodes[1].Outbound.Options)
	}
	if httpOpts.Username != "user" {
		t.Errorf("http username = %q", httpOpts.Username)
	}
}

func TestParseSubscriptionWholeBodyBase64(t *testing.T) {
	plain := strings.Join([]string{
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:pw1")) + "@a.example.com:8388#A",
		"trojan://pw2@b.example.com:443#B",
	}, "\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(plain))
	res := ParseSubscription([]byte(encoded))
	if len(res.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (skipped=%d)", len(res.Nodes), res.SkippedLines)
	}
}

func TestParseSubscriptionSkipsUnknownProtocol(t *testing.T) {
	body := strings.Join([]string{
		"wireguard://something",
		"trojan://pw@c.example.com:443#C",
		"# a comment",
		"not-a-link",
	}, "\n")
	res := ParseSubscription([]byte(body))
	if len(res.Nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(res.Nodes))
	}
	if res.SkippedLines != 2 {
		t.Errorf("skipped = %d, want 2", res.SkippedLines)
	}
}

func TestUniquifyTagAvoidsReservedAndDuplicates(t *testing.T) {
	seen := make(map[string]int)
	// A node named "auto" must not collide with the built-in URLTest group.
	first := uniquifyTag("auto", "trojan", "h1", 443, 0, seen)
	if first == domain.SingboxDefaultOutboundTag {
		t.Fatalf("tag %q collides with reserved %q", first, domain.SingboxDefaultOutboundTag)
	}
	// Two nodes with the same display name get distinct tags.
	a := uniquifyTag("Tokyo", "trojan", "h1", 443, 1, seen)
	b := uniquifyTag("Tokyo", "trojan", "h2", 443, 2, seen)
	if a == b {
		t.Fatalf("duplicate tags generated: %q", a)
	}
	if !domain.ValidSingboxTag(a) || !domain.ValidSingboxTag(b) {
		t.Fatalf("generated invalid tags: %q / %q", a, b)
	}
	// No display name falls back to a positional tag.
	fallback := uniquifyTag("", "vmess", "", 0, 5, seen)
	if !domain.ValidSingboxTag(fallback) {
		t.Fatalf("fallback tag %q invalid", fallback)
	}
}

func TestBuildTLSRealityOverridesPlain(t *testing.T) {
	tls := buildTLS(nil, "reality", "sni.example.com")
	if tls == nil || tls.Reality == nil {
		t.Fatal("reality must produce a Reality block")
	}
	if !tls.Enabled {
		t.Error("tls should be enabled")
	}
}

func TestBuildTLSDisabledForNone(t *testing.T) {
	if tls := buildTLS(nil, "none", ""); tls != nil {
		t.Fatalf("security=none should yield nil, got %+v", tls)
	}
	if tls := buildTLS(nil, "", ""); tls != nil {
		t.Fatalf("absent security should yield nil, got %+v", tls)
	}
}

func TestFetchSubscription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != subscriptionUserAgent {
			t.Errorf("user agent = %q, want %q", ua, subscriptionUserAgent)
		}
		_, _ = w.Write([]byte("trojan://pw@x.example.com:443#X"))
	}))
	defer srv.Close()

	body, err := FetchSubscription(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "trojan://") {
		t.Fatalf("body = %q", body)
	}
}

func TestFetchSubscriptionRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := FetchSubscription(context.Background(), srv.URL); err == nil {
		t.Fatal("expected an error for a 403 response")
	}
}

func TestFetchSubscriptionRejectsEmptyURL(t *testing.T) {
	if _, err := FetchSubscription(context.Background(), "   "); err == nil {
		t.Fatal("expected an error for an empty URL")
	}
}

// Providers pad a pool with the same physical endpoint under variant display
// names (the Datacamp SNI/DIRECT clones are the real-world case). The filter
// keys on server:port alone: the first link wins, the clone is counted as a
// duplicate, and a different port is a genuinely different destination.
func TestParseSubscriptionDropsDuplicateEndpoints(t *testing.T) {
	userinfo := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:secret"))
	lines := []string{
		"ss://" + userinfo + "@156.146.38.169:443#1-----Datacamp-Limited-SHADOWSOCKS-SNI",
		"ss://" + userinfo + "@156.146.38.169:443#2-----Datacamp-Limited-SHADOWSOCKS-DIRECT",
		// Same host, different port: kept — a distinct endpoint.
		"ss://" + userinfo + "@156.146.38.169:8443#3-----Other-Port",
	}
	res := ParseSubscription([]byte(strings.Join(lines, "\n")))

	if len(res.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (skipped=%d duplicates=%d)", len(res.Nodes), res.SkippedLines, res.Duplicates)
	}
	if res.Duplicates != 1 {
		t.Errorf("duplicates = %d, want 1", res.Duplicates)
	}
	if res.SkippedLines != 0 {
		t.Errorf("skipped = %d, want 0 (dedup must not count as skip)", res.SkippedLines)
	}
	// First link wins: its display name is the surviving tag.
	if res.Nodes[0].Tag != "1-----Datacamp-Limited-SHADOWSOCKS-SNI" {
		t.Errorf("first tag = %q, want the first link's name", res.Nodes[0].Tag)
	}
	if res.Nodes[1].Port != 8443 {
		t.Errorf("second node port = %d, want 8443", res.Nodes[1].Port)
	}
	// The dedup key is case-insensitive on the host, and a host that parsed
	// without a server never participates (it would otherwise collapse all
	// serverless entries into one bucket).
	res2 := ParseSubscription([]byte(
		"ss://" + userinfo + "@EXAMPLE.com:443#A\n" +
			"ss://" + userinfo + "@example.COM:443#B"))
	if len(res2.Nodes) != 1 || res2.Duplicates != 1 {
		t.Errorf("case-insensitive host: nodes=%d duplicates=%d, want 1/1", len(res2.Nodes), res2.Duplicates)
	}
}
