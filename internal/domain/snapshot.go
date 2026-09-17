package domain

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// CatalogSnapshot is an immutable, fully-resolved view of all configuration.
// Once built it MUST NOT be mutated; reloads build a new snapshot and swap it
// atomically. This allows lock-free reads on the hot path.
type CatalogSnapshot struct {
	generation uint64

	upstreams     map[string]*Upstream
	upstreamOrder []string

	models          map[string]*ModelEntry
	enabledModelIDs []string

	tenantsByHash map[string]*Tenant
	tenantOrder   []string

	combos     map[string]*Combo
	comboOrder []string

	tokenSaver TokenSaverConfig
}

// SnapshotOption configures optional fields on a CatalogSnapshot.
type SnapshotOption func(*CatalogSnapshot)

// WithCombos attaches combos and their order to the snapshot.
func WithCombos(combos map[string]*Combo, comboOrder []string) SnapshotOption {
	return func(s *CatalogSnapshot) {
		if combos != nil {
			s.combos = combos
		}
		s.comboOrder = comboOrder
	}
}

// WithTokenSaver attaches token saver configuration to the snapshot.
func WithTokenSaver(cfg TokenSaverConfig) SnapshotOption {
	return func(s *CatalogSnapshot) {
		s.tokenSaver = cfg
	}
}

// TokenSaver returns the token saver configuration for this snapshot.
func (s *CatalogSnapshot) TokenSaver() TokenSaverConfig {
	return s.tokenSaver
}

// NewCatalogSnapshot constructs a snapshot from already-validated parts. It
// takes ownership of the maps and must be called only by the registry builder
// after validation has passed.
func NewCatalogSnapshot(
	generation uint64,
	upstreams map[string]*Upstream,
	upstreamOrder []string,
	models map[string]*ModelEntry,
	enabledModelIDs []string,
	tenantsByHash map[string]*Tenant,
	tenantOrder []string,
	opts ...SnapshotOption,
) *CatalogSnapshot {
	s := &CatalogSnapshot{
		generation:      generation,
		upstreams:       upstreams,
		upstreamOrder:   upstreamOrder,
		models:          models,
		enabledModelIDs: enabledModelIDs,
		tenantsByHash:   tenantsByHash,
		tenantOrder:     tenantOrder,
		combos:          make(map[string]*Combo),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Generation returns the monotonic generation id of this snapshot.
func (s *CatalogSnapshot) Generation() uint64 { return s.generation }

// Upstream returns the upstream with the given name, if present.
func (s *CatalogSnapshot) Upstream(name string) (*Upstream, bool) {
	u, ok := s.upstreams[name]
	return u, ok
}

// UpstreamNames returns upstream names in stable order.
func (s *CatalogSnapshot) UpstreamNames() []string {
	out := make([]string, len(s.upstreamOrder))
	copy(out, s.upstreamOrder)
	return out
}

// Model returns the catalog entry for a public model name, if present (even if
// disabled). Callers that must honor Enabled should check it explicitly.
func (s *CatalogSnapshot) Model(publicName string) (*ModelEntry, bool) {
	m, ok := s.models[publicName]
	return m, ok
}

// EnabledModels returns the ids of enabled models in stable order. This is what
// the /v1/models endpoint exposes.
func (s *CatalogSnapshot) EnabledModels() []string {
	out := make([]string, len(s.enabledModelIDs))
	copy(out, s.enabledModelIDs)
	return out
}

// Combo returns the combo with the given name, if present.
func (s *CatalogSnapshot) Combo(name string) (*Combo, bool) {
	c, ok := s.combos[name]
	return c, ok
}

// ComboNames returns combo names in stable order.
func (s *CatalogSnapshot) ComboNames() []string {
	out := make([]string, len(s.comboOrder))
	copy(out, s.comboOrder)
	return out
}

// AllCombos returns all combos in stable order.
func (s *CatalogSnapshot) AllCombos() []*Combo {
	out := make([]*Combo, 0, len(s.comboOrder))
	for _, name := range s.comboOrder {
		if c, ok := s.combos[name]; ok {
			out = append(out, c)
		}
	}
	return out
}

// TenantByHash returns the tenant for a key hash, if present.
func (s *CatalogSnapshot) TenantByHash(keyHash string) (*Tenant, bool) {
	t, ok := s.tenantsByHash[keyHash]
	return t, ok
}

// TenantHashes returns tenant key hashes in stable order.
func (s *CatalogSnapshot) TenantHashes() []string {
	out := make([]string, len(s.tenantOrder))
	copy(out, s.tenantOrder)
	return out
}

// ResolveTarget maps a tenant + requested public model name to a routing
// Target. Errors carry a machine-readable kind so the transport layer can map
// them to OpenAI-shaped status codes.
func (s *CatalogSnapshot) ResolveTarget(tenant *Tenant, publicName string) (*Target, error) {
	t, _, err := s.ResolveTargetWithBreaker(tenant, publicName, nil)
	return t, err
}

// ResolveTargetWithBreaker resolves a Target for tenant and publicName, checking
// upstream availability via canUseUpstream and KeyRing slot availability.
// If publicName is a Combo, it evaluates the combo's member models and applies
// the combo's routing strategy (round_robin, least_inflight, or failover).
// Otherwise it resolves the concrete model directly.
func (s *CatalogSnapshot) ResolveTargetWithBreaker(
	tenant *Tenant,
	publicName string,
	canUseUpstream func(upstreamName string) bool,
) (*Target, bool, error) {
	if tenant == nil {
		return nil, false, &ResolveError{Kind: ResolveTenantInvalid}
	}

	// 1. Check if publicName is a Combo.
	if combo, isCombo := s.combos[publicName]; isCombo {
		if !combo.Enabled {
			return nil, false, &ResolveError{Kind: ResolveModelNotFound, Model: publicName}
		}
		if !tenant.AllowsModel(publicName) {
			return nil, false, &ResolveError{Kind: ResolveModelForbidden, Model: publicName, Tenant: tenant.Name}
		}
		if len(combo.Models) == 0 {
			return nil, false, &ResolveError{Kind: ResolveModelNotFound, Model: publicName}
		}

		nowNano := time.Now().UnixNano()
		var hadExhaustedKey bool

		// tryModel evaluates a concrete model candidate within the combo.
		tryModel := func(modelName string) (*Target, bool, bool) {
			entry, ok := s.models[modelName]
			if !ok || !entry.Enabled {
				return nil, false, false
			}
			up, ok := s.upstreams[entry.Upstream]
			if !ok || up.Disabled {
				return nil, false, false
			}
			if canUseUpstream != nil && !canUseUpstream(up.Name) {
				return nil, false, false
			}

			cred := tenant.CredentialRef
			var slot *KeySlot
			if cred == "" {
				if up.KeyRing != nil && len(up.KeyRing.Slots) > 0 {
					selectedSlot, err := up.KeyRing.SelectKey(nowNano)
					if err != nil {
						return nil, true, false
					}
					slot = selectedSlot
					cred = slot.Ref
				} else {
					cred = up.CredentialRef
				}
			} else if up.KeyRing != nil {
				slot = up.KeyRing.SlotByRef(cred)
				if slot != nil && !slot.IsAvailable(nowNano) {
					return nil, true, false
				}
			}

			target := &Target{
				Upstream:      up,
				UpstreamModel: entry.UpstreamModel,
				CredentialRef: cred,
				KeySlot:       slot,
			}
			return target, false, true
		}

		switch combo.Strategy {
		case RoutingStrategyLeastInflight:
			var bestTarget *Target
			bestInflight := int64(math.MaxInt64)
			var bestIdx int

			for idx, mName := range combo.Models {
				tgt, keyExhausted, ok := tryModel(mName)
				if keyExhausted {
					hadExhaustedKey = true
				}
				if !ok {
					continue
				}
				inflight := tgt.Upstream.Inflight.Load()
				if inflight < bestInflight {
					bestInflight = inflight
					bestTarget = tgt
					bestIdx = idx
					if inflight == 0 {
						break
					}
				}
			}

			if bestTarget != nil {
				fallbackUsed := (bestIdx != 0)
				return bestTarget, fallbackUsed, nil
			}

		case RoutingStrategyRoundRobin:
			n := len(combo.Models)
			start := combo.NextCursor()
			for i := 0; i < n; i++ {
				idx := int((start + uint64(i)) % uint64(n))
				mName := combo.Models[idx]
				tgt, keyExhausted, ok := tryModel(mName)
				if keyExhausted {
					hadExhaustedKey = true
				}
				if ok {
					fallbackUsed := (idx != 0)
					return tgt, fallbackUsed, nil
				}
			}

		default: // RoutingStrategyFailover
			for idx, mName := range combo.Models {
				tgt, keyExhausted, ok := tryModel(mName)
				if keyExhausted {
					hadExhaustedKey = true
				}
				if ok {
					fallbackUsed := (idx != 0)
					return tgt, fallbackUsed, nil
				}
			}
		}

		if hadExhaustedKey {
			return nil, false, ErrAllKeysExhausted
		}
		return nil, false, &ResolveError{Kind: ResolveUpstreamUnavailable, Model: publicName}
	}

	// 2. Direct model resolution (non-combo).
	entry, ok := s.models[publicName]
	if !ok || !entry.Enabled {
		return nil, false, &ResolveError{Kind: ResolveModelNotFound, Model: publicName}
	}
	if !tenant.AllowsModel(publicName) {
		return nil, false, &ResolveError{Kind: ResolveModelForbidden, Model: publicName, Tenant: tenant.Name}
	}

	up, ok := s.upstreams[entry.Upstream]
	if !ok || up.Disabled {
		return nil, false, &ResolveError{Kind: ResolveUpstreamUnavailable, Model: publicName}
	}
	if canUseUpstream != nil && !canUseUpstream(up.Name) {
		return nil, false, &ResolveError{Kind: ResolveUpstreamUnavailable, Model: publicName}
	}

	nowNano := time.Now().UnixNano()
	cred := tenant.CredentialRef
	var slot *KeySlot
	if cred == "" {
		if up.KeyRing != nil && len(up.KeyRing.Slots) > 0 {
			selectedSlot, err := up.KeyRing.SelectKey(nowNano)
			if err != nil {
				return nil, false, ErrAllKeysExhausted
			}
			slot = selectedSlot
			cred = slot.Ref
		} else {
			cred = up.CredentialRef
		}
	} else if up.KeyRing != nil {
		slot = up.KeyRing.SlotByRef(cred)
		if slot != nil && !slot.IsAvailable(nowNano) {
			return nil, false, ErrAllKeysExhausted
		}
	}

	target := &Target{
		Upstream:      up,
		UpstreamModel: entry.UpstreamModel,
		CredentialRef: cred,
		KeySlot:       slot,
	}
	return target, false, nil
}

// ResolveKind classifies a resolution failure for status-code mapping.
type ResolveKind int

const (
	ResolveModelNotFound ResolveKind = iota + 1
	ResolveModelForbidden
	ResolveTenantInvalid
	ResolveUpstreamUnavailable
)

// ResolveError is returned by ResolveTarget with a classified kind.
type ResolveError struct {
	Kind   ResolveKind
	Model  string
	Tenant string
}

func (e *ResolveError) Error() string {
	switch e.Kind {
	case ResolveModelNotFound:
		return fmt.Sprintf("model %q not found", e.Model)
	case ResolveModelForbidden:
		return fmt.Sprintf("tenant %q is not allowed to use model %q", e.Tenant, e.Model)
	case ResolveTenantInvalid:
		return "tenant is missing or invalid"
	case ResolveUpstreamUnavailable:
		return fmt.Sprintf("no available upstream for model %q (upstream disabled or circuit open)", e.Model)
	default:
		return "model resolution failed"
	}
}

// SortedPublicModelIDs returns all catalog model ids (enabled or not) sorted.
// Useful for diagnostics and tests.
func (s *CatalogSnapshot) SortedPublicModelIDs() []string {
	out := make([]string, 0, len(s.models))
	for name := range s.models {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
