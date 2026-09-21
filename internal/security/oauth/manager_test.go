package oauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
	"github.com/stretchr/testify/require"
)

type mockProvider struct {
	name      string
	flowType  domain.FlowType
	sensitive bool
}

func (m *mockProvider) Name() string              { return m.name }
func (m *mockProvider) FlowType() domain.FlowType { return m.flowType }
func (m *mockProvider) IsSensitive() bool         { return m.sensitive }

func (m *mockProvider) PrepareAuth(ctx context.Context, redirectURI string) (*ports.AuthSession, error) {
	return &ports.AuthSession{
		ID:          "sess-1",
		Provider:    m.name,
		State:       "state-xyz",
		RedirectURI: redirectURI,
		AuthURL:     "https://auth.example.com?state=state-xyz",
		CreatedAt:   time.Now(),
	}, nil
}

func (m *mockProvider) ExchangeCode(ctx context.Context, code string, session *ports.AuthSession) (*domain.OAuthConnection, error) {
	if code != "valid-code" {
		return nil, errors.New("invalid code")
	}
	return &domain.OAuthConnection{
		ID:       m.name + "-test-user",
		Provider: m.name,
		Email:    "test@example.com",
		Token: domain.OAuthToken{
			AccessToken:  "initial-access-token",
			RefreshToken: "refresh-token-123",
			ExpiresAt:    time.Now().Add(10 * time.Minute),
		},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

func (m *mockProvider) RefreshToken(ctx context.Context, conn *domain.OAuthConnection) (*domain.OAuthToken, error) {
	return &domain.OAuthToken{
		AccessToken:  "new-access-token",
		RefreshToken: conn.Token.RefreshToken,
		ExpiresAt:    time.Now().Add(1 * time.Hour),
	}, nil
}

func TestManager_FlowAndTokenSource(t *testing.T) {
	store, _ := NewStore("")
	mgr := NewManager(store)

	mock := &mockProvider{name: "test-prov", flowType: domain.FlowTypeStandardAuthCode}
	if err := mgr.RegisterProvider(mock); err != nil {
		t.Fatalf("RegisterProvider error: %v", err)
	}

	ctx := context.Background()

	// 1. PrepareAuth
	session, err := mgr.PrepareAuth(ctx, "test-prov", "http://localhost/cb")
	if err != nil {
		t.Fatalf("PrepareAuth error: %v", err)
	}
	if session.State != "state-xyz" {
		t.Fatalf("unexpected state: %s", session.State)
	}

	// 2. ExchangeCallback
	conn, err := mgr.ExchangeCallback(ctx, "state-xyz", "valid-code")
	if err != nil {
		t.Fatalf("ExchangeCallback error: %v", err)
	}
	if conn.ID != "test-prov-test-user" || conn.Token.AccessToken != "initial-access-token" {
		t.Fatalf("unexpected connection: %+v", conn)
	}

	// Double exchange on used state should fail
	if _, err := mgr.ExchangeCallback(ctx, "state-xyz", "valid-code"); err == nil {
		t.Fatal("expected error on reused state, got nil")
	}

	// 3. TokenSource
	ts := mgr.TokenSource(conn.ID)
	tok, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("TokenSource error: %v", err)
	}
	if tok != "initial-access-token" {
		t.Fatalf("unexpected token: %s", tok)
	}

	// 4. Force expired and check TokenSource transparent refresh
	conn.Token.ExpiresAt = time.Now().Add(2 * time.Minute) // within 5 minute lead
	_ = store.Save(ctx, conn)

	refreshedTok, err := ts.Token(ctx)
	if err != nil {
		t.Fatalf("TokenSource refresh error: %v", err)
	}
	if refreshedTok != "new-access-token" {
		t.Fatalf("expected refreshed token, got: %s", refreshedTok)
	}
}

// blockingProvider blocks inside RefreshToken until the test releases it, so the
// test can act while a shared refresh flight is in progress.
type blockingProvider struct {
	name    string
	entered chan struct{}
	release chan struct{}
	ctxLost chan struct{}
}

func (p *blockingProvider) Name() string              { return p.name }
func (p *blockingProvider) FlowType() domain.FlowType { return domain.FlowTypeStandardAuthCode }
func (p *blockingProvider) IsSensitive() bool         { return false }
func (p *blockingProvider) PrepareAuth(context.Context, string) (*ports.AuthSession, error) {
	return nil, nil
}

func (p *blockingProvider) ExchangeCode(context.Context, string, *ports.AuthSession) (*domain.OAuthConnection, error) {
	return nil, nil
}

func (p *blockingProvider) RefreshToken(ctx context.Context, conn *domain.OAuthConnection) (*domain.OAuthToken, error) {
	close(p.entered)
	select {
	case <-p.release:
	case <-ctx.Done():
		close(p.ctxLost)
		return nil, ctx.Err()
	}
	return &domain.OAuthToken{
		AccessToken:  "rotated-access-token",
		RefreshToken: conn.Token.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Hour),
	}, nil
}

// A refresh is shared work: the provider has already rotated the token by the
// time a caller disappears, so a caller's cancellation must not abort the flight
// (the new refresh token would be lost, breaking the connection for good) nor
// discard the store write.
func TestManager_RefreshTokenSurvivesCallerCancel(t *testing.T) {
	store, _ := NewStore("")
	mgr := NewManager(store)

	p := &blockingProvider{
		name:    "blocking-prov",
		entered: make(chan struct{}),
		release: make(chan struct{}),
		ctxLost: make(chan struct{}),
	}
	require.NoError(t, mgr.RegisterProvider(p))
	require.NoError(t, store.Save(context.Background(), &domain.OAuthConnection{
		ID:       "blocking-user",
		Provider: p.name,
		Token: domain.OAuthToken{
			AccessToken:  "stale-access-token",
			RefreshToken: "refresh-token-1",
			ExpiresAt:    time.Now().Add(-time.Hour),
		},
	}))

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := mgr.RefreshToken(ctx, "blocking-user")
		first <- err
	}()
	<-p.entered

	// The caller that started the flight walks away...
	cancel()

	// ...and another caller that gives up stops waiting without disturbing it.
	abandoned, abandon := context.WithCancel(context.Background())
	abandon()
	if _, err := mgr.RefreshToken(abandoned, "blocking-user"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a waiter with a canceled context must return promptly, got %v", err)
	}

	select {
	case <-p.ctxLost:
		t.Fatal("the shared refresh was canceled by a caller that walked away")
	case <-time.After(100 * time.Millisecond):
	}

	close(p.release)

	// The flight finishes on its own and the rotated token is persisted, so the
	// next request gets a working connection.
	require.Eventually(t, func() bool {
		conn, err := store.Get(context.Background(), "blocking-user")
		return err == nil && conn.Token.AccessToken == "rotated-access-token"
	}, 2*time.Second, 5*time.Millisecond, "the rotated token must be persisted")

	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("the canceled caller must observe its own cancellation, got %v", err)
	}
}

type mockDeviceProvider struct {
	mockProvider
	pollAttempts int
}

func (m *mockDeviceProvider) PollToken(ctx context.Context, session *ports.AuthSession) (*domain.OAuthConnection, bool, error) {
	m.pollAttempts++
	if m.pollAttempts == 1 {
		return nil, true, nil
	}
	return &domain.OAuthConnection{
		ID:       "poll-user-1",
		Provider: m.name,
		Token: domain.OAuthToken{
			AccessToken: "polled-token",
			ExpiresAt:   time.Now().Add(1 * time.Hour),
		},
	}, false, nil
}

func TestManager_PollAuth(t *testing.T) {
	store, _ := NewStore("")
	mgr := NewManager(store)

	devMock := &mockDeviceProvider{mockProvider: mockProvider{name: "dev-prov", flowType: domain.FlowTypeDeviceCode}}
	if err := mgr.RegisterProvider(devMock); err != nil {
		t.Fatalf("RegisterProvider error: %v", err)
	}

	ctx := context.Background()
	session, err := mgr.PrepareAuth(ctx, "dev-prov", "")
	if err != nil {
		t.Fatalf("PrepareAuth error: %v", err)
	}

	// 1st poll: pending
	conn, pending, err := mgr.PollAuth(ctx, session.State)
	if err != nil {
		t.Fatalf("PollAuth error: %v", err)
	}
	if !pending || conn != nil {
		t.Fatalf("expected pending=true, conn=nil; got pending=%v, conn=%v", pending, conn)
	}

	// 2nd poll: success
	conn, pending, err = mgr.PollAuth(ctx, session.State)
	if err != nil {
		t.Fatalf("PollAuth error: %v", err)
	}
	if pending || conn == nil || conn.Token.AccessToken != "polled-token" {
		t.Fatalf("expected pending=false and connection, got pending=%v, conn=%v", pending, conn)
	}

	// 3rd poll: expired/already removed
	_, _, err = mgr.PollAuth(ctx, session.State)
	if err == nil {
		t.Fatal("expected error on already consumed session state, got nil")
	}
}

func TestManager_StatelessCallback_And_Polling(t *testing.T) {
	store, _ := NewStore("")
	mgr := NewManager(store)

	mock := &mockProvider{name: "cline", flowType: domain.FlowTypeStandardAuthCode}
	if err := mgr.RegisterProvider(mock); err != nil {
		t.Fatalf("RegisterProvider error: %v", err)
	}

	ctx := context.Background()

	// 1. PrepareAuth creates a session for "cline"
	session, err := mgr.PrepareAuth(ctx, "cline", "http://localhost:8080/cb")
	if err != nil {
		t.Fatalf("PrepareAuth error: %v", err)
	}

	// 2. PollAuth before callback returns pending=true (even though cline is not a device provider)
	conn, pending, err := mgr.PollAuth(ctx, session.State)
	if err != nil {
		t.Fatalf("PollAuth error: %v", err)
	}
	if !pending || conn != nil {
		t.Fatalf("expected pending=true, conn=nil; got pending=%v, conn=%v", pending, conn)
	}

	// 3. Callback arrives with empty state (Cline's callback behavior)
	conn, err = mgr.ExchangeCallback(ctx, "", "valid-code")
	if err != nil {
		t.Fatalf("ExchangeCallback without state error: %v", err)
	}
	if conn == nil || conn.ID != "cline-test-user" {
		t.Fatalf("unexpected connection: %+v", conn)
	}

	// 4. PollAuth now picks up the completed connection
	conn, pending, err = mgr.PollAuth(ctx, session.State)
	if err != nil {
		t.Fatalf("PollAuth after callback error: %v", err)
	}
	if pending || conn == nil || conn.ID != "cline-test-user" {
		t.Fatalf("expected pending=false and connection, got pending=%v, conn=%v", pending, conn)
	}

	// 5. Subsequent poll fails because session was removed
	_, _, err = mgr.PollAuth(ctx, session.State)
	if err == nil {
		t.Fatal("expected error on consumed session, got nil")
	}
}

func TestManager_ResolveConnection(t *testing.T) {
	store, _ := NewStore("")
	mgr := NewManager(store)
	ctx := context.Background()

	conn := &domain.OAuthConnection{
		ID:       "ag-test-conn",
		Provider: "antigravity",
		Token: domain.OAuthToken{
			AccessToken: "ya29.sample-token",
		},
		ProviderSpecificData: map[string]string{
			"project_id": "google-companion-123",
		},
	}
	if err := store.Save(ctx, conn); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// 1. Resolve with oauth: prefix
	resolved, err := mgr.ResolveConnection(ctx, "oauth:ag-test-conn")
	if err != nil {
		t.Fatalf("ResolveConnection error: %v", err)
	}
	if resolved.ID != "ag-test-conn" || resolved.ProviderSpecificData["project_id"] != "google-companion-123" {
		t.Fatalf("unexpected connection: %+v", resolved)
	}

	// 2. Resolve without prefix
	resolved2, err := mgr.ResolveConnection(ctx, "ag-test-conn")
	if err != nil {
		t.Fatalf("ResolveConnection without prefix error: %v", err)
	}
	if resolved2.ID != "ag-test-conn" {
		t.Fatalf("unexpected connection: %+v", resolved2)
	}
}
