package qoder

import "encoding/base64"

// Qoder body encoding ported from qoder2api's QoderEncoding.java (via the
// CLIProxyAPIPlus qoder-provider branch).
//
// Algorithm:
//  1. base64-encode the plaintext bytes (standard alphabet).
//  2. Rearrange: split into thirds, reorder as [tail][mid][head].
//  3. Substitute each character via a custom alphabet mapping.
//
// The encoded body must be sent with `&Encode=1` appended to the URL so the
// server decodes in reverse. The obfuscation prevents Alibaba Cloud WAF from
// pattern-matching the plaintext request body.
const (
	qoderStdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	qoderCustomAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
)

// qoderS2C maps a standard base64 char code to its custom substitution (-1 = passthrough).
var qoderS2C = func() [128]int16 {
	var table [128]int16
	for i := range table {
		table[i] = -1
	}
	for i := 0; i < 64; i++ {
		table[qoderStdAlphabet[i]] = int16(qoderCustomAlphabet[i])
	}
	table['='] = '$'
	return table
}()

// qoderEncodeBody encodes plaintext bytes using Qoder's WAF-bypass scheme and
// returns the encoded bytes (latin1 byte sequence in the JS reference; here the
// raw bytes, which are what must be sent on the wire and signed).
func qoderEncodeBody(plaintext []byte) []byte {
	std := base64.StdEncoding.EncodeToString(plaintext)
	n := len(std)
	a := n / 3
	// [tail][mid][head]
	rearranged := std[n-a:] + std[a:n-a] + std[:a]

	out := make([]byte, n)
	for i := 0; i < n; i++ {
		c := rearranged[i]
		if c < 128 && qoderS2C[c] >= 0 {
			out[i] = byte(qoderS2C[c])
		} else {
			out[i] = c
		}
	}
	return out
}
