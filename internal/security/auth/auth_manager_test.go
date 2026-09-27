package auth

import (
	"context"
	"sync"
	"testing"
)

func TestAuthManager_DefaultPassword(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr := NewManager(dir, "", "12345678")

	// Authenticate with default password
	sess, err := mgr.Authenticate(ctx, "12345678")
	if err != nil {
		t.Fatalf("Authenticate failed with default password: %v", err)
	}
	if sess.Token == "" {
		t.Fatal("expected non-empty token")
	}

	// Validate token
	if !mgr.ValidateToken(ctx, sess.Token) {
		t.Fatal("expected token to be valid")
	}

	// Wrong password fails
	if _, err := mgr.Authenticate(ctx, "wrong-password"); err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestAuthManager_AdminTokenBypass(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	adminToken := "secret-admin-token"
	mgr := NewManager(dir, adminToken, "12345678")

	// Static admin token validates directly
	if !mgr.ValidateToken(ctx, adminToken) {
		t.Fatal("expected admin token to validate directly")
	}

	// Admin token also acts as valid master password for login
	sess, err := mgr.Authenticate(ctx, adminToken)
	if err != nil {
		t.Fatalf("expected admin token to allow login, got: %v", err)
	}
	if !mgr.ValidateToken(ctx, sess.Token) {
		t.Fatal("expected issued session token to be valid")
	}
}

// TestAuthManager_RevokeSession covers the sign-out contract: stateless tokens
// carry no server-side record, so signing out retires the whole credential
// generation instead of one entry. Every token issued before the call stops
// validating — including a second browser — and the retirement is persisted, so
// a process that starts later honors it too.
func TestAuthManager_RevokeSession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr := NewManager(dir, "", "12345678")

	sess, err := mgr.Authenticate(ctx, "12345678")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	other, err := mgr.Authenticate(ctx, "12345678")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}

	if !mgr.ValidateToken(ctx, sess.Token) {
		t.Fatal("token should be valid")
	}

	// A token that was never issued must not be able to force a write.
	mgr.RevokeSession(ctx, "ff_sess_not-a-real-token")
	if !mgr.ValidateToken(ctx, sess.Token) {
		t.Fatal("revoking an unknown token must not retire a live session")
	}

	mgr.RevokeSession(ctx, sess.Token)

	if mgr.ValidateToken(ctx, sess.Token) {
		t.Fatal("token should be invalid after revocation")
	}
	if mgr.ValidateToken(ctx, other.Token) {
		t.Fatal("sign-out retires the generation, so a second session is invalidated too")
	}

	// The retirement survives a restart: a process that reads auth.json now
	// refuses the retired token.
	restarted := NewManager(dir, "", "")
	if restarted.ValidateToken(ctx, sess.Token) {
		t.Fatal("a retired token must stay retired across restarts")
	}
}

func TestAuthManager_UpdatePassword(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr := NewManager(dir, "", "12345678")

	// Active session before change
	sess1, err := mgr.Authenticate(ctx, "12345678")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}

	// Bad old password fails
	if err := mgr.UpdatePassword(ctx, "wrong-old", "newpassword123"); err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}

	// Too short new password fails
	if err := mgr.UpdatePassword(ctx, "12345678", "12"); err != ErrPasswordTooShort {
		t.Fatalf("expected ErrPasswordTooShort, got %v", err)
	}

	// Successful change
	if err := mgr.UpdatePassword(ctx, "12345678", "newpassword123"); err != nil {
		t.Fatalf("UpdatePassword failed: %v", err)
	}

	// Old session should be revoked
	if mgr.ValidateToken(ctx, sess1.Token) {
		t.Fatal("old session should be revoked after password change")
	}

	// Old password cannot authenticate
	if _, err := mgr.Authenticate(ctx, "12345678"); err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials for old password, got %v", err)
	}

	// New password authenticates
	sess2, err := mgr.Authenticate(ctx, "newpassword123")
	if err != nil {
		t.Fatalf("Authenticate with new password failed: %v", err)
	}
	if !mgr.ValidateToken(ctx, sess2.Token) {
		t.Fatal("new token should be valid")
	}

	// Persistence reload check
	reloadedMgr := NewManager(dir, "", "")
	if _, err := reloadedMgr.Authenticate(ctx, "newpassword123"); err != nil {
		t.Fatalf("reloaded manager failed to authenticate with updated password: %v", err)
	}
}

func TestAuthManager_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, "", "12345678")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled context

	if _, err := mgr.Authenticate(ctx, "12345678"); err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	if mgr.ValidateToken(ctx, "any-token") {
		t.Fatal("canceled context should return false on ValidateToken")
	}

	if err := mgr.UpdatePassword(ctx, "12345678", "newpass"); err != context.Canceled {
		t.Fatalf("expected context.Canceled on UpdatePassword, got %v", err)
	}
}

// TestAuthManager_ConcurrentAccess exercises the manager under contention. It
// deliberately does not call RevokeSession: with stateless sessions a sign-out
// retires the credential generation for everyone, so a concurrent revocation
// would (correctly) invalidate the other goroutines' tokens mid-loop and make
// this test assert the wrong thing. Revocation semantics are covered by
// TestAuthManager_RevokeSession.
func TestAuthManager_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr := NewManager(dir, "", "12345678")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				sess, err := mgr.Authenticate(ctx, "12345678")
				if err != nil {
					t.Errorf("concurrent auth error: %v", err)
					return
				}
				if !mgr.ValidateToken(ctx, sess.Token) {
					t.Errorf("concurrent token validation failed")
					return
				}
			}
		}()
	}
	wg.Wait()
}
