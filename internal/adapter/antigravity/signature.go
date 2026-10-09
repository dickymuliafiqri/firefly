package antigravity

import (
	"sort"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// Gemini 3 models (e.g. gemini-3.8-flash) enforce strict validation on tool
// calling: every functionCall part echoed back inside request.contents must
// carry the thoughtSignature that was returned alongside the original call,
// otherwise the upstream rejects the request with:
//
//	"Function call is missing a thought_signature in functionCall parts."
//
// Standard OpenAI clients discard unknown JSON fields, so the signature cannot
// be relied upon to survive the round trip through the client (Cline, Roo, the
// OpenAI SDKs, ...). SignatureStore remembers it server-side, keyed by the
// tool call id — a protocol-required field every client round-trips — for a
// bounded window covering a coding session.
const (
	signatureTTL        = 2 * time.Hour
	signatureMaxEntries = 8192
)

// SignatureStore is a bounded, TTL-evicting map of tool call id -> Gemini 3
// thought signature. It is safe for concurrent use.
type SignatureStore struct {
	mu      sync.RWMutex
	entries map[string]signatureEntry
	seq     uint64
	ttl     time.Duration
	max     int
}

type signatureEntry struct {
	sig  string
	seq  uint64
	when time.Time
}

// NewSignatureStore builds a store with the default TTL and capacity.
func NewSignatureStore() *SignatureStore {
	return &SignatureStore{
		entries: make(map[string]signatureEntry),
		ttl:     signatureTTL,
		max:     signatureMaxEntries,
	}
}

// Record stores the signature for a tool call id. Empty ids or signatures are
// ignored.
func (s *SignatureStore) Record(id, sig string) {
	if s == nil || id == "" || sig == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) >= s.max {
		s.evictLocked(now)
		if len(s.entries) >= s.max {
			// Stay bounded rather than grow unbounded; the oldest quarter was
			// just dropped, so this only happens under sustained pressure.
			return
		}
	}
	s.seq++
	s.entries[id] = signatureEntry{sig: sig, seq: s.seq, when: now}
}

// Lookup returns the remembered signature for a tool call id, or "" when the
// id is unknown or its entry has expired.
func (s *SignatureStore) Lookup(id string) string {
	if s == nil || id == "" {
		return ""
	}
	now := time.Now()
	s.mu.RLock()
	entry, ok := s.entries[id]
	s.mu.RUnlock()
	if !ok {
		return ""
	}
	if now.Sub(entry.when) > s.ttl {
		s.mu.Lock()
		if cur, exists := s.entries[id]; exists && now.Sub(cur.when) > s.ttl {
			delete(s.entries, id)
		}
		s.mu.Unlock()
		return ""
	}
	return entry.sig
}

// Len reports the number of live entries (used by tests).
func (s *SignatureStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

// evictLocked drops expired entries first, then the oldest quarter of what
// remains when the store is still at capacity. Callers must hold s.mu.
func (s *SignatureStore) evictLocked(now time.Time) {
	for id, e := range s.entries {
		if now.Sub(e.when) > s.ttl {
			delete(s.entries, id)
		}
	}
	if len(s.entries) < s.max {
		return
	}
	drop := s.max / 4
	if drop == 0 {
		drop = 1
	}
	type keyed struct {
		id  string
		seq uint64
	}
	all := make([]keyed, 0, len(s.entries))
	for id, e := range s.entries {
		all = append(all, keyed{id, e.seq})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].seq < all[j].seq })
	for _, k := range all[:drop] {
		delete(s.entries, k.id)
	}
}

// recordToolSignaturesFromOpenAI stores signatures carried by a translated
// non-streaming OpenAI response so they can be replayed on the next turn.
func recordToolSignaturesFromOpenAI(store *SignatureStore, openAIBody []byte) {
	if store == nil || len(openAIBody) == 0 || !gjson.ValidBytes(openAIBody) {
		return
	}
	gjson.GetBytes(openAIBody, "choices.0.message.tool_calls").ForEach(func(_, tc gjson.Result) bool {
		id := tc.Get("id").String()
		sig := tc.Get("thought_signature").String()
		if id != "" && sig != "" {
			store.Record(id, sig)
		}
		return true
	})
}
