package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/dickymuliafiqri/firefly/internal/domain"
)

// FileNameSingbox is the configuration file name for the embedded sing-box
// egress engine. It persists the operator's subscription source and tuning so a
// restart resumes the same node pool instead of booting with no egress.
const FileNameSingbox = "singbox.json"

// SingboxDTO is the persisted sing-box egress configuration.
//
// SubscriptionURL is a capability: a subscription link routinely carries a
// per-account token in its path or query, so a file that carries one is written
// owner-only (SecretFileMode), exactly like tunnel.json with its tunnel token.
type SingboxDTO struct {
	// SubscriptionURL is the single upstream subscription link. Empty means the
	// engine is configured but has no source, which the manager reports as "not
	// ready" and dials direct for.
	SubscriptionURL string `json:"subscription_url,omitempty"`
	// RefreshIntervalS is how often the subscription is re-fetched. Zero means
	// "unset": domain.DefaultSingboxRefreshIntervalS applies. The range policy
	// belongs to the manager (domain.ClampSingboxRefreshIntervalS), so this
	// loader stays a plain strict decoder.
	RefreshIntervalS int `json:"refresh_interval_s,omitempty"`
	// DefaultOutboundTag is the group an upstream with no tag of its own dials.
	// Empty means domain.SingboxDefaultOutboundTag (the URLTest group).
	DefaultOutboundTag string `json:"default_outbound_tag,omitempty"`
	// Strategy selects how the default dial path picks a node: "urltest"
	// (fastest node, sing-box's own group) or "round_robin" (the Manager rotates
	// across healthy nodes). Empty means domain.SingboxStrategyURLTest, which is
	// what keeps a singbox.json written before this field existed valid.
	Strategy string `json:"strategy,omitempty"`
}

// NormalizeDefaultTag trims the operator's default tag and returns the built-in
// URLTest group when it is unset, so callers never branch on "".
func (c SingboxDTO) NormalizeDefaultTag() string {
	if tag := strings.TrimSpace(c.DefaultOutboundTag); domain.ValidSingboxTag(tag) {
		return tag
	}
	return domain.SingboxDefaultOutboundTag
}

// Validate reports whether every persisted field is usable. It is called by both
// the loader and the saver so a value written by the dashboard can never be one
// the loader would later reject.
func (c SingboxDTO) Validate() error {
	if raw := strings.TrimSpace(c.SubscriptionURL); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("singbox config: subscription_url must be a valid http or https URL")
		}
		if u.Host == "" {
			return fmt.Errorf("singbox config: subscription_url must include a host")
		}
	}
	if tag := strings.TrimSpace(c.DefaultOutboundTag); tag != "" && !domain.ValidSingboxTag(tag) {
		return fmt.Errorf("singbox config: default_outbound_tag is not a valid outbound tag")
	}
	if s := strings.TrimSpace(c.Strategy); s != "" && !domain.ValidSingboxStrategy(s) {
		return fmt.Errorf("singbox config: strategy must be one of %q, %q, %q, %q (got %q)",
			domain.SingboxStrategyFallback, domain.SingboxStrategyRoundRobin, domain.SingboxStrategyURLTest, domain.SingboxStrategyConsistentHash, s)
	}
	if c.RefreshIntervalS < 0 {
		return fmt.Errorf("singbox config: refresh_interval_s must not be negative (got %d)", c.RefreshIntervalS)
	}
	return nil
}

// LoadSingbox loads singbox.json. A missing file means the engine was never
// configured, which keeps upgrades backward compatible.
func LoadSingbox(dir string) (SingboxDTO, error) {
	var cfg SingboxDTO
	if dir == "" {
		return cfg, nil
	}

	raw, err := os.ReadFile(filepath.Join(dir, FileNameSingbox))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read singbox config: %w", err)
	}
	if err := decodeStrict("singbox", raw, &cfg); err != nil {
		return cfg, err
	}
	cfg.SubscriptionURL = strings.TrimSpace(cfg.SubscriptionURL)
	cfg.DefaultOutboundTag = strings.TrimSpace(cfg.DefaultOutboundTag)
	cfg.Strategy = strings.TrimSpace(cfg.Strategy)
	if err := cfg.Validate(); err != nil {
		return SingboxDTO{}, err
	}
	return cfg, nil
}

// SaveSingbox writes singbox.json atomically so a crash cannot leave a partial
// configuration behind. The subscription URL is treated as a secret, so a
// configured file is written owner-only (SecretFileMode); an empty configuration
// (no URL) uses the normal config permission.
func SaveSingbox(dir string, cfg SingboxDTO) error {
	if dir == "" {
		return nil
	}
	cfg.SubscriptionURL = strings.TrimSpace(cfg.SubscriptionURL)
	cfg.DefaultOutboundTag = strings.TrimSpace(cfg.DefaultOutboundTag)
	cfg.Strategy = strings.TrimSpace(cfg.Strategy)
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal singbox config: %w", err)
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(dir, ".singbox.json-*")
	if err != nil {
		return fmt.Errorf("create singbox temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	perm := os.FileMode(0o644)
	if cfg.SubscriptionURL != "" {
		perm = SecretFileMode
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set singbox temp file mode: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write singbox temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close singbox temp file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, FileNameSingbox)); err != nil {
		return fmt.Errorf("commit singbox config: %w", err)
	}
	return nil
}
