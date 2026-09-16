// Command firefly is an OpenAI-wire-compatible, multi-tenant reverse proxy.
//
// It loads a hot-reloadable JSON config set (upstreams/models/tenants), serves
// the OpenAI API surface with per-tenant auth and rate limiting, and shuts down
// gracefully on SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/analytics"
	"github.com/dickymuliafiqri/firefly/internal/anthropic"
	"github.com/dickymuliafiqri/firefly/internal/antigravity"
	"github.com/dickymuliafiqri/firefly/internal/cline"
	"github.com/dickymuliafiqri/firefly/internal/codebuddy"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/grok"
	"github.com/dickymuliafiqri/firefly/internal/oauth"
	antigravityProvider "github.com/dickymuliafiqri/firefly/internal/oauth/providers/antigravity"
	clineProvider "github.com/dickymuliafiqri/firefly/internal/oauth/providers/cline"
	codebuddyProvider "github.com/dickymuliafiqri/firefly/internal/oauth/providers/codebuddy"

	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/logging"
	"github.com/dickymuliafiqri/firefly/internal/metrics"
	"github.com/dickymuliafiqri/firefly/internal/openai"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/server"
	"github.com/dickymuliafiqri/firefly/internal/turso"
	"github.com/dickymuliafiqri/firefly/internal/upstream"
	"github.com/dickymuliafiqri/firefly/internal/usage"
	"github.com/dickymuliafiqri/firefly/internal/watch"
)

var (
	// Version, Commit, and BuildDate are populated at build time via -ldflags.
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configDir         = flag.String("config-dir", "configs", "directory containing upstreams.json, models.json, tenants.json")
		addr              = flag.String("addr", "0.0.0.0:8080", "listen address")
		adminAddr         = flag.String("admin-addr", "", "admin listen address for /metrics and guarded /debug/* (empty disables)")
		logLevel          = flag.String("log-level", "info", "log level: debug|info|warn|error")
		graceSecs         = flag.Int("shutdown-grace-seconds", 30, "max seconds to drain in-flight requests on shutdown")
		adminToken        = flag.String("admin-token", "", "bearer token guarding /debug/* endpoints (defaults to $FIREFLY_ADMIN_TOKEN; empty disables them)")
		dashboardPassword = flag.String("dashboard-password", "", "master password for dashboard access (defaults to $FIREFLY_DASHBOARD_PASSWORD or 12345678)")
		healthInterval    = flag.Duration("health-check-interval", upstream.DefaultHealthCheckInterval, "interval between background upstream health checks (0 to disable)")
		showVersion       = flag.Bool("version", false, "print version information and exit")
		tursoURL          = flag.String("turso-url", "", "Turso database URL (e.g. libsql://...; defaults to $TURSO_DATABASE_URL)")
		tursoToken        = flag.String("turso-token", "", "Turso JWT auth token (defaults to $TURSO_AUTH_TOKEN)")
		tursoLocalPath    = flag.String("turso-local-path", "data/firefly.db", "local embedded replica database path (defaults to $FIREFLY_TURSO_LOCAL_PATH)")
		tursoSyncInterval = flag.Duration("turso-sync-interval", 15*time.Second, "interval to pull changes from Turso cloud (defaults to $FIREFLY_TURSO_SYNC_INTERVAL)")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("firefly %s (commit: %s, built at: %s)\n", Version, Commit, BuildDate)
		return nil
	}

	if *configDir == "configs" && os.Getenv("FIREFLY_CONFIG_DIR") != "" {
		*configDir = os.Getenv("FIREFLY_CONFIG_DIR")
	}
	if *addr == "0.0.0.0:8080" && os.Getenv("FIREFLY_ADDR") != "" {
		*addr = os.Getenv("FIREFLY_ADDR")
	}
	if *adminAddr == "" && os.Getenv("FIREFLY_ADMIN_ADDR") != "" {
		*adminAddr = os.Getenv("FIREFLY_ADMIN_ADDR")
	}
	if *logLevel == "info" && os.Getenv("FIREFLY_LOG_LEVEL") != "" {
		*logLevel = os.Getenv("FIREFLY_LOG_LEVEL")
	}
	if *adminToken == "" {
		*adminToken = os.Getenv("FIREFLY_ADMIN_TOKEN")
	}
	if *tursoURL == "" {
		*tursoURL = os.Getenv("TURSO_DATABASE_URL")
	}
	if *tursoToken == "" {
		*tursoToken = os.Getenv("TURSO_AUTH_TOKEN")
	}
	if *tursoURL == "" || *tursoToken == "" {
		if tcfg, err := config.LoadTursoConfig(*configDir); err == nil {
			if *tursoURL == "" && tcfg.DatabaseURL != "" {
				*tursoURL = tcfg.DatabaseURL
			}
			if *tursoToken == "" && tcfg.AuthToken != "" {
				*tursoToken = tcfg.AuthToken
			}
			if *tursoLocalPath == "data/firefly.db" && tcfg.LocalPath != "" {
				*tursoLocalPath = tcfg.LocalPath
			}
			if tcfg.SyncIntervalSec > 0 {
				*tursoSyncInterval = time.Duration(tcfg.SyncIntervalSec) * time.Second
			}
		}
	}
	if *tursoLocalPath == "data/firefly.db" && os.Getenv("FIREFLY_TURSO_LOCAL_PATH") != "" {
		*tursoLocalPath = os.Getenv("FIREFLY_TURSO_LOCAL_PATH")
	}
	if envInterval := os.Getenv("FIREFLY_TURSO_SYNC_INTERVAL"); envInterval != "" {
		if d, err := time.ParseDuration(envInterval); err == nil {
			*tursoSyncInterval = d
		}
	}

	// Anchor a relative embedded-replica path to the (writable) config directory.
	// Under systemd the process CWD is typically "/", so a relative default like
	// "data/firefly.db" would fail with "mkdir data: permission denied".
	if *tursoLocalPath != "" && !filepath.IsAbs(*tursoLocalPath) && *configDir != "" {
		*tursoLocalPath = filepath.Join(*configDir, *tursoLocalPath)
	}

	logger := logging.New(os.Stdout, *logLevel)
	slog.SetDefault(logger)

	logger.Info("starting firefly", "version", Version, "commit", Commit, "built_at", BuildDate, "addr", *addr)

	// Ensure config directory and initial template files exist so watcher and
	// settings persist cleanly even on a fresh startup with zero initial files.
	if err := config.EnsureConfigFiles(*configDir); err != nil {
		logger.Warn("could not initialize config directory/files", "dir", *configDir, "err", err)
	}

	// Root context cancelled on SIGINT/SIGTERM; drives server + watcher shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// metrics: private Prometheus registry, exposed on the admin plane only.
	mx := metrics.New()

	// 1. Load config and build the initial snapshot. If no configuration exists,
	//    or if initial configuration fails validation (e.g. unconfigured env vars),
	//    firefly starts up with an empty snapshot and remains running so it can
	//    be configured via the frontend settings interface.
	reg := registry.New()

	var (
		tursoClient *turso.Client
		tursoStore  *turso.Store
	)

	if *tursoURL != "" {
		client, err := turso.NewClient(ctx, turso.Config{
			RemoteURL:    *tursoURL,
			AuthToken:    *tursoToken,
			LocalPath:    *tursoLocalPath,
			SyncInterval: *tursoSyncInterval,
			Logger:       logger,
		})
		if err != nil {
			return fmt.Errorf("initialize turso client: %w", err)
		}
		tursoClient = client
		tursoStore = turso.NewStore(client)

		// Bootstrap from existing config directory if database upstreams table is empty
		if err := turso.BootstrapFromFiles(ctx, *configDir, tursoStore, logger); err != nil {
			logger.Warn("turso bootstrap warning", "err", err)
		}

		if snap, warnings, err := tursoStore.LoadCatalogSnapshot(ctx, os.LookupEnv); err != nil {
			logger.Info("no initial config applied from turso, running in zero-config mode (ready for frontend configuration)", "detail", err.Error())
		} else {
			for _, w := range warnings {
				logger.Warn("turso config warning", "warning", w)
			}
			reg.Store(snap)
			mx.SetConfigGeneration(reg.CurrentGeneration())
			logger.Info("turso config loaded", "generation", reg.CurrentGeneration(), "enabled_models", len(snap.EnabledModels()))
		}
	} else {
		src := config.NewFileConfigSource(*configDir)
		if warnings, err := reg.BuildAndStore(ctx, src, os.LookupEnv); err != nil {
			logger.Info("no initial config applied, running in zero-config mode (ready for frontend configuration)", "detail", err.Error())
		} else {
			for _, w := range warnings {
				logger.Warn("config warning", "warning", w)
			}
			mx.SetConfigGeneration(reg.CurrentGeneration())
			logger.Info("config loaded", "dir", *configDir, "generation", reg.CurrentGeneration())
		}
	}

	// 2. Background workers are joined on shutdown so we never exit with a
	//    goroutine still holding a signal registration or a file watcher.
	var wg sync.WaitGroup

	if tursoClient != nil {
		syncer := turso.NewSyncer(tursoClient, tursoStore, reg, turso.SyncerConfig{
			Interval:  *tursoSyncInterval,
			EnvLookup: os.LookupEnv,
			Logger:    logger,
			Metrics:   mx,
		})
		initialRev, _ := tursoStore.GetCatalogRevision(ctx)
		syncer.SetInitialRevision(initialRev)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := syncer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("turso syncer stopped", "err", err)
			}
		}()
	} else {
		// 2a. Hot-reload watcher (fsnotify + poll + SIGHUP). The reload callback
		//     delegates to the registry, which is fail-closed: a bad reload keeps
		//     the previous snapshot serving.
		src := config.NewFileConfigSource(*configDir)
		w := watch.New(watch.Options{Dir: *configDir, Logger: logger}, reg)
		wg.Add(1)
		go func() {
			defer wg.Done()
			reload := func(rctx context.Context) error {
				warnings, err := reg.BuildAndStore(rctx, src, os.LookupEnv)
				if err != nil {
					return err
				}
				mx.SetConfigGeneration(reg.CurrentGeneration())
				for _, warn := range warnings {
					logger.Warn("config warning", "warning", warn)
				}
				return nil
			}
			if err := w.Run(ctx, reload); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("watcher stopped", "err", err)
			}
		}()
	}

	// 2b. Usage flusher / exporter
	counters := usage.NewCounters()
	var usageRecorder ports.UsageRecorder = counters
	var usageFlusher *turso.UsageFlusher

	if tursoStore != nil {
		usageFlusher = turso.NewUsageFlusher(tursoStore, counters, 30*time.Second, logger)
		usageRecorder = usageFlusher
		wg.Add(1)
		go func() {
			defer wg.Done()
			usageFlusher.Run(ctx)
		}()
	}

	exporter := &usage.Exporter{Counters: counters, Interval: 30 * time.Second, Logger: logger, Metrics: mx}
	wg.Add(1)
	go func() {
		defer wg.Done()
		exporter.Run(ctx)
	}()

	// 3. Outbound transport: per-upstream client pool + circuit breakers.
	pool := upstream.NewPool()
	breakers := upstream.NewBreakerRegistry(upstream.BreakerConfig{
		FailureThreshold: 5,
		SuccessThreshold: 2,
		Cooldown:         10 * time.Second,
	})
	retry := upstream.DefaultRetryPolicy()

	// 3a. Active health checker prober (periodic, bounded concurrency via errgroup).
	if *healthInterval > 0 {
		healthChecker := upstream.NewHealthChecker(upstream.HealthCheckConfig{
			Interval: *healthInterval,
			Logger:   logger,
		}, reg, pool, breakers)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := healthChecker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("health checker stopped", "err", err)
			}
		}()
	}

	openAIAdapter := openai.NewAdapter(pool, breakers, openai.Config{
		SecretLookup:     os.LookupEnv,
		Retry:            retry,
		Logger:           logger,
		MaxBufferedBytes: 32 << 20,
		Metrics:          mx,
		Notifier:         usageFlusher,
	})

	adapterRegistry := registry.NewAdapterRegistry()
	if err := adapterRegistry.Register(domain.ProtocolOpenAI, openAIAdapter); err != nil {
		return fmt.Errorf("register openai adapter: %w", err)
	}
	anthropicAdapter := anthropic.NewAdapter(pool, breakers, anthropic.Config{
		SecretLookup:     os.LookupEnv,
		Retry:            retry,
		Logger:           logger,
		MaxBufferedBytes: 32 << 20,
		Metrics:          mx,
		Notifier:         usageFlusher,
	})
	if err := adapterRegistry.Register(domain.ProtocolAnthropic, anthropicAdapter); err != nil {
		return fmt.Errorf("register anthropic adapter: %w", err)
	}

	// 3b. OAuth subsystem & persistent token store
	var oauthStore ports.TokenStore
	if tursoClient != nil {
		oauthStore = turso.NewOAuthStore(tursoClient)
		logger.Info("using turso centralized oauth token store")
	} else {
		var oerr error
		oauthStore, oerr = oauth.NewStore(filepath.Join(*configDir, "oauth.json"))
		if oerr != nil {
			return fmt.Errorf("init oauth store: %w", oerr)
		}
	}
	oauthMgr := oauth.NewManager(oauthStore, oauth.WithLogger(logger))
	if err := oauthMgr.RegisterProvider(antigravityProvider.New()); err != nil {
		logger.Warn("could not register antigravity oauth provider", "err", err)
	}
	if err := oauthMgr.RegisterProvider(clineProvider.New()); err != nil {
		logger.Warn("could not register cline oauth provider", "err", err)
	}
	if err := oauthMgr.RegisterProvider(codebuddyProvider.NewCN()); err != nil {
		logger.Warn("could not register codebuddy-cn oauth provider", "err", err)
	}
	if err := oauthMgr.RegisterProvider(codebuddyProvider.NewIntl()); err != nil {
		logger.Warn("could not register codebuddy-intl oauth provider", "err", err)
	}

	// 3c. Background proactive token refresher
	refresher := oauth.NewRefresher(oauthMgr, oauth.RefresherConfig{
		Logger: logger,
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		refresher.Run(ctx)
	}()

	antigravityAdapter := antigravity.NewAdapter(pool, breakers, antigravity.Config{
		TokenResolver:    oauthMgr.ResolveToken,
		SecretLookup:     os.LookupEnv,
		Retry:            retry,
		Logger:           logger,
		MaxBufferedBytes: 32 << 20,
		Metrics:          mx,
		Notifier:         usageFlusher,
	})
	if err := adapterRegistry.Register(domain.ProtocolAntigravity, antigravityAdapter); err != nil {
		return fmt.Errorf("register antigravity adapter: %w", err)
	}
	clineAdapter := cline.NewAdapter(pool, breakers, cline.Config{
		TokenResolver:    oauthMgr.ResolveToken,
		SecretLookup:     os.LookupEnv,
		Retry:            retry,
		Logger:           logger,
		MaxBufferedBytes: 32 << 20,
		Metrics:          mx,
		Notifier:         usageFlusher,
	})
	if err := adapterRegistry.Register(domain.ProtocolCline, clineAdapter); err != nil {
		return fmt.Errorf("register cline adapter: %w", err)
	}
	codebuddyAdapter := codebuddy.NewAdapter(pool, breakers, codebuddy.Config{
		TokenResolver:    oauthMgr.ResolveToken,
		SecretLookup:     os.LookupEnv,
		Retry:            retry,
		Logger:           logger,
		MaxBufferedBytes: 32 << 20,
		Metrics:          mx,
		Notifier:         usageFlusher,
	})
	if err := adapterRegistry.Register(domain.ProtocolCodeBuddyCN, codebuddyAdapter); err != nil {
		return fmt.Errorf("register codebuddy-cn adapter: %w", err)
	}
	if err := adapterRegistry.Register(domain.ProtocolCodeBuddyIntl, codebuddyAdapter); err != nil {
		return fmt.Errorf("register codebuddy-intl adapter: %w", err)
	}
	grokAdapter := grok.NewAdapter(pool, breakers, grok.Config{
		TokenResolver:    oauthMgr.ResolveToken,
		SecretLookup:     os.LookupEnv,
		Retry:            retry,
		Logger:           logger,
		MaxBufferedBytes: 32 << 20,
		Metrics:          mx,
		Notifier:         usageFlusher,
	})
	if err := adapterRegistry.Register(domain.ProtocolGrokCLI, grokAdapter); err != nil {
		return fmt.Errorf("register grok-cli adapter: %w", err)
	}

	// 4. Analytics, Token Ledger, and Request History Persistent Storage.
	analyticsStore, err := analytics.NewStore(*configDir)
	if err != nil {
		return fmt.Errorf("init analytics store: %w", err)
	}
	// Restore persisted circuit breaker states
	if overrides, err := analyticsStore.GetBreakerOverrides(ctx); err == nil {
		for upName, stateStr := range overrides {
			if state, ok := upstream.ParseBreakerState(stateStr); ok {
				breakers.SetState(upName, state)
			}
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		analyticsStore.Start(ctx, 3*time.Second)
	}()

	// 5. Authentication and HTTP server with the full middleware chain.
	authMgr := auth.NewManager(*configDir, *adminToken, *dashboardPassword)
	tenantStore := auth.NewStore(reg)
	tenantStore.SetAuthManager(authMgr)

	liveLogs := server.NewLiveLogHub()
	liveLogs.AttachStore(analyticsStore)
	autoTLS := server.NewAutoTLS(filepath.Join(*configDir, "certificates"), logger)

	deps := server.RouterDeps{
		Snapshots:    reg,
		Registry:     reg,
		ConfigDir:    *configDir,
		AdminToken:   *adminToken,
		Auth:         authMgr,
		TenantStore:  tenantStore,
		Limiter:      limits.New(),
		Adapters:     adapterRegistry,
		Adapter:      openAIAdapter,
		Usage:        usageRecorder,
		Breakers:     breakers,
		Analytics:    analyticsStore,
		LiveLogs:     liveLogs,
		AutoTLS:      autoTLS,
		OAuthManager: oauthMgr,
		TursoStore:   tursoStore,
		TursoManager: server.NewTursoManager(*configDir, tursoStore, logger),
		Logger:       logger,
		Metrics:      mx,
	}
	graceDuration := time.Duration(*graceSecs) * time.Second
	srv := server.New(server.Config{
		Addr:          *addr,
		ShutdownGrace: graceDuration,
	}, deps, ctx, logger)
	srv.AttachAutoTLS(autoTLS)

	// Auto-TLS is disabled by default. When enabled through Settings, it owns
	// standards ports 80/443 for HTTP-01 validation and HTTPS while the normal
	// data listener remains available at its configured address.
	if tlsSettings, err := config.LoadAutoTLS(*configDir); err != nil {
		logger.Warn("could not load auto TLS configuration; leaving TLS disabled", "err", err)
	} else if err := autoTLS.Apply(server.AutoTLSConfig{
		Enabled: tlsSettings.Enabled,
		Domain:  tlsSettings.Domain,
		Email:   tlsSettings.Email,
	}); err != nil {
		logger.Warn("could not start auto TLS; Firefly remains available on its HTTP listener", "err", err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		autoTLS.Run(ctx)
	}()

	// 5. Admin plane (/metrics + guarded /debug/*), drained alongside the data
	//    plane. Only started when an address is configured.
	var admin *server.Admin
	if *adminAddr != "" {
		admin = server.NewAdmin(*adminAddr, *adminToken, mx.Handler(), logger, graceDuration)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := admin.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("admin server stopped", "err", err)
			}
		}()
	}

	// 6. Serve the data plane; returns once ctx is cancelled and the server has
	//    drained (bounded by ShutdownGrace).
	serveErr := srv.Serve(ctx)
	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		stop() // cancel workers before joining
		wg.Wait()
		return serveErr
	}

	// 7. Cancel any remaining workers (the watcher/exporter observe ctx) and
	//    join them. JoinWithTimeout bounds the wait so a wedged worker cannot
	//    hang shutdown forever.
	wg.Wait()
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 3*time.Second)
	_ = analyticsStore.Flush(flushCtx)
	if usageFlusher != nil {
		_ = usageFlusher.Flush(flushCtx)
	}
	flushCancel()
	if tursoClient != nil {
		_ = tursoClient.Close()
	}
	logger.Info("bye")
	return nil
}
