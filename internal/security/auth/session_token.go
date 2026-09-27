package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Dashboard session tokens are STATELESS: the token carries its own claims and
// is authenticated with an HMAC, so any process holding the same signing secret
// can verify it without shared memory.
//
// A session used to be a random string whose record existed only in the issuing
// process's `sessions` map. On any horizontally scaled or ephemeral host
// (Railway with more than one replica, Vercel/Lambda cold starts, container
// restarts, zero-downtime instance swaps) the next request — the dashboard polls
// /api/telemetry every 2s — was routinely served by a process that had never
// seen that token, so ValidateToken returned false, the frontend treated the 401
// as a dead session and bounced the operator back to the login page a few
// seconds after signing in.
//
// Layout:
//
//	ff_sess_<base64url(payload)>.<base64url(HMAC-SHA256(key, payload))>
//
// The payload is readable by design and holds no secrets. Forging a token
// requires the signing secret in order to compute a matching tag; editing any
// claim invalidates the tag, and the tag is compared in constant time.
const (
	sessionTokenPrefix = "ff_sess_"

	// sessionKeyLabel domain-separates the session HMAC key from every other
	// use of the operator secret.
	sessionKeyLabel = "firefly/dashboard-session/v1"

	// sessionSubject marks claims minted for the dashboard operator.
	sessionSubject = "dashboard"
)

// SessionSecretEnvVar is the preferred source of the session signing secret. It
// is the only source that survives a cold start on hosts with an ephemeral
// filesystem, so it is the recommended setting for serverless and
// multi-instance deployments: with a secret taken from disk, every cold start
// would mint a fresh key and instantly invalidate the token the browser holds.
const SessionSecretEnvVar = "FIREFLY_SESSION_SECRET"

// sessionClaims is the signed claim set of a dashboard session token.
type sessionClaims struct {
	Subject       string `json:"sub"`
	IssuedAt      int64  `json:"iat"`
	ExpiresAt     int64  `json:"exp"`
	TokenID       string `json:"jti"`
	PasswordEpoch int64  `json:"ep"`
}

// deriveSessionKey turns the operator secret into a fixed-length HMAC key.
func deriveSessionKey(secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sessionKeyLabel))
	return mac.Sum(nil)
}

// generateSessionSecret returns a fresh 32-byte secret as hex. It is used only
// when neither the environment nor auth.json supplies one (single-instance
// deployments with a persistent config directory).
func generateSessionSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Fallback to a timestamp hash if crypto rand fails (same policy as
		// generateSalt).
		sum := sha256.Sum256([]byte("firefly-session:" + time.Now().String()))
		return hex.EncodeToString(sum[:])
	}
	return hex.EncodeToString(b)
}

// generateSessionID returns an unpredictable token identifier.
func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		return hex.EncodeToString(sum[:16])
	}
	return hex.EncodeToString(b)
}

// signSessionBody computes the HMAC tag over an encoded payload body.
func signSessionBody(key []byte, body string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(body))
	return mac.Sum(nil)
}

// issueSessionToken mints a signed, self-verifying dashboard session token.
func issueSessionToken(key []byte, ttl time.Duration, passwordEpoch int64, now time.Time) (Session, error) {
	expiresAt := now.Add(ttl)
	claims := sessionClaims{
		Subject:       sessionSubject,
		IssuedAt:      now.Unix(),
		ExpiresAt:     expiresAt.Unix(),
		TokenID:       generateSessionID(),
		PasswordEpoch: passwordEpoch,
	}

	raw, err := json.Marshal(claims)
	if err != nil {
		return Session{}, fmt.Errorf("marshal session claims: %w", err)
	}

	body := base64.RawURLEncoding.EncodeToString(raw)
	tag := signSessionBody(key, body)

	return Session{
		Token:     sessionTokenPrefix + body + "." + base64.RawURLEncoding.EncodeToString(tag),
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}, nil
}

// verifySessionToken authenticates a token and returns its claims. It fails
// closed on every malformed, forged, expired, or superseded-generation input.
func verifySessionToken(key []byte, token string, passwordEpoch int64, now time.Time) (sessionClaims, bool) {
	rest, ok := strings.CutPrefix(token, sessionTokenPrefix)
	if !ok {
		return sessionClaims{}, false
	}

	body, tagB64, ok := strings.Cut(rest, ".")
	if !ok || body == "" || tagB64 == "" {
		return sessionClaims{}, false
	}

	tag, err := base64.RawURLEncoding.DecodeString(tagB64)
	if err != nil {
		return sessionClaims{}, false
	}
	// Constant-time tag comparison: a forged or edited payload cannot produce a
	// matching tag without the signing secret.
	if !hmac.Equal(tag, signSessionBody(key, body)) {
		return sessionClaims{}, false
	}

	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return sessionClaims{}, false
	}
	var claims sessionClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return sessionClaims{}, false
	}

	if claims.Subject != sessionSubject || claims.ExpiresAt == 0 {
		return sessionClaims{}, false
	}
	if !now.Before(time.Unix(claims.ExpiresAt, 0)) {
		return sessionClaims{}, false
	}
	// Generation check: changing the password or signing out advances the epoch,
	// which retires every token issued under the previous generation.
	if claims.PasswordEpoch != passwordEpoch {
		return sessionClaims{}, false
	}

	return claims, true
}
