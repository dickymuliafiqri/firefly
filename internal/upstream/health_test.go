package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"go.uber.org/goleak"
)

type mockBreakerReporter struct {
	mu      sync.Mutex
	reports map[string][]bool
}

func newMockBreakerReporter() *mockBreakerReporter {
	return &mockBreakerReporter{reports: make(map[string][]bool)}
}

func (m *mockBreakerReporter) Allow(name string) error {
	return nil
}

func (m *mockBreakerReporter) Report(name string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reports[name] = append(m.reports[name], ok)
}

func (m *mockBreakerReporter) getReports(name string) []bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	rep := m.reports[name]
	out := make([]bool, len(rep))
	copy(out, rep)
	return out
}

func TestHealthChecker_ProbeOnce(t *testing.T) {
	defer goleak.VerifyNone(t)

	// Healthy server
	srv1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "healthy")
	}))
	defer srv1.Close()

	// Unhealthy 500 server
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "down")
	}))
	defer srv2.Close()

	// 401 server (Layer 1 key issue, but Layer 2 host is UP)
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "unauthorized")
	}))
	defer srv3.Close()

	up1 := &domain.Upstream{Name: "up1", BaseURL: srv1.URL}
	up2 := &domain.Upstream{Name: "up2", BaseURL: srv2.URL}
	up3 := &domain.Upstream{Name: "up3", BaseURL: srv3.URL}
	up4 := &domain.Upstream{Name: "up4", BaseURL: "http://127.0.0.1:1"} // connection refused / dead host

	reporter := newMockBreakerReporter()
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Timeout:     1 * time.Second,
			Concurrency: 4,
		},
		[]*domain.Upstream{up1, up2, up3, up4},
		nil,
		reporter,
	)

	ctx := context.Background()
	if err := checker.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce returned error: %v", err)
	}

	// Verify up1 is healthy
	if rep, ok := reporter.reports["up1"]; !ok || len(rep) == 0 || !rep[0] {
		t.Fatalf("expected up1 healthy, got: %v", rep)
	}

	// Verify up2 is unhealthy (500)
	if rep, ok := reporter.reports["up2"]; !ok || len(rep) == 0 || rep[0] {
		t.Fatalf("expected up2 unhealthy, got: %v", rep)
	}

	// Verify up3 is healthy (401 is client/key error, not host down)
	if rep, ok := reporter.reports["up3"]; !ok || len(rep) == 0 || !rep[0] {
		t.Fatalf("expected up3 healthy, got: %v", rep)
	}

	// Verify up4 is unhealthy (connection refused)
	if rep, ok := reporter.reports["up4"]; !ok || len(rep) == 0 || rep[0] {
		t.Fatalf("expected up4 unhealthy, got: %v", rep)
	}
}

func TestHealthChecker_ConcurrencyLimit(t *testing.T) {
	defer goleak.VerifyNone(t)

	var currentInflight atomic.Int32
	var maxInflightObserved atomic.Int32

	const concurrencyLimit = 3
	const numServers = 12

	servers := make([]*httptest.Server, numServers)
	upstreams := make([]*domain.Upstream, numServers)

	for i := 0; i < numServers; i++ {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cur := currentInflight.Add(1)
			for {
				oldMax := maxInflightObserved.Load()
				if cur <= oldMax || maxInflightObserved.CompareAndSwap(oldMax, cur) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			currentInflight.Add(-1)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		servers[i] = srv
		upstreams[i] = &domain.Upstream{Name: srv.URL, BaseURL: srv.URL}
	}

	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Timeout:     1 * time.Second,
			Concurrency: concurrencyLimit,
		},
		upstreams,
		nil,
		newMockBreakerReporter(),
	)

	ctx := context.Background()
	if err := checker.ProbeOnce(ctx); err != nil {
		t.Fatalf("ProbeOnce failed: %v", err)
	}

	observed := maxInflightObserved.Load()
	if observed > concurrencyLimit {
		t.Fatalf("observed max in-flight concurrency %d exceeded limit %d", observed, concurrencyLimit)
	}
	if observed == 0 {
		t.Fatal("observed max in-flight was 0, expected probes to execute")
	}
}

func TestHealthChecker_BreakerTrippingAndRecovery(t *testing.T) {
	defer goleak.VerifyNone(t)

	var return500 atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if return500.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	upstreamName := "test-target"
	up := &domain.Upstream{Name: upstreamName, BaseURL: srv.URL}

	breakers := NewBreakerRegistry(BreakerConfig{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Cooldown:         50 * time.Millisecond,
	})

	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Timeout:     500 * time.Millisecond,
			Concurrency: 2,
		},
		[]*domain.Upstream{up},
		nil,
		breakers,
	)

	ctx := context.Background()

	// 1. Initial probe: healthy
	_ = checker.ProbeOnce(ctx)
	if err := breakers.Allow(upstreamName); err != nil {
		t.Fatalf("expected breaker closed, got: %v", err)
	}

	// 2. Server starts returning 500: trip breaker after FailureThreshold=3
	return500.Store(true)
	for i := 0; i < 3; i++ {
		_ = checker.ProbeOnce(ctx)
	}

	if err := breakers.Allow(upstreamName); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected breaker open, got: %v", err)
	}

	// 3. Wait for cooldown to transition to HalfOpen
	time.Sleep(60 * time.Millisecond)

	// 4. Server recovers: returning 200
	return500.Store(false)

	// In half-open, 2 successes will close the breaker
	for i := 0; i < 2; i++ {
		_ = checker.ProbeOnce(ctx)
	}

	if err := breakers.Allow(upstreamName); err != nil {
		t.Fatalf("expected breaker closed after recovery, got: %v", err)
	}
}

func TestHealthChecker_RunLifecycleAndCleanShutdown(t *testing.T) {
	defer goleak.VerifyNone(t)

	var probeCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	up := &domain.Upstream{Name: "up", BaseURL: srv.URL}
	checker := NewHealthCheckerWithStaticUpstreams(
		HealthCheckConfig{
			Interval:    10 * time.Millisecond,
			Timeout:     100 * time.Millisecond,
			Concurrency: 2,
		},
		[]*domain.Upstream{up},
		nil,
		newMockBreakerReporter(),
	)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- checker.Run(ctx)
	}()

	// Allow several probe ticks
	time.Sleep(50 * time.Millisecond)

	// Shutdown
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled error, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("HealthChecker.Run did not exit on context cancellation")
	}

	if probeCount.Load() == 0 {
		t.Fatal("expected at least 1 probe to execute during Run")
	}
}
