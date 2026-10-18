package singbox

import (
	"strings"

	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

// splitProxyAuthority splits a plain `user:pass@host:port` authority, the
// shape SOCKS and HTTP share links use. Credentials are optional.
func splitProxyAuthority(body string) (user, password, host string, port uint16, err error) {
	at := strings.LastIndex(body, "@")
	if at < 0 {
		host, port, err = parseAuthority(percentDecode(body))
		return "", "", host, port, err
	}
	user, password = splitCredentials(percentDecode(body[:at]))
	host, port, err = parseAuthority(percentDecode(body[at+1:]))
	return user, password, host, port, err
}

// parseSOCKS translates a `socks://user:pass@host:port#name` share link into a
// SOCKS5 outbound.
func parseSOCKS(body string) (*Node, error) {
	body, fragment := splitFragment(body)
	body, _ = splitQuery(body)
	user, password, host, port, err := splitProxyAuthority(body)
	if err != nil {
		return nil, err
	}
	options := &option.SOCKSOutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		Version:       "5",
		Username:      user,
		Password:      password,
	}
	return nodeOutbound(constant.TypeSOCKS, fragment, host, port, option.Outbound{Options: options}), nil
}

// parseHTTP translates a `http://` / `https://` proxy share link. The `https`
// scheme enables TLS against the proxy itself; certificate verification is
// only disabled when the link explicitly asks for it.
func parseHTTP(body, scheme string) (*Node, error) {
	body, fragment := splitFragment(body)
	body, query := splitQuery(body)
	user, password, host, port, err := splitProxyAuthority(body)
	if err != nil {
		return nil, err
	}
	options := &option.HTTPOutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		Username:      user,
		Password:      password,
	}
	if strings.EqualFold(scheme, "https") {
		options.OutboundTLSOptionsContainer = option.OutboundTLSOptionsContainer{
			TLS: buildTLS(query, "tls", queryFirst(query, "sni", "host")),
		}
	}
	return nodeOutbound(constant.TypeHTTP, fragment, host, port, option.Outbound{Options: options}), nil
}
