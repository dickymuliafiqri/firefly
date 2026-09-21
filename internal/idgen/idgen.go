// Package idgen provides shared, dependency-free identifier generators used by
// upstream adapters and other modules: random RFC-4122 v4 UUIDs, short random
// hex ids, and deterministic UUIDs derived from a seed. It is a leaf package
// (only the standard library) so any package can import it without creating
// import cycles.
//
// The project intentionally hand-rolls these with crypto/rand rather than
// pulling in an external uuid dependency; idgen centralizes that convention.
package idgen

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// formatUUID renders 16 bytes as the canonical 8-4-4-4-12 hex string.
func formatUUID(b []byte) string {
	s := hex.EncodeToString(b[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[0:8], s[8:12], s[12:16], s[16:20], s[20:32])
}

// UUIDv4 returns a random RFC-4122 version-4 UUID string. On the (practically
// impossible) event that the system RNG fails, it still returns a well-formed
// UUID from whatever bytes were produced.
func UUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC-4122 variant
	return formatUUID(b[:])
}

// Short returns a random hex id of n bytes (2*n hex chars). n<=0 defaults to 6
// (12 hex chars), matching the previous per-adapter shortID helpers.
func Short(n int) string {
	if n <= 0 {
		n = 6
	}
	b := make([]byte, n)
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b)
}

// UUIDFromSeed returns a deterministic RFC-4122-shaped UUID derived from the
// SHA-256 of seed. The same seed always yields the same UUID. It sets the
// standard version-4 nibble and RFC-4122 variant.
func UUIDFromSeed(seed string) string {
	return uuidFromSeedVersion(seed, 0x40)
}

// UUIDFromSeedVersion is like UUIDFromSeed but lets the caller pin the version
// nibble (high 4 bits of byte 6). Some upstreams expect a non-standard value
// (e.g. Antigravity uses 0x50); pass 0x40 for a normal v4-shaped id.
func UUIDFromSeedVersion(seed string, versionNibble byte) string {
	return uuidFromSeedVersion(seed, versionNibble)
}

func uuidFromSeedVersion(seed string, versionNibble byte) string {
	h := sha256.Sum256([]byte(seed))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | (versionNibble & 0xf0)
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

// Hash16 returns the first 16 hex chars of SHA-256(prefix \0 parts...). It is a
// stable, collision-resistant short id used for deriving session/record ids
// from request content.
func Hash16(prefix string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(prefix))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
