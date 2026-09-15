package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

const (
	// AutoTLSHTTPAddr and AutoTLSHTTPSAddr are intentionally fixed. Let’s
	// Encrypt HTTP-01 validates through the public standard ports; accepting
	// arbitrary UI-configured ports would imply a working setup when validation
	// cannot succeed.
	AutoTLSHTTPAddr  = ":80"
	AutoTLSHTTPSAddr = ":443"
)

var dnsLabelRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// AutoTLSConfig contains only the public ACME account and host information.
// Private key/certificate material is owned exclusively by autocert.DirCache.
type AutoTLSConfig struct {
	Enabled bool
	Domain  string
	Email   string
}

// NormalizeAutoTLSConfig validates untrusted Settings input and returns its
// canonical representation. The single exact hostname becomes the autocert
// allow-list, so Firefly can never mint certificates for arbitrary Host values.
func NormalizeAutoTLSConfig(cfg AutoTLSConfig) (AutoTLSConfig, error) {
	if !cfg.Enabled {
		return AutoTLSConfig{}, nil
	}

	cfg.Domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(cfg.Domain), "."))
	if cfg.Domain == "" {
		return AutoTLSConfig{}, errors.New("auto TLS domain is required")
	}
	if len(cfg.Domain) > 253 || net.ParseIP(cfg.Domain) != nil || strings.ContainsAny(cfg.Domain, "/:@") {
		return AutoTLSConfig{}, errors.New("auto TLS domain must be a public DNS hostname, not an IP address or URL")
	}
	for _, label := range strings.Split(cfg.Domain, ".") {
		if !dnsLabelRE.MatchString(label) {
			return AutoTLSConfig{}, fmt.Errorf("auto TLS domain contains invalid DNS label %q", label)
		}
	}
	// A single-label hostname cannot be publicly validated by Let’s Encrypt.
	if !strings.Contains(cfg.Domain, ".") {
		return AutoTLSConfig{}, errors.New("auto TLS domain must be a fully qualified public hostname")
	}

	cfg.Email = strings.TrimSpace(cfg.Email)
	address, err := mail.ParseAddress(cfg.Email)
	if err != nil || address.Address != cfg.Email || !strings.Contains(cfg.Email, "@") {
		return AutoTLSConfig{}, errors.New("a valid ACME notification email is required")
	}
	return cfg, nil
}

// AutoTLS owns the optional HTTP-01 and HTTPS listeners. It is deliberately
// separate from the normal data listener: existing HTTP deployments retain
// their configured address while HTTPS is introduced on standards ports 80/443.
type AutoTLS struct {
	mu sync.Mutex

	cacheDir string
	logger   *slog.Logger

	handler   http.Handler
	streamCtx context.Context
	grace     time.Duration

	config  AutoTLSConfig
	manager *autocert.Manager
	http    *http.Server
	https   *http.Server
	active  bool
}

// NewAutoTLS creates a controller with a persistent certificate cache path.
func NewAutoTLS(cacheDir string, logger *slog.Logger) *AutoTLS {
	if logger == nil {
		logger = slog.Default()
	}
	return &AutoTLS{
		cacheDir:  cacheDir,
		logger:    logger,
		streamCtx: context.Background(),
		grace:     DefaultShutdownGrace,
	}
}

// bind connects the controller to Firefly's already-wrapped data handler and
// decoupled stream context. It must happen before an enabled configuration is
// applied.
func (a *AutoTLS) bind(handler http.Handler, streamCtx context.Context, grace time.Duration) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.handler = handler
	if streamCtx != nil {
		a.streamCtx = streamCtx
	}
	if grace > 0 {
		a.grace = grace
	}
}

// Config returns a snapshot of the currently applied runtime configuration.
func (a *AutoTLS) Config() AutoTLSConfig {
	if a == nil {
		return AutoTLSConfig{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.config
}

// Active reports whether the HTTP-01 and HTTPS listeners are currently live.
func (a *AutoTLS) Active() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

// Apply starts, replaces, or stops the TLS listeners. Listener binding occurs
// before the method returns, allowing Settings to report a port/firewall error
// immediately instead of silently persisting a non-working configuration.
func (a *AutoTLS) Apply(input AutoTLSConfig) error {
	if a == nil {
		return errors.New("auto TLS controller is unavailable")
	}
	cfg, err := NormalizeAutoTLSConfig(input)
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.config == cfg && a.active == cfg.Enabled {
		return nil
	}
	if a.handler == nil {
		return errors.New("auto TLS controller is not attached to the Firefly server")
	}
	if err := a.stopLocked(); err != nil {
		return err
	}
	if !cfg.Enabled {
		a.config = cfg
		return nil
	}
	if err := os.MkdirAll(a.cacheDir, 0o700); err != nil {
		return fmt.Errorf("create certificate cache: %w", err)
	}
	if err := os.Chmod(a.cacheDir, 0o700); err != nil {
		return fmt.Errorf("secure certificate cache: %w", err)
	}

	manager := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(a.cacheDir),
		Email:      cfg.Email,
		HostPolicy: autocert.HostWhitelist(cfg.Domain),
	}
	fallback := a.redirectHandler(cfg.Domain)
	httpServer := a.newHTTPServer(manager.HTTPHandler(fallback))
	httpListener, err := net.Listen("tcp", AutoTLSHTTPAddr)
	if err != nil {
		return fmt.Errorf("listen HTTP-01 on %s: %w", AutoTLSHTTPAddr, err)
	}

	tlsConfig := manager.TLSConfig()
	tlsConfig.MinVersion = tls.VersionTLS12
	httpsServer := a.newHTTPServer(a.handler)
	httpsListener, err := tls.Listen("tcp", AutoTLSHTTPSAddr, tlsConfig)
	if err != nil {
		_ = httpListener.Close()
		return fmt.Errorf("listen HTTPS on %s: %w", AutoTLSHTTPSAddr, err)
	}

	a.manager = manager
	a.http = httpServer
	a.https = httpsServer
	a.config = cfg
	a.active = true
	a.serveAsync("auto TLS HTTP-01 server", httpServer, httpListener)
	a.serveAsync("auto TLS HTTPS server", httpsServer, httpsListener)
	a.logger.Info("auto TLS enabled", "domain", cfg.Domain, "http_addr", AutoTLSHTTPAddr, "https_addr", AutoTLSHTTPSAddr, "cache_dir", a.cacheDir)
	return nil
}

func (a *AutoTLS) newHTTPServer(handler http.Handler) *http.Server {
	streamCtx := a.streamCtx
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		IdleTimeout:       DefaultIdleTimeout,
		// Do not set WriteTimeout: HTTPS serves long-lived SSE exactly like the data listener.
		BaseContext: func(net.Listener) context.Context { return streamCtx },
	}
}

func (a *AutoTLS) redirectHandler(domain string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requestHostMatches(r.Host, domain) {
			http.NotFound(w, r)
			return
		}
		// Use the validated configured host rather than r.Host, avoiding a Host
		// header based open redirect while retaining path and query exactly.
		http.Redirect(w, r, "https://"+domain+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
}

func requestHostMatches(host, domain string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".") == domain
}

func (a *AutoTLS) serveAsync(name string, srv *http.Server, ln net.Listener) {
	go func() {
		a.logger.Info(name+" listening", "addr", ln.Addr().String())
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logger.Error(name+" stopped", "err", err)
		}
	}()
}

// Run waits for application shutdown and drains both optional listeners with
// the same grace period as the data listener.
func (a *AutoTLS) Run(ctx context.Context) {
	if a == nil {
		return
	}
	<-ctx.Done()
	if err := a.Shutdown(); err != nil {
		a.logger.Warn("auto TLS shutdown incomplete", "err", err)
	}
}

// Shutdown stops accepting ACME and HTTPS connections, then lets existing
// streams drain until the configured grace deadline.
func (a *AutoTLS) Shutdown() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopLocked()
}

func (a *AutoTLS) stopLocked() error {
	if a.http == nil && a.https == nil {
		a.active = false
		a.manager = nil
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.grace)
	defer cancel()
	var errs []error
	if a.https != nil {
		if err := a.https.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown HTTPS listener: %w", err))
			_ = a.https.Close()
		}
	}
	if a.http != nil {
		if err := a.http.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown HTTP-01 listener: %w", err))
			_ = a.http.Close()
		}
	}
	a.http = nil
	a.https = nil
	a.manager = nil
	a.active = false
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
