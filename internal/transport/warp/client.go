package warp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"

	"github.com/dickymuliafiqri/firefly/internal/textx"
)

// CloudflareDefaults holds canonical WARP endpoints and peer public keys.
const (
	DefaultRegistrationURL = "https://api.cloudflareclient.com/v0a3304/reg"
	DefaultPeerPublicKey   = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo="
	DefaultPeerEndpoint    = "162.159.192.1:2408"
	DefaultTraceURL        = "https://cloudflare.com/cdn-cgi/trace"
)

// maxErrorBodySnippet bounds how much of an upstream error body is copied into
// an error message (which ends up in logs and in the WARP status payload).
const maxErrorBodySnippet = 200

// KeyPair stores private and public Curve25519 WireGuard keys in both raw and base64 formats.
type KeyPair struct {
	PrivateKey    [32]byte
	PublicKey     [32]byte
	PrivateKeyB64 string
	PublicKeyB64  string
	PrivateKeyHex string
}

// GenerateKeyPair generates a new cryptographically secure Curve25519 keypair for WireGuard.
func GenerateKeyPair() (*KeyPair, error) {
	var priv [32]byte
	if _, err := io.ReadFull(rand.Reader, priv[:]); err != nil {
		return nil, fmt.Errorf("read random bytes: %w", err)
	}

	// Clamp the private key according to Curve25519 / RFC 7748
	priv[0] &= 248
	priv[31] = (priv[31] & 127) | 64

	var pub [32]byte
	curve25519.ScalarBaseMult(&pub, &priv)

	return &KeyPair{
		PrivateKey:    priv,
		PublicKey:     pub,
		PrivateKeyB64: base64.StdEncoding.EncodeToString(priv[:]),
		PublicKeyB64:  base64.StdEncoding.EncodeToString(pub[:]),
		PrivateKeyHex: hex.EncodeToString(priv[:]),
	}, nil
}

// PeerEndpoint stores peer network address details.
type PeerEndpoint struct {
	V4   string `json:"v4"`
	V6   string `json:"v6"`
	Host string `json:"host"`
}

// PeerConfig describes a WireGuard peer from Cloudflare.
type PeerConfig struct {
	PublicKey string       `json:"public_key"`
	Endpoint  PeerEndpoint `json:"endpoint"`
}

// InterfaceAddresses stores assigned internal tunnel IP addresses.
type InterfaceAddresses struct {
	V4 string `json:"v4"`
	V6 string `json:"v6"`
}

// RegistrationResponse represents the JSON payload from api.cloudflareclient.com/reg.
type RegistrationResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Key     string `json:"key"`
	Account struct {
		ID          string `json:"id"`
		AccountType string `json:"account_type"`
		Warp        bool   `json:"warp"`
	} `json:"account"`
	Config struct {
		ClientID  string       `json:"client_id"`
		Peers     []PeerConfig `json:"peers"`
		Interface struct {
			Addresses InterfaceAddresses `json:"addresses"`
		} `json:"interface"`
	} `json:"config"`
	Token string `json:"token"`

	// RegisteredAt mirrors Cloudflare's created_at for this registration; the
	// identity cache and telemetry use it to tell a fresh device from a restored one.
	RegisteredAt time.Time `json:"created_at,omitzero"`

	// LicenseApplied and LicenseError report the outcome of an optional WARP+
	// license attach. They are computed locally and never part of the payload.
	LicenseApplied bool   `json:"-"`
	LicenseError   string `json:"-"`
}

// RegisterDevice calls the Cloudflare WARP client API to register a new WireGuard
// peer. registrationURL defaults to DefaultRegistrationURL when empty; tests and
// mirrors point it elsewhere. A license is attached on the same host as the
// registration, so overriding the URL keeps both calls consistent.
func RegisterDevice(ctx context.Context, httpClient *http.Client, keys *KeyPair, licenseKey, registrationURL string) (*RegistrationResponse, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	endpoint := strings.TrimSpace(registrationURL)
	if endpoint == "" {
		endpoint = DefaultRegistrationURL
	}

	reqBody := map[string]any{
		"key":           keys.PublicKeyB64,
		"install_id":    "",
		"fcm_token":     "",
		"tos":           time.Now().UTC().Format(time.RFC3339Nano),
		"model":         "Firefly Gateway",
		"serial_number": "",
		"locale":        "en_US",
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal registration request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("create registration request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("User-Agent", "okhttp/3.12.1")
	req.Header.Set("CF-Client-Version", "a-6.3-2020")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute registration request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read registration response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("registration failed with status %d: %s", resp.StatusCode, textx.Excerpt(bodyBytes, maxErrorBodySnippet))
	}

	var regResp RegistrationResponse
	if err := json.Unmarshal(bodyBytes, &regResp); err != nil {
		return nil, fmt.Errorf("unmarshal registration response: %w", err)
	}

	if len(regResp.Config.Peers) == 0 {
		// Fallback to default peer if empty
		regResp.Config.Peers = []PeerConfig{
			{
				PublicKey: DefaultPeerPublicKey,
				Endpoint: PeerEndpoint{
					V4: DefaultPeerEndpoint,
				},
			},
		}
	}

	// A WARP+ license that cannot be attached is not fatal: the tunnel still
	// works on the plan the registration returned. Record why so the manager can
	// log it instead of every caller silently discarding the error.
	if licenseKey != "" && regResp.ID != "" && regResp.Token != "" {
		accountURL := strings.TrimSuffix(endpoint, "/") + "/" + regResp.ID + "/account"
		if err := updateLicenseKey(ctx, httpClient, &regResp, accountURL, licenseKey); err != nil {
			regResp.LicenseError = err.Error()
		} else {
			regResp.LicenseApplied = true
		}
	}

	return &regResp, nil
}

// updateLicenseKey attaches a WARP+ license to a fresh registration.
func updateLicenseKey(ctx context.Context, httpClient *http.Client, reg *RegistrationResponse, accountURL, license string) error {
	if reg == nil || reg.ID == "" || reg.Token == "" {
		return errors.New("registration is missing the id or token needed to attach a license")
	}

	data, err := json.Marshal(map[string]string{"license": strings.TrimSpace(license)})
	if err != nil {
		return fmt.Errorf("marshal license request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, accountURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create license request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The registration token is a bearer credential: it goes on the wire, never
	// into an error message or a log line.
	req.Header.Set("Authorization", "Bearer "+reg.Token)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute license request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("license attach failed with status %d: %s", resp.StatusCode, textx.Excerpt(body, maxErrorBodySnippet))
	}
	return nil
}
