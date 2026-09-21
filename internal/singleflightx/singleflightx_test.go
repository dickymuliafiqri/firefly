package singleflightx_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dickymuliafiqri/firefly/internal/singleflightx"
)

func TestDoCoalescesConcurrentCallers(t *testing.T) {
	var g singleflightx.Group[string]
	var calls int32
	var mu sync.Mutex

	release := make(chan struct{})
	start := make(chan struct{})

	const callers = 16
	results := make([]string, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			v, err := g.Do(context.Background(), "shared", func() (string, error) {
				mu.Lock()
				calls++
				mu.Unlock()
				<-release
				return "value", nil
			})
			results[i] = v
			require.NoError(t, err)
		}(i)
	}
	close(start)
	// Give every caller a chance to join the flight before it completes.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	require.Equal(t, int32(1), calls, "fn must run once for one key")
	for i, r := range results {
		require.Equal(t, "value", r, "caller %d", i)
	}
}

func TestDoCanceledWaiterDoesNotCancelFlight(t *testing.T) {
	var g singleflightx.Group[string]

	flightRunning := make(chan struct{})
	release := make(chan struct{})

	type outcome struct {
		val string
		err error
	}
	flight := make(chan outcome, 1)
	go func() {
		v, err := g.Do(context.Background(), "key", func() (string, error) {
			close(flightRunning)
			<-release
			return "done", nil
		})
		flight <- outcome{v, err}
	}()
	<-flightRunning

	// A second caller gives up while the flight is still running.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v, err := g.Do(ctx, "key", func() (string, error) {
		t.Error("fn must not run for a caller that joined an existing flight")
		return "", nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "", v)

	// The flight outlived the waiter that gave up: the caller that started it
	// still gets its result instead of a canceled context.
	close(release)
	select {
	case got := <-flight:
		require.NoError(t, got.err)
		require.Equal(t, "done", got.val)
	case <-time.After(2 * time.Second):
		t.Fatal("the flight did not finish after its waiter was canceled")
	}
}

func TestDoPropagatesErrorToEveryWaiter(t *testing.T) {
	var g singleflightx.Group[int]
	boom := errors.New("boom")

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := g.Do(context.Background(), "err", func() (int, error) { return 0, boom })
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.ErrorIs(t, err, boom, "caller %d", i)
	}
}

func TestDoSeparatesKeys(t *testing.T) {
	var g singleflightx.Group[string]

	var mu sync.Mutex
	seen := map[string]int{}
	for _, key := range []string{"a", "b", "a", "b"} {
		key := key
		_, err := g.Do(context.Background(), key, func() (string, error) {
			mu.Lock()
			seen[key]++
			mu.Unlock()
			return key, nil
		})
		require.NoError(t, err)
	}
	require.Equal(t, map[string]int{"a": 2, "b": 2}, seen, "each key runs its own flight")
}

func TestDoReturnsTypedNil(t *testing.T) {
	type session struct{ id string }
	var g singleflightx.Group[*session]

	got, err := g.Do(context.Background(), "key", func() (*session, error) { return nil, nil })
	require.NoError(t, err)
	require.Nil(t, got)
}
