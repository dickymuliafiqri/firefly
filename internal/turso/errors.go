package turso

import "errors"

var (
	// ErrConflict is returned when an optimistic concurrency check fails (version mismatch).
	ErrConflict = errors.New("concurrent modification detected: version mismatch")
	// ErrNotFound is returned when a requested entity does not exist.
	ErrNotFound = errors.New("resource not found")
)
