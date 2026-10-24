package mihomo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/metacubex/mihomo/common/convert"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	_ "github.com/metacubex/mihomo/hub/executor"
	"gopkg.in/yaml.v3"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

const (
	// subscriptionMaxBytes bounds a downloaded subscription body (10MB).
	subscriptionMaxBytes = 10 << 20
	// subscriptionTimeout bounds the whole download.
	subscriptionTimeout = 30 * time.Second
	// subscriptionUserAgent is presented when downloading subscriptions.
	// Many panels (v2board, sspanel, etc.) recognize ClashMeta/Mihomo and return the full node list.
	subscriptionUserAgent = "ClashMeta; mihomo"

	// defaultHealthCheckURL is Cloudflare's generate_204 endpoint.
	defaultHealthCheckURL = "https://cp.cloudflare.com/generate_204"
	// defaultHealthCheckIntervalS is the evaluation interval for proxy groups in seconds.
	defaultHealthCheckIntervalS = 180
)

// Node represents a single parsed proxy node.
type Node struct {
	Tag    string
	Type   string
	Server string
	Port   uint16
	Proxy  C.Proxy
}

// ParseResult is the outcome of parsing a subscription body into Mihomo proxies and groups.
type ParseResult struct {
	Nodes        []Node
	Proxies      map[string]C.Proxy
	SkippedLines int
	Duplicates   int
}

// FetchSubscription downloads a subscription URL within a bounded time and size.
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

// ParseSubscription parses subscription bytes (base64 share links, plain share links, or Clash YAML)
// and compiles them into Mihomo proxy adapters and proxy groups according to the given strategy.
func ParseSubscription(raw []byte, strategy string, defaultTag string) (*ParseResult, error) {
	rawText := strings.TrimSpace(string(raw))
	if rawText == "" {
		return &ParseResult{Proxies: make(map[string]C.Proxy)}, nil
	}

	var rawProxies []map[string]any
	var skippedCount int

	// 1. Try parsing as V2Ray / Clash share link subscription (plain or base64)
	proxies, err := convert.ConvertsV2Ray(raw)
	if err == nil && len(proxies) > 0 {
		rawProxies = proxies
	} else {
		// 2. Try parsing as Clash YAML format
		var clashCfg struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if yerr := yaml.Unmarshal(raw, &clashCfg); yerr == nil && len(clashCfg.Proxies) > 0 {
			rawProxies = clashCfg.Proxies
		} else {
			return nil, fmt.Errorf("failed to parse subscription: %w", err)
		}
	}

	if len(rawProxies) == 0 {
		return nil, errors.New("no proxies found in subscription")
	}

	// Endpoint dedup: drop duplicates that target the same server:port
	seenEndpoints := make(map[string]struct{})
	validProxies := make([]map[string]any, 0, len(rawProxies))
	var duplicatesCount int

	for _, p := range rawProxies {
		server, _ := p["server"].(string)
		var portStr string
		switch v := p["port"].(type) {
		case int:
			portStr = strconv.Itoa(v)
		case string:
			portStr = v
		case float64:
			portStr = strconv.Itoa(int(v))
		default:
			portStr = fmt.Sprintf("%v", v)
		}

		if server != "" && portStr != "" && portStr != "0" {
			endpoint := strings.ToLower(server) + ":" + portStr
			if _, dup := seenEndpoints[endpoint]; dup {
				duplicatesCount++
				continue
			}
			seenEndpoints[endpoint] = struct{}{}
		}

		name, _ := p["name"].(string)
		if strings.TrimSpace(name) == "" {
			skippedCount++
			continue
		}

		// Ensure tag is clean
		sanitized := domain.SanitizeSingboxTag(name)
		if sanitized != "" {
			p["name"] = sanitized
		}

		validProxies = append(validProxies, p)
	}

	if len(validProxies) == 0 {
		return nil, errors.New("no valid proxies after filtering duplicates")
	}

	var nodeNames []string
	for _, p := range validProxies {
		if name, ok := p["name"].(string); ok && name != "" {
			nodeNames = append(nodeNames, name)
		}
	}

	// Determine strategy for group "auto"
	strat := domain.NormalizeSingboxStrategy(strategy)
	autoGroupType := "fallback"
	var autoGroupStrategy string

	switch strat {
	case domain.SingboxStrategyRoundRobin:
		autoGroupType = "load-balance"
		autoGroupStrategy = "round-robin"
	case domain.SingboxStrategyConsistentHash:
		autoGroupType = "load-balance"
		autoGroupStrategy = "consistent-hashing"
	case domain.SingboxStrategyURLTest:
		autoGroupType = "url-test"
	case domain.SingboxStrategyFallback:
		autoGroupType = "fallback"
	default:
		autoGroupType = "fallback"
	}

	autoGroup := map[string]any{
		"name":     domain.SingboxDefaultOutboundTag, // "auto"
		"type":     autoGroupType,
		"url":      defaultHealthCheckURL,
		"interval": defaultHealthCheckIntervalS,
		"proxies":  nodeNames,
	}
	if autoGroupStrategy != "" {
		autoGroup["strategy"] = autoGroupStrategy
	}

	// Build standard proxy groups
	proxyGroups := []map[string]any{
		autoGroup,
		{
			"name":     domain.SingboxFallbackOutboundTag, // "fallback"
			"type":     "fallback",
			"url":      defaultHealthCheckURL,
			"interval": defaultHealthCheckIntervalS,
			"proxies":  nodeNames,
		},
		{
			"name":     domain.SingboxRoundRobinOutboundTag, // "round-robin"
			"type":     "load-balance",
			"strategy": "round-robin",
			"url":      defaultHealthCheckURL,
			"interval": defaultHealthCheckIntervalS,
			"proxies":  nodeNames,
		},
		{
			"name":     "url-test", // "url-test"
			"type":     "url-test",
			"url":      defaultHealthCheckURL,
			"interval": defaultHealthCheckIntervalS,
			"proxies":  nodeNames,
		},
		{
			"name":    domain.SingboxSelectorOutboundTag, // "select"
			"type":    "select",
			"proxies": append([]string{domain.SingboxDefaultOutboundTag, domain.SingboxFallbackOutboundTag, domain.SingboxRoundRobinOutboundTag, "url-test"}, nodeNames...),
		},
	}

	rawCfg := config.DefaultRawConfig()
	rawCfg.Proxy = validProxies
	rawCfg.ProxyGroup = proxyGroups

	cfg, err := config.ParseRawConfig(rawCfg)
	if err != nil {
		return nil, fmt.Errorf("compile mihomo config: %w", err)
	}

	// Extract parsed nodes
	nodes := make([]Node, 0, len(validProxies))
	for _, p := range validProxies {
		name, _ := p["name"].(string)
		ptype, _ := p["type"].(string)
		server, _ := p["server"].(string)
		var port uint16
		switch v := p["port"].(type) {
		case int:
			port = uint16(v)
		case string:
			if u, err := strconv.ParseUint(v, 10, 16); err == nil {
				port = uint16(u)
			}
		case float64:
			port = uint16(v)
		}

		proxyAdapter := cfg.Proxies[name]
		nodes = append(nodes, Node{
			Tag:    name,
			Type:   ptype,
			Server: server,
			Port:   port,
			Proxy:  proxyAdapter,
		})
	}

	return &ParseResult{
		Nodes:        nodes,
		Proxies:      cfg.Proxies,
		SkippedLines: skippedCount,
		Duplicates:   duplicatesCount,
	}, nil
}
