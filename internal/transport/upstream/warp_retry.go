package upstream

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/dickymuliafiqri/firefly/internal/transport/warp"
)

// warpEgressRetryBudget caps how many times one request may re-dial through a
// different WARP session after a 429. The loop is self-terminating in practice —
// RotateEgress reports false once no unpenalized session is left — so this is a
// backstop against a pathological pool that keeps un-penalizing slots between
// attempts, not a tuning knob. Exhausting it hands the 429 back untouched and
// the adapter's key cooldown/failover path runs exactly as it does today.
const warpEgressRetryBudget = 3

// warpEgressErrorBodyLimit bounds how much of a 429 body is buffered so it can
// be replayed to the caller when no alternate egress exists. Provider error
// bodies are small JSON; anything larger is truncated rather than held in
// memory on the request path.
const warpEgressErrorBodyLimit = 64 << 10

// idleConnCloser is the subset of *http.Transport the WARP rotation needs. It
// is declared as an interface so a wrapped transport still satisfies it.
type idleConnCloser interface{ CloseIdleConnections() }

// egressRotator is the seam the retry transport uses to move egress off a
// rate-limited IP. Declaring it here (rather than taking *warp.Manager
// directly) keeps the retry logic testable without standing up a real WARP
// pool, and keeps this package free of any Cloudflare lifecycle knowledge.
type egressRotator interface {
	// RotateEgress penalizes the IP that just answered 429 and reports
	// whether a different, currently-healthy egress is available to retry on.
	RotateEgress() bool
}

// warpRetryTransport wraps the real *http.Transport for an upstream whose
// egress is the embedded WARP tunnel. It exists because a 429 on such an
// upstream is usually IP-bound rather than key-bound: the provider is
// rate-limiting the tunnel's current public address, and the credential is
// perfectly healthy. Charging that to the key slot (cooldown + consecutive
// error counter) idles a good identity and can eventually arm the key-error
// deactivate/delete threshold against it.
//
// Instead, the first 429 is intercepted here — before the adapter's key-failover
// logic ever sees it — and the request is replayed through a different
// already-live session in the WARP pool. No new Cloudflare device registration
// happens (that is slow and itself rate-limited), so this is safe to run inline
// on the request path.
//
// When the pool has no healthy alternate, RotateEgress reports false and the
// 429 is returned unchanged, so the pre-existing key cooldown/failover behavior
// is preserved with no regression for single-session pools.
type warpRetryTransport struct {
	base http.RoundTripper
	warp egressRotator
}

// RoundTrip implements http.RoundTripper. The original request is never mutated:
// every retry runs against a clone with a fresh body from GetBody.
func (t *warpRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t == nil || t.base == nil {
		return nil, errors.New("warp retry transport is not configured")
	}
	resp, err := t.base.RoundTrip(req)
	// Only a 429 is an egress-IP signal. Transport errors and every other
	// status keep flowing through the existing breaker/key paths untouched.
	if err != nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		return resp, err
	}
	// A body that cannot be replayed cannot be retried. Hand the 429 back so
	// the adapter's key failover owns it.
	if req.Body != nil && req.GetBody == nil {
		return resp, nil
	}

	for attempt := 0; attempt < warpEgressRetryBudget; attempt++ {
		// The client may have walked away while the first attempt was in
		// flight; retrying would burn upstream quota for nobody.
		if req.Context().Err() != nil {
			return resp, nil
		}
		retryReq, buildErr := replayRequest(req)
		if buildErr != nil {
			return resp, nil
		}
		// Release the 429 connection back to the pool *before* rotating, so
		// the rotation's CloseIdleConnections actually drops the socket
		// instead of leaving it pinned to the penalized IP. The body is
		// buffered (not discarded) because it may still have to be handed
		// back to the caller if no alternate exists.
		resp.Body = bufferAndClose(resp.Body)

		if !t.warp.RotateEgress() {
			// No healthy alternate session: surface the 429 verbatim so the
			// adapter cools down the key and fails over exactly as before.
			return resp, nil
		}

		retryResp, retryErr := t.base.RoundTrip(retryReq)
		if retryErr != nil {
			return retryResp, retryErr
		}
		if retryResp == nil {
			return nil, errors.New("warp egress retry: upstream returned no response")
		}
		resp = retryResp
		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
	}
	return resp, nil
}

// CloseIdleConnections forwards to the wrapped transport so a WARP rotation can
// still drop sockets pinned to a retired egress IP through the wrapper.
func (t *warpRetryTransport) CloseIdleConnections() {
	if t == nil || t.base == nil {
		return
	}
	if ic, ok := t.base.(idleConnCloser); ok {
		ic.CloseIdleConnections()
	}
}

// replayRequest clones req with a freshly opened body. http.Transport consumes
// and closes the request body it is handed (per the RoundTripper contract), so
// the retry needs its own reader rather than the already-drained original.
func replayRequest(req *http.Request) (*http.Request, error) {
	if req.GetBody == nil {
		return nil, errors.New("request body is not replayable")
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.Body = body
	return clone, nil
}

// bufferAndClose reads a bounded error body and closes the original reader so
// the underlying connection is released. The buffered copy lets the caller keep
// serving the 429 to the client after the socket is gone.
func bufferAndClose(body io.ReadCloser) io.ReadCloser {
	if body == nil {
		return http.NoBody
	}
	defer body.Close()
	b, _ := io.ReadAll(io.LimitReader(body, warpEgressErrorBodyLimit))
	if len(b) == 0 {
		return http.NoBody
	}
	return io.NopCloser(bytes.NewReader(b))
}

// compile-time proof that the wrapper satisfies the contracts it is installed
// against — a plain RoundTripper for http.Client, an idle-connection dropper for
// the WARP rotation observer — and that the real WARP manager satisfies the
// egressRotator seam this package depends on.
var (
	_ http.RoundTripper = (*warpRetryTransport)(nil)
	_ idleConnCloser    = (*warpRetryTransport)(nil)
	_ egressRotator     = (*warp.Manager)(nil)
)
