package supervisor

import (
	"context"
	"io"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func atomicAdd(p *int32) int32  { return atomic.AddInt32(p, 1) }
func atomicLoad(p *int32) int32 { return atomic.LoadInt32(p) }
func debugStack() []byte        { return debug.Stack() }

type observerSpy struct {
	mu       sync.Mutex
	panics   map[string]int
	restarts map[string]int
}

func newObserverSpy() *observerSpy {
	return &observerSpy{panics: map[string]int{}, restarts: map[string]int{}}
}

func (o *observerSpy) WorkerPanic(worker string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.panics[worker]++
}

func (o *observerSpy) WorkerRestart(worker string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.restarts[worker]++
}

func (o *observerSpy) counts(worker string) (int, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.panics[worker], o.restarts[worker]
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// lookup returns the current status of a worker, if it has been started.
func lookup(worker string) (Status, bool) {
	for _, s := range Snapshot() {
		if s.Worker == worker {
			return s, true
		}
	}
	return Status{}, false
}

// waitForStatus polls the registry until cond holds or the deadline passes.
func waitForStatus(t *testing.T, worker string, cond func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last Status
	for time.Now().Before(deadline) {
		if s, ok := lookup(worker); ok {
			last = s
			if cond(s) {
				return s
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition never held for %q; last status: %+v", worker, last)
	return last
}

func TestGoRestartsWorkerAfterPanic(t *testing.T) {
	spy := newObserverSpy()
	Configure(discardLogger(), spy)
	worker := "test.restart"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	done := make(chan struct{})
	var once sync.Once

	Go(ctx, worker, Policy{MinBackoff: 5 * time.Millisecond, MaxBackoff: 10 * time.Millisecond},
		func(context.Context) {
			// The first attempt panics; the restart must reach the success
			// path, proving the loop was actually re-entered.
			if atomicAdd(&calls) == 1 {
				panic("boom")
			}
			once.Do(func() { close(done) })
		})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker was never restarted after its panic")
	}

	st := waitForStatus(t, worker, func(s Status) bool { return s.Panics == 1 && s.Restarts == 1 })
	if st.LastPanic != "boom" {
		t.Errorf("LastPanic = %q, want %q", st.LastPanic, "boom")
	}
	if st.Location == "" || !strings.Contains(st.Location, "supervisor_test.go") {
		t.Errorf("Location = %q, want a frame inside supervisor_test.go", st.Location)
	}
	if !strings.Contains(st.Location, "TestGoRestartsWorkerAfterPanic") {
		t.Errorf("Location = %q, want the panicking function name", st.Location)
	}
	if !st.Supervised {
		t.Error("Supervised = false, want true for a worker launched with Go")
	}
	if st.LastPanicAt == 0 {
		t.Error("LastPanicAt not recorded")
	}
	if st.Disabled {
		t.Error("Disabled = true, want false with unlimited restarts")
	}

	if got, want := spy.panics[worker], 1; got != want {
		t.Errorf("observer panics = %d, want %d", got, want)
	}
	if got, want := spy.restarts[worker], 1; got != want {
		t.Errorf("observer restarts = %d, want %d", got, want)
	}
}

func TestGoDoesNotRestartOnNormalReturn(t *testing.T) {
	Configure(discardLogger(), nil)
	worker := "test.normal-return"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	Go(ctx, worker, DefaultPolicy(), func(context.Context) { atomicAdd(&calls) })

	st := waitForStatus(t, worker, func(s Status) bool { return !s.Running })
	if st.Panics != 0 || st.Restarts != 0 {
		t.Errorf("Panics/Restarts = %d/%d, want 0/0", st.Panics, st.Restarts)
	}
	if got := atomicLoad(&calls); got != 1 {
		t.Errorf("worker ran %d times, want exactly 1", got)
	}
}

func TestGoStopsRestartingWhenContextCancelled(t *testing.T) {
	Configure(discardLogger(), nil)
	worker := "test.cancel"

	ctx, cancel := context.WithCancel(context.Background())

	var calls int32
	Go(ctx, worker, Policy{MinBackoff: time.Hour, MaxBackoff: time.Hour}, func(context.Context) {
		atomicAdd(&calls)
		panic("always")
	})

	// First attempt panics, then the worker sleeps for an hour. Cancelling must
	// abandon that wait instead of keeping the goroutine alive.
	waitForStatus(t, worker, func(s Status) bool { return s.Panics == 1 })
	cancel()

	st := waitForStatus(t, worker, func(s Status) bool { return !s.Running && s.BackoffMs == 0 })
	if st.Restarts != 0 {
		t.Errorf("Restarts = %d, want 0: the backoff wait was cut short", st.Restarts)
	}
	time.Sleep(20 * time.Millisecond)
	if got := atomicLoad(&calls); got != 1 {
		t.Errorf("worker ran %d times after cancel, want 1", got)
	}
}

func TestGoDisablesWorkerAfterMaxRestarts(t *testing.T) {
	Configure(discardLogger(), nil)
	worker := "test.max-restarts"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	Go(ctx, worker, Policy{
		MinBackoff:  time.Millisecond,
		MaxBackoff:  2 * time.Millisecond,
		MaxRestarts: 2,
	}, func(context.Context) {
		atomicAdd(&calls)
		panic("always")
	})

	st := waitForStatus(t, worker, func(s Status) bool { return s.Disabled })
	if st.Restarts != 2 {
		t.Errorf("Restarts = %d, want 2 (MaxRestarts)", st.Restarts)
	}
	if st.Running {
		t.Error("Running = true, want false once disabled")
	}
	time.Sleep(20 * time.Millisecond)
	if got := atomicLoad(&calls); got != 3 {
		t.Errorf("worker ran %d times, want 3 (initial + 2 restarts)", got)
	}
}

func TestGoReturnsDoneChannelWhenWorkerStops(t *testing.T) {
	Configure(discardLogger(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := Go(ctx, "test.done", DefaultPolicy(), func(context.Context) {})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("done channel never closed after a normal return")
	}

	blocked := Go(ctx, "test.done-cancel", DefaultPolicy(), func(c context.Context) { <-c.Done() })
	cancel()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("done channel never closed after the context was cancelled")
	}
}

func TestGuardRecoversOneShotTask(t *testing.T) {
	spy := newObserverSpy()
	Configure(discardLogger(), spy)
	worker := "test.guard"

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer Guard(worker)()
		panic("one-shot boom")
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("guarded goroutine did not finish")
	}

	st := waitForStatus(t, worker, func(s Status) bool { return s.Panics == 1 })
	if st.Supervised {
		t.Error("Supervised = true, want false for a Guard task")
	}
	if st.Restarts != 0 {
		t.Errorf("Restarts = %d, want 0", st.Restarts)
	}
	if !strings.Contains(st.Location, "TestGuardRecoversOneShotTask") {
		t.Errorf("Location = %q, want the panicking function", st.Location)
	}
	if p, _ := spy.counts(worker); p != 1 {
		t.Errorf("observer panics = %d, want 1", p)
	}
}

func TestGuardIsNoOpWithoutPanic(t *testing.T) {
	Configure(discardLogger(), nil)
	worker := "test.guard-clean"

	func() {
		defer Guard(worker)()
	}()

	for _, s := range Snapshot() {
		if s.Worker == worker {
			t.Fatalf("clean task recorded status %+v, want nothing", s)
		}
	}
}

func TestSnapshotIsSortedByName(t *testing.T) {
	Configure(discardLogger(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, name := range []string{"test.sort-c", "test.sort-a", "test.sort-b"} {
		Go(ctx, name, DefaultPolicy(), func(context.Context) {})
	}
	waitForStatus(t, "test.sort-a", func(Status) bool { return true })

	names := make([]string, 0, len(Snapshot()))
	for _, s := range Snapshot() {
		names = append(names, s.Worker)
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("snapshot not sorted: %v", names)
		}
	}
}

func TestPanicLocationFromRealStack(t *testing.T) {
	stack := capturePanicStack()
	loc := PanicLocation(stack)

	if !strings.Contains(loc, "supervisor_test.go:") {
		t.Errorf("location = %q, want a supervisor_test.go frame", loc)
	}
	if !strings.Contains(loc, "panicHere") {
		t.Errorf("location = %q, want the innermost function name", loc)
	}
}

// panicHere panics two frames deep so the location must point at the innermost
// frame, not at the recovering caller.
func panicHere() {
	var m map[string]int
	m["boom"] = 1
}

func callPanicHere() { panicHere() }

func capturePanicStack() []byte {
	var captured []byte
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				captured = debugStack()
			}
		}()
		callPanicHere()
	}()
	return captured
}

func TestPanicLocationFallsBackForUnusualDump(t *testing.T) {
	loc := PanicLocation([]byte("goroutine 1 [running]:\nmain.x()\n\t/abs/path/main.go:7 +0x1\n"))
	if !strings.Contains(loc, "main.go:7") {
		t.Errorf("location = %q, want the main.go:7 frame", loc)
	}
	if PanicLocation(nil) != "unknown" {
		t.Errorf("location for an empty dump = %q, want unknown", PanicLocation(nil))
	}
}

func TestTrimFuncAndShortenPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"main.inner(...)", "main.inner"},
		{modulePrefix + "internal/transport/warp.(*Manager).autoRotationLoop(...)", "internal/transport/warp.(*Manager).autoRotationLoop"},
		{"runtime/debug.Stack()", "runtime/debug.Stack"},
		{"panic({0xe84ecc8?, 0xe862150?})", "panic"},
	}
	for _, tc := range cases {
		if got := trimFunc(tc.in); got != tc.want {
			t.Errorf("trimFunc(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	if got := shortenPath("/home/u/go/src/" + modulePrefix + "internal/server/router.go:12"); got != "internal/server/router.go:12" {
		t.Errorf("shortenPath = %q", got)
	}
	if got := shortenPath("/elsewhere/code/internal/x/y.go:3"); got != "internal/x/y.go:3" {
		t.Errorf("shortenPath = %q", got)
	}
}

func TestPolicyDefaults(t *testing.T) {
	p := Policy{}.withDefaults()
	if p.MinBackoff != DefaultMinBackoff {
		t.Errorf("MinBackoff = %v, want %v", p.MinBackoff, DefaultMinBackoff)
	}
	if p.MaxBackoff != DefaultMaxBackoff {
		t.Errorf("MaxBackoff = %v, want %v", p.MaxBackoff, DefaultMaxBackoff)
	}
	if p.ResetAfter != DefaultResetAfter {
		t.Errorf("ResetAfter = %v, want %v", p.ResetAfter, DefaultResetAfter)
	}

	// A ceiling below the floor must not invert the growth.
	p = Policy{MinBackoff: time.Second, MaxBackoff: time.Millisecond}.withDefaults()
	if p.MaxBackoff < p.MinBackoff {
		t.Errorf("MaxBackoff %v < MinBackoff %v", p.MaxBackoff, p.MinBackoff)
	}
}

func TestGoIgnoresNilFunc(t *testing.T) {
	Go(context.Background(), "test.nil-fn", DefaultPolicy(), nil)
	for _, s := range Snapshot() {
		if s.Worker == "test.nil-fn" {
			t.Fatalf("nil func recorded status %+v", s)
		}
	}
}
