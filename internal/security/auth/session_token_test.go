package auth

import (
	"context"
	"strings"
	"testing"
	"time"
)

// testSessionSecret is a fixed signing secret for tests that need several
// processes to agree on a key.
const testSessionSecret = "5f2a9c1d4b7e8036af15c9d2e6b04713"

// TestSessionToken_ValidAcrossInstances is the regression test for the
// production bug where the dashboard was signed out a few seconds after login.
//
// The session used to live in the issuing process's memory, so the dashboard's
// 2-second /api/telemetry poll routinely landed on a process that had never seen
// the token and answered 401. Two managers sharing one config directory model
// two replicas of the same gateway: the second must honor the first one's token
// without any shared session record.
func TestSessionToken_ValidAcrossInstances(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	issuer := NewManager(dir, "", "12345678")
	sess, err := issuer.Authenticate(ctx, "12345678")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}

	replica := NewManager(dir, "", "")
	if !replica.ValidateToken(ctx, sess.Token) {
		t.Fatal("a session issued by one instance must validate on another instance")
	}
}

// TestSessionToken_SurvivesColdStartWithEnvSecret models a host whose filesystem
// does not persist (Vercel, Railway without a volume): every process starts with
// an empty config directory, so only $FIREFLY_SESSION_SECRET can keep the signing
// key identical. The token issued before the restart must still validate.
func TestSessionToken_SurvivesColdStartWithEnvSecret(t *testing.T) {
	t.Setenv(SessionSecretEnvVar, testSessionSecret)

	ctx := context.Background()
	before := NewManager(t.TempDir(), "", "12345678")
	sess, err := before.Authenticate(ctx, "12345678")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}

	// Brand-new process, brand-new empty directory, same exported secret.
	after := NewManager(t.TempDir(), "", "12345678")
	if !after.ValidateToken(ctx, sess.Token) {
		t.Fatal("a session must survive a cold start when the signing secret comes from the environment")
	}
}

// TestSessionToken_RotatedSecretRetiresSessions documents the remedy for a
// deployment that cannot share state: replacing the signing secret invalidates
// every outstanding session, on every instance, at the next start.
func TestSessionToken_RotatedSecretRetiresSessions(t *testing.T) {
	ctx := context.Background()

	t.Setenv(SessionSecretEnvVar, testSessionSecret)
	issuer := NewManager(t.TempDir(), "", "12345678")
	sess, err := issuer.Authenticate(ctx, "12345678")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}

	t.Setenv(SessionSecretEnvVar, "a-completely-different-secret-value-0f9c2e")
	rotated := NewManager(t.TempDir(), "", "12345678")
	if rotated.ValidateToken(ctx, sess.Token) {
		t.Fatal("rotating the signing secret must invalidate sessions signed with the old one")
	}
}

// TestSessionToken_RejectsMalformedAndForged covers the fail-closed contract of
// the token parser: nothing that was not signed by this key may authenticate.
func TestSessionToken_RejectsMalformedAndForged(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(t.TempDir(), "", "12345678")

	tokenA, err := mgr.Authenticate(ctx, "12345678")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	tokenB, err := mgr.Authenticate(ctx, "12345678")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}

	bodyA, sigA, ok := splitTestToken(tokenA.Token)
	if !ok {
		t.Fatalf("issued token %q does not follow the expected layout", tokenA.Token)
	}
	bodyB, _, ok := splitTestToken(tokenB.Token)
	if !ok {
		t.Fatalf("issued token %q does not follow the expected layout", tokenB.Token)
	}

	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"no prefix", strings.TrimPrefix(tokenA.Token, sessionTokenPrefix)},
		{"missing signature", sessionTokenPrefix + bodyA},
		{"empty payload", sessionTokenPrefix + "." + sigA},
		{"empty signature", sessionTokenPrefix + bodyA + "."},
		{"signature not base64", sessionTokenPrefix + bodyA + ".!!!!"},
		{"legacy random token", "ff_sess_" + strings.Repeat("ab", 32)},
		{"garbage", "not-a-token-at-all"},
		// A payload from one token wearing another token's signature.
		{"signature of another payload", sessionTokenPrefix + bodyB + "." + sigA},
		{"flipped payload character", sessionTokenPrefix + flipLastChar(bodyA) + "." + sigA},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if mgr.ValidateToken(ctx, tc.token) {
				t.Fatalf("token %q must not validate", tc.token)
			}
		})
	}

	// Sanity check: the untampered tokens do validate, so the cases above prove
	// tampering is rejected rather than the parser rejecting everything.
	if !mgr.ValidateToken(ctx, tokenA.Token) || !mgr.ValidateToken(ctx, tokenB.Token) {
		t.Fatal("untampered tokens must validate")
	}
}

// TestSessionToken_ExpiryIsEnforced proves the TTL travels inside the token and
// is checked by an instance that never issued it.
func TestSessionToken_ExpiryIsEnforced(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr := NewManager(dir, "", "12345678")

	expired, err := issueSessionToken(mgr.sessionKey, -time.Minute, mgr.passwordEpoch, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("issueSessionToken failed: %v", err)
	}
	replica := NewManager(dir, "", "")
	if replica.ValidateToken(ctx, expired.Token) {
		t.Fatal("an expired token must be rejected")
	}

	fresh, err := mgr.Authenticate(ctx, "12345678")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if !replica.ValidateToken(ctx, fresh.Token) {
		t.Fatal("a token inside its TTL must validate")
	}
	if remaining := time.Until(fresh.ExpiresAt); remaining < 23*time.Hour {
		t.Fatalf("expected a ~24h session, got %s", remaining)
	}
}

// TestSessionToken_EpochMismatchRejected proves the credential-generation claim
// is part of validation, which is what retires sessions after a password change
// or a sign-out.
func TestSessionToken_EpochMismatchRejected(t *testing.T) {
	key := deriveSessionKey([]byte(testSessionSecret))
	sess, err := issueSessionToken(key, time.Hour, 7, time.Now())
	if err != nil {
		t.Fatalf("issueSessionToken failed: %v", err)
	}

	if _, ok := verifySessionToken(key, sess.Token, 7, time.Now()); !ok {
		t.Fatal("token must verify against its own generation")
	}
	if _, ok := verifySessionToken(key, sess.Token, 8, time.Now()); ok {
		t.Fatal("token from a retired generation must not verify")
	}
	other := deriveSessionKey([]byte("another-secret"))
	if _, ok := verifySessionToken(other, sess.Token, 7, time.Now()); ok {
		t.Fatal("token signed with another key must not verify")
	}
}

// TestAuthManager_PasswordChangeRetiresSessionsPersistently checks that the
// credential generation advanced by UpdatePassword outlives the process: a
// manager that starts afterwards must still refuse the pre-change token.
func TestAuthManager_PasswordChangeRetiresSessionsPersistently(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr := NewManager(dir, "", "12345678")

	before, err := mgr.Authenticate(ctx, "12345678")
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}

	if err := mgr.UpdatePassword(ctx, "12345678", "newpassword123"); err != nil {
		t.Fatalf("UpdatePassword failed: %v", err)
	}

	restarted := NewManager(dir, "", "")
	if restarted.ValidateToken(ctx, before.Token) {
		t.Fatal("a token from before the password change must not validate after a restart")
	}

	after, err := restarted.Authenticate(ctx, "newpassword123")
	if err != nil {
		t.Fatalf("Authenticate with the new password failed: %v", err)
	}
	if !restarted.ValidateToken(ctx, after.Token) {
		t.Fatal("a fresh token must validate")
	}
}

// splitTestToken splits a token into its payload and signature halves.
func splitTestToken(token string) (body, signature string, ok bool) {
	rest, ok := strings.CutPrefix(token, sessionTokenPrefix)
	if !ok {
		return "", "", false
	}
	body, signature, ok = strings.Cut(rest, ".")
	if !ok || body == "" || signature == "" {
		return "", "", false
	}
	return body, signature, true
}

// flipLastChar returns body with its final character replaced, producing a
// payload whose signature no longer matches.
func flipLastChar(body string) string {
	if body == "" {
		return body
	}
	replacement := byte('A')
	if body[len(body)-1] == 'A' {
		replacement = 'B'
	}
	return body[:len(body)-1] + string(replacement)
}
