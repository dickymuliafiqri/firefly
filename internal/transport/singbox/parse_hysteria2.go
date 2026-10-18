package singbox

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

// parseHysteria2 translates a `hysteria2://` (or `hy2://`) share link.
// Hysteria2 runs over QUIC, so TLS is inherent rather than optional and an
// `insecure=1` flag is honored only when the link states it.
func parseHysteria2(body string) (*Node, error) {
	body, fragment := splitFragment(body)
	body, query := splitQuery(body)

	at := strings.LastIndex(body, "@")
	if at < 0 {
		return nil, errors.New("hysteria2 link is missing its password")
	}
	password := percentDecode(body[:at])
	host, port, extraPorts, err := splitHostPortList(percentDecode(body[at+1:]))
	if err != nil {
		return nil, err
	}

	options := &option.Hysteria2OutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: port},
		ServerPorts:   extraPorts,
		Password:      password,
		UpMbps:        queryInt(query, 0, "up", "upmbps", "up_mbps"),
		DownMbps:      queryInt(query, 0, "down", "downmbps", "down_mbps"),
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
			TLS: buildTLS(query, "tls", queryFirst(query, "sni", "peer", "host")),
		},
	}
	if obfs := queryFirst(query, "obfs"); obfs != "" {
		options.Obfs = &option.Hysteria2Obfs{
			Type:     strings.ToLower(obfs),
			Password: queryFirst(query, "obfs-password", "obfs_password", "obfsParam"),
		}
	}
	return nodeOutbound(constant.TypeHysteria2, fragment, host, port, option.Outbound{Options: options}), nil
}

// splitHostPortList splits a share-link authority that may carry a
// comma-separated port list (hysteria2 port hopping), returning the primary
// port plus the remaining ones when there is more than one.
func splitHostPortList(hostport string) (host string, port uint16, extra []string, err error) {
	hostport = strings.TrimSpace(hostport)
	comma := strings.LastIndex(hostport, ",")
	if comma < 0 {
		host, port, err = parseAuthority(hostport)
		return host, port, nil, err
	}
	host, port, err = parseAuthority(hostport[:comma])
	if err != nil {
		return "", 0, nil, err
	}
	for _, candidate := range strings.Split(hostport[comma+1:], ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return "", 0, nil, fmt.Errorf("invalid server port list %q", hostport)
		}
		extra = append(extra, candidate)
	}
	return host, port, extra, nil
}
