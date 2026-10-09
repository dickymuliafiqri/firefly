package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// posterAttempts is how many times a transient failure is retried. A 4xx from
// the consumer is a permanent rejection and is never retried.
const posterAttempts = 3

// posterRetryBackoff is the fixed pause between attempts.
const posterRetryBackoff = 250 * time.Millisecond

// posterTimeout bounds one HTTP attempt.
const posterTimeout = 10 * time.Second

// Poster delivers events over HTTP. It signs the raw body with HMAC-SHA256,
// refuses to dial addresses that only make sense inside a private network, and
// retries transient failures a bounded number of times.
type Poster struct {
	// Client is the HTTP client used for delivery. Tests replace it.
	Client *http.Client
	// AllowPrivate lifts the SSRF guard for local development and tests.
	AllowPrivate bool
	// Now is overridable for signature tests.
	Now func() time.Time
}

// NewPoster builds a poster with the hardened default transport.
func NewPoster(allowPrivate bool) *Poster {
	return &Poster{
		Client: &http.Client{
			Timeout:   posterTimeout,
			Transport: safeTransport(allowPrivate),
		},
		AllowPrivate: allowPrivate,
		Now:          time.Now,
	}
}

// safeTransport builds a transport whose dialer resolves the host first and
// refuses loopback, link-local, and private ranges. Cloud metadata endpoints
// (169.254.169.254) and router admin panels are the classic SSRF targets, and
// a webhook URL is operator input, so the guard belongs here rather than in
// validation that can be bypassed by a DNS rebind.
func safeTransport(allowPrivate bool) *http.Transport {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("notify: split host port %q: %w", addr, err)
			}
			if allowPrivate {
				return dialer.DialContext(ctx, network, addr)
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("notify: resolve %q: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("notify: no addresses for %q", host)
			}
			for _, ip := range ips {
				if !isPublicIP(ip.IP) {
					return nil, fmt.Errorf("notify: %q resolves to non-public address %s", host, ip.IP)
				}
			}
			// Dial the first validated address directly so a second lookup
			// cannot swap in a private IP after the check.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// isPublicIP reports whether ip is routable on the public internet.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	// Private ranges: 10/8, 172.16/12, 192.168/16, fc00::/7, plus the CGNAT
	// block 100.64/10 that some clouds hand out for internal services.
	if ip.IsPrivate() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127:
			return false
		case ip4[0] == 0:
			return false
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0:
			return false
		case ip4[0] == 198 && ip4[1] == 18 && ip4[2] == 0:
			return false
		case ip4[0] == 198 && ip4[1] == 51 && ip4[2] == 100:
			return false
		case ip4[0] == 203 && ip4[1] == 0 && ip4[2] == 113:
			return false
		case ip4[0] >= 240:
			return false
		}
	}
	return true
}

// Sign computes the X-Firefly-Signature value for a body: the Unix timestamp
// and the hex HMAC-SHA256 of "t.body" keyed by secret. Including the timestamp
// in the signed material is what lets a consumer reject replays.
func Sign(secret string, body []byte, now time.Time) string {
	ts := now.Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

// redactURL keeps the scheme and host for diagnostics and drops everything
// after the path, because webhook URLs routinely embed a token.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[unparseable-url]"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// Post renders the event for the channel's format and delivers it, retrying
// transient failures. A 4xx response is returned as a permanent error.
func (p *Poster) Post(ctx context.Context, ch Channel, ev Event) error {
	if strings.TrimSpace(ch.URL) == "" {
		return errors.New("notify: channel url is empty")
	}
	if !IsKnownFormat(ch.Format) {
		return fmt.Errorf("notify: unknown channel format %q", ch.Format)
	}

	body, contentType, err := Render(ch.Format, ev)
	if err != nil {
		return err
	}

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	now := p.Now
	if now == nil {
		now = time.Now
	}

	var lastErr error
	for attempt := 1; attempt <= posterAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(posterRetryBackoff):
			}
		}

		reqCtx, cancel := context.WithTimeout(ctx, posterTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, ch.URL, strings.NewReader(string(body)))
		if err != nil {
			cancel()
			return fmt.Errorf("notify: build request: %w", err)
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("User-Agent", "firefly-notify/1")
		if ch.Bearer != "" {
			req.Header.Set("Authorization", "Bearer "+ch.Bearer)
		}
		if ch.Secret != "" {
			req.Header.Set("X-Firefly-Signature", Sign(ch.Secret, body, now()))
		}

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("notify: post attempt %d: %w", attempt, err)
			continue
		}
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		cancel()
		if readErr != nil {
			lastErr = fmt.Errorf("notify: read response attempt %d: %w", attempt, readErr)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		// A 4xx is the consumer rejecting the payload; retrying the same bytes
		// will produce the same answer, so stop immediately.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return fmt.Errorf("notify: channel rejected event with %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		}
		lastErr = fmt.Errorf("notify: channel returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return lastErr
}
