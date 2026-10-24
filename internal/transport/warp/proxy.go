package warp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/supervisor"
)

// EgressConfig specifies the outbound routing parameters.
type EgressConfig struct {
	Mode       string
	ProxyURL   string
	WarpDialer Dialer
	// SingboxDialer carries traffic for Mode == "singbox". It is a per-upstream
	// dialer already bound to the upstream's outbound tag, so the transport does
	// not need to know which node group a request belongs to.
	SingboxDialer Dialer
	DialTimeout   time.Duration
}

// EgressModeWarp selects the embedded WARP tunnel.
//
// It is an alias of domain.EgressModeWarp so that the config validator, the
// transport router, and the dashboard compare the same literal.
const EgressModeWarp = domain.EgressModeWarp

// IsWarpEgress reports whether mode selects the WARP tunnel. It lives next to the
// dialer selection it mirrors so that callers reasoning about a client's egress —
// dropping idle sockets on rotation, for instance — cannot drift from the
// transport that actually dials.
func IsWarpEgress(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), EgressModeWarp)
}

// IsSingboxEgress reports whether mode routes through the embedded proxy pool (sing-box or mihomo).
// It mirrors IsWarpEgress and lives beside the router that consumes it so the two
// cannot disagree.
func IsSingboxEgress(mode string) bool {
	m := strings.TrimSpace(mode)
	return strings.EqualFold(m, domain.EgressModeSingbox) || strings.EqualFold(m, domain.EgressModeMihomo)
}

// ConfigureTransportEgress configures the outbound dialer and proxy function on the provided http.Transport
// according to the specified egress mode (direct, warp, socks5/http proxy, or sing-box).
func ConfigureTransportEgress(tr *http.Transport, cfg EgressConfig) {
	if tr == nil {
		return
	}
	timeout := cfg.DialTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	dialContext := (&net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
	}).DialContext

	var proxyFunc func(*http.Request) (*url.URL, error) = http.ProxyFromEnvironment

	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch {
	case IsWarpEgress(mode):
		// Without a dialer there is no tunnel to route through, and the
		// environment proxy is left in place rather than disabled.
		if cfg.WarpDialer != nil {
			dialContext = cfg.WarpDialer.DialContext
			proxyFunc = nil
		}
	case IsSingboxEgress(mode):
		// Same contract as WARP: no dialer means no pool to route through, so
		// the environment proxy stays in place instead of being disabled. The
		// manager itself fails open to a direct dial when its Box is not ready.
		if cfg.SingboxDialer != nil {
			dialContext = cfg.SingboxDialer.DialContext
			proxyFunc = nil
		}
	case mode == domain.EgressModeProxy:
		trimmedURL := strings.TrimSpace(cfg.ProxyURL)
		if trimmedURL != "" {
			lowerURL := strings.ToLower(trimmedURL)
			if strings.HasPrefix(lowerURL, "socks5://") || strings.HasPrefix(lowerURL, "socks5h://") {
				if d, err := ProxyDialer(trimmedURL); err == nil {
					dialContext = d
					proxyFunc = nil
				}
			} else if pu, err := url.Parse(trimmedURL); err == nil {
				proxyFunc = http.ProxyURL(pu)
			}
		}
	}

	tr.DialContext = dialContext
	tr.Proxy = proxyFunc
}

// ProxyDialer returns a context-aware dialer function for a given proxy URL.
// Supported schemes: socks5, socks5h.
func ProxyDialer(proxyURLStr string) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	if strings.TrimSpace(proxyURLStr) == "" {
		return nil, fmt.Errorf("proxy URL cannot be empty")
	}

	u, err := url.Parse(proxyURLStr)
	if err != nil {
		return nil, fmt.Errorf("parse proxy url: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "socks5" && scheme != "socks5h" {
		return nil, fmt.Errorf("unsupported proxy scheme %q for direct dialing (use standard http proxy for http/https)", scheme)
	}

	forward := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	dialer, err := proxy.FromURL(u, forward)
	if err != nil {
		return nil, fmt.Errorf("create socks5 proxy dialer: %w", err)
	}

	if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
		return contextDialer.DialContext, nil
	}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		type result struct {
			conn net.Conn
			err  error
		}
		resCh := make(chan result, 1)

		go func() {
			defer supervisor.Guard("warp.egress-dial")()
			c, err := dialer.Dial(network, addr)
			resCh <- result{conn: c, err: err}
		}()

		select {
		case <-ctx.Done():
			go func() {
				defer supervisor.Guard("warp.egress-dial")()
				res := <-resCh
				if res.conn != nil {
					_ = res.conn.Close()
				}
			}()
			return nil, ctx.Err()
		case res := <-resCh:
			return res.conn, res.err
		}
	}, nil
}
