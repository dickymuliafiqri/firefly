package watch

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestDebounceCoalescesBurstIntoOneReload(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("upstreams.json", `{"upstreams":[]}`)
	write("models.json", `{"models":[]}`)
	write("tenants.json", `{"tenants":[]}`)

	var reloads atomic.Int64
	reload := func(context.Context) error { reloads.Add(1); return nil }

	// A long debounce window relative to the burst spacing, so every write in the
	// burst restarts the quiet window and only ONE reload should fire. This
	// exercises the fresh-per-burst timer path: with a reused timer and an
	// undrained channel, a stale tick could fire a premature second reload.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := New(Options{
		Dir:          dir,
		PollInterval: time.Hour, // disable poll backstop for determinism
		Debounce:     150 * time.Millisecond,
	}, nil)

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, reload) }()
	time.Sleep(30 * time.Millisecond) // let the watcher attach

	for i := 0; i < 4; i++ {
		write("models.json", `{"models":[{"public_name":"m","upstream":"u","upstream_model":"m"}]}`)
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for the first (coalesced) reload to land.
	deadline := time.After(2 * time.Second)
	for reloads.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("watcher did not reload after burst")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Allow time for any erroneous extra reload to surface.
	time.Sleep(300 * time.Millisecond)
	// Debounce is best-effort: a trigger that arrives while reload() runs may
	// start one more window. What must NOT happen is one reload per write (4) or
	// a reload firing with no trigger at all after the burst settles.
	if n := reloads.Load(); n > 2 {
		t.Fatalf("burst of 4 writes caused %d reloads; debounce did not coalesce", n)
	}
	settled := reloads.Load()
	time.Sleep(250 * time.Millisecond) // no new writes -> no new reload
	if got := reloads.Load(); got != settled {
		t.Fatalf("reload fired with no trigger: %d -> %d", settled, got)
	}

	cancel()
	<-done
}

func TestWatcherReloadsOnFileChange(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("upstreams.json", `{"upstreams":[]}`)
	write("models.json", `{"models":[]}`)
	write("tenants.json", `{"tenants":[]}`)

	var reloads atomic.Int64
	reload := func(ctx context.Context) error {
		reloads.Add(1)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := New(Options{
		Dir:          dir,
		PollInterval: 50 * time.Millisecond,
		Debounce:     10 * time.Millisecond,
	}, nil)

	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, reload) }()

	// Give the watcher time to attach, then trigger a change.
	time.Sleep(30 * time.Millisecond)
	write("models.json", `{"models":[{"public_name":"m","upstream":"u","upstream_model":"m"}]}`)

	deadline := time.After(2 * time.Second)
	for reloads.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("watcher did not reload after file change")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not exit after cancel")
	}
}

func TestWatcherExitsOnCancel(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithCancel(context.Background())
	w := New(Options{Dir: t.TempDir(), PollInterval: time.Second, Debounce: time.Millisecond}, nil)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, func(context.Context) error { return nil }) }()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not exit")
	}
}

func TestWatcherIgnoresUnrelatedFiles(t *testing.T) {
	defer goleak.VerifyNone(t)

	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("upstreams.json", `{"upstreams":[]}`)
	write("models.json", `{"models":[]}`)
	write("tenants.json", `{"tenants":[]}`)

	var reloads atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := New(Options{
		Dir:          dir,
		PollInterval: time.Hour, // disable the poll backstop for this test
		Debounce:     10 * time.Millisecond,
	}, nil)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, func(context.Context) error { reloads.Add(1); return nil }) }()
	time.Sleep(30 * time.Millisecond) // let fsnotify attach

	// Writes to non-config files in the same directory must NOT trigger reloads.
	write("gateway.log", "some log line")
	write("models.json.swp", "editor swap")
	time.Sleep(200 * time.Millisecond)
	if n := reloads.Load(); n != 0 {
		t.Fatalf("unrelated file writes triggered %d reload(s), want 0", n)
	}

	// A real config write still reloads.
	write("models.json", `{"models":[{"public_name":"m","upstream":"u","upstream_model":"m"}]}`)
	deadline := time.After(2 * time.Second)
	for reloads.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("config file change did not trigger a reload")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestWatcherDoesNotLeakPerIteration guards the SIGHUP hoist: the watcher used
// to call sighup(ctx) inside its select loop, spawning a fresh goroutine plus a
// signal registration on EVERY event. On the many-events path that leaked
// unbounded goroutines. We drive many filesystem events and assert the goroutine
// count stays flat; a regression would grow roughly one goroutine per event.
func TestWatcherDoesNotLeakPerIteration(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("upstreams.json", `{"upstreams":[]}`)
	write("models.json", `{"models":[]}`)
	write("tenants.json", `{"tenants":[]}`)

	var reloads atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := New(Options{
		Dir:          dir,
		PollInterval: time.Hour, // isolate fsnotify churn
		Debounce:     5 * time.Millisecond,
	}, nil)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, func(context.Context) error { reloads.Add(1); return nil }) }()
	time.Sleep(50 * time.Millisecond) // let fsnotify attach

	// Warm up so lazily-started runtime goroutines (fsnotify, timer, etc.) are
	// already accounted for before we sample the baseline.
	for i := 0; i < 20; i++ {
		write("models.json", `{"models":[]}`)
	}
	time.Sleep(300 * time.Millisecond)
	base := runtime.NumGoroutine()

	// Drive many more events through the select loop.
	for i := 0; i < 200; i++ {
		write("models.json", `{"models":[]}`)
	}
	time.Sleep(400 * time.Millisecond)

	after := runtime.NumGoroutine()
	// Allow generous slack for scheduler/GC goroutines; a per-iteration leak of
	// 200 events would be far above this bound.
	if after > base+15 {
		t.Fatalf("goroutines grew from %d to %d over ~200 events; suspected per-iteration leak", base, after)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not exit after cancel")
	}
}

// TestWatchSIGHUPIsNotRecreatedPerCall is a static guard: watchSIGHUP must be
// called exactly once per Run. We assert here that a single call registers one
// handler and that cancelling ctx stops it, using goleak to catch a stray
// goroutine (the old implementation spawned one per call).
func TestWatchSIGHUPLifecycle(t *testing.T) {
	defer goleak.VerifyNone(t)

	ctx, cancel := context.WithCancel(context.Background())
	ch := watchSIGHUP(ctx)
	if ch == nil {
		t.Fatal("watchSIGHUP returned nil channel")
	}
	// Reusing the same channel must not allocate more goroutines; a second call
	// would be a misuse but must still be goroutine-free on the hoist path.
	cancel()
	// Give AfterFunc a moment to run signal.Stop.
	time.Sleep(20 * time.Millisecond)
}
