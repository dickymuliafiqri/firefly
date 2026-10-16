package selfupdate

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// checksumLen is the length of a lowercase hex SHA-256 digest.
const checksumLen = 64

// ParseChecksums reads the output of `sha256sum firefly_*`, which is how the
// release workflow produces checksums.txt.
//
// Both formats the tool can emit are accepted — "<hex>  <name>" for text mode
// and "<hex> *<name>" for binary mode — and a leading "./" or any directory part
// is stripped from the name, so the file can also be produced locally with a
// different invocation. Blank lines and lines that do not look like a digest
// entry are skipped rather than failing the parse: one stray comment must not
// make a release uninstallable.
func ParseChecksums(r io.Reader) (map[string]string, error) {
	sums := make(map[string]string)
	scanner := bufio.NewScanner(r)
	// Digests are short, but use a generous line cap so an oddly long path
	// cannot trip the scanner's default 64 KiB limit.
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Split on the first run of whitespace; the name may contain spaces.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		digest := fields[0]
		if len(digest) != checksumLen || !isHex(digest) {
			continue
		}

		name := strings.TrimPrefix(line[len(digest):], " ")
		name = strings.TrimPrefix(name, "*")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		sums[filepath.Base(name)] = strings.ToLower(digest)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	if len(sums) == 0 {
		return nil, fmt.Errorf("checksum file contained no usable digests")
	}
	return sums, nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
