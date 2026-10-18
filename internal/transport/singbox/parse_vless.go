package singbox

import (
	"errors"
	"strings"

	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

// parseVLESS translates a `vless://uuid@host:port?params#name` share link,
// including its Reality parameters. A Reality link that loses `pbk`/`sid` does
// not degrade gracefully — it cannot connect — so those are carried through
// verbatim rather than normalized away.
func parseVLESS(body string) (*Node, error) {
	body, fragment := splitFragment(body)
	body, query := splitQuery(body)

	at := strings.LastIndex(body, "@")
	if at < 0 {
		return nil, errors.New("vless link is missing its uuid")
	}
	uuid := percentDecode(body[:at])
	if uuid == "" {
		return nil, errors.New("vless link is missing its uuid")
	}
	host, port, err := parseAuthority(percentDecode(body[at+1:]))
	if err != nil {
		return nil, err
	}

	options := &option.VLESSOutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		UUID:          uuid,
		Flow:          queryFirst(query, "flow"),
		Transport:     buildTransport(query),
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
			TLS: buildTLS(query, queryFirst(query, "security"), queryFirst(query, "sni", "peer", "host")),
		},
	}
	return nodeOutbound(constant.TypeVLESS, fragment, host, port, option.Outbound{Options: options}), nil
}

// parseTrojan translates a `trojan://password@host:port?params#name` share
// link. Trojan is TLS-only in practice, so an absent `security` defaults to
// TLS instead of leaving the node unencrypted.
func parseTrojan(body string) (*Node, error) {
	body, fragment := splitFragment(body)
	body, query := splitQuery(body)

	at := strings.LastIndex(body, "@")
	if at < 0 {
		return nil, errors.New("trojan link is missing its password")
	}
	password := percentDecode(body[:at])
	if password == "" {
		return nil, errors.New("trojan link is missing its password")
	}
	host, port, err := parseAuthority(percentDecode(body[at+1:]))
	if err != nil {
		return nil, err
	}

	security := queryFirst(query, "security")
	if security == "" {
		security = "tls"
	}
	options := &option.TrojanOutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		Password:      password,
		Transport:     buildTransport(query),
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
			TLS: buildTLS(query, security, queryFirst(query, "sni", "peer", "host")),
		},
	}
	return nodeOutbound(constant.TypeTrojan, fragment, host, port, option.Outbound{Options: options}), nil
}
