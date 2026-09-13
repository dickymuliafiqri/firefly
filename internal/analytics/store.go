package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxHistory = 200
	analyticsFilename = "analytics.json"
)

// ErrInvalidBreakerState is returned when setting an unrecognized circuit breaker state.
var ErrInvalidBreakerState = errors.New("invalid circuit breaker state: must be CLOSED, OPEN, or HALF-OPEN")

// Store provides thread-safe, persistent storage for request history, token metrics,
// and circuit breaker overrides. All operations accept context.Context.
type Store struct {
	configDir string
	filePath  string
	mu        sync.RWMutex
	history   []RequestLog
	summary   TokenLedger
	breakers  map[string]string
	dirty     bool
}

// NewStore initializes an analytics store backed by the specified configuration directory.
// If configDir is empty, the store operates purely in-memory.
func NewStore(configDir string) (*Store, error) {
	s := &Store{
		configDir: configDir,
		history:   make([]RequestLog, 0, defaultMaxHistory),
		breakers:  make(map[string]string),
	}

	if configDir == "" {
		return s, nil
	}

	s.filePath = filepath.Join(configDir, analyticsFilename)
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read analytics file: %w", err)
	}

	var state PersistedState
	if err := json.Unmarshal(data, &state); err != nil {
		// Log or ignore corrupted state file and start clean
		return s, nil
	}

	s.summary = state.Summary
	if state.Breakers != nil {
		s.breakers = state.Breakers
	}
	if state.History != nil {
		s.history = state.History
		if len(s.history) > defaultMaxHistory {
			s.history = s.history[:defaultMaxHistory]
		}
	}

	return s, nil
}

// Record appends or updates an inbound request log entry, recalculates cumulative
// token/cost metrics, and marks the state as dirty.
func (s *Store) Record(ctx context.Context, log RequestLog) error {
	if s == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Update existing in-flight log in place
	found := false
	prevStatus := 0
	for i := range s.history {
		if s.history[i].ID == log.ID {
			prevStatus = s.history[i].Status
			s.history[i] = log
			found = true
			break
		}
	}

	if !found {
		s.history = append([]RequestLog{log}, s.history...)
		if len(s.history) > defaultMaxHistory {
			s.history = s.history[:defaultMaxHistory]
		}
	}

	// Update cumulative ledger metrics upon completion (Status > 0) only if not already counted
	if log.Status > 0 && (!found || prevStatus == 0) {
		s.summary.TotalRequests++
		if log.Status >= 400 {
			s.summary.TotalErrors++
		}
		if log.TokensIn > 0 {
			s.summary.InputTokens += int64(log.TokensIn)
		}
		if log.TokensOut > 0 {
			s.summary.OutputTokens += int64(log.TokensOut)
		}
		totalTok := log.Tokens
		if totalTok <= 0 {
			totalTok = log.TokensIn + log.TokensOut
		}
		if totalTok > 0 {
			s.summary.TotalTokens += int64(totalTok)
		}
		if log.EstimatedCost > 0 {
			s.summary.EstimatedCostUSD += log.EstimatedCost
		}
	}

	s.dirty = true
	return nil
}

// History returns a copy of recent request logs (newest first) up to the specified limit.
func (s *Store) History(ctx context.Context, limit int) ([]RequestLog, error) {
	if s == nil {
		return nil, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.history) {
		limit = len(s.history)
	}

	out := make([]RequestLog, limit)
	copy(out, s.history[:limit])
	return out, nil
}

// ClearHistory resets the recent request log history while preserving cumulative totals.
func (s *Store) ClearHistory(ctx context.Context) error {
	if s == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.history = make([]RequestLog, 0, defaultMaxHistory)
	s.dirty = true
	return s.flushLocked(ctx)
}

// Summary returns the current cumulative token and billing summary.
func (s *Store) Summary(ctx context.Context) (TokenLedger, error) {
	if s == nil {
		return TokenLedger{}, nil
	}
	select {
	case <-ctx.Done():
		return TokenLedger{}, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.summary, nil
}

// SetBreakerOverride updates and persists the manual circuit breaker state for an upstream.
func (s *Store) SetBreakerOverride(ctx context.Context, upstream string, state string) error {
	if s == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	normalized := strings.ToUpper(strings.TrimSpace(state))
	switch normalized {
	case "CLOSED", "OPEN", "HALF-OPEN":
	default:
		return ErrInvalidBreakerState
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.breakers == nil {
		s.breakers = make(map[string]string)
	}
	s.breakers[upstream] = normalized
	s.dirty = true

	return s.flushLocked(ctx)
}

// GetBreakerOverrides returns a map of all persisted circuit breaker overrides.
func (s *Store) GetBreakerOverrides(ctx context.Context) (map[string]string, error) {
	if s == nil {
		return make(map[string]string), nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]string, len(s.breakers))
	for k, v := range s.breakers {
		out[k] = v
	}
	return out, nil
}

// Flush writes the current state to disk if modified.
func (s *Store) Flush(ctx context.Context) error {
	if s == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.flushLocked(ctx)
}

// flushLocked writes to disk atomically without releasing the lock.
func (s *Store) flushLocked(ctx context.Context) error {
	if s.configDir == "" || !s.dirty {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	state := PersistedState{
		Summary:   s.summary,
		Breakers:  s.breakers,
		History:   s.history,
		UpdatedAt: time.Now().UnixMilli(),
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal analytics state: %w", err)
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", s.filePath, os.Getpid())
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("write tmp analytics file: %w", err)
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("rename analytics file: %w", err)
	}

	s.dirty = false
	return nil
}

// Start launches a background worker that flushes dirty state periodically until ctx is cancelled.
func (s *Store) Start(ctx context.Context, flushInterval time.Duration) {
	if s == nil || s.configDir == "" {
		return
	}
	if flushInterval <= 0 {
		flushInterval = 5 * time.Second
	}

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = s.Flush(flushCtx)
			cancel()
			return
		case <-ticker.C:
			_ = s.Flush(ctx)
		}
	}
}
