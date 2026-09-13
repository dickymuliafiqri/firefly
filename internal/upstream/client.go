// Package upstream owns outbound HTTP: a per-upstream connection pool cache and
// the transport tuning derived from each Upstream's config.
//
// Why a cache keyed by upstream name: http.Transport is expensive and holds
// keep-alive connections. Rebuilding one per request would defeat connection
// reuse and leak file descriptors. We therefore build one *http.Client per
// upstream and reuse it for the lifetime of the process. Upstreams are keyed by
// name and treated as effectively immutable; a reload that changes transport
// settings for an existing upstream name requires a restart (documented in
// config_schema.md). This is a deliberate simplicity trade-off for v1.
package upstream

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// Pool caches one *http.Client per upstream name. It is safe for concurrent use.
type Pool struct {
	mu      sync.RWMutex
	clients map[string]*http.Client
}

// NewPool returns an empty Pool.
func NewPool() *Pool {
	return &Pool{clients: make(map[string]*http.Client)}
}

// Client returns the pooled client for the upstream, building it on first use.
func (p *Pool) Client(u *domain.Upstream) *http.Client {
	if u == nil {
		return http.DefaultClient
	}
	p.mu.RLock()
	c, ok := p.clients[u.Name]
	p.mu.RUnlock()
	if ok {
		return c
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[u.Name]; ok { // lost the race; reuse
		return c
	}
	c = buildClient(u)
	p.clients[u.Name] = c
	return c
}

// buildClient constructs an *http.Client tuned from the Upstream definition.
//
// Tuned for high-concurrency (1,000+ concurrent streaming users):
// - MaxIdleConns >= 2,000 prevents connection thrashing.
// - MaxIdleConnsPerHost >= 1,000 keeps persistent connections warm.
// - MaxConnsPerHost >= 1,500 accommodates concurrent peak streams without queuing sockets.
// - ForceAttemptHTTP2 enables HTTP/2 multiplexing across physical TCP connections.
//
// Note: we deliberately do NOT set http.Client.Timeout. A client timeout caps
// the ENTIRE request including response body read, which would truncate long
// SSE streams. Instead we bound connection establishment (dial/header) via the
// transport and leave body streaming to per-request context deadlines.
func buildClient(u *domain.Upstream) *http.Client {
	idle := time.Duration(u.IdleTimeoutMs) * time.Millisecond
	if idle <= 0 {
		idle = 90 * time.Second
	}
	// Connection/response-header budget. This is a "time to start responding"
	// limit, not a total-request limit, so it is safe for streaming.
	headerTimeout := time.Duration(u.TimeoutMs) * time.Millisecond
	if headerTimeout <= 0 {
		headerTimeout = 30 * time.Second
	}

	maxIdlePerHost := u.MaxIdleConnsPerHost
	if maxIdlePerHost < 1000 {
		maxIdlePerHost = 1000
	}
	maxIdle := maxIdlePerHost * 2
	if maxIdle < 2000 {
		maxIdle = 2000
	}
	maxConnsPerHost := u.MaxConnsPerHost
	if maxConnsPerHost < 1500 {
		maxConnsPerHost = 1500
	}

	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          maxIdle,
		MaxIdleConnsPerHost:   maxIdlePerHost,
		MaxConnsPerHost:       maxConnsPerHost,
		IdleConnTimeout:       idle,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		// ResponseHeaderTimeout bounds the wait for upstream to send headers.
		ResponseHeaderTimeout: headerTimeout,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport: tr,
		// No overall Timeout: see doc comment.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Upstreams must not redirect API calls; a redirect is a
			// misconfiguration, not something to follow silently.
			return http.ErrUseLastResponse
		},
	}
}
