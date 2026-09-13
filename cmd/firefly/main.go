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
	"sync"
	"syscall"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/anthropic"
	"github.com/dickymuliafiqri/firefly/internal/domain"

	"github.com/dickymuliafiqri/firefly/internal/auth"
	"github.com/dickymuliafiqri/firefly/internal/config"
	"github.com/dickymuliafiqri/firefly/internal/limits"
	"github.com/dickymuliafiqri/firefly/internal/logging"
	"github.com/dickymuliafiqri/firefly/internal/metrics"
	"github.com/dickymuliafiqri/firefly/internal/openai"
	"github.com/dickymuliafiqri/firefly/internal/registry"
	"github.com/dickymuliafiqri/firefly/internal/server"
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
		configDir      = flag.String("config-dir", "configs", "directory containing upstreams.json, models.json, tenants.json")
		addr           = flag.String("addr", "0.0.0.0:8080", "listen address")
		adminAddr      = flag.String("admin-addr", "", "admin listen address for /metrics and guarded /debug/* (empty disables)")
		logLevel       = flag.String("log-level", "info", "log level: debug|info|warn|error")
		graceSecs      = flag.Int("shutdown-grace-seconds", 30, "max seconds to drain in-flight requests on shutdown")
		adminToken     = flag.String("admin-token", "", "bearer token guarding /debug/* endpoints (defaults to $FIREFLY_ADMIN_TOKEN; empty disables them)")
		healthInterval = flag.Duration("health-check-interval", upstream.DefaultHealthCheckInterval, "interval between background upstream health checks (0 to disable)")
		showVersion    = flag.Bool("version", false, "print version information and exit")
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

	// 2. Background workers are joined on shutdown so we never exit with a
	//    goroutine still holding a signal registration or a file watcher.
	var wg sync.WaitGroup

	// 2a. Hot-reload watcher (fsnotify + poll + SIGHUP). The reload callback
	//     delegates to the registry, which is fail-closed: a bad reload keeps
	//     the previous snapshot serving.
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

	// 2b. Usage exporter: logs snapshots and pushes usage deltas to metrics.
	counters := usage.NewCounters()
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
	})
	if err := adapterRegistry.Register(domain.ProtocolAnthropic, anthropicAdapter); err != nil {
		return fmt.Errorf("register anthropic adapter: %w", err)
	}


	// 4. HTTP server with the full middleware chain.
	deps := server.RouterDeps{
		Snapshots:   reg,
		Registry:    reg,
		ConfigDir:   *configDir,
		AdminToken:  *adminToken,
		TenantStore: auth.NewStore(reg),
		Limiter:     limits.New(),
		Adapters:    adapterRegistry,
		Adapter:     openAIAdapter,
		Usage:       counters,
		Breakers:    breakers,

		Logger:      logger,
		Metrics:     mx,
	}
	graceDuration := time.Duration(*graceSecs) * time.Second
	srv := server.New(server.Config{
		Addr:          *addr,
		ShutdownGrace: graceDuration,
	}, deps, ctx, logger)

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
	logger.Info("bye")
	return nil
}
