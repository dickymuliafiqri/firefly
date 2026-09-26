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
	"strconv"
	"strings"

	"sync"
	"syscall"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/adapter/anthropic"
	"github.com/dickymuliafiqri/firefly/internal/adapter/antigravity"
	"github.com/dickymuliafiqri/firefly/internal/adapter/cline"
	"github.com/dickymuliafiqri/firefly/internal/adapter/codebuddy"
	"github.com/dickymuliafiqri/firefly/internal/adapter/grok"
	"github.com/dickymuliafiqri/firefly/internal/security/oauth"
	antigravityProvider "github.com/dickymuliafiqri/firefly/internal/security/oauth/providers/antigravity"
	clineProvider "github.com/dickymuliafiqri/firefly/internal/security/oauth/providers/cline"
	codebuddyProvider "github.com/dickymuliafiqri/firefly/internal/security/oauth/providers/codebuddy"
	grokcliProvider "github.com/dickymuliafiqri/firefly/internal/security/oauth/providers/grokcli"
	"github.com/dickymuliafiqri/firefly/internal/storage/analytics"

	"github.com/dickymuliafiqri/firefly/internal/adapter/openai"
	"github.com/dickymuliafiqri/firefly/internal/adapter/opencode"
	"github.com/dickymuliafiqri/firefly/internal/adapter/qoder"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/observability/logging"
	"github.com/dickymuliafiqri/firefly/internal/observability/metrics"
	"github.com/dickymuliafiqri/firefly/internal/observability/usage"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/security/auth"
	"github.com/dickymuliafiqri/firefly/internal/server"
	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
	"github.com/dickymuliafiqri/firefly/internal/transport/httpx"
	"github.com/dickymuliafiqri/firefly/internal/transport/tunnel"
	"github.com/dickymuliafiqri/firefly/internal/transport/upstream"
	"github.com/dickymuliafiqri/firefly/internal/transport/warp"

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

// flagWasSet reports whether the named flag was supplied on the command line, so
// an explicit flag can outrank the environment and the persisted config file.
func flagWasSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func run() error {
	var (
		configDir            = flag.String("config-dir", "configs", "directory containing upstreams.json, models.json, tenants.json")
		addr                 = flag.String("addr", "0.0.0.0:8080", "listen address")
		adminAddr            = flag.String("admin-addr", "", "admin listen address for /metrics and guarded /debug/* (empty disables)")
		logLevel             = flag.String("log-level", "info", "log level: debug|info|warn|error")
		graceSecs            = flag.Int("shutdown-grace-seconds", 30, "max seconds to drain in-flight requests on shutdown")
		adminToken           = flag.String("admin-token", "", "bearer token guarding /debug/* endpoints (defaults to $FIREFLY_ADMIN_TOKEN; empty disables them)")
		dashboardPassword    = flag.String("dashboard-password", "", "master password for dashboard access (defaults to $INITIAL_PASSWORD, $FIREFLY_DASHBOARD_PASSWORD, or 12345678)")
		healthInterval       = flag.Duration("health-check-interval", upstream.DefaultHealthCheckInterval, "interval between background upstream health checks (0 to disable)")
		showVersion          = flag.Bool("version", false, "print version information and exit")
		tursoURL             = flag.String("turso-url", "", "Turso database URL (e.g. libsql://...; defaults to $TURSO_DATABASE_URL)")
		tursoToken           = flag.String("turso-token", "", "Turso JWT auth token (defaults to $TURSO_AUTH_TOKEN)")
		tursoLocalPath       = flag.String("turso-local-path", "data/firefly.db", "local embedded replica database path (defaults to $FIREFLY_TURSO_LOCAL_PATH)")
		tursoSyncInterval    = flag.Duration("turso-sync-interval", turso.DefaultSyncInterval, "interval to pull changes from Turso cloud after an observed change (defaults to $FIREFLY_TURSO_SYNC_INTERVAL)")
		tursoSyncMaxInterval = flag.Duration("turso-sync-max-interval", turso.DefaultSyncMaxInterval, "upper bound for the idle pull backoff; consecutive change-free pulls double the delay until it reaches this (defaults to $FIREFLY_TURSO_SYNC_MAX_INTERVAL)")
		warpRotateInterval   = flag.Duration("warp-rotate-interval", warp.DefaultAutoRotateInterval, "interval between automatic periodic Cloudflare WARP IP rotations (0 disables; defaults to $FIREFLY_WARP_ROTATE_INTERVAL or 5m)")
		tunnelMode           = flag.String("tunnel", "", "Cloudflare Tunnel mode: quick|named (empty disables; defaults to $FIREFLY_TUNNEL)")
		tunnelToken          = flag.String("tunnel-token", "", "Cloudflare Tunnel token for named tunnels (defaults to $FIREFLY_TUNNEL_TOKEN)")
		tunnelBinDir         = flag.String("tunnel-bin-dir", "", "directory to store or find cloudflared binary (defaults to ~/.firefly/bin or $FIREFLY_TUNNEL_BIN_DIR)")

		openaiDefaultMax = flag.Int64("openai-default-max-tokens", 0, "default max_tokens injected into OpenAI-protocol requests that omit an output-token limit (0 disables; defaults to $FIREFLY_OPENAI_DEFAULT_MAX_TOKENS)")
		openaiMinMax     = flag.Int64("openai-min-max-tokens", 0, "minimum max_tokens floor for OpenAI-protocol requests; smaller client values are raised to this (0 disables; defaults to $FIREFLY_OPENAI_MIN_MAX_TOKENS)")
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
	if *tunnelMode == "" {
		*tunnelMode = os.Getenv("FIREFLY_TUNNEL")
	}
	if *tunnelToken == "" {
		*tunnelToken = os.Getenv("FIREFLY_TUNNEL_TOKEN")
	}
	if *tunnelBinDir == "" {
		*tunnelBinDir = os.Getenv("FIREFLY_TUNNEL_BIN_DIR")
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

	// turso.json persists the replica options. Pacing and the local path must be
	// picked up even when the credentials came from a flag or the environment:
	// gating this block on the credential source previously discarded a
	// configured sync interval whenever the URL came from outside the file.
	tcfg, _ := config.LoadTursoConfig(*configDir)
	if *tursoURL == "" {
		*tursoURL = tcfg.DatabaseURL
	}
	if *tursoToken == "" {
		*tursoToken = tcfg.AuthToken
	}
	if !flagWasSet("turso-local-path") {
		if v := os.Getenv("FIREFLY_TURSO_LOCAL_PATH"); v != "" {
			*tursoLocalPath = v
		} else if tcfg.LocalPath != "" {
			*tursoLocalPath = tcfg.LocalPath
		}
	}
	// Replica pull pacing, most specific source first: flag, then environment,
	// then turso.json, else the built-in default.
	if !flagWasSet("turso-sync-interval") {
		if v := os.Getenv("FIREFLY_TURSO_SYNC_INTERVAL"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				*tursoSyncInterval = d
			}
		} else if tcfg.SyncIntervalSec > 0 {
			*tursoSyncInterval = time.Duration(tcfg.SyncIntervalSec) * time.Second
		}
	}
	if !flagWasSet("turso-sync-max-interval") {
		if v := os.Getenv("FIREFLY_TURSO_SYNC_MAX_INTERVAL"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				*tursoSyncMaxInterval = d
			}
		} else if tcfg.SyncMaxIntervalSec > 0 {
			*tursoSyncMaxInterval = time.Duration(tcfg.SyncMaxIntervalSec) * time.Second
		}
	}
	if envWarp := os.Getenv("FIREFLY_WARP_ROTATE_INTERVAL"); envWarp != "" {
		if d, err := time.ParseDuration(envWarp); err == nil {
			*warpRotateInterval = d
		}
	}
	if *openaiDefaultMax == 0 {
		if v, err := strconv.ParseInt(os.Getenv("FIREFLY_OPENAI_DEFAULT_MAX_TOKENS"), 10, 64); err == nil && v > 0 {
			*openaiDefaultMax = v
		}
	}
	if *openaiMinMax == 0 {
		if v, err := strconv.ParseInt(os.Getenv("FIREFLY_OPENAI_MIN_MAX_TOKENS"), 10, 64); err == nil && v > 0 {
			*openaiMinMax = v
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

	// Stamp the ldflags-injected version into the served OpenAPI document so
	// /api/openapi.yaml reports the build that is actually running.
	server.SetBuildVersion(Version)

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
			Interval:    *tursoSyncInterval,
			MaxInterval: *tursoSyncMaxInterval,
			EnvLookup:   os.LookupEnv,
			Logger:      logger,
			Metrics:     mx,
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
	warpLicense := os.Getenv("FIREFLY_WARP_LICENSE")
	if warpLicense == "" {
		warpLicense = os.Getenv("WARP_LICENSE_KEY")
	}
	warpManager := warp.NewManager(logger, warpLicense, filepath.Join(*configDir, "data", "warp_identity.json"))
	// The interval is applied even when it is zero: Status() mirrors the schedule,
	// so leaving the constructor default in place made the dashboard advertise a
	// rotation that would never happen. `0` means no autonomous WARP work at all
	// — neither the scheduler nor the cold-start warm-up below runs.
	warpManager.SetAutoRotateInterval(*warpRotateInterval)
	if *warpRotateInterval > 0 {
		warpManager.StartAutoRotation()
	}

	pool := upstream.NewPool(warpManager)
	// A rotation gives the tunnel a new egress address, but transports keep their
	// warm connections from the previous one. Drop those idle sockets so the new
	// IP is actually used by the next request.
	warpManager.SetRotationObserver(pool.CloseIdleWarpConnections)

	// The 429 that triggered the rotation charged its cooldown and its error
	// counter to the key slot, but the free-tier quota belongs to the egress IP
	// that was just replaced. Release both so traffic resumes on the fresh
	// identity instead of idling behind the penalty (measured: 302s of a 300s cap
	// in the e2e sandbox) and so an IP-bound storm cannot arm the key-error
	// threshold against a healthy key.
	warpManager.SetAutoRotationObserver(func(upstreamName string) {
		upstream.ReleaseUpstreamKeyPenalties(reg, upstreamName, logger)
	})

	// A cold start has no live tunnel: the session is dialled lazily by the first
	// WARP-egress request, or by the first rotation tick a full interval away
	// (5m by default). Until then /api/warp/status answers enabled:false, the
	// dashboard shows a healthy engine as DISABLED, and the operator cannot even
	// force it from the UI. Establish the tunnel eagerly instead, after both
	// rotation observers above are registered. Skipped when automatic rotation is
	// off: `-warp-rotate-interval=0` requests no autonomous WARP work at all.
	if *warpRotateInterval > 0 {
		go func() {
			if err := warpManager.WarmUp(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("warp cold-start warm-up failed; the tunnel stays lazy", "err", err)
			}
		}()
	}
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
		}, reg, pool, breakers, usageFlusher)
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
		DefaultMaxTokens: *openaiDefaultMax,
		MinMaxTokens:     *openaiMinMax,
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
	if err := oauthMgr.RegisterProvider(grokcliProvider.New()); err != nil {
		logger.Warn("could not register grok-cli oauth provider", "err", err)
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
		ConnectionLookup: oauthMgr.ResolveConnection,
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
	openCodeAdapter := opencode.NewAdapter(pool, breakers, opencode.Config{
		SecretLookup:     os.LookupEnv,
		Retry:            retry,
		Logger:           logger,
		MaxBufferedBytes: 32 << 20,
		Metrics:          mx,
		Notifier:         usageFlusher,
	})
	if err := adapterRegistry.Register(domain.ProtocolOpenCode, openCodeAdapter); err != nil {
		return fmt.Errorf("register opencode adapter: %w", err)
	}
	if err := adapterRegistry.Register(domain.ProtocolOpenCodeGo, openCodeAdapter); err != nil {
		return fmt.Errorf("register opencode-go adapter: %w", err)
	}
	qoderAdapter := qoder.NewAdapter(pool, breakers, qoder.Config{
		TokenResolver:    oauthMgr.ResolveToken,
		SecretLookup:     os.LookupEnv,
		Retry:            retry,
		Logger:           logger,
		MaxBufferedBytes: 32 << 20,
		Metrics:          mx,
		Notifier:         usageFlusher,
	})
	if err := adapterRegistry.Register(domain.ProtocolQoder, qoderAdapter); err != nil {
		return fmt.Errorf("register qoder adapter: %w", err)
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

	// Global admission gate (layer 1 of the limiter stack). It is constructed
	// here, not inside the router, so this exact instance is shared by the
	// middleware chain and the telemetry handler; otherwise telemetry would
	// report occupancy from a throwaway limiter nobody routes through.
	globalLimiter := httpx.NewGlobalLimiter(httpx.DefaultGlobalMaxInflight, httpx.DefaultGlobalWaitTimeout)
	// Cloudflare Tunnel Ingress Engine
	var tMode tunnel.Mode
	switch strings.ToLower(*tunnelMode) {
	case "quick":
		tMode = tunnel.ModeQuick
	case "named":
		tMode = tunnel.ModeNamed
	default:
		tMode = tunnel.ModeDisabled
	}

	tunnelLocalURL := "http://" + *addr
	if strings.HasPrefix(*addr, "0.0.0.0:") {
		tunnelLocalURL = "http://127.0.0.1:" + strings.TrimPrefix(*addr, "0.0.0.0:")
	}

	tunnelManager := tunnel.NewManager(tunnel.Config{
		AppCtx:   ctx,
		Mode:     tMode,
		LocalURL: tunnelLocalURL,
		Token:    *tunnelToken,
		BinDir:   *tunnelBinDir,
		Logger:   logger,
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := tunnelManager.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("cloudflare tunnel stopped", "err", err)
		}
	}()

	deps := server.RouterDeps{
		Snapshots:     reg,
		Registry:      reg,
		ConfigDir:     *configDir,
		AdminToken:    *adminToken,
		Auth:          authMgr,
		TenantStore:   tenantStore,
		Limiter:       limits.New(),
		GlobalLimiter: globalLimiter,
		Adapters:      adapterRegistry,
		Adapter:       openAIAdapter,
		Usage:         usageRecorder,
		Breakers:      breakers,
		Analytics:     analyticsStore,
		LiveLogs:      liveLogs,
		AutoTLS:       autoTLS,
		OAuthManager:  oauthMgr,
		TursoStore:    tursoStore,
		TursoManager:  server.NewTursoManager(*configDir, tursoStore, logger),
		WarpManager:   warpManager,
		Logger:        logger,
		TunnelManager: tunnelManager,

		Metrics: mx,
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
	if warpManager != nil {
		warpManager.Close()
	}
	if tunnelManager != nil {
		tunnelManager.Close()
	}

	logger.Info("bye")
	return nil
}
