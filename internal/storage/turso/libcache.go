package turso

import "os"

// IsolateNativeLibraryCache points the tursogo loader at a private, per-process
// cache directory and returns a cleanup function that removes it.
//
// The loader extracts the embedded ~19MB shared object to
// $TURSO_GO_CACHE_DIR, else <os.UserCacheDir()>/turso-go/<embedded hash>/, and
// only accepts a non-empty file whose sha256 matches the embedded hash. Because
// `go test ./...` runs package test binaries in parallel, two binaries sharing
// the default path race: whichever one stats the file mid-extraction hashes a
// truncated prefix and panics with "cached library file hash sum mismatch" —
// failing a package that has nothing wrong with it. Callers that open the
// "turso" driver directly (rather than through NewClient, which already points
// the loader next to the local replica) should call this from TestMain.
//
// Cleanup is best-effort: Windows locks the file of a loaded DLL, so the
// directory is left behind on the OS temp dir there (Linux unlinks it fine).
func IsolateNativeLibraryCache() (cleanup func(), err error) {
	if os.Getenv("TURSO_GO_CACHE_DIR") != "" {
		return func() {}, nil
	}
	cacheDir, err := os.MkdirTemp("", "firefly-turso-lib-")
	if err != nil {
		return nil, err
	}
	if err := os.Setenv("TURSO_GO_CACHE_DIR", cacheDir); err != nil {
		_ = os.RemoveAll(cacheDir)
		return nil, err
	}
	return func() { _ = os.RemoveAll(cacheDir) }, nil
}
