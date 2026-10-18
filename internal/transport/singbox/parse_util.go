package singbox

import (
	"net/url"
	"strings"
)

func percentDecode(s string) string {
	if s == "" {
		return s
	}
	decoded, err := url.QueryUnescape(s)
	if err != nil {
		decoded, err = url.PathUnescape(s)
	}
	if err != nil {
		return s
	}
	decoded = strings.ReplaceAll(decoded, "+", " ")
	return decoded
}
