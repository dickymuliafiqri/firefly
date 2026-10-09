package notify

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// errTransport is the transient failure the poster retries.
var errTransport = errors.New("simulated transport failure")

// recordingClient captures every request body it is handed and answers 200.
func recordingClient(t *testing.T, mu *sync.Mutex, sink *[]Event) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var ev Event
		if err := json.Unmarshal(body, &ev); err != nil {
			t.Errorf("body is not an event: %v", err)
		}
		mu.Lock()
		*sink = append(*sink, ev)
		mu.Unlock()
		return newResponse(200, `{"ok":true}`), nil
	})}
}

// countingClient counts successful deliveries.
func countingClient(t *testing.T, count *atomic.Int64) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, r.Body)
		count.Add(1)
		return newResponse(200, `{"ok":true}`), nil
	})}
}

// blockingClient parks the caller until release is closed, signalling on the
// first call so a test can observe that the worker is stuck.
func blockingClient(t *testing.T, release <-chan struct{}, reached chan<- struct{}) *http.Client {
	t.Helper()
	var once sync.Once
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { reached <- struct{}{} })
		<-release
		return newResponse(200, `{"ok":true}`), nil
	})}
}

// statusClient always answers with the given status.
func statusClient(status int) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, r.Body)
		return newResponse(status, `{"detail":"nope"}`), nil
	})}
}

// failingClient always fails at the transport level, which is the transient
// case the poster retries.
func failingClient() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, r.Body)
		return nil, errTransport
	})}
}

// headerCaptureClient records the request headers alongside the body.
func headerCaptureClient(t *testing.T, mu *sync.Mutex, gotHeader *http.Header, gotBody *[]byte) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		mu.Lock()
		*gotHeader = r.Header.Clone()
		*gotBody = body
		mu.Unlock()
		return newResponse(200, `{"ok":true}`), nil
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
