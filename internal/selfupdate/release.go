package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultRepo is the upstream the dashboard checks. It is a constant rather
	// than a setting on purpose: an operator-editable update source would let
	// anyone who can reach the settings API choose which binary the gateway
	// downloads and executes.
	DefaultRepo = "dickymuliafiqri/firefly"

	// DefaultTTL bounds how often the Releases API is polled. The endpoint is
	// unauthenticated for a self-hosted gateway, and GitHub allows 60 calls per
	// hour per IP; fifteen minutes keeps a single instance comfortably inside
	// that even with several dashboard tabs open. The version endpoint shares
	// this cache, so polling the dashboard does not multiply API traffic.
	DefaultTTL = 15 * time.Minute

	// defaultAPIBase is GitHub's public API. Overridable only through
	// WithAPIBase, which the server never wires to configuration.
	defaultAPIBase = "https://api.github.com"

	// maxReleaseBody bounds how much of the API response is decoded, so a
	// hostile or unexpected response cannot exhaust memory.
	maxReleaseBody = 1 << 20

	userAgent = "firefly-selfupdate"
)

// ErrStaleRelease reports that a lookup failed and the release returned with it
// is the previously cached one, not a fresh answer from GitHub. It is wrapped
// into the error so a caller can show the stale version while still telling the
// operator that it could not be refreshed.
var ErrStaleRelease = errors.New("latest lookup failed; showing the cached release")

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// Release is the subset of the GitHub Releases payload the dashboard needs.
// The tag is read verbatim from Tag: the release pipeline tags with a "v"
// prefix, which ParseVersion understands.
type Release struct {
	Tag        string    `json:"tag_name"`
	Name       string    `json:"name"`
	Notes      string    `json:"body"`
	HTMLURL    string    `json:"html_url"`
	Prerelease bool      `json:"prerelease"`
	Draft      bool      `json:"draft"`
	Published  time.Time `json:"published_at"`
	Assets     []Asset   `json:"assets"`
}

// Asset looks an asset up by exact filename.
func (r *Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Client reads the newest release from GitHub and caches it.
//
// It is safe for concurrent use: the dashboard polls from several endpoints and
// the cache must collapse those into one upstream call.
type Client struct {
	http    *http.Client
	repo    string
	apiBase string
	ttl     time.Duration
	token   string
	logger  *slog.Logger
	now     func() time.Time

	// restrictAssetHosts pins downloads to GitHub's asset hosts. It is turned off
	// by WithAPIBase, because a client aimed at a different API server is by
	// definition serving its assets from somewhere else.
	restrictAssetHosts bool

	mu       sync.Mutex
	cached   *Release
	cachedAt time.Time
	etag     string
}

// Option customises a Client at construction.
type Option func(*Client)

// WithHTTPClient supplies the HTTP client used for API calls.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) {
		if c != nil {
			cl.http = c
		}
	}
}

// WithRepo overrides the upstream repository ("owner/name"). Exported for forks
// and tests; the server always uses DefaultRepo.
func WithRepo(repo string) Option {
	return func(cl *Client) {
		if strings.TrimSpace(repo) != "" {
			cl.repo = strings.TrimSpace(repo)
		}
	}
}

// WithAPIBase overrides the API host, for GitHub Enterprise and tests. This is
// the one knob that decides where a binary comes from, so the server must not
// expose it to operator input. Setting it also relaxes the asset-host allowlist,
// since a different API serves assets from a different host.
func WithAPIBase(base string) Option {
	return func(cl *Client) {
		if strings.TrimSpace(base) != "" {
			cl.apiBase = strings.TrimRight(strings.TrimSpace(base), "/")
			cl.restrictAssetHosts = false
		}
	}
}

// WithTTL overrides how long a fetched release is reused.
func WithTTL(d time.Duration) Option {
	return func(cl *Client) {
		if d > 0 {
			cl.ttl = d
		}
	}
}

// WithToken sets a GitHub token used for API calls. Optional: it raises the
// unauthenticated rate limit when an operator supplies one, and is never
// required for the public repository.
func WithToken(token string) Option {
	return func(cl *Client) { cl.token = token }
}

// WithLogger attaches a logger for background-cache decisions.
func WithLogger(l *slog.Logger) Option {
	return func(cl *Client) { cl.logger = l }
}

// WithClock replaces the time source, for tests that exercise cache expiry.
func WithClock(now func() time.Time) Option {
	return func(cl *Client) {
		if now != nil {
			cl.now = now
		}
	}
}

// NewClient builds a release client with DefaultRepo, DefaultTTL, and a
// 15-second per-request timeout.
func NewClient(opts ...Option) *Client {
	c := &Client{
		http:               &http.Client{Timeout: 15 * time.Second},
		repo:               DefaultRepo,
		apiBase:            defaultAPIBase,
		ttl:                DefaultTTL,
		now:                time.Now,
		restrictAssetHosts: true,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Repo returns the configured "owner/name".
func (c *Client) Repo() string { return c.repo }

// Latest returns the newest published release, reusing a cached copy while it is
// younger than the TTL.
//
// A conditional request is sent once the TTL lapses, so an unchanged release
// costs GitHub one 304 and no body. If the API fails while a previous release is
// still known, that stale copy is returned instead of the error: a dashboard
// that shows a slightly old version beats one that shows "rate limit exceeded"
// for an hour, and the caller learns about the degradation through the error
// being nil while the release is old.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	c.mu.Lock()
	if c.cached != nil && c.now().Sub(c.cachedAt) < c.ttl {
		rel := c.cached
		c.mu.Unlock()
		return rel, nil
	}
	etag := c.etag
	stale := c.cached
	c.mu.Unlock()

	rel, newETag, err := c.fetchLatest(ctx, etag)
	if err != nil {
		if stale != nil {
			if c.logger != nil {
				c.logger.Warn("using cached release after a failed update check", "err", err, "tag", stale.Tag)
			}
			// The stale release is still useful — a rate-limited API must not
			// blank the dashboard — but the failure is reported alongside it so
			// the caller can say the version it shows is not fresh.
			return stale, fmt.Errorf("%w: %v", ErrStaleRelease, err)
		}
		return nil, err
	}
	if rel == nil {
		// 304 Not Modified: the cached copy is current.
		c.mu.Lock()
		c.cachedAt = c.now()
		rel = c.cached
		c.mu.Unlock()
		if rel == nil {
			return nil, fmt.Errorf("github returned not-modified with no cached release")
		}
		return rel, nil
	}

	c.mu.Lock()
	c.cached = rel
	c.cachedAt = c.now()
	if newETag != "" {
		c.etag = newETag
	}
	c.mu.Unlock()
	return rel, nil
}

// fetchLatest performs one conditional request. It returns (nil, etag, nil) for
// a 304, in which case the caller keeps its cached copy.
func (c *Client) fetchLatest(ctx context.Context, etag string) (*Release, string, error) {
	url := c.apiBase + "/repos/" + c.repo + "/releases/latest"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("contact github releases: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, "", nil
	case http.StatusOK:
		// decoded below
	case http.StatusNotFound:
		return nil, "", fmt.Errorf("repository %s has no published releases", c.repo)
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, "", rateLimitError(resp)
	default:
		return nil, "", fmt.Errorf("github releases returned %s", resp.Status)
	}

	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseBody)).Decode(&rel); err != nil {
		return nil, "", fmt.Errorf("decode release: %w", err)
	}
	if rel.Tag == "" {
		return nil, "", fmt.Errorf("github release has no tag_name")
	}
	return &rel, resp.Header.Get("ETag"), nil
}

// rateLimitError turns a 403/429 into something an operator can act on. GitHub
// reports an exhausted anonymous quota as 403 with the reset timestamp in a
// header, which is otherwise indistinguishable from a permissions problem.
func rateLimitError(resp *http.Response) error {
	if resp.Header.Get("X-RateLimit-Remaining") != "0" {
		return fmt.Errorf("github releases returned %s", resp.Status)
	}
	msg := "github api rate limit exceeded"
	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		if secs, err := strconv.ParseInt(reset, 10, 64); err == nil {
			msg += " (resets at " + time.Unix(secs, 0).UTC().Format(time.RFC3339) + ")"
		}
	}
	return fmt.Errorf("%s", msg)
}
