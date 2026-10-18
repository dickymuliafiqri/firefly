package singbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
)

// flexiblePort accepts a JSON number or a quoted number, because VMess share
// links are produced by many clients and disagree on which one `port`/`aid`
// should be.
type flexiblePort uint16

func (p *flexiblePort) UnmarshalJSON(data []byte) error {
	raw := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if raw == "" || raw == "null" {
		return nil
	}
	value, err := strconv.ParseUint(raw, 10, 16)
	if err != nil {
		return fmt.Errorf("invalid numeric field %q", raw)
	}
	*p = flexiblePort(value)
	return nil
}

// vmessShareLink is the base64-encoded JSON payload of a `vmess://` link.
type vmessShareLink struct {
	Name        string       `json:"ps"`
	Address     string       `json:"add"`
	Port        flexiblePort `json:"port"`
	UUID        string       `json:"id"`
	AlterID     flexiblePort `json:"aid"`
	Security    string       `json:"scy"`
	Network     string       `json:"net"`
	HeaderType  string       `json:"type"`
	Host        string       `json:"host"`
	Path        string       `json:"path"`
	TLS         string       `json:"tls"`
	SNI         string       `json:"sni"`
	ALPN        string       `json:"alpn"`
	Fingerprint string       `json:"fp"`
	PublicKey   string       `json:"pbk"`
	ShortID     string       `json:"sid"`
}

// vmessSecurityOrDefault maps a share link's `scy` onto a sing-box `security`
// value. An unrecognized or absent value becomes the protocol default rather
// than a value sing-box would reject.
func vmessSecurityOrDefault(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "aes-128-gcm":
		return "aes-128-gcm"
	case "chacha20-poly1305":
		return "chacha20-poly1305"
	case "none":
		return "none"
	case "zero":
		return "zero"
	default:
		return "auto"
	}
}

// parseVMess translates a `vmess://` share link: a base64 blob containing a
// JSON node description, optionally followed by a real query string carrying
// the Reality/TLS extras some providers moved out of the JSON.
func parseVMess(body string) (*Node, error) {
	body, fragment := splitFragment(body)
	body, query := splitQuery(body)

	decoded, err := decodeBase64String(body)
	if err != nil {
		return nil, fmt.Errorf("decode vmess link: %w", err)
	}
	var link vmessShareLink
	if err := json.Unmarshal([]byte(decoded), &link); err != nil {
		return nil, fmt.Errorf("decode vmess json: %w", err)
	}
	if strings.TrimSpace(link.Address) == "" || strings.TrimSpace(link.UUID) == "" {
		return nil, errors.New("vmess link is missing its address or uuid")
	}
	if link.Port == 0 {
		return nil, errors.New("vmess link is missing its server port")
	}
	host := percentDecode(link.Address)

	// TLS-related values may live in the JSON or in the trailing query; the
	// query wins when both are present.
	tlsQuery := url.Values{}
	if link.ALPN != "" {
		tlsQuery.Set("alpn", link.ALPN)
	}
	if link.Fingerprint != "" {
		tlsQuery.Set("fp", link.Fingerprint)
	}
	if link.PublicKey != "" {
		tlsQuery.Set("pbk", link.PublicKey)
	}
	if link.ShortID != "" {
		tlsQuery.Set("sid", link.ShortID)
	}
	if link.SNI != "" {
		tlsQuery.Set("sni", link.SNI)
	}
	for key, values := range query {
		if len(values) > 0 && values[0] != "" {
			tlsQuery.Set(key, values[0])
		}
	}
	security := strings.ToLower(strings.TrimSpace(link.TLS))
	if fromQuery := queryFirst(tlsQuery, "security"); fromQuery != "" {
		security = strings.ToLower(fromQuery)
	}
	serverName := queryFirst(tlsQuery, "sni", "host")

	options := &option.VMessOutboundOptions{
		ServerOptions: option.ServerOptions{Server: host, ServerPort: uint16(link.Port)},
		UUID:          strings.TrimSpace(link.UUID),
		Security:      vmessSecurityOrDefault(link.Security),
		// alter_id is rejected by sing-box 1.14 for non-zero values and the
		// AEAD-only era needs it at zero, so an `aid` in the link is ignored.
		AlterId:   0,
		Transport: buildTransport(vmessTransportQuery(link)),
		OutboundTLSOptionsContainer: option.OutboundTLSOptionsContainer{
			TLS: buildTLS(tlsQuery, security, serverName),
		},
	}
	return nodeOutbound(constant.TypeVMess, fragment, host, uint16(link.Port), option.Outbound{Options: options}), nil
}

// vmessTransportQuery reshapes the link's transport fields into the query
// spelling buildTransport understands. VMess carries the gRPC service name in
// `path` rather than a dedicated field.
func vmessTransportQuery(link vmessShareLink) url.Values {
	query := url.Values{}
	query.Set("type", strings.ToLower(strings.TrimSpace(link.Network)))
	if link.Path != "" {
		query.Set("path", link.Path)
	}
	if link.Host != "" {
		query.Set("host", link.Host)
	}
	if strings.EqualFold(link.Network, "grpc") && link.Path != "" {
		query.Set("serviceName", link.Path)
	}
	return query
}
