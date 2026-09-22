package turso

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	cleanup, err := IsolateNativeLibraryCache()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate turso native library cache: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}
