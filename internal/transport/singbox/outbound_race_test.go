package singbox

import (
	"context"
	"sync"
	"testing"
)

// DialContext must be safe for concurrent use with Stop/Refresh, failing open
// cleanly to direct dial without racing or panicking.
func TestDialIsRaceFreeWithConcurrentStop(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)

	const iterations = 5000
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			// Dial an unroutable loopback port — should return error cleanly, not panic
			conn, err := m.DialContext(context.Background(), "tcp", "127.0.0.1:0")
			if err == nil && conn != nil {
				conn.Close()
			}
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			m.Stop()
		}
	}()

	wg.Wait()
}

// RedactURL must remove a subscription URL embedded in a fetch error and leave
// unrelated text, and an empty URL, untouched.
func TestRedactURL(t *testing.T) {
	t.Parallel()

	sub := "https://example.com/sub?token=secret"
	errMsg := `fetch subscription: Get "https://example.com/sub?token=secret": dial tcp: i/o timeout`
	got := RedactURL(errMsg, sub)
	if got == errMsg {
		t.Fatalf("RedactURL left the credential in place: %q", got)
	}
	if want := `fetch subscription: Get "[REDACTED]": dial tcp: i/o timeout`; got != want {
		t.Errorf("RedactURL = %q, want %q", got, want)
	}
	if got := RedactURL("plain error", ""); got != "plain error" {
		t.Errorf("RedactURL with empty URL = %q, want unchanged", got)
	}
}
