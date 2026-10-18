package singbox

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

// parseShadowsocks translates an `ss://` share link. Both published shapes are
// accepted: SIP002 (`ss://base64(method:password)@host:port`) and the legacy
// form where the whole `method:password@host:port` is base64 encoded.
func parseShadowsocks(body string) (*Node, error) {
	body, fragment := splitFragment(body)
	body, query := splitQuery(body)

	var userinfo, hostport string
	if at := strings.LastIndex(body, "@"); at >= 0 {
		userinfo, hostport = body[:at], body[at+1:]
	} else {
		decoded, err := decodeBase64String(body)
		if err != nil {
			return nil, fmt.Errorf("decode shadowsocks link: %w", err)
		}
		at := strings.LastIndex(decoded, "@")
		if at < 0 {
			return nil, errors.New("shadowsocks link is missing its server address")
		}
		userinfo, hostport = decoded[:at], decoded[at+1:]
	}

	method, password, err := unwrapUserinfo(userinfo)
	if err != nil {
		return nil, err
	}
	if method == "" || password == "" {
		return nil, errors.New("shadowsocks link is missing its method or password")
	}
	host, port, err := parseAuthority(percentDecode(hostport))
	if err != nil {
		return nil, err
	}

	options := &option.ShadowsocksOutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		Method:        strings.ToLower(method),
		Password:      password,
	}
	if plugin := queryFirst(query, "plugin"); plugin != "" {
		options.Plugin = plugin
		options.PluginOptions = queryFirst(query, "plugin_opts", "plugin-opts")
	}
	return nodeOutbound(constant.TypeShadowsocks, fragment, host, port, option.Outbound{Options: options}), nil
}
