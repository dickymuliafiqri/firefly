package oauth

import (
	"context"
	"crypto/rand"
	"log/slog"
	"math/big"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

const (
	// DefaultRefreshInterval is how often the background refresh worker checks for expiring tokens.
	DefaultRefreshInterval = 5 * time.Minute

	// DefaultRefreshLeadTime triggers a refresh if the token expires within this window.
	DefaultRefreshLeadTime = 30 * time.Minute

	// DefaultSensitiveDelay is the sequential pause between refreshing sensitive accounts (e.g. Google Cloud).
	DefaultSensitiveDelay = 12 * time.Second

	// DefaultNormalDelay is the sequential pause between refreshing standard accounts.
	DefaultNormalDelay = 1500 * time.Millisecond
)

// RefresherConfig tunes the proactive background token refresher.
type RefresherConfig struct {
	Interval       time.Duration
	LeadTime       time.Duration
	SensitiveDelay time.Duration
	NormalDelay    time.Duration
	Logger         *slog.Logger
}

// Refresher periodically scans OAuth connections and proactively rotates access tokens
// before they expire, enforcing sequential anti-abuse rate limits for sensitive providers.
type Refresher struct {
	manager *Manager
	cfg     RefresherConfig
	logger  *slog.Logger
}

// NewRefresher constructs a Refresher instance.
func NewRefresher(manager *Manager, cfg RefresherConfig) *Refresher {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultRefreshInterval
	}
	if cfg.LeadTime <= 0 {
		cfg.LeadTime = DefaultRefreshLeadTime
	}
	if cfg.SensitiveDelay <= 0 {
		cfg.SensitiveDelay = DefaultSensitiveDelay
	}
	if cfg.NormalDelay <= 0 {
		cfg.NormalDelay = DefaultNormalDelay
	}
	l := cfg.Logger
	if l == nil {
		l = slog.Default()
	}

	return &Refresher{
		manager: manager,
		cfg:     cfg,
		logger:  l,
	}
}

// Run blocks and executes the periodic refresh loop until ctx is canceled.
func (r *Refresher) Run(ctx context.Context) {
	// Initial delay so startup completes cleanly before first sweep
	initTimer := time.NewTimer(5 * time.Second)
	select {
	case <-ctx.Done():
		initTimer.Stop()
		return
	case <-initTimer.C:
		r.Tick(ctx)
	}

	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Tick(ctx)
		}
	}
}

// Tick executes a single refresh sweep across all connected accounts.
func (r *Refresher) Tick(ctx context.Context) {
	if r.manager == nil || r.manager.store == nil {
		return
	}

	connections, err := r.manager.store.List(ctx)
	if err != nil {
		r.logger.Warn("background token refresh list failed", "err", err)
		return
	}

	// Filter connections needing proactive refresh
	var due []*domain.OAuthConnection
	for _, c := range connections {
		if c == nil || c.Token.RefreshToken == "" {
			continue
		}
		if c.Token.IsExpired(r.cfg.LeadTime) {
			due = append(due, c)
		}
	}

	if len(due) == 0 {
		return
	}

	r.logger.Info("proactive token refresh sweep started", "due_count", len(due))

	for i, conn := range due {
		if err := ctx.Err(); err != nil {
			return
		}

		isSensitive := false
		if p, ok := r.manager.GetProvider(conn.Provider); ok && p != nil {
			isSensitive = p.IsSensitive()
		}

		if _, err := r.manager.RefreshToken(ctx, conn.ID); err != nil {
			r.logger.Warn("proactive refresh failed for connection",
				"connection_id", conn.ID,
				"provider", conn.Provider,
				"err", err,
			)
		} else {
			r.logger.Info("proactive refresh succeeded",
				"connection_id", conn.ID,
				"provider", conn.Provider,
			)
		}

		// Apply sequential delay between accounts to protect against upstream anti-abuse triggers
		if i < len(due)-1 {
			delay := r.cfg.NormalDelay
			if isSensitive {
				jitterMs := int64(0)
				if n, err := rand.Int(rand.Reader, big.NewInt(4000)); err == nil {
					jitterMs = n.Int64()
				}
				delay = r.cfg.SensitiveDelay + time.Duration(jitterMs)*time.Millisecond
			}

			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
