package qoder

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// cosyCreds carries the fields needed to build a COSY signature.
type cosyCreds struct {
	UserID    string // stable Qoder user id
	AuthToken string // device/job access token (dt-/jt-)
	Name      string // display name (optional)
	Email     string // email (optional)
	MachineID string // persisted machine UUID
}

// rsaPublicKey is parsed once from the embedded PEM.
var rsaPublicKey = func() *rsa.PublicKey {
	block, _ := pem.Decode([]byte(QoderRSAPublicKey))
	if block == nil {
		panic("qoder: failed to decode RSA public key PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic("qoder: failed to parse RSA public key: " + err.Error())
	}
	key, ok := pub.(*rsa.PublicKey)
	if !ok {
		panic("qoder: embedded key is not an RSA public key")
	}
	return key
}()

// generateAESKey mirrors qodercli: the first 16 chars of a fresh UUID string
// (hyphens included). Fresh per request so the IV (which reuses key bytes) is
// unique per request.
func generateAESKey() string {
	return newUUID()[:16]
}

// pkcs7Pad pads data to a multiple of blockSize using PKCS#7.
func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - (len(data) % blockSize)
	padded := make([]byte, len(data)+padding)
	copy(padded, data)
	for i := len(data); i < len(padded); i++ {
		padded[i] = byte(padding)
	}
	return padded
}

// aesEncryptCBCBase64 encrypts plaintext with AES-128-CBC using keyStr bytes as
// both key and IV (matching qodercli/Veria), manual PKCS7 padding, base64 output.
func aesEncryptCBCBase64(plaintext, keyStr string) (string, error) {
	keyBytes := []byte(keyStr)
	if len(keyBytes) != 16 {
		return "", fmt.Errorf("aes key must be 16 bytes, got %d", len(keyBytes))
	}
	blk, err := aes.NewCipher(keyBytes)
	if err != nil {
		return "", err
	}
	iv := keyBytes[:16]
	padded := pkcs7Pad([]byte(plaintext), 16)
	enc := make([]byte, len(padded))
	cipher.NewCBCEncrypter(blk, iv).CryptBlocks(enc, padded)
	return base64.StdEncoding.EncodeToString(enc), nil
}

// rsaEncryptBase64 encrypts data with RSA PKCS1v15 and base64-encodes it.
func rsaEncryptBase64(data string) (string, error) {
	enc, err := rsa.EncryptPKCS1v15(rand.Reader, rsaPublicKey, []byte(data))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(enc), nil
}

// encryptUserInfo wraps the user info in AES, wraps the AES key in RSA.
func encryptUserInfo(userInfo map[string]string) (cosyKey, info string, err error) {
	aesKey := generateAESKey()
	plaintext, err := json.Marshal(userInfo)
	if err != nil {
		return "", "", err
	}
	info, err = aesEncryptCBCBase64(string(plaintext), aesKey)
	if err != nil {
		return "", "", err
	}
	cosyKey, err = rsaEncryptBase64(aesKey)
	if err != nil {
		return "", "", err
	}
	return cosyKey, info, nil
}

func md5Hex(input []byte) string {
	sum := md5.Sum(input)
	return hex.EncodeToString(sum[:])
}

// computeSigPath strips the leading "/algo" prefix from the request path.
// Matches qodercli convention; empty/invalid input returns "".
func computeSigPath(requestURL string) string {
	u, err := url.Parse(requestURL)
	if err != nil {
		return ""
	}
	p := u.Path
	if len(p) >= 5 && p[:5] == "/algo" {
		return p[5:]
	}
	return p
}

// latin1Bytes converts a byte slice to a string where each byte maps to one
// rune 0..255, then back to the UTF-8 bytes of that string. This reproduces
// JavaScript's Buffer.toString("latin1") round-trip used in the reference sig
// input, where the encoded/binary body bytes are concatenated as a latin1
// string before MD5.
//
// We instead build the MD5 input directly from raw bytes (see buildCosyHeaders),
// which is byte-identical to hashing the latin1 string's original bytes because
// MD5 operates on bytes, not runes. This helper is kept for clarity/tests.
func latin1Bytes(b []byte) []byte {
	return b
}

// buildCosyHeaders builds the full Cosy-* header set for a single Qoder request.
//
// body must be the exact bytes that will be sent (for GET, pass nil/empty).
// requestURL is the full request URL (used for sigPath).
func buildCosyHeaders(body []byte, requestURL string, creds cosyCreds) (map[string]string, error) {
	if creds.UserID == "" {
		return nil, errors.New("cosy: user id is empty")
	}
	if creds.AuthToken == "" {
		return nil, errors.New("cosy: auth token is empty")
	}

	cosyKey, info, err := encryptUserInfo(map[string]string{
		"uid":                  creds.UserID,
		"security_oauth_token": creds.AuthToken,
		"name":                 creds.Name,
		"aid":                  "",
		"email":                creds.Email,
	})
	if err != nil {
		return nil, err
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	requestID := newUUID()

	payload := map[string]string{
		"version":     "v1",
		"requestId":   requestID,
		"info":        info,
		"cosyVersion": QoderIDEVersion,
		"ideVersion":  "",
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	payloadB64 := base64.StdEncoding.EncodeToString(payloadJSON)

	sigPath := computeSigPath(requestURL)

	// sigInput = payloadB64 \n cosyKey \n timestamp \n body \n sigPath
	// The JS joins these as a latin1 string then MD5s its bytes. Because MD5
	// operates on bytes, building the byte buffer directly is identical.
	var sigBuf []byte
	sigBuf = append(sigBuf, payloadB64...)
	sigBuf = append(sigBuf, '\n')
	sigBuf = append(sigBuf, cosyKey...)
	sigBuf = append(sigBuf, '\n')
	sigBuf = append(sigBuf, timestamp...)
	sigBuf = append(sigBuf, '\n')
	sigBuf = append(sigBuf, latin1Bytes(body)...)
	sigBuf = append(sigBuf, '\n')
	sigBuf = append(sigBuf, sigPath...)
	sig := md5Hex(sigBuf)

	machineID := creds.MachineID
	if machineID == "" {
		machineID = newUUID()
	}
	bodyHash := md5Hex(body)
	bodyLength := strconv.Itoa(len(body))

	return map[string]string{
		"Authorization":          "Bearer COSY." + payloadB64 + "." + sig,
		"Cosy-Key":               cosyKey,
		"Cosy-User":              creds.UserID,
		"Cosy-Date":              timestamp,
		"Cosy-Version":           QoderIDEVersion,
		"Cosy-Machineid":         machineID,
		"Cosy-Machinetoken":      machineID,
		"Cosy-Machinetype":       QoderMachineType,
		"Cosy-Machineos":         QoderMachineOS,
		"Cosy-Clienttype":        QoderClientType,
		"Cosy-Clientip":          "127.0.0.1",
		"Cosy-Bodyhash":          bodyHash,
		"Cosy-Bodylength":        bodyLength,
		"Cosy-Sigpath":           sigPath,
		"Cosy-Data-Policy":       QoderDataPolicy,
		"Cosy-Organization-Id":   "",
		"Cosy-Organization-Tags": "",
		"Login-Version":          QoderLoginVersion,
		"X-Request-Id":           newUUID(),
	}, nil
}
