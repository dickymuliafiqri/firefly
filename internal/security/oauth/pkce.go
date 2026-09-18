package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// GenerateRandomString generates a secure URL-safe base64 string of the specified byte length.
func GenerateRandomString(byteLen int) (string, error) {
	if byteLen <= 0 {
		byteLen = 32
	}
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto rand read: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GeneratePKCE creates an RFC 7636 compliant code_verifier and code_challenge (S256).
func GeneratePKCE() (verifier string, challenge string, err error) {
	verifier, err = GenerateRandomString(32)
	if err != nil {
		return "", "", err
	}
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}
