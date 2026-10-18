package singbox

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
)

// errSkipNode marks a subscription line this package deliberately does not
// translate (an unsupported protocol, or a line it cannot make sense of). The
// caller counts these and moves on to the next line rather than failing the
// whole subscription.
var errSkipNode = errors.New("skipped subscription node")

// decodeBase64String decodes a base64 payload in any of the four common
// alphabets (standard/URL-safe, padded or not) after removing the whitespace
// providers wrap long blobs in. A subscription body or a `ss://` userinfo is
// never assumed to use one specific alphabet.
func decodeBase64String(s string) (string, error) {
	cleaned := strings.NewReplacer(
		"\n", "", "\r", "", " ", "", "\t", "",
		"-", "+", "_", "/",
	).Replace(strings.TrimSpace(s))
	if cleaned == "" {
		return "", errors.New("empty base64 payload")
	}
	if pad := len(cleaned) % 4; pad != 0 {
		cleaned += strings.Repeat("=", 4-pad)
	}
	decoded, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// splitFragment separates a share link's `#fragment` (the node's display name).
func splitFragment(rest string) (body, fragment string) {
	if i := strings.Index(rest, "#"); i >= 0 {
		return rest[:i], decodeFragment(rest[i+1:])
	}
	return rest, ""
}

// splitQuery separates a share link's query string into parsed values.
func splitQuery(body string) (string, url.Values) {
	i := strings.Index(body, "?")
	if i < 0 {
		return body, url.Values{}
	}
	values, err := url.ParseQuery(body[i+1:])
	if err != nil {
		return body[:i], url.Values{}
	}
	return body[:i], values
}

// decodeFragment percent-decodes a `#name` fragment, falling back to the raw
// text when it is not valid percent-encoding.
func decodeFragment(fragment string) string {
	if fragment == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(fragment); err == nil {
		return strings.TrimSpace(decoded)
	}
	return strings.TrimSpace(fragment)
}

// parseAuthority splits a `host:port` authority. A bare host without a port is
// accepted (the protocol keeps its own default) because some providers omit
// it; an unparseable port is an error, not a silent default.
func parseAuthority(hostport string) (string, uint16, error) {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return "", 0, errors.New("missing server address")
	}
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		return strings.Trim(hostport, "[]"), 0, nil
	}
	if host == "" {
		return "", 0, errors.New("missing server host")
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return "", 0, fmt.Errorf("invalid server port %q", portStr)
	}
	return host, uint16(port), nil
}

// splitCredentials splits a `user:password` pair. A value without a colon is
// returned as the username so protocols whose credential is a single token
// (trojan, hysteria2) can reuse the helper.
func splitCredentials(s string) (string, string) {
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// unwrapUserinfo returns the plaintext credential pair from a share link's
// userinfo, which providers encode either as plain `user:pass` or as base64.
// The encoding is detected from the decoded result, never assumed.
func unwrapUserinfo(userinfo string) (string, string, error) {
	if strings.Contains(userinfo, ":") {
		user, pass := splitCredentials(userinfo)
		return user, pass, nil
	}
	decoded, err := decodeBase64String(userinfo)
	if err != nil {
		return "", "", fmt.Errorf("decode credentials: %w", err)
	}
	if !strings.Contains(decoded, ":") {
		return decoded, "", nil
	}
	user, pass := splitCredentials(decoded)
	return user, pass, nil
}

// queryTruthy reports whether any of keys carries an explicit affirmative
// value. Only these spellings count, so an unrecognized value leaves a
// security-relevant switch at its safe default.
func queryTruthy(q url.Values, keys ...string) bool {
	for _, key := range keys {
		switch strings.ToLower(strings.TrimSpace(queryValue(q, key))) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

// queryValue returns the first value for key, matching the key
// case-insensitively. Share links disagree on the casing of the same parameter
// (`allowInsecure` vs `allowinsecure`, `serviceName` vs `servicename`), and
// url.Values is keyed exactly, so an exact lookup silently drops the parameter
// that turns a TLS verification off (or on).
func queryValue(q url.Values, key string) string {
	if v, ok := q[key]; ok {
		if len(v) > 0 {
			return v[0]
		}
		return ""
	}
	for k, values := range q {
		if strings.EqualFold(k, key) {
			if len(values) > 0 {
				return values[0]
			}
			return ""
		}
	}
	return ""
}

// queryFirst returns the first non-empty value among keys, in order.
func queryFirst(q url.Values, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(queryValue(q, key)); v != "" {
			return v
		}
	}
	return ""
}

// queryInt returns the first key that parses as an integer, or def.
func queryInt(q url.Values, def int, keys ...string) int {
	for _, key := range keys {
		raw := strings.TrimSpace(queryValue(q, key))
		if raw == "" {
			continue
		}
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	}
	return def
}

// queryList splits a comma-separated query value into its members.
func queryList(q url.Values, key string) badoption.Listable[string] {
	raw := strings.TrimSpace(queryValue(q, key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make(badoption.Listable[string], 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.ToLower(strings.TrimSpace(part)); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildTLS translates a share link's TLS query into sing-box outbound TLS
// options.
//
// Two rules are non-negotiable here: certificate verification is only disabled
// when the link explicitly asks for it (an insecure/allowInsecure/
// skip-cert-verify flag), and `security=reality` is modeled as a real
// RealityOptions block instead of being downgraded to plain TLS — a downgraded
// node does not merely lose its camouflage, it fails to connect at all.
func buildTLS(q url.Values, security, serverName string) *option.OutboundTLSOptions {
	switch strings.ToLower(strings.TrimSpace(security)) {
	case "tls", "reality":
	default:
		return nil
	}
	tls := &option.OutboundTLSOptions{
		Enabled:    true,
		ServerName: serverName,
		Insecure: queryTruthy(q,
			"allowinsecure", "allow_insecure", "insecure", "skip-cert-verify", "skip_cert_verify"),
	}
	if alpn := queryList(q, "alpn"); len(alpn) > 0 {
		tls.ALPN = alpn
	}
	// A uTLS fingerprint makes the handshake look like a real browser; the
	// literal "none" means "use the default stack".
	if fp := queryFirst(q, "fp", "fingerprint"); fp != "" && !strings.EqualFold(fp, "none") {
		tls.UTLS = &option.OutboundUTLSOptions{Fingerprint: fp}
	}
	if strings.EqualFold(strings.TrimSpace(security), "reality") {
		tls.Reality = &option.OutboundRealityOptions{
			Enabled:   true,
			PublicKey: queryFirst(q, "pbk", "publicKey", "public_key"),
			ShortID:   queryFirst(q, "sid", "shortId", "short_id"),
		}
	}
	return tls
}

// buildTransport translates a share link's `type=` transport parameter into a
// V2Ray transport block. An absent or plain-TCP value yields nil, which lets
// sing-box use the protocol's default transport.
func buildTransport(q url.Values) *option.V2RayTransportOptions {
	transportType := strings.ToLower(strings.TrimSpace(queryValue(q, "type")))
	path := queryValue(q, "path")
	host := queryFirst(q, "host", "sni-host")
	switch transportType {
	case "", "tcp", "raw":
		return nil
	case "ws", "websocket":
		ws := option.V2RayWebsocketOptions{Path: path}
		if host != "" {
			ws.Headers = badoption.HTTPHeader{"Host": badoption.Listable[string]{host}}
		}
		return &option.V2RayTransportOptions{
			Type:             constant.V2RayTransportTypeWebsocket,
			WebsocketOptions: ws,
		}
	case "grpc":
		return &option.V2RayTransportOptions{
			Type: constant.V2RayTransportTypeGRPC,
			GRPCOptions: option.V2RayGRPCOptions{
				ServiceName: queryFirst(q, "serviceName", "service_name"),
			},
		}
	case "http", "h2":
		httpOptions := option.V2RayHTTPOptions{Path: path}
		if host != "" {
			httpOptions.Host = badoption.Listable[string]{host}
		}
		return &option.V2RayTransportOptions{
			Type:        constant.V2RayTransportTypeHTTP,
			HTTPOptions: httpOptions,
		}
	case "quic":
		return &option.V2RayTransportOptions{Type: constant.V2RayTransportTypeQUIC}
	case "httpupgrade":
		return &option.V2RayTransportOptions{
			Type: constant.V2RayTransportTypeHTTPUpgrade,
			HTTPUpgradeOptions: option.V2RayHTTPUpgradeOptions{
				Host: host,
				Path: path,
			},
		}
	default:
		return nil
	}
}
