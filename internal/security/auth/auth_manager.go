package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// Default dashboard credentials and session duration.
const (
	DefaultDashboardPassword = "12345678"
	DefaultSessionTTL        = 24 * time.Hour
	AuthFileName             = "auth.json"
)

// Sentinel authentication errors.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrPasswordTooShort   = errors.New("password must be at least 4 characters")
)

// AuthFile represents the persistent backend configuration for dashboard credentials.
type AuthFile struct {
	PasswordHash string `json:"password_hash"`
	Salt         string `json:"salt"`
	// SessionSecret is the HMAC key material for stateless dashboard session
	// tokens. It is generated on first boot; export SessionSecretEnvVar to keep
	// it stable across hosts whose filesystem does not persist between starts.
	SessionSecret string `json:"session_secret,omitempty"`
	// PasswordEpoch is the credential generation counter. Changing the password
	// or signing out advances it, which retires every session token issued under
	// the previous generation.
	PasswordEpoch int64     `json:"password_epoch,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Session represents an active authenticated dashboard session.
type Session struct {
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Manager manages backend credential verification, hashing, password updates,
// and session token lifecycle.
type Manager struct {
	mu           sync.RWMutex
	configDir    string
	passwordHash string
	salt         string
	adminToken   string
	// sessionSecret is the raw signing material (from the environment or
	// auth.json) and sessionKey is its derived HMAC key, cached so that token
	// validation never re-derives it per request.
	sessionSecret string
	sessionKey    []byte
	// passwordEpoch is the credential generation embedded in every issued token.
	passwordEpoch int64
	sessionTTL    time.Duration
}

// NewManager initializes the authentication manager. It attempts to load
// credentials from auth.json inside configDir. If not found, it initializes
// with the provided default password (or DefaultDashboardPassword) and persists it.
func NewManager(configDir, adminToken, defaultPassword string) *Manager {
	if defaultPassword == "" {
		if envPass := os.Getenv("INITIAL_PASSWORD"); envPass != "" {
			defaultPassword = envPass
		} else if envPass := os.Getenv("FIREFLY_DASHBOARD_PASSWORD"); envPass != "" {
			defaultPassword = envPass
		} else {
			defaultPassword = DefaultDashboardPassword
		}
	}
	if adminToken == "" {
		adminToken = os.Getenv("FIREFLY_ADMIN_TOKEN")
	}

	m := &Manager{
		configDir:  configDir,
		adminToken: adminToken,
		sessionTTL: DefaultSessionTTL,
	}

	// Try loading existing auth.json
	var af AuthFile
	loaded := false
	if configDir != "" {
		path := filepath.Join(configDir, AuthFileName)
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &af); err == nil && af.PasswordHash != "" && af.Salt != "" {
				m.passwordHash = af.PasswordHash
				m.salt = af.Salt
				m.passwordEpoch = af.PasswordEpoch
				loaded = true
			}
		}
	}

	// If not loaded from disk, initialize with default credentials
	if !loaded {
		salt := generateSalt()
		m.salt = salt
		m.passwordHash = hashPassword(defaultPassword, salt)
	}

	// Resolve the session signing secret. The environment wins because it is the
	// only source that survives a cold start on a host with an ephemeral
	// filesystem: a secret read back from disk would be regenerated on every
	// start, minting a new key and instantly invalidating the token the browser
	// still holds — the exact "signed out seconds after login" symptom.
	readSecret, readEpoch := af.SessionSecret, af.PasswordEpoch
	if envSecret := strings.TrimSpace(os.Getenv(SessionSecretEnvVar)); envSecret != "" {
		m.sessionSecret = envSecret
	} else {
		m.sessionSecret = readSecret
	}
	if m.sessionSecret == "" {
		m.sessionSecret = generateSessionSecret()
	}
	m.sessionKey = deriveSessionKey([]byte(m.sessionSecret))

	// The credential generation starts at 1 so the zero value of an auth.json
	// written by an older build reads as "absent" instead of as a real
	// generation.
	if m.passwordEpoch == 0 {
		m.passwordEpoch = 1
	}

	// Persist only when the file is new or the resolved secret/generation
	// differs from what is stored (e.g. the operator exported a new
	// SessionSecretEnvVar). Rewriting an already-current file on every boot
	// would make its timestamp meaningless.
	if configDir != "" && (!loaded || m.sessionSecret != readSecret || m.passwordEpoch != readEpoch) {
		_ = m.persistToDisk(context.Background())
	}

	return m
}

// hashPassword produces a salted SHA-256 hash.
func hashPassword(password, salt string) string {
	sum := sha256.Sum256([]byte(salt + ":" + password))
	return hex.EncodeToString(sum[:])
}

// generateSalt generates 16 bytes of cryptographically secure random salt.
func generateSalt() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp hash if crypto rand fails
		sum := sha256.Sum256([]byte(time.Now().String()))
		return hex.EncodeToString(sum[:16])
	}
	return hex.EncodeToString(b)
}

// Authenticate verifies the presented password against backend credentials.
// On success, it issues a self-verifying (stateless) session token with a
// 24-hour expiration that any gateway instance can validate.
func (m *Manager) Authenticate(ctx context.Context, password string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 1. Check against master password hash
	expectedHash := hashPassword(password, m.salt)
	match := subtle.ConstantTimeCompare([]byte(expectedHash), []byte(m.passwordHash)) == 1

	// 2. Also accept static admin token as valid master credential if configured
	if !match && m.adminToken != "" {
		match = subtle.ConstantTimeCompare([]byte(password), []byte(m.adminToken)) == 1
	}

	if !match {
		return Session{}, ErrInvalidCredentials
	}

	// 3. Issue a token bound to the current credential generation. Nothing is
	// recorded server-side, so the session is honored by every instance and
	// survives restarts.
	return issueSessionToken(m.sessionKey, m.sessionTTL, m.passwordEpoch, time.Now())
}

// ValidateToken verifies whether the supplied token is a valid active session
// or matches the server-configured AdminToken. Session validation is stateless:
// the token's HMAC and claims are checked here, so a session issued by one
// instance is honored by every other instance (and survives restarts) as long as
// they share the same signing secret.
func (m *Manager) ValidateToken(ctx context.Context, token string) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}

	m.mu.RLock()
	adminTok := m.adminToken
	key := m.sessionKey
	epoch := m.passwordEpoch
	m.mu.RUnlock()

	// 1. Static admin token bypass
	if adminTok != "" && subtle.ConstantTimeCompare([]byte(token), []byte(adminTok)) == 1 {
		return true
	}

	// 2. Stateless session token: recompute the HMAC over the payload and check
	// the claims. No shared state is consulted.
	if len(key) == 0 {
		return false
	}
	_, ok := verifySessionToken(key, token, epoch, time.Now())
	return ok
}

// RevokeSession retires the dashboard session generation, invalidating every
// token issued before this call.
//
// Stateless tokens carry no per-token server-side record, so there is nothing to
// delete: advancing the credential generation instead makes each previously
// issued token fail validation, on this instance and on every other instance
// sharing the same auth.json. The token argument is still accepted (callers pass
// the token being signed out) but only genuine session tokens trigger the bump,
// so a random or admin-issued bearer token cannot force a write on every call.
//
// Consequence worth knowing: signing out anywhere signs out everywhere. That is
// the intended semantic for a single-operator dashboard, and it is what makes
// sign-out meaningful for tokens the server never stored.
func (m *Manager) RevokeSession(ctx context.Context, token string) {
	if err := ctx.Err(); err != nil {
		return
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := verifySessionToken(m.sessionKey, token, m.passwordEpoch, time.Now()); !ok {
		return
	}

	m.passwordEpoch++
	_ = m.persistToDisk(ctx)
}

// UpdatePassword updates the dashboard access password after verifying the
// current password.
func (m *Manager) UpdatePassword(ctx context.Context, currentPassword, newPassword string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	trimmedNew := strings.TrimSpace(newPassword)
	if len(trimmedNew) < 4 {
		return ErrPasswordTooShort
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Validate current password
	expectedHash := hashPassword(currentPassword, m.salt)
	match := subtle.ConstantTimeCompare([]byte(expectedHash), []byte(m.passwordHash)) == 1
	if !match && m.adminToken != "" {
		match = subtle.ConstantTimeCompare([]byte(currentPassword), []byte(m.adminToken)) == 1
	}
	if !match {
		return ErrInvalidCredentials
	}

	// Compute new credentials
	newSalt := generateSalt()
	newHash := hashPassword(trimmedNew, newSalt)

	m.salt = newSalt
	m.passwordHash = newHash

	// Retire every outstanding session: advancing the credential generation
	// forces re-authentication with the new password on all instances.
	m.passwordEpoch++

	// Persist to disk
	return m.persistToDisk(ctx)
}

// persistToDisk writes the current credentials to auth.json with 0600 permissions.
func (m *Manager) persistToDisk(ctx context.Context) error {
	if m.configDir == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	af := AuthFile{
		PasswordHash:  m.passwordHash,
		Salt:          m.salt,
		SessionSecret: m.sessionSecret,
		PasswordEpoch: m.passwordEpoch,
		UpdatedAt:     time.Now().UTC(),
	}

	data, err := json.MarshalIndent(af, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth: %w", err)
	}

	path := filepath.Join(m.configDir, AuthFileName)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write auth.json: %w", err)
	}

	return nil
}

// AdminTenant returns a synthetic tenant instance representing the authenticated
// dashboard operator, allowing full access to all models for Playground testing.
func (m *Manager) AdminTenant(ctx context.Context) *domain.Tenant {
	if err := ctx.Err(); err != nil {
		return nil
	}
	return &domain.Tenant{
		KeyHash:       "admin-dashboard-session",
		Name:          "admin-dashboard",
		Status:        domain.TenantStatusActive,
		AllowedModels: []string{"*"},
		RateLimit: domain.RateLimit{
			RPS:           1000,
			Burst:         2000,
			MaxConcurrent: 100,
		},
	}
}
