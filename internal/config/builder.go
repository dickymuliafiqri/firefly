package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/dickymuliafiqri/firefly/internal/domain"
	"github.com/dickymuliafiqri/firefly/internal/ports"
)

// ErrEmptyFile is returned when a config file has zero non-whitespace bytes.
var ErrEmptyFile = errors.New("empty file")

// Compiled validation patterns (compiled once at package level).
var (
	upstreamNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9\-_]{0,63}$`)
	modelNameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]{0,127}$`)
	keyHashRe      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// envVarNameRe matches POSIX-style ENV var names (UPPER_SNAKE_CASE). Only
	// credential refs matching this pattern are eligible to be resolved from
	// the environment when their secret is empty. DB-generated refs such as
	// "openai-cred-3" deliberately do not match, so a missing DB secret fails
	// loudly instead of being silently reinterpreted as an ENV var lookup.
	envVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// BuildResult is the fully-resolved, validated content ready to become a
// snapshot. Maps are owned by the caller.
type BuildResult struct {
	Upstreams       map[string]*domain.Upstream
	UpstreamOrder   []string
	Models          map[string]*domain.ModelEntry
	EnabledModelIDs []string
	TenantsByHash   map[string]*domain.Tenant
	TenantOrder     []string
	Combos          map[string]*domain.Combo
	ComboOrder      []string
	TokenSaver      domain.TokenSaverConfig
	Warnings        []string
}

// ParseError is returned for any malformed JSON with the logical file name.
type ParseError struct {
	File string
	Err  error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("config parse error in %s: %v", e.File, e.Err)
}
func (e *ParseError) Unwrap() error { return e.Err }

// Build parses all config files, applies defaults, validates cross-references,
// and returns a validated BuildResult. On any error nothing is returned that
// should be applied — callers keep their previous snapshot.
//
// envLookup abstracts os.LookupEnv so tests can inject a fake environment.
func Build(fs FileSet, envLookup func(string) (string, bool)) (*BuildResult, error) {
	var upFile UpstreamsFile
	if err := decodeStrict("upstreams", fs.Upstreams, &upFile); err != nil {
		return nil, err
	}
	var modelFile ModelsFile
	if err := decodeStrict("models", fs.Models, &modelFile); err != nil {
		return nil, err
	}
	var tenantFile TenantsFile
	if err := decodeStrict("tenants", fs.Tenants, &tenantFile); err != nil {
		return nil, err
	}
	var comboFile CombosFile
	if len(bytes.TrimSpace(fs.Combos)) > 0 {
		if err := decodeStrict("combos", fs.Combos, &comboFile); err != nil {
			return nil, err
		}
	}

	tokenSaverCfg := domain.DefaultTokenSaverConfig()
	if len(bytes.TrimSpace(fs.TokenSaver)) > 0 {
		var tsFile TokenSaverDTO
		if err := decodeStrict("tokensaver", fs.TokenSaver, &tsFile); err == nil {
			tokenSaverCfg.Enabled = tsFile.Enabled
			tokenSaverCfg.CompressToolOutput = tsFile.CompressToolOutput
			tokenSaverCfg.TerseOutput = tsFile.TerseOutput
			tokenSaverCfg.MinimalCode = tsFile.MinimalCode
			tokenSaverCfg.CompressContext = tsFile.CompressContext
			if tsFile.MaxToolOutputChars != nil && *tsFile.MaxToolOutputChars > 0 {
				tokenSaverCfg.MaxToolOutputChars = *tsFile.MaxToolOutputChars
			}
			if tsFile.ContextThreshold != nil && *tsFile.ContextThreshold > 0 {
				tokenSaverCfg.ContextThreshold = *tsFile.ContextThreshold
			}
		}
	}

	res := &BuildResult{
		Upstreams:     make(map[string]*domain.Upstream, len(upFile.Upstreams)),
		Models:        make(map[string]*domain.ModelEntry, len(modelFile.Models)),
		TenantsByHash: make(map[string]*domain.Tenant, len(tenantFile.Tenants)),
		Combos:        make(map[string]*domain.Combo, len(comboFile.Combos)),
		TokenSaver:    tokenSaverCfg,
	}

	for i, d := range upFile.Upstreams {
		u, err := translateUpstream(i, d, envLookup)
		if err != nil {
			return nil, err
		}
		if _, dup := res.Upstreams[u.Name]; dup {
			return nil, &ValidationError{Field: "upstreams", Msg: "duplicate upstream name: " + u.Name}
		}
		res.Upstreams[u.Name] = u
		res.UpstreamOrder = append(res.UpstreamOrder, u.Name)
	}
	sort.Strings(res.UpstreamOrder)

	enabled := make([]string, 0, len(modelFile.Models))
	for i, d := range modelFile.Models {
		m, err := translateModel(i, d, res.Upstreams)
		if err != nil {
			return nil, err
		}
		if _, dup := res.Models[m.PublicName]; dup {
			return nil, &ValidationError{Field: "models", Msg: "duplicate public_name: " + m.PublicName}
		}
		res.Models[m.PublicName] = m
		if m.Enabled {
			enabled = append(enabled, m.PublicName)
		}
	}
	sort.Strings(enabled)
	res.EnabledModelIDs = enabled

	for i, d := range comboFile.Combos {
		c, err := translateCombo(i, d, res.Models, res.Combos)
		if err != nil {
			return nil, err
		}
		if _, dup := res.Models[c.Name]; dup {
			return nil, &ValidationError{Field: fmt.Sprintf("combos[%d].name", i), Msg: "combo name collides with existing model: " + c.Name}
		}
		if _, dup := res.Combos[c.Name]; dup {
			return nil, &ValidationError{Field: fmt.Sprintf("combos[%d].name", i), Msg: "duplicate combo name: " + c.Name}
		}
		res.Combos[c.Name] = c
		res.ComboOrder = append(res.ComboOrder, c.Name)
	}
	sort.Strings(res.ComboOrder)

	for i, d := range tenantFile.Tenants {
		t, warns, err := translateTenant(i, d, res.Models, res.Combos, envLookup)
		if err != nil {
			return nil, err
		}
		res.Warnings = append(res.Warnings, warns...)
		primaryKey := t.KeyHash
		if primaryKey == "" {
			primaryKey = t.APIKey
		}
		if _, dup := res.TenantsByHash[primaryKey]; dup {
			return nil, &ValidationError{Field: "tenants", Msg: "duplicate key_hash"}
		}
		res.TenantsByHash[primaryKey] = t
		res.TenantOrder = append(res.TenantOrder, primaryKey)
	}
	sort.Strings(res.TenantOrder)

	return res, nil
}

// decodeStrict decodes JSON and rejects unknown fields (fail-closed).
func decodeStrict(file string, raw []byte, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &ParseError{File: file, Err: err}
	}
	return nil
}

// ValidationError describes a single validation failure.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error: %s: %s", e.Field, e.Msg)
}

// EnvLookupOrOS is the production environment lookup.
func EnvLookupOrOS(name string) (string, bool) { return os.LookupEnv(name) }

// NormalizeProtocol resolves an upstream's protocol aliases to their canonical
// domain value, applying the default when it is absent. The catalog builder and
// the exported PinOAuthManagedEndpoints share it, so an alias can never reach the
// OAuth endpoint pin as an unrecognized protocol.
func NormalizeProtocol(proto string) string {
	if proto == "" {
		return DefaultProtocol
	}
	switch proto {
	case "codebuddy", "codebuddy_cn":
		// Bare "codebuddy" is the dormant alias the dashboard's protocol picker
		// used to emit; the adapter's non-international branch, the OAuth
		// provider id, and the probe surface all spell the region "-cn".
		return string(domain.ProtocolCodeBuddyCN)
	case "codebuddy_intl":
		return string(domain.ProtocolCodeBuddyIntl)
	case "grok_cli", "grok", "gcli", "grok-build":
		return string(domain.ProtocolGrokCLI)
	case "opencode_go", "opencode-go", "ocg", "oc":
		return string(domain.ProtocolOpenCode)
	case "qoder", "qodercli", "qoder-cli":
		return string(domain.ProtocolQoder)
	case "antigravity-go", "antigravity_go":
		return string(domain.ProtocolAntigravity)
	}
	return proto
}

func normalizeProtocol(proto string) string {
	return NormalizeProtocol(proto)
}

// pinOAuthManagedEndpoint replaces base_url and base_urls with the
// provider-managed endpoint when the protocol authenticates through an OAuth
// token, dropping any operator-defined fallback host.
//
// The value is replaced rather than rejected: an OAuth bearer token is issued for
// one provider host, so a stale value in an existing file, a database row, or a
// raw API payload must neither retarget that token nor keep the gateway from
// starting.
func pinOAuthManagedEndpoint(d *UpstreamDTO) {
	norm := normalizeProtocol(d.Protocol)
	endpoint, ok := domain.OAuthManagedBaseURL(domain.Protocol(norm))
	if !ok {
		return
	}
	d.Protocol = norm
	d.BaseURL = endpoint
	d.BaseURLs = []string{endpoint}
}

// PinOAuthManagedEndpoints applies the OAuth endpoint pin to every upstream in
// the slice. Build applies the same pin while translating, but the settings
// surface persists the operator payload (to disk and to Turso) before the catalog
// is rebuilt, so it normalizes the payload first: the file, the stored rows, the
// rebuilt snapshot, and the value read back by GET /api/settings then agree.
func PinOAuthManagedEndpoints(upstreams []UpstreamDTO) {
	for i := range upstreams {
		if upstreams[i].Protocol != "" {
			upstreams[i].Protocol = normalizeProtocol(upstreams[i].Protocol)
		}
		pinOAuthManagedEndpoint(&upstreams[i])
	}
}

func translateUpstream(i int, d UpstreamDTO, envLookup func(string) (string, bool)) (*domain.Upstream, error) {
	if !upstreamNameRe.MatchString(d.Name) {
		return nil, &ValidationError{Field: fmt.Sprintf("upstreams[%d].name", i), Msg: "invalid or empty name"}
	}
	proto := normalizeProtocol(d.Protocol)

	switch domain.Protocol(proto) {
	case domain.ProtocolOpenAI, domain.ProtocolAnthropic, domain.ProtocolAntigravity, domain.ProtocolCline, domain.ProtocolCodeBuddyCN, domain.ProtocolCodeBuddyIntl, domain.ProtocolGrokCLI, domain.ProtocolOpenCode, domain.ProtocolQoder:
		// Valid protocol
	default:
		return nil, &ValidationError{Field: fmt.Sprintf("upstreams[%d].protocol", i), Msg: "unsupported protocol: " + proto}
	}

	if domain.Protocol(proto) == domain.ProtocolOpenCode || domain.Protocol(proto) == domain.ProtocolOpenCodeGo {
		isFree := len(d.CredentialPool) == 0 && len(d.APIKeys) == 0 && d.APIKey == ""
		if d.BaseURL == "" && len(d.BaseURLs) == 0 {
			if !isFree || domain.Protocol(proto) == domain.ProtocolOpenCodeGo {
				d.BaseURL = "https://opencode.ai/zen/go/v1"
			} else {
				d.BaseURL = "https://opencode.ai/zen/v1"
			}
			d.BaseURLs = []string{d.BaseURL}
		} else if isFree && d.BaseURL == "https://opencode.ai/zen/go/v1" {
			d.BaseURL = "https://opencode.ai/zen/v1"
			if len(d.BaseURLs) == 1 && d.BaseURLs[0] == "https://opencode.ai/zen/go/v1" {
				d.BaseURLs = []string{d.BaseURL}
			}
		}
	}

	if domain.Protocol(proto) == domain.ProtocolQoder {
		// Qoder's inference host is fixed (api3); the adapter routes jt- tokens to
		// api2 at request time. Default it so a dashboard-created upstream needs no
		// base_url. Kept as a literal to avoid importing the adapter into config.
		if d.BaseURL == "" && len(d.BaseURLs) == 0 {
			d.BaseURL = "https://api3.qoder.sh"
			d.BaseURLs = []string{d.BaseURL}
		}
	}

	// OAuth-authenticated protocols (antigravity, cline, codebuddy) are pinned to
	// the endpoint their provider issued the token for. Every write path — a
	// hand-edited file, a database row, a dashboard payload — funnels through this
	// builder, so normalizing here is what makes the endpoint immutable.
	pinOAuthManagedEndpoint(&d)

	if d.BaseURL == "" && len(d.BaseURLs) > 0 {
		d.BaseURL = d.BaseURLs[0]
	}
	if d.BaseURL != "" && len(d.BaseURLs) == 0 {
		d.BaseURLs = []string{d.BaseURL}
	}
	if len(d.BaseURLs) == 0 && d.BaseURL == "" {
		return nil, &ValidationError{Field: fmt.Sprintf("upstreams[%d].base_url", i), Msg: "base_url or base_urls required"}
	}
	for _, uURL := range d.BaseURLs {
		if !strings.HasPrefix(uURL, "https://") && !(pickBool(d.AllowInsecure, false) && strings.HasPrefix(uURL, "http://")) {
			return nil, &ValidationError{Field: fmt.Sprintf("upstreams[%d].base_url", i), Msg: "must be https (or http with allow_insecure)"}
		}
	}

	strategyStr := d.KeyStrategy
	if strategyStr == "" {
		strategyStr = DefaultKeyStrategy
	}
	strategy := domain.KeyStrategy(strategyStr)
	if strategy != domain.KeyStrategyRoundRobin && strategy != domain.KeyStrategyLeastInflight {
		return nil, &ValidationError{
			Field: fmt.Sprintf("upstreams[%d].key_strategy", i),
			Msg:   fmt.Sprintf("unsupported key_strategy: %s", strategyStr),
		}
	}

	var slots []*domain.KeySlot
	// Deduplicate at the KEY level (resolved secret), not the ref/name level.
	// The same upstream can carry the same credential under different refs
	// (e.g. "dahl-1" and "upstream-dahl-1" from overlapping import sources);
	// those must collapse to a single key slot rather than inflating the ring
	// or failing the build. The first occurrence wins (it keeps its ref and any
	// rps/max_concurrent overrides); later duplicates are skipped.
	seenSecrets := make(map[string]bool)

	if len(d.CredentialPool) > 0 {
		for j, k := range d.CredentialPool {
			secret := k.APIKey
			if secret == "" {
				secret = k.Secret
			}
			ref := k.Ref
			if secret != "" {
				if ref == "" {
					ref = fmt.Sprintf("%s-key-%d", d.Name, j+1)
				}
			} else if strings.HasPrefix(ref, "oauth:") {
				secret = ref
			} else {
				if ref == "" {
					return nil, &ValidationError{
						Field: fmt.Sprintf("upstreams[%d].credential_pool[%d].ref", i, j),
						Msg:   "required",
					}
				}
				// An empty secret is only reinterpreted as an ENV var
				// reference when the ref follows ENV var naming conventions
				// (e.g. OPENAI_KEY_1). DB-sourced credentials use refs like
				// "openai-cred-3" / "openai-key-5"; if such a credential
				// arrives with an empty secret (e.g. the harvester row has not
				// replicated yet) we must NOT silently probe a non-existent ENV
				// var — that produced a misleading "ENV var not set" error and
				// failed the entire snapshot build into zero-config mode.
				if !envVarNameRe.MatchString(ref) {
					return nil, &ValidationError{
						Field: fmt.Sprintf("upstreams[%d].credential_pool[%d].secret", i, j),
						Msg:   "empty secret for credential ref " + ref + " (secret not resolved from config or database)",
					}
				}
				sec, ok := envLookup(ref)
				if !ok || sec == "" {
					return nil, &ValidationError{
						Field: fmt.Sprintf("upstreams[%d].credential_pool[%d].ref", i, j),
						Msg:   "ENV var not set: " + ref,
					}
				}
				secret = sec
			}

			// Skip duplicate credentials (same resolved secret). This collapses
			// the same key imported under different refs into one slot.
			if seenSecrets[secret] {
				continue
			}
			seenSecrets[secret] = true
			rps := pickFloat(k.RPS, 0)
			if rps < 0 {
				return nil, &ValidationError{
					Field: fmt.Sprintf("upstreams[%d].credential_pool[%d].rps", i, j),
					Msg:   "must be non-negative",
				}
			}
			maxConcurrent := pickInt(k.MaxConcurrent, 0)
			if maxConcurrent < 0 {
				return nil, &ValidationError{
					Field: fmt.Sprintf("upstreams[%d].credential_pool[%d].max_concurrent", i, j),
					Msg:   "must be non-negative",
				}
			}
			slots = append(slots, &domain.KeySlot{
				Ref:           ref,
				Secret:        secret,
				RPS:           rps,
				MaxConcurrent: maxConcurrent,
			})
		}
	} else if len(d.APIKeys) > 0 {
		for j, k := range d.APIKeys {
			if k == "" {
				continue
			}
			if seenSecrets[k] {
				continue
			}
			seenSecrets[k] = true
			ref := fmt.Sprintf("%s-key-%d", d.Name, j+1)
			slots = append(slots, &domain.KeySlot{
				Ref:           ref,
				Secret:        k,
				RPS:           pickFloat(d.CredentialRPS, 0),
				MaxConcurrent: pickInt(d.CredentialMaxConcurrent, 0),
			})
		}
		if len(slots) == 0 {
			return nil, &ValidationError{
				Field: fmt.Sprintf("upstreams[%d].api_keys", i),
				Msg:   "at least one non-empty api_key required",
			}
		}
	} else if d.APIKey != "" {
		ref := fmt.Sprintf("%s-key-1", d.Name)
		slots = append(slots, &domain.KeySlot{
			Ref:           ref,
			Secret:        d.APIKey,
			RPS:           pickFloat(d.CredentialRPS, 0),
			MaxConcurrent: pickInt(d.CredentialMaxConcurrent, 0),
		})
	} else if d.CredentialRef != "" {
		var secret string
		if strings.HasPrefix(d.CredentialRef, "oauth:") {
			secret = d.CredentialRef
		} else {
			sec, ok := envLookup(d.CredentialRef)
			if !ok || sec == "" {
				return nil, &ValidationError{
					Field: fmt.Sprintf("upstreams[%d].credential_ref", i),
					Msg:   "ENV var not set: " + d.CredentialRef,
				}
			}
			secret = sec
		}
		rps := pickFloat(d.CredentialRPS, 0)
		if rps < 0 {
			return nil, &ValidationError{
				Field: fmt.Sprintf("upstreams[%d].credential_rps", i),
				Msg:   "must be non-negative",
			}
		}
		maxConcurrent := pickInt(d.CredentialMaxConcurrent, 0)
		if maxConcurrent < 0 {
			return nil, &ValidationError{
				Field: fmt.Sprintf("upstreams[%d].credential_max_concurrent", i),
				Msg:   "must be non-negative",
			}
		}
		slots = append(slots, &domain.KeySlot{
			Ref:           d.CredentialRef,
			Secret:        secret,
			RPS:           rps,
			MaxConcurrent: maxConcurrent,
		})
	}

	// Keyless Free tier auto-provisioning: automatically injects a "opencode-free-public"
	// key slot (KeySlot.Secret = "public") for OpenCode protocol when no credentials are provided.
	if (domain.Protocol(proto) == domain.ProtocolOpenCode || domain.Protocol(proto) == domain.ProtocolOpenCodeGo) && len(slots) == 0 {
		slots = append(slots, &domain.KeySlot{
			Ref:           "opencode-free-public",
			Secret:        "public",
			RPS:           pickFloat(d.CredentialRPS, 0),
			MaxConcurrent: pickInt(d.CredentialMaxConcurrent, 0),
		})
	}

	// Note: an upstream with no credential material at all is allowed. It is
	// created with an empty key ring so operators can add keys later. Any
	// request routed to it will fail closed at forward time (no secret), but the
	// configuration itself is valid and hot-swappable.

	keyRing := domain.NewKeyRing(strategy, slots)
	primarySlot := keyRing.PrimarySlot()

	primaryRef := ""
	var primaryRPS float64
	var primaryMaxConcurrent int
	if primarySlot != nil {
		primaryRef = primarySlot.Ref
		primaryRPS = primarySlot.RPS
		primaryMaxConcurrent = primarySlot.MaxConcurrent
	}

	if d.CredentialRef != "" && d.CredentialRef != primaryRef && !strings.HasPrefix(d.CredentialRef, "oauth:") {
		if _, ok := envLookup(d.CredentialRef); !ok {
			return nil, &ValidationError{
				Field: fmt.Sprintf("upstreams[%d].credential_ref", i),
				Msg:   "ENV var not set: " + d.CredentialRef,
			}
		}
	}

	baseURLs := make([]string, len(d.BaseURLs))
	for idx, u := range d.BaseURLs {
		baseURLs[idx] = strings.TrimRight(u, "/")
	}

	egressMode := strings.ToLower(strings.TrimSpace(d.EgressMode))
	if egressMode == "" {
		egressMode = "direct"
	}
	if egressMode != "direct" && egressMode != "warp" && egressMode != "proxy" {
		return nil, &ValidationError{
			Field: fmt.Sprintf("upstreams[%d].egress_mode", i),
			Msg:   "invalid egress_mode (must be 'direct', 'warp', or 'proxy')",
		}
	}
	proxyURL := strings.TrimSpace(d.ProxyURL)
	if egressMode == "proxy" {
		if proxyURL == "" {
			return nil, &ValidationError{
				Field: fmt.Sprintf("upstreams[%d].proxy_url", i),
				Msg:   "proxy_url is required when egress_mode is 'proxy'",
			}
		}
		pu, err := url.Parse(proxyURL)
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https" && pu.Scheme != "socks5" && pu.Scheme != "socks5h") {
			return nil, &ValidationError{
				Field: fmt.Sprintf("upstreams[%d].proxy_url", i),
				Msg:   "proxy_url must be a valid http, https, socks5, or socks5h URL",
			}
		}
	}

	// Per-status key error rules: validate each row before accepting the build.
	// Threshold must be >= 1 (0 would never fire, and would also read as
	// "disabled" like the legacy field), action must be a known verb, and a
	// cooldown rule should carry a sane duration (the UI default is 300s).
	keyErrorRules := make([]domain.KeyErrorRule, 0, len(d.KeyErrorRules))
	for j, r := range d.KeyErrorRules {
		if r.StatusCode < 400 || r.StatusCode > 599 {
			return nil, &ValidationError{
				Field: fmt.Sprintf("upstreams[%d].key_error_rules[%d].status_code", i, j),
				Msg:   "must be an HTTP error status between 400 and 599",
			}
		}
		if r.Threshold < 1 {
			return nil, &ValidationError{
				Field: fmt.Sprintf("upstreams[%d].key_error_rules[%d].threshold", i, j),
				Msg:   "must be >= 1 (1 fires on the first matching error)",
			}
		}
		action := strings.ToLower(strings.TrimSpace(r.Action))
		if action == "" {
			action = string(ports.KeyActionDeactivate)
		}
		switch ports.KeyAction(action) {
		case ports.KeyActionDeactivate, ports.KeyActionDelete, ports.KeyActionCooldown:
		default:
			return nil, &ValidationError{
				Field: fmt.Sprintf("upstreams[%d].key_error_rules[%d].action", i, j),
				Msg:   "must be one of: deactivate, delete, cooldown",
			}
		}
		var cooldownS int
		if r.CooldownDurationS != nil {
			cooldownS = *r.CooldownDurationS
		}
		if ports.KeyAction(action) == ports.KeyActionCooldown && cooldownS <= 0 {
			cooldownS = 300
		}
		keyErrorRules = append(keyErrorRules, domain.KeyErrorRule{
			StatusCode:        r.StatusCode,
			Threshold:         r.Threshold,
			Action:            action,
			CooldownDurationS: cooldownS,
		})
	}

	return &domain.Upstream{
		Name:                    d.Name,
		Protocol:                domain.Protocol(proto),
		BaseURL:                 strings.TrimRight(d.BaseURL, "/"),
		BaseURLs:                baseURLs,
		CredentialRef:           primaryRef,
		KeyStrategy:             strategy,
		KeyRing:                 keyRing,
		TimeoutMs:               pickInt(d.TimeoutMs, DefaultTimeoutMs),
		IdleTimeoutMs:           pickInt(d.IdleTimeoutMs, DefaultIdleTimeoutMs),
		StreamIdleTimeoutMs:     pickInt(d.StreamIdleTimeoutMs, DefaultStreamIdleTimeoutMs),
		MaxIdleConnsPerHost:     pickInt(d.MaxIdleConnsPerHost, DefaultMaxIdleConnsPerHost),
		MaxConnsPerHost:         pickInt(d.MaxConnsPerHost, DefaultMaxConnsPerHost),
		ExtraHeaders:            d.ExtraHeaders,
		AllowInsecure:           pickBool(d.AllowInsecure, false),
		CredentialRPS:           primaryRPS,
		CredentialMaxConcurrent: primaryMaxConcurrent,
		Disabled:                d.Enabled != nil && !*d.Enabled,
		KeyErrorThreshold:       pickInt(d.KeyErrorThreshold, 0),
		KeyErrorAction:          d.KeyErrorAction,
		KeyCooldownDurationMs:   pickInt(d.KeyCooldownDurationMs, 300000),
		KeyErrorRules:           keyErrorRules,
		ProbeModel:              strings.TrimSpace(d.ProbeModel),
		EgressMode:              egressMode,
		ProxyURL:                proxyURL,
	}, nil
}

func translateModel(i int, d ModelDTO, ups map[string]*domain.Upstream) (*domain.ModelEntry, error) {
	if !modelNameRe.MatchString(d.PublicName) {
		return nil, &ValidationError{Field: fmt.Sprintf("models[%d].public_name", i), Msg: "invalid or empty name"}
	}
	if _, ok := ups[d.Upstream]; !ok {
		return nil, &ValidationError{Field: fmt.Sprintf("models[%d].upstream", i), Msg: "references unknown upstream: " + d.Upstream}
	}
	if d.UpstreamModel == "" {
		return nil, &ValidationError{Field: fmt.Sprintf("models[%d].upstream_model", i), Msg: "required"}
	}
	var caps domain.Capabilities
	if d.Capabilities != nil {
		caps = domain.Capabilities{
			Stream:     d.Capabilities.Stream,
			Tools:      d.Capabilities.Tools,
			Vision:     d.Capabilities.Vision,
			JSONMode:   d.Capabilities.JSONMode,
			Embeddings: d.Capabilities.Embeddings,
			Audio:      d.Capabilities.Audio,
		}
	}

	return &domain.ModelEntry{
		PublicName:    d.PublicName,
		Upstream:      d.Upstream,
		UpstreamModel: d.UpstreamModel,
		Capabilities:  caps,
		MaxContext:    pickInt(d.MaxContext, 0),
		Enabled:       pickBool(d.Enabled, true),
	}, nil
}

func translateCombo(i int, d ComboDTO, models map[string]*domain.ModelEntry, combos map[string]*domain.Combo) (*domain.Combo, error) {
	if !modelNameRe.MatchString(d.Name) {
		return nil, &ValidationError{Field: fmt.Sprintf("combos[%d].name", i), Msg: "invalid or empty name"}
	}
	if len(d.Models) == 0 {
		return nil, &ValidationError{Field: fmt.Sprintf("combos[%d].models", i), Msg: "must contain at least one model"}
	}
	for j, m := range d.Models {
		if _, ok := models[m]; !ok {
			return nil, &ValidationError{
				Field: fmt.Sprintf("combos[%d].models[%d]", i, j),
				Msg:   "references unknown model: " + m,
			}
		}
	}
	strategyStr := strings.ToLower(strings.TrimSpace(d.Strategy))
	if strategyStr == "" {
		strategyStr = string(domain.RoutingStrategyFailover)
	}
	strategy := domain.RoutingStrategy(strategyStr)
	if strategy != domain.RoutingStrategyFailover &&
		strategy != domain.RoutingStrategyRoundRobin &&
		strategy != domain.RoutingStrategyLeastInflight {
		return nil, &ValidationError{
			Field: fmt.Sprintf("combos[%d].strategy", i),
			Msg:   "must be failover, round_robin, or least_inflight",
		}
	}
	return &domain.Combo{
		Name:     d.Name,
		Strategy: strategy,
		Models:   d.Models,
		Enabled:  pickBool(d.Enabled, true),
	}, nil
}

func translateTenant(i int, d TenantDTO, models map[string]*domain.ModelEntry, combos map[string]*domain.Combo, envLookup func(string) (string, bool)) (*domain.Tenant, []string, error) {
	var warns []string
	apiKey := d.APIKey
	if apiKey != "" {
		if d.KeyHash == "" {
			sum := sha256.Sum256([]byte(apiKey))
			d.KeyHash = "sha256:" + hex.EncodeToString(sum[:])
		}
	} else if d.KeyHash != "" {
		if !keyHashRe.MatchString(d.KeyHash) {
			return nil, nil, &ValidationError{Field: fmt.Sprintf("tenants[%d].key_hash", i), Msg: "must be sha256:<64 hex> (or provide api_key)"}
		}
		apiKey = d.KeyHash
	} else {
		return nil, nil, &ValidationError{Field: fmt.Sprintf("tenants[%d].api_key", i), Msg: "api_key or key_hash required"}
	}

	if d.Name == "" {
		return nil, nil, &ValidationError{Field: fmt.Sprintf("tenants[%d].name", i), Msg: "required"}
	}
	status := d.Status
	if status == "" {
		status = string(domain.TenantStatusActive)
	}
	switch domain.TenantStatus(status) {
	case domain.TenantStatusActive, domain.TenantStatusSuspended, domain.TenantStatusExhausted, domain.TenantStatusExpired:
		// Valid
	default:
		return nil, nil, &ValidationError{Field: fmt.Sprintf("tenants[%d].status", i), Msg: "must be active, suspended, exhausted, or expired"}
	}
	allowed := d.AllowedModels
	if len(allowed) == 0 {
		allowed = []string{"*"}
	}
	for _, m := range allowed {
		if m == "*" {
			continue
		}
		_, inModels := models[m]
		_, inCombos := combos[m]
		if !inModels && !inCombos {
			return nil, nil, &ValidationError{Field: fmt.Sprintf("tenants[%d].allowed_models", i), Msg: "unknown model: " + m}
		}
	}
	if d.CredentialRef != "" {
		if _, ok := envLookup(d.CredentialRef); !ok {
			return nil, nil, &ValidationError{Field: fmt.Sprintf("tenants[%d].credential_ref", i), Msg: "ENV var not set: " + d.CredentialRef}
		}
	}

	rl := domain.RateLimit{RPS: DefaultTenantRPS, MaxConcurrent: DefaultTenantMaxConcurrent}
	if d.RateLimit != nil {
		rl.RPS = pickFloat(d.RateLimit.RPS, DefaultTenantRPS)
		rl.MaxConcurrent = pickInt(d.RateLimit.MaxConcurrent, DefaultTenantMaxConcurrent)
		rl.Burst = pickInt(d.RateLimit.Burst, 0)
	}
	if rl.Burst == 0 {
		rl.Burst = int(rl.RPS * 2)
	}
	if rl.RPS < 0 || rl.Burst < 0 || rl.MaxConcurrent < 0 {
		return nil, nil, &ValidationError{Field: fmt.Sprintf("tenants[%d].rate_limit", i), Msg: "values must be non-negative"}
	}
	if rl.RPS > 0 && float64(rl.Burst) < rl.RPS {
		warns = append(warns, fmt.Sprintf("tenant %q: burst (%d) < rps (%.0f)", d.Name, rl.Burst, rl.RPS))
	}
	if rl.MaxConcurrent == 0 {
		warns = append(warns, fmt.Sprintf("tenant %q: max_concurrent=0 (unlimited) risks noisy-neighbor", d.Name))
	}
	if rl.RPS == 0 {
		warns = append(warns, fmt.Sprintf("tenant %q: rps=0 disables rate limiting", d.Name))
	}

	if d.MaxTokens < 0 {
		return nil, nil, &ValidationError{Field: fmt.Sprintf("tenants[%d].max_tokens", i), Msg: "must be non-negative"}
	}
	var exp int64
	if d.ExpiresAt != nil {
		if *d.ExpiresAt < 0 {
			return nil, nil, &ValidationError{Field: fmt.Sprintf("tenants[%d].expires_at", i), Msg: "must be non-negative"}
		}
		exp = *d.ExpiresAt
	}
	usedTokens := new(atomic.Int64)
	if d.UsedTokens > 0 {
		usedTokens.Store(d.UsedTokens)
	}

	return &domain.Tenant{
		APIKey:        apiKey,
		KeyHash:       d.KeyHash,
		Name:          d.Name,
		Status:        domain.TenantStatus(status),
		AllowedModels: allowed,
		CredentialRef: d.CredentialRef,
		RateLimit:     rl,
		MaxTokens:     d.MaxTokens,
		UsedTokens:    usedTokens,
		ExpiresAt:     exp,
		Metadata:      d.Metadata,
	}, warns, nil
}
