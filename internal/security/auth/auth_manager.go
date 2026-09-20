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
	PasswordHash string    `json:"password_hash"`
	Salt         string    `json:"salt"`
	UpdatedAt    time.Time `json:"updated_at"`
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
	sessions     map[string]Session
	sessionTTL   time.Duration
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
		sessions:   make(map[string]Session),
		sessionTTL: DefaultSessionTTL,
	}

	// Try loading existing auth.json
	loaded := false
	if configDir != "" {
		path := filepath.Join(configDir, AuthFileName)
		if data, err := os.ReadFile(path); err == nil {
			var af AuthFile
			if err := json.Unmarshal(data, &af); err == nil && af.PasswordHash != "" && af.Salt != "" {
				m.passwordHash = af.PasswordHash
				m.salt = af.Salt
				loaded = true
			}
		}
	}

	// If not loaded from disk, initialize with default credentials and save
	if !loaded {
		salt := generateSalt()
		m.salt = salt
		m.passwordHash = hashPassword(defaultPassword, salt)
		if configDir != "" {
			_ = m.persistToDisk(context.Background())
		}
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

// generateToken generates a cryptographically random session token.
func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		return "ff_sess_" + hex.EncodeToString(sum[:])
	}
	return "ff_sess_" + hex.EncodeToString(b)
}

// Authenticate verifies the presented password against backend credentials.
// On success, it issues a secure session token with a 24-hour expiration.
func (m *Manager) Authenticate(ctx context.Context, password string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Clean expired sessions
	now := time.Now()
	for tok, s := range m.sessions {
		if now.After(s.ExpiresAt) {
			delete(m.sessions, tok)
		}
	}

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

	sess := Session{
		Token:     generateToken(),
		CreatedAt: now,
		ExpiresAt: now.Add(m.sessionTTL),
	}
	m.sessions[sess.Token] = sess

	return sess, nil
}

// ValidateToken verifies whether the supplied token is a valid active session
// or matches the server-configured AdminToken.
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
	m.mu.RUnlock()

	// 1. Static admin token bypass
	if adminTok != "" && subtle.ConstantTimeCompare([]byte(token), []byte(adminTok)) == 1 {
		return true
	}

	// 2. Dynamic session token lookup
	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[token]
	if !ok {
		return false
	}

	if time.Now().After(sess.ExpiresAt) {
		delete(m.sessions, token)
		return false
	}

	return true
}

// RevokeSession invalidates an active session token.
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
	delete(m.sessions, token)
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

	// Invalidate all existing sessions to force re-authentication with new password
	m.sessions = make(map[string]Session)

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
		PasswordHash: m.passwordHash,
		Salt:         m.salt,
		UpdatedAt:    time.Now().UTC(),
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
