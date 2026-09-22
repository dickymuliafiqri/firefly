package oauth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRefresherProvider struct {
	name        string
	flow        domain.FlowType
	sensitive   bool
	refreshedCh chan string
}

func (m *mockRefresherProvider) Name() string              { return m.name }
func (m *mockRefresherProvider) FlowType() domain.FlowType { return m.flow }
func (m *mockRefresherProvider) IsSensitive() bool         { return m.sensitive }

func (m *mockRefresherProvider) PrepareAuth(ctx context.Context, redirectURI string) (*ports.AuthSession, error) {
	return nil, nil
}

func (m *mockRefresherProvider) ExchangeCode(ctx context.Context, code string, session *ports.AuthSession) (*domain.OAuthConnection, error) {
	return nil, nil
}

func (m *mockRefresherProvider) RefreshToken(ctx context.Context, conn *domain.OAuthConnection) (*domain.OAuthToken, error) {
	if m.refreshedCh != nil {
		m.refreshedCh <- conn.Token.RefreshToken
	}
	return &domain.OAuthToken{
		AccessToken:  "new-access-token",
		RefreshToken: conn.Token.RefreshToken,
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}, nil
}

func TestNewRefresher_Defaults(t *testing.T) {
	t.Parallel()
	r := NewRefresher(nil, RefresherConfig{})
	require.NotNil(t, r)
	assert.Equal(t, DefaultRefreshInterval, r.cfg.Interval)
	assert.Equal(t, DefaultRefreshLeadTime, r.cfg.LeadTime)
	assert.Equal(t, DefaultSensitiveDelay, r.cfg.SensitiveDelay)
	assert.Equal(t, DefaultNormalDelay, r.cfg.NormalDelay)
}

func TestRefresher_Tick(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "oauth.json")

	store, err := NewStore(storePath)
	require.NoError(t, err)

	refreshedCh := make(chan string, 10)
	provider := &mockRefresherProvider{
		name:        "mock-prov",
		flow:        domain.FlowTypeAuthCodePKCE,
		sensitive:   false,
		refreshedCh: refreshedCh,
	}

	mgr := NewManager(store)
	require.NoError(t, mgr.RegisterProvider(provider))

	// Connection 1: Expired (needs refresh)
	conn1 := &domain.OAuthConnection{
		ID:       "conn-1",
		Provider: "mock-prov",
		Token: domain.OAuthToken{
			AccessToken:  "old-token-1",
			RefreshToken: "refresh-token-1",
			ExpiresAt:    time.Now().Add(-10 * time.Minute),
		},
	}
	require.NoError(t, store.Save(context.Background(), conn1))

	// Connection 2: Not expired, outside lead time (skip refresh)
	conn2 := &domain.OAuthConnection{
		ID:       "conn-2",
		Provider: "mock-prov",
		Token: domain.OAuthToken{
			AccessToken:  "valid-token-2",
			RefreshToken: "refresh-token-2",
			ExpiresAt:    time.Now().Add(2 * time.Hour),
		},
	}
	require.NoError(t, store.Save(context.Background(), conn2))

	// Connection 3: Expired without refresh token (skip refresh)
	conn3 := &domain.OAuthConnection{
		ID:       "conn-3",
		Provider: "mock-prov",
		Token: domain.OAuthToken{
			AccessToken: "valid-token-3",
			ExpiresAt:   time.Now().Add(-10 * time.Minute),
		},
	}
	require.NoError(t, store.Save(context.Background(), conn3))

	refresher := NewRefresher(mgr, RefresherConfig{
		LeadTime:       30 * time.Minute,
		NormalDelay:    5 * time.Millisecond,
		SensitiveDelay: 10 * time.Millisecond,
	})

	ctx := context.Background()
	refresher.Tick(ctx)

	// Verify only conn-1 was refreshed
	select {
	case tok := <-refreshedCh:
		assert.Equal(t, "refresh-token-1", tok)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for refresh")
	}

	// Ensure no extra refresh was called
	select {
	case tok := <-refreshedCh:
		t.Fatalf("unexpected refresh called for: %s", tok)
	default:
	}

	// Verify updated token in store
	updated, err := store.Get(ctx, "conn-1")
	require.NoError(t, err)
	assert.Equal(t, "new-access-token", updated.Token.AccessToken)
}

func TestRefresher_Tick_ContextCancellation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "oauth.json")

	store, err := NewStore(storePath)
	require.NoError(t, err)

	refreshedCh := make(chan string, 10)
	provider := &mockRefresherProvider{
		name:        "sensitive-prov",
		flow:        domain.FlowTypeAuthCodePKCE,
		sensitive:   true,
		refreshedCh: refreshedCh,
	}

	mgr := NewManager(store)
	require.NoError(t, mgr.RegisterProvider(provider))

	// 2 expiring connections
	for i := 1; i <= 2; i++ {
		conn := &domain.OAuthConnection{
			ID:       "conn-" + string(rune('0'+i)),
			Provider: "sensitive-prov",
			Token: domain.OAuthToken{
				AccessToken:  "old-token",
				RefreshToken: "refresh-" + string(rune('0'+i)),
				ExpiresAt:    time.Now().Add(-5 * time.Minute),
			},
		}
		require.NoError(t, store.Save(context.Background(), conn))
	}

	// Long delay, cancel context right after start
	refresher := NewRefresher(mgr, RefresherConfig{
		LeadTime:       30 * time.Minute,
		SensitiveDelay: 5 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Wait for first token to be processed, then cancel context during delay
		<-refreshedCh
		cancel()
	}()

	done := make(chan struct{})
	go func() {
		refresher.Tick(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Succeeded in aborting promptly
	case <-time.After(2 * time.Second):
		t.Fatal("refresher.Tick did not abort promptly on context cancellation")
	}

	// The sweep aborted, but the refresh it had already started is shared work: it
	// finishes on its own so the rotated token is not lost. Wait for that write to
	// land, otherwise the detached flight would race the t.TempDir cleanup. Get
	// blocks on the store mutex Save holds across its write, so a successful read
	// of the new token means the file is on disk.
	require.Eventually(t, func() bool {
		conn, err := store.Get(context.Background(), "conn-1")
		return err == nil && conn.Token.AccessToken == "new-access-token"
	}, 2*time.Second, 5*time.Millisecond, "the shared refresh must persist the rotated token")
}

func TestRefresher_Run_Shutdown(t *testing.T) {
	t.Parallel()
	r := NewRefresher(nil, RefresherConfig{
		Interval: 10 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Exited cleanly
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not exit on canceled context")
	}
}
