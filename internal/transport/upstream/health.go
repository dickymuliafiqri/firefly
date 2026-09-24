// Package upstream owns outbound HTTP: connection pool, circuit breakers,
// and background active health checking across upstream hosts.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"golang.org/x/sync/errgroup"
)

// Default health check tuning.
const (
	DefaultHealthCheckInterval    = 15 * time.Second
	DefaultHealthCheckTimeout     = 3 * time.Second
	DefaultHealthCheckConcurrency = 10
	DefaultHealthCheckMethod      = http.MethodGet
)

// HealthCheckConfig holds configuration parameters for the active health checker.
type HealthCheckConfig struct {
	// Interval is the duration between periodic probing rounds.
	Interval time.Duration

	// Timeout is the maximum duration for an individual upstream probe.
	Timeout time.Duration

	// Concurrency is the maximum number of concurrent in-flight probes (via errgroup.SetLimit).
	Concurrency int

	// Method is the HTTP method to use for probing (GET or HEAD).
	Method string

	// Path is an optional path appended to the upstream BaseURL (e.g. "/healthz").
	Path string

	// Logger for structured diagnostics.
	Logger *slog.Logger
}

func (c HealthCheckConfig) withDefaults() HealthCheckConfig {
	if c.Interval <= 0 {
		c.Interval = DefaultHealthCheckInterval
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultHealthCheckTimeout
	}
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultHealthCheckConcurrency
	}
	if c.Method == "" {
		c.Method = DefaultHealthCheckMethod
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// SnapshotProvider yields the current catalog snapshot for dynamic upstream discovery.
type SnapshotProvider interface {
	Current() *domain.CatalogSnapshot
}

// BreakerManager checks and records health probe outcomes for named upstreams.
type BreakerManager interface {
	Allow(name string) error
	Report(name string, ok bool)
}

// ClientProvider provides HTTP clients for upstreams.
type ClientProvider interface {
	Client(u *domain.Upstream) *http.Client
}

// HealthChecker periodically and concurrently probes upstream hosts using errgroup
// with bounded concurrency, reporting status to circuit breakers. Probe outcomes
// on a selected key slot are routed through the shared key error policy
// (HandleKeyOutcome) so background failures advance ConsecutiveErrors toward the
// upstream's key_error_threshold and persist threshold actions via notifier.
type HealthChecker struct {
	cfg      HealthCheckConfig
	provider SnapshotProvider
	clients  ClientProvider
	breakers BreakerManager
	notifier ports.KeyActionNotifier
	logger   *slog.Logger

	// staticUpstreams is used when provider is nil (e.g. in targeted tests)
	staticUpstreams []*domain.Upstream
}

// NewHealthChecker constructs a HealthChecker. notifier may be nil (or a
// typed-nil) — threshold actions then stay in-memory only.
func NewHealthChecker(
	cfg HealthCheckConfig,
	provider SnapshotProvider,
	clients ClientProvider,
	breakers BreakerManager,
	notifier ports.KeyActionNotifier,
) *HealthChecker {
	cfg = cfg.withDefaults()
	return &HealthChecker{
		cfg:      cfg,
		provider: provider,
		clients:  clients,
		breakers: breakers,
		notifier: notifier,
		logger:   cfg.Logger,
	}
}

// NewHealthCheckerWithStaticUpstreams constructs a HealthChecker with a fixed list of upstreams (useful for testing).
func NewHealthCheckerWithStaticUpstreams(
	cfg HealthCheckConfig,
	upstreams []*domain.Upstream,
	clients ClientProvider,
	breakers BreakerManager,
	notifier ports.KeyActionNotifier,
) *HealthChecker {
	cfg = cfg.withDefaults()
	return &HealthChecker{
		cfg:             cfg,
		staticUpstreams: upstreams,
		clients:         clients,
		breakers:        breakers,
		notifier:        notifier,
		logger:          cfg.Logger,
	}
}

// Run starts the periodic health check loop and runs until ctx is cancelled.
// It executes cleanly without leaking background goroutines.
func (h *HealthChecker) Run(ctx context.Context) error {
	ticker := time.NewTicker(h.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := h.ProbeOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				h.logger.Warn("health check round finished with error", "err", err)
			}
		}
	}
}

// ProbeOnce runs a single round of parallel probes across all active upstreams
// using errgroup.WithContext and bounded concurrency via SetLimit.
func (h *HealthChecker) ProbeOnce(ctx context.Context) error {
	upstreams := h.getUpstreams()
	if len(upstreams) == 0 {
		return nil
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(h.cfg.Concurrency)

	for _, u := range upstreams {
		if u == nil {
			continue
		}
		u := u
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			h.probeOne(gctx, u)
			return nil
		})
	}

	return g.Wait()
}

func (h *HealthChecker) getUpstreams() []*domain.Upstream {
	if !isNil(h.provider) {
		snap := h.provider.Current()
		if snap == nil {
			return nil
		}
		names := snap.UpstreamNames()
		ups := make([]*domain.Upstream, 0, len(names))
		for _, name := range names {
			if u, ok := snap.Upstream(name); ok && u != nil {
				ups = append(ups, u)
			}
		}
		return ups
	}
	return h.staticUpstreams
}

func (h *HealthChecker) probeOne(ctx context.Context, u *domain.Upstream) {
	defer func() {
		if rec := recover(); rec != nil {
			h.logger.Error("panic recovered during upstream probe", "upstream", u.Name, "panic", rec)
			h.reportOutcome(u.Name, false, errors.New("panic during probe"), 0)
		}
	}()

	if !isNil(h.breakers) {
		if err := h.breakers.Allow(u.Name); err != nil && errors.Is(err, ErrCircuitOpen) {
			// Breaker is open and cooldown has not elapsed yet; skip probing until cooldown elapses
			return
		}
	}

	trimmedBase := strings.TrimRight(u.BaseURL, "/")
	var activeSlot *domain.KeySlot

	if u.KeyRing != nil && len(u.KeyRing.Slots) > 0 {
		activeSlot, _ = u.KeyRing.SelectKey(time.Now().UnixNano())
		if activeSlot == nil {
			activeSlot = u.KeyRing.PrimarySlot()
			if activeSlot != nil && activeSlot.Revoked.Load() {
				for _, s := range u.KeyRing.Slots {
					if s != nil && !s.Revoked.Load() {
						activeSlot = s
						break
					}
				}
			}
		}
	}

	keySecret := ""
	if activeSlot != nil {
		if activeSlot.Secret != "" {
			keySecret = activeSlot.Secret
		} else {
			keySecret = activeSlot.Ref
		}
	} else if u.CredentialRef != "" {
		keySecret = u.CredentialRef
	}

	isOpenCode := u.Protocol == domain.ProtocolOpenCode || u.Protocol == domain.ProtocolOpenCodeGo
	isFreeOpenCode := false
	if isOpenCode {
		if keySecret == "" || strings.EqualFold(keySecret, "public") {
			keySecret = "public"
			isFreeOpenCode = true
		}
		// Auto-correct endpoint tier mismatch if user configured base URL with wrong tier
		if isFreeOpenCode && strings.Contains(trimmedBase, "/zen/go/v1") {
			trimmedBase = strings.Replace(trimmedBase, "/zen/go/v1", "/zen/v1", 1)
		} else if !isFreeOpenCode && strings.Contains(trimmedBase, "/zen/v1") && !strings.Contains(trimmedBase, "/zen/go/v1") {
			trimmedBase = strings.Replace(trimmedBase, "/zen/v1", "/zen/go/v1", 1)
		}
	}

	canProbeModel := u.ProbeModel != "" && (activeSlot != nil || isFreeOpenCode)

	// Model inference requires adequate breathing room beyond network TTFB
	probeTimeout := h.cfg.Timeout
	if canProbeModel && probeTimeout < 10*time.Second {
		probeTimeout = 10 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	var req *http.Request
	var err error

	if canProbeModel {
		var probeURL string
		var probeBody []byte

		if isOpenCode {
			if IsOpenCodeResponsesModel(u.ProbeModel) {
				probePath := "/responses"
				if !strings.HasSuffix(trimmedBase, "/v1") {
					probePath = "/v1/responses"
				}
				probeURL = trimmedBase + probePath
				if isFreeOpenCode {
					probeBody = OpenCodeOfficialResponsesProbePayload(MapOpenCodeFreeModel(u.ProbeModel))
				} else {
					probeBody, _ = json.Marshal(map[string]any{
						"model": u.ProbeModel,
						"input": []map[string]any{
							{
								"type": "message",
								"role": "user",
								"content": []map[string]string{
									{"type": "input_text", "text": "ping"},
								},
							},
						},
						"max_output_tokens": 16,
						"stream":            true,
						"store":             false,
					})
				}
			} else {
				probePath := "/chat/completions"
				if !strings.HasSuffix(trimmedBase, "/v1") {
					probePath = "/v1/chat/completions"
				}
				probeURL = trimmedBase + probePath
				if isFreeOpenCode {
					probeBody = OpenCodeOfficialTitleProbePayload(u.ProbeModel)
				} else {
					probeBody, _ = json.Marshal(map[string]any{
						"model": u.ProbeModel,
						"messages": []map[string]string{
							{"role": "user", "content": "ping"},
						},
						"max_tokens": 16,
					})
				}
			}
		} else if u.Protocol == domain.ProtocolAnthropic {
			probePath := "/v1/messages"
			if strings.HasSuffix(trimmedBase, "/v1") {
				probePath = "/messages"
			}
			probeURL = trimmedBase + probePath
			probeBody = []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1}`, u.ProbeModel))
		} else {
			probePath := "/v1/chat/completions"
			if strings.HasSuffix(trimmedBase, "/v1") {
				probePath = "/chat/completions"
			}
			probeURL = trimmedBase + probePath
			probeBody = []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"ping"}],"max_tokens":1}`, u.ProbeModel))
		}

		req, err = http.NewRequestWithContext(probeCtx, http.MethodPost, probeURL, bytes.NewReader(probeBody))
	} else {
		probePath := h.cfg.Path
		if probePath == "" && isOpenCode {
			probePath = "/models"
			if !strings.HasSuffix(trimmedBase, "/v1") {
				probePath = "/v1/models"
			}
		}
		url := trimmedBase + probePath
		req, err = http.NewRequestWithContext(probeCtx, h.cfg.Method, url, nil)
	}

	if err != nil {
		h.reportOutcome(u.Name, false, err, 0)
		return
	}

	req.Header.Set("User-Agent", "firefly-healthcheck/1.0")
	if isOpenCode {
		SetOpenCodeHeaders(req, keySecret, isFreeOpenCode, canProbeModel)
	} else if u.Protocol == domain.ProtocolAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Accept", "application/json")
		if canProbeModel {
			req.Header.Set("Content-Type", "application/json")
		}
		if keySecret != "" {
			req.Header.Set("x-api-key", keySecret)
		}
	} else {
		req.Header.Set("Accept", "application/json")
		if canProbeModel {
			req.Header.Set("Content-Type", "application/json")
		}
		if keySecret != "" {
			req.Header.Set("Authorization", "Bearer "+keySecret)
		}
	}
	for k, v := range u.ExtraHeaders {
		req.Header.Set(k, v)
	}

	var client *http.Client
	if !isNil(h.clients) {
		client = h.clients.Client(u)
	}
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		// A transport failure is not a credential error: reset the slot's
		// consecutive-error counter (Layer 1 invariant) so flaky probes never
		// accumulate toward the key threshold.
		h.handleProbeKeyOutcome(u, activeSlot, isFreeOpenCode, 0, "")
		// If a model-level probe timed out or failed, verify host reachability before tripping circuit breaker
		if canProbeModel && h.probeHostFallback(ctx, client, u, trimmedBase, isOpenCode, keySecret) {
			return
		}
		h.reportOutcome(u.Name, false, err, 0)
		return
	}

	// Drain bounded body and close to prevent connection leaks
	if resp.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}

	// Route the probe status through the shared key error policy BEFORE any
	// early return: credential errors (429/401/402/403) advance the slot's
	// ConsecutiveErrors toward key_error_threshold (triggering the configured
	// deactivate/delete/cooldown action + DB notification when reached), while
	// success, 4xx, and 5xx reset the counter — mirroring the data plane.
	h.handleProbeKeyOutcome(u, activeSlot, isFreeOpenCode, resp.StatusCode, resp.Header.Get("Retry-After"))

	// Status < 500 indicates the host is reachable and healthy at Layer 2.
	// 4xx client errors (401/404/429) are Layer 1 / key issues, NOT host failures.
	isHealthy := resp.StatusCode < 500
	if !isHealthy && canProbeModel {
		// If model returns 5xx, check if upstream host itself is alive
		if h.probeHostFallback(ctx, client, u, trimmedBase, isOpenCode, keySecret) {
			return
		}
	}
	h.reportOutcome(u.Name, isHealthy, nil, resp.StatusCode)
}

// handleProbeKeyOutcome feeds a background-probe result for the selected key
// slot into HandleKeyOutcome. Free/public OpenCode slots are never counted or
// acted on — they must stay routable regardless of probe status.
func (h *HealthChecker) handleProbeKeyOutcome(
	u *domain.Upstream,
	slot *domain.KeySlot,
	isFreeOpenCode bool,
	statusCode int,
	retryAfterHeader string,
) {
	if u == nil || slot == nil || isFreeOpenCode {
		return
	}
	if slot.Ref == "opencode-free-public" || strings.EqualFold(slot.Secret, "public") {
		return
	}

	actionTaken, _ := HandleKeyOutcome(u, slot, statusCode, retryAfterHeader, h.notifier, h.logger)

	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		if actionTaken {
			// Threshold action already revoked the slot and notified the sink.
			return
		}
		// Below threshold: keep the key out of in-memory rotation immediately;
		// the DB row is handled once the threshold is reached.
		slot.Revoked.Store(true)
		h.logger.Warn("key marked revoked during background probe",
			"upstream", u.Name, "key_ref", slot.Ref, "status", statusCode,
			"consecutive_errors", slot.ConsecutiveErrors.Load(),
			"threshold", u.KeyErrorThreshold,
		)
	case http.StatusTooManyRequests:
		// HandleKeyOutcome already applied the Retry-After cooldown and counted it.
		h.logger.Warn("key placed in cooldown during background probe",
			"upstream", u.Name, "key_ref", slot.Ref,
			"consecutive_errors", slot.ConsecutiveErrors.Load(),
			"threshold", u.KeyErrorThreshold,
		)
	}
}

func (h *HealthChecker) probeHostFallback(
	ctx context.Context,
	client *http.Client,
	u *domain.Upstream,
	trimmedBase string,
	isOpenCode bool,
	keySecret string,
) bool {
	fallbackTimeout := 3 * time.Second
	fbCtx, fbCancel := context.WithTimeout(ctx, fallbackTimeout)
	defer fbCancel()

	probePath := h.cfg.Path
	if probePath == "" && isOpenCode {
		probePath = "/models"
		if !strings.HasSuffix(trimmedBase, "/v1") {
			probePath = "/v1/models"
		}
	}
	url := trimmedBase + probePath
	req, err := http.NewRequestWithContext(fbCtx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "firefly-healthcheck/1.0")
	if isOpenCode {
		SetOpenCodeHeaders(req, keySecret, false, false)
	} else if keySecret != "" {
		req.Header.Set("Authorization", "Bearer "+keySecret)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	if resp.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}

	if resp.StatusCode < 500 {
		h.logger.Debug("upstream host alive via fallback probe despite model probe failure", "upstream", u.Name, "status", resp.StatusCode)
		h.reportOutcome(u.Name, true, nil, resp.StatusCode)
		return true
	}
	return false
}

func (h *HealthChecker) reportOutcome(name string, ok bool, err error, status int) {
	if !isNil(h.breakers) {
		h.breakers.Report(name, ok)
	}
	if !ok {
		if err != nil {
			h.logger.Warn("upstream probe failed", "upstream", name, "err", err)
		} else {
			h.logger.Warn("upstream probe returned 5xx", "upstream", name, "status", status)
		}
	} else {
		h.logger.Debug("upstream probe healthy", "upstream", name, "status", status)
	}
}

// isNil reports whether v is nil, including typed-nil pointers stored in interfaces.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
