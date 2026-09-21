package qoder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"github.com/dickymuliafiqri/firefly/internal/textx"
)

// A Qoder Personal Access Token (pt-...) cannot COSY-sign requests directly. It
// must be exchanged for a short-lived job token (jt-...) via jobToken/exchange
// (plain JSON POST, NOT COSY-signed), then the userId is resolved from
// /userinfo. Job tokens last ~24h; we cache and re-exchange near expiry.

const (
	patDefaultTTL    = 24 * time.Hour
	patRefreshBuffer = 5 * time.Minute
	patMachineIDSeed = "qoder-machine" // stable machineId derived per PAT
)

// resolvedPAT is a cached PAT exchange result.
type resolvedPAT struct {
	jobToken  string
	userID    string
	machineID string
	expiresAt time.Time
}

// patCache caches PAT->jobToken exchanges keyed by the PAT, single-flighted.
type patCache struct {
	mu       sync.Mutex
	entries  map[string]*resolvedPAT
	inflight map[string]*sync.WaitGroup
}

func newPATCache() *patCache {
	return &patCache{
		entries:  make(map[string]*resolvedPAT),
		inflight: make(map[string]*sync.WaitGroup),
	}
}

// isPAT reports whether a token is a Qoder personal access token.
func isPAT(token string) bool {
	return len(token) >= len(QoderPATPrefix) && token[:len(QoderPATPrefix)] == QoderPATPrefix
}

// resolve exchanges a PAT for a job token + userId, caching the result. It
// coalesces concurrent exchanges of the same PAT.
func (c *patCache) resolve(ctx context.Context, client *http.Client, pat string) (*resolvedPAT, error) {
	c.mu.Lock()
	if e, ok := c.entries[pat]; ok && time.Until(e.expiresAt) > patRefreshBuffer {
		c.mu.Unlock()
		return e, nil
	}
	if wg, ok := c.inflight[pat]; ok {
		c.mu.Unlock()
		wg.Wait()
		c.mu.Lock()
		if e, ok := c.entries[pat]; ok && time.Until(e.expiresAt) > patRefreshBuffer {
			c.mu.Unlock()
			return e, nil
		}
		c.mu.Unlock()
		return c.doExchange(ctx, client, pat)
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	c.inflight[pat] = wg
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.inflight, pat)
		c.mu.Unlock()
		wg.Done()
	}()

	return c.doExchange(ctx, client, pat)
}

func (c *patCache) doExchange(ctx context.Context, client *http.Client, pat string) (*resolvedPAT, error) {
	jobToken, expiresAt, err := exchangeJobToken(ctx, client, pat)
	if err != nil {
		return nil, err
	}
	userID := fetchUserID(ctx, client, jobToken)
	if userID == "" {
		return nil, fmt.Errorf("qoder: could not resolve userId for job token")
	}
	e := &resolvedPAT{
		jobToken:  jobToken,
		userID:    userID,
		machineID: stableHash16(patMachineIDSeed, pat), // stable per PAT
		expiresAt: expiresAt,
	}
	c.mu.Lock()
	c.entries[pat] = e
	c.mu.Unlock()
	return e, nil
}

// exchangeJobToken exchanges a PAT for a job token via jobToken/exchange.
// This endpoint is a plain JSON POST — NOT COSY-signed.
func exchangeJobToken(ctx context.Context, client *http.Client, pat string) (jobToken string, expiresAt time.Time, err error) {
	body, _ := json.Marshal(map[string]string{"personal_token": pat})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, QoderJobTokenExchangeURL, bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "qodercli/1.0.0")
	req.Header.Set("Cosy-Version", QoderIDEVersion)
	req.Header.Set("Cosy-ClientType", QoderClientType)

	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("qoder PAT exchange failed: %d %s", resp.StatusCode, textx.Excerpt(buf, 180))
	}
	if !gjson.ValidBytes(buf) {
		return "", time.Time{}, fmt.Errorf("qoder PAT exchange returned invalid json")
	}
	r := gjson.ParseBytes(buf)
	jobToken = r.Get("token").String()
	if jobToken == "" {
		return "", time.Time{}, fmt.Errorf("qoder PAT exchange returned no job token")
	}
	expiresAt = time.Now().Add(patDefaultTTL)
	if v := r.Get("expires_at").String(); v != "" {
		if t, perr := time.Parse(time.RFC3339, v); perr == nil {
			expiresAt = t
		}
	} else if secs := r.Get("expires_in").Int(); secs > 0 {
		expiresAt = time.Now().Add(time.Duration(secs) * time.Second)
	}
	return jobToken, expiresAt, nil
}

// fetchUserID resolves the Qoder userId for a job token via /userinfo. Returns
// "" on any failure.
func fetchUserID(ctx context.Context, client *http.Client, jobToken string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, QoderUserinfoURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+jobToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "qodercli/1.0.0")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if !gjson.ValidBytes(buf) {
		return ""
	}
	r := gjson.ParseBytes(buf)
	for _, k := range []string{"id", "userId", "user_id", "uid"} {
		if v := r.Get(k).String(); v != "" {
			return v
		}
	}
	return ""
}
