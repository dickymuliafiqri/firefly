package singbox

import (
	"errors"
	"strings"

	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

// normalizeCongestionControl maps the spellings share links use onto the three
// values sing-box accepts.
func normalizeCongestionControl(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "cubic":
		return "cubic"
	case "new_reno", "newreno", "reno":
		return "new_reno"
	case "bbr":
		return "bbr"
	default:
		return ""
	}
}

// parseTUIC translates a `tuic://uuid:password@host:port?params#name` share
// link. TUIC is a QUIC protocol, so TLS is inherent and `insecure` is honored
// only when the link asks for it.
func parseTUIC(body string) (*Node, error) {
	body, fragment := splitFragment(body)
	body, query := splitQuery(body)

	at := strings.LastIndex(body, "@")
	if at < 0 {
		return nil, errors.New("tuic link is missing its credentials")
	}
	uuid, password, err := unwrapUserinfo(percentDecode(body[:at]))
	if err != nil {
		return nil, err
	}
	if uuid == "" {
		return nil, errors.New("tuic link is missing its uuid")
	}
	host, port, err := parseAuthority(percentDecode(body[at+1:]))
	if err != nil {
		return nil, err
	}

	options := &option.TUICOutboundOptions{
		ServerOptions:     option.ServerOptions{Server: host, ServerPort: port},
		UUID:              uuid,
		Password:          password,
		CongestionControl: normalizeCongestionControl(queryFirst(query, "congestion_control", "congestion")),
		UDPRelayMode:      strings.ToLower(queryFirst(query, "udp_relay_mode", "udp-relay-mode")),
		ZeroRTTHandshake:  queryTruthy(query, "zero_rtt_handshake", "zero-rtt-handshake"),
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
			TLS: buildTLS(query, "tls", queryFirst(query, "sni", "peer", "host")),
		},
	}
	return nodeOutbound(constant.TypeTUIC, fragment, host, port, option.Outbound{Options: options}), nil
}
