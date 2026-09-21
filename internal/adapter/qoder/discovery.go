package qoder

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// DefaultBaseURL is the canonical Qoder inference host to store on an upstream.
// Device tokens (dt-) are served here; job tokens (jt-, minted from PATs) are
// transparently routed to api2 at request time by qoderInferenceBase.
const DefaultBaseURL = QoderChatBase

// FetchModels discovers the live Qoder model catalog for a credential, for use
// by the dashboard "fetch models" / health-check probes. It performs the full
// flow: PAT (pt-) exchange -> userId resolution -> COSY-signed /model/list.
//
// token is the raw credential secret (a PAT `pt-`, job token `jt-`, or device
// token `dt-`, or a JSON identity blob). baseURL may be empty (the token's
// natural host is used). Returns the routable model ids (visible first).
func FetchModels(ctx context.Context, client *http.Client, baseURL, token string) ([]string, error) {
	if client == nil {
		return nil, fmt.Errorf("qoder: nil http client")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("qoder: empty credential")
	}

	creds, err := resolveDiscoveryCreds(ctx, client, token)
	if err != nil {
		return nil, err
	}

	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = qoderInferenceBase(creds.AuthToken)
	}

	cat, err := fetchQoderCatalog(ctx, client, base, creds)
	if err != nil {
		return nil, err
	}

	var visible, hidden []string
	for _, m := range cat.models {
		if m.Hidden {
			hidden = append(hidden, m.ID)
		} else {
			visible = append(visible, m.ID)
		}
	}
	sort.Strings(visible)
	sort.Strings(hidden)
	return append(visible, hidden...), nil
}

// resolveDiscoveryCreds turns a raw token into COSY-signable creds, running the
// PAT exchange when needed. It mirrors the adapter's resolveIdentity but is
// standalone so the dashboard can call it without a full adapter/keyring.
func resolveDiscoveryCreds(ctx context.Context, client *http.Client, token string) (cosyCreds, error) {
	// JSON identity blob path (token + userId + machineId already present).
	if id, ok := parseIdentityBlob(token); ok {
		mid := id.MachineID
		if mid == "" {
			mid = newUUID()
		}
		return cosyCreds{UserID: id.UserID, AuthToken: id.Token, MachineID: mid, Email: id.Email, Name: id.Name}, nil
	}

	if isPAT(token) {
		jobToken, _, err := exchangeJobToken(ctx, client, token)
		if err != nil {
			return cosyCreds{}, err
		}
		userID := fetchUserID(ctx, client, jobToken)
		if userID == "" {
			return cosyCreds{}, fmt.Errorf("qoder: could not resolve userId for job token")
		}
		return cosyCreds{UserID: userID, AuthToken: jobToken, MachineID: stableHash16(patMachineIDSeed, token)}, nil
	}

	// Bare dt-/jt- token: resolve userId via /userinfo (works for both).
	userID := fetchUserID(ctx, client, token)
	if userID == "" {
		return cosyCreds{}, fmt.Errorf("qoder: could not resolve userId; provide a PAT (pt-) or a full identity blob")
	}
	return cosyCreds{UserID: userID, AuthToken: token, MachineID: stableHash16(patMachineIDSeed, token)}, nil
}
