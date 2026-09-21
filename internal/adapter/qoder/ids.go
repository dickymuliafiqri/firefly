package qoder

import "github.com/dickymuliafiqri/firefly/internal/idgen"

// Thin local aliases over the shared idgen package, kept so the call sites in
// this adapter stay concise. See internal/idgen for the implementations.

// newUUID returns a random RFC-4122 v4 UUID string.
func newUUID() string { return idgen.UUIDv4() }

// shortID returns a 12-hex-char random id for chat completion ids.
func shortID() string { return idgen.Short(6) }

// stableHash16 returns the first 16 hex chars of sha256(prefix \0 parts...),
// used for deriving stable session/record ids.
func stableHash16(prefix string, parts ...string) string {
	return idgen.Hash16(prefix, parts...)
}
