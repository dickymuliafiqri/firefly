package notify

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDispatcher_DeliversToMatchingChannel(t *testing.T) {
	var mu sync.Mutex
	var got []Event

	poster := &Poster{Client: recordingClient(t, &mu, &got), Now: time.Now}
	d := NewDispatcher(DispatcherConfig{
		QueueSize: 8,
		Channels: func() []Channel {
			return []Channel{{URL: "https://example.invalid/hook", Format: "generic", Enabled: true}}
		},
		Poster: poster,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	d.Emit(sampleEvent())
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	})

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher did not stop within 5s of context cancellation")
	}

	stats := d.Stats()
	if stats.Delivered != 1 {
		t.Fatalf("delivered = %d, want 1 (stats: %+v)", stats.Delivered, stats)
	}
	if stats.Dropped != 0 {
		t.Fatalf("dropped = %d, want 0", stats.Dropped)
	}
}

func TestDispatcher_DropsWhenQueueFull(t *testing.T) {
	// A poster that blocks until released keeps the worker parked, so the queue
	// fills and every further emit must be dropped rather than blocking.
	release := make(chan struct{})
	blocked := make(chan struct{}, 1)

	poster := &Poster{Client: blockingClient(t, release, blocked), Now: time.Now}
	d := NewDispatcher(DispatcherConfig{
		QueueSize: 2,
		Channels: func() []Channel {
			return []Channel{{URL: "https://example.invalid/hook", Format: "generic", Enabled: true}}
		},
		Poster: poster,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	// Park the worker inside the poster so the queue can actually fill.
	d.Emit(sampleEvent())
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never reached the poster")
	}

	for i := 0; i < 10; i++ {
		d.Emit(sampleEvent())
	}

	deadline := time.After(2 * time.Second)
	for {
		if d.Stats().Dropped > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("expected drops once the queue filled, stats: %+v", d.Stats())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	close(release)
}

func TestDispatcher_EmitAfterShutdownDrops(t *testing.T) {
	d := NewDispatcher(DispatcherConfig{QueueSize: 4})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	// Let Run flip the running flag before cancelling.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher did not stop")
	}

	d.Emit(sampleEvent())
	if got := d.Stats().Dropped; got != 1 {
		t.Fatalf("dropped = %d, want 1 for an emit after shutdown", got)
	}
}

func TestDispatcher_NilIsSafe(t *testing.T) {
	var d *Dispatcher
	d.Emit(sampleEvent()) // must not panic
	if got := d.Stats(); got != (Stats{}) {
		t.Fatalf("nil dispatcher stats = %+v, want zero", got)
	}
}

func TestDispatcher_ConcurrentEmitNoRace(t *testing.T) {
	var count atomic.Int64
	poster := &Poster{Client: countingClient(t, &count), Now: time.Now}
	d := NewDispatcher(DispatcherConfig{
		QueueSize: 64,
		Channels: func() []Channel {
			return []Channel{{URL: "https://example.invalid/hook", Format: "generic", Enabled: true}}
		},
		Poster: poster,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Emit(sampleEvent())
		}()
	}
	wg.Wait()

	// The queue is deliberately smaller than the burst, so some events are
	// expected to drop. What must hold is that every event is accounted for
	// exactly once — delivered or dropped, never both, never lost silently.
	waitFor(t, func() bool {
		s := d.Stats()
		return s.Delivered+s.Dropped == 100
	})
	if got := d.Stats().Delivered; got == 0 {
		t.Fatal("nothing was delivered")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher did not stop")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within 3s")
}
