package singbox

import (
	"sync"
	"testing"
)

// outbound must read the active Box exactly once, through the lock, so a
// concurrent Stop/Refresh swap cannot race the read (detected by -race) nor
// turn the following dereference into a nil-pointer panic. Regression for the
// unlocked `if m.box == nil` read plus the racy `m.currentBox().Outbound()`
// dereference that could see a Box cleared underneath it.
func TestOutboundIsRaceFreeWithConcurrentStop(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)

	const iterations = 20000
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if _, ok := m.outbound("auto"); ok {
				t.Error("outbound reported a box with no subscription configured")
				return
			}
		}
	}()

	go func() {
		defer wg.Done()
		// Stop() clears m.box under the write lock; it is idempotent.
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
