package singbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sagernet/sing-box/option"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

const (
	// subscriptionMaxBytes bounds a downloaded subscription body. Share-link
	// lists are a few hundred kilobytes at most; anything larger is not one.
	subscriptionMaxBytes = 10 << 20
	// subscriptionTimeout bounds the whole download, not just the handshake.
	subscriptionTimeout = 30 * time.Second
	// subscriptionUserAgent is what the fetch presents. Providers commonly
	// serve a different (or no) payload to an unknown client, so this stays a
	// plain HTTP client identity rather than a friendly custom one.
	subscriptionUserAgent = "sing-box"
)

// Node is one parsed subscription entry: enough metadata for the dashboard to
// render it, plus the sing-box outbound it was translated into.
type Node struct {
	Tag      string
	Type     string
	Server   string
	Port     uint16
	Outbound option.Outbound
}

// ParseResult is the outcome of parsing a subscription body. SkippedLines
// counts entries that were recognized as a share link but could not be
// translated (an unsupported protocol or a malformed line); Duplicates counts
// entries dropped because their server:port endpoint already appeared earlier
// in the list. Both are reported rather than silently dropped, because a
// provider padding a pool with the same host under different display names is
// an operator-visible property of the subscription.
type ParseResult struct {
	Nodes        []Node
	SkippedLines int
	Duplicates   int
}

// FetchSubscription downloads a subscription URL within a bounded time and
// size, so a hostile or misconfigured endpoint cannot make the gateway hold an
// unbounded buffer.
func FetchSubscription(ctx context.Context, url string) ([]byte, error) {
	if strings.TrimSpace(url) == "" {
		return nil, errors.New("subscription url is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build subscription request: %w", err)
	}
	req.Header.Set("User-Agent", subscriptionUserAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch subscription: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch subscription: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, subscriptionMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read subscription: %w", err)
	}
	if len(body) > subscriptionMaxBytes {
		return nil, fmt.Errorf("subscription exceeds %d bytes", subscriptionMaxBytes)
	}
	return body, nil
}

// ParseSubscription translates a subscription body into sing-box outbounds.
// It accepts both a plain newline-separated share-link list and the common
// wrapper where the whole list is one base64 blob, and it never fails as a
// whole because one entry is unreadable.
func ParseSubscription(raw []byte) ParseResult {
	text := decodeSubscriptionBody(raw)
	if text == "" {
		return ParseResult{}
	}
	var result ParseResult
	seen := make(map[string]int)
	// Endpoint dedup: providers repeat one physical server under variant
	// display names (SNI vs DIRECT suffixes, numbered clones), which inflates
	// the pool without adding a destination. The key is server:port only —
	// protocol is deliberately not part of it, so two schemes on the same
	// endpoint collapse to whichever link came first.
	seenEndpoints := make(map[string]struct{})
	for index, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		node, err := parseNode(line)
		if err != nil {
			result.SkippedLines++
			continue
		}
		if node.Server != "" {
			endpoint := strings.ToLower(node.Server) + ":" + strconv.Itoa(int(node.Port))
			if _, dup := seenEndpoints[endpoint]; dup {
				result.Duplicates++
				continue
			}
			seenEndpoints[endpoint] = struct{}{}
		}
		node.Tag = uniquifyTag(node.Tag, node.Type, node.Server, node.Port, index, seen)
		node.Outbound.Tag = node.Tag
		result.Nodes = append(result.Nodes, *node)
	}
	return result
}

// decodeSubscriptionBody unwraps the whole-body base64 wrapper some providers
// use. Detection is content-based: a body that already contains a share-link
// scheme is returned untouched, so a plain list is never mangled.
func decodeSubscriptionBody(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	if strings.Contains(text, "://") {
		return text
	}
	if decoded, err := decodeBase64String(text); err == nil && strings.Contains(decoded, "://") {
		return strings.TrimSpace(decoded)
	}
	return text
}

// parseNode dispatches one share link to its protocol parser.
func parseNode(line string) (*Node, error) {
	separator := strings.Index(line, "://")
	if separator <= 0 {
		return nil, errSkipNode
	}
	scheme := strings.ToLower(strings.TrimSpace(line[:separator]))
	body := line[separator+3:]
	switch scheme {
	case "ss":
		return parseShadowsocks(body)
	case "vmess":
		return parseVMess(body)
	case "vless":
		return parseVLESS(body)
	case "trojan":
		return parseTrojan(body)
	case "hysteria2", "hy2":
		return parseHysteria2(body)
	case "tuic":
		return parseTUIC(body)
	case "socks", "socks5":
		return parseSOCKS(body)
	case "http", "https":
		return parseHTTP(body, scheme)
	default:
		// Known-but-unsupported protocols fail the same way an unknown one
		// does: the line is counted as skipped, never guessed at.
		return nil, errSkipNode
	}
}

// uniquifyTag produces a stable, unique, sing-box-valid tag for a node. The
// display name from the share link is preferred; a reserved group tag and a
// duplicate both fall back to a deterministic suffix so two nodes can never
// collapse into one.
func uniquifyTag(preferred, nodeType, server string, port uint16, index int, seen map[string]int) string {
	base := domain.SanitizeSingboxTag(preferred)
	if base == "" {
		if server != "" {
			base = domain.SanitizeSingboxTag(nodeType + "-" + server)
		}
		if base == "" {
			base = fmt.Sprintf("%s-%d", nodeType, index+1)
		}
	}
	switch base {
	case domain.SingboxDefaultOutboundTag, domain.SingboxSelectorOutboundTag, domain.SingboxDirectOutboundTag:
		base = "node-" + base
	}
	count := seen[base]
	seen[base] = count + 1
	if count == 0 {
		return base
	}
	suffix := fmt.Sprintf("-%d", count+1)
	prefix := domain.SanitizeSingboxTag(strings.TrimSuffix(base, suffix))
	limit := domain.MaxSingboxTagLen - len(suffix)
	if len(prefix) > limit {
		prefix = strings.TrimRight(prefix[:limit], "._-")
	}
	if prefix == "" {
		return fmt.Sprintf("node-%d%s", index+1, suffix)
	}
	return prefix + suffix
}

// nodeOutbound assembles a parsed node, keeping Type/Tag/Server consistent
// with the outbound it wraps.
func nodeOutbound(nodeType, tag, server string, port uint16, outbound option.Outbound) *Node {
	outbound.Type = nodeType
	return &Node{
		Tag:      tag,
		Type:     nodeType,
		Server:   server,
		Port:     port,
		Outbound: outbound,
	}
}
