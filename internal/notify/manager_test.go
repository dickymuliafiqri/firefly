package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestManager_DeliversToConfiguredChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var mu sync.Mutex
	var got int
	m := NewManager(ManagerConfig{
		Channels: func() []Channel {
			return []Channel{{URL: srv.URL, Format: "generic", Enabled: true}}
		},
		AllowPrivate: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	m.Emit(sampleEvent())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := got
		mu.Unlock()
		if m.Stats().Delivered > 0 {
			break
		}
		_ = n
		time.Sleep(5 * time.Millisecond)
	}
	if m.Stats().Delivered != 1 {
		t.Fatalf("delivered = %d, want 1 (stats: %+v)", m.Stats().Delivered, m.Stats())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("manager did not stop within 5s")
	}
}

func TestManager_NoChannelsIsSilent(t *testing.T) {
	m := NewManager(ManagerConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	m.Emit(sampleEvent())
	time.Sleep(50 * time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("manager did not stop")
	}
	if s := m.Stats(); s.Delivered != 0 || s.Failed != 0 {
		t.Fatalf("stats = %+v, want all zero", s)
	}
}

func TestManager_NilIsSafe(t *testing.T) {
	var m *Manager
	m.Emit(sampleEvent()) // must not panic
	m.Run(context.Background())
	if got := m.Stats(); got != (Stats{}) {
		t.Fatalf("nil manager stats = %+v", got)
	}
}

func TestManager_ChannelsReReadPerDelivery(t *testing.T) {
	// The channel list is a func so a settings hot-reload takes effect without
	// restarting the worker; prove the second delivery sees the new list.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var enabled atomicBool
	m := NewManager(ManagerConfig{
		Channels: func() []Channel {
			if !enabled.get() {
				return nil
			}
			return []Channel{{URL: srv.URL, Format: "generic", Enabled: true}}
		},
		AllowPrivate: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	m.Emit(sampleEvent())
	time.Sleep(50 * time.Millisecond)
	if m.Stats().Delivered != 0 {
		t.Fatalf("delivered = %d, want 0 while no channel is configured", m.Stats().Delivered)
	}

	enabled.set(true)
	m.Emit(sampleEvent())
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && m.Stats().Delivered == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if m.Stats().Delivered != 1 {
		t.Fatalf("delivered = %d, want 1 after the channel appeared", m.Stats().Delivered)
	}
}

type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (a *atomicBool) get() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.v
}

func (a *atomicBool) set(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.v = v
}
