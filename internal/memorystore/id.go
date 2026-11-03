package memorystore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

// contentHash is the canonical serialization of the fields that identify a
// record's content: everything except the timestamps and the lifecycle block
// (Expiration), which legitimately change without the content changing
// (re-verification, supersession, invalidation).
//
// Fields are marshaled in fixed order through a small struct so the digest
// is stable across Go versions and map-iteration randomization.
type contentHash struct {
	Kind           memory.MemoryKind      `json:"kind"`
	Scope          memory.MemoryScope     `json:"scope"`
	Subject        memory.EntityRef       `json:"subject"`
	Field          string                 `json:"field,omitempty"`
	Value          json.RawMessage        `json:"value"`
	Provenance     memory.Provenance      `json:"provenance"`
	Confidence     memory.ConfidenceLevel `json:"confidence"`
	Volatility     memory.Volatility      `json:"volatility"`
	LastVerifiedAt time.Time              `json:"last_verified_at"`
	StaleAfter     time.Duration          `json:"stale_after"`
}

// DeriveStableID derives a deterministic, content-addressed stable ID for a
// record: "mem-" + the first 16 bytes of the SHA-256 digest of the record's
// canonical content.
//
// The digest covers the content fields (kind, scope, subject, field, value,
// provenance, confidence, volatility, freshness) but deliberately excludes
// CreatedAt/UpdatedAt and the Expiration lifecycle block: re-deriving the ID
// for the same content after a re-verification or a status change yields the
// same ID, which is what makes upsert and supersession link records
// correctly.
//
// Note on collision probability: with 128 digest bits the collision
// probability stays negligible for any realistic local memory size (10^9
// records → ~10^-12 per birthday-paradox estimate); the Store enforces a
// content check on upsert so even a collision is detected and reported,
// never silently merged.
func DeriveStableID(rec *memory.MemoryRecord) memory.MemoryStableID {
	if rec == nil {
		return ""
	}
	ch := contentHash{
		Kind:           rec.Kind,
		Scope:          rec.Scope,
		Subject:        rec.Subject,
		Field:          rec.Field,
		Value:          canonicalRawMessage(rec.Value),
		Provenance:     rec.Provenance,
		Confidence:     rec.Confidence,
		Volatility:     rec.Volatility,
		LastVerifiedAt: rec.Freshness.LastVerifiedAt,
		StaleAfter:     rec.Freshness.StaleAfter,
	}
	data, err := json.Marshal(&ch)
	if err != nil {
		// Unreachable for these types; fail loudly rather than silently
		// deriving from a partial view.
		panic("memorystore: derive content hash: " + err.Error())
	}
	sum := sha256.Sum256(data)
	return memory.MemoryStableID(memory.MemoryStableIDPrefix + hex.EncodeToString(sum[:16]))
}

// canonicalRawMessage re-marshals a raw JSON message into canonical form
// (sorted keys) so that the digest depends only on the JSON content, not on
// the key order it was produced in.
func canonicalRawMessage(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}
