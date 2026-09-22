package server

import (
	"fmt"
	"os"
	"testing"

	"github.com/dickymuliafiqri/firefly/internal/storage/turso"
)

// TestMain isolates the tursogo native-library cache for this test binary: the
// harnesses here open the "turso" driver directly, and the shared default cache
// path is raced by the other test binaries `go test ./...` runs in parallel.
func TestMain(m *testing.M) {
	cleanup, err := turso.IsolateNativeLibraryCache()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate turso native library cache: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
