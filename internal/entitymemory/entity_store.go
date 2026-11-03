package entitymemory

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

// recordPrefix marks an entity memory record in the KindEntity namespace and
// lets a Store implementation that shares its database with other KindEntity
// writers (or other entities) refuse to clobber records it does not own.
const recordPrefix = "entity:"

// recordKind and recordScope are the v0.7 memory taxonomy slots every entity
// record occupies.
const (
	recordKind  = memory.KindEntity
	recordScope = memory.ScopeEntity
)

// Entity is a canonical, reusable memory of a real-world entity.
//
// An entity is the stable half of the ticket's model: its identity (canonical
// ID, type, canonical name, aliases) plus its attributes, each of which
// carries its own volatility and provenance. Entities persist through the
// v0.7 Store as one KindEntity MemoryRecord; the Entity type is the
// application-level projection of that record.
type Entity struct {
	// ID is the canonical, stable identifier of the entity within this
	// memory. It is caller-chosen (e.g. "player-jordan-rivers") and must be
	// unique within the store. Resolution prefers matching against IDs
	// before any name or alias.
	ID string `json:"id"`

	// Type is the entity's category (see EntityType).
	Type EntityType `json:"type"`

	// CanonicalName is the entity's preferred display name. Exact (folded)
	// matches against it are a resolution tier.
	CanonicalName string `json:"canonical_name"`

	// Aliases are known alternative names or identifiers (nicknames, former
	// names, handle names). Exact (folded) matches against them are a
	// resolution tier below canonical names.
	Aliases []string `json:"aliases,omitempty"`

	// Attributes are the entity's attributes with per-attribute volatility
	// and provenance (see Attribute).
	Attributes []Attribute `json:"attributes,omitempty"`

	// Confidence rates the entity as a whole (its identity, not its
	// attributes). Attributes carry their own provenance.
	Confidence memory.ConfidenceLevel `json:"confidence"`

	// VerifiedBy is the identifier of the process or component that last
	// confirmed the entity's identity.
	VerifiedBy string `json:"verified_by,omitempty"`

	// CreatedAt is when the entity was first written to memory (UTC).
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the entity was last modified (UTC).
	UpdatedAt time.Time `json:"updated_at"`

	// LastVerifiedAt is when the entity's identity was last confirmed
	// (zero means never verified).
	LastVerifiedAt time.Time `json:"last_verified_at,omitempty"`

	// memoryID is the stable ID of the backing memory record; assigned on
	// first persist and reused for all later writes of this entity.
	memoryID memory.MemoryStableID
}

// Validate checks the entity's invariants:
//
//   - ID, Type, and CanonicalName are present and well-formed;
//   - no alias (or canonical name) collides with another entity is *not*
//     checkable here (single-entity view); that is the Store's job;
//   - every attribute is valid and keys are unique;
//   - timestamps are consistent.
func (e *Entity) Validate() error {
	var errs []error
	if e == nil {
		return fmt.Errorf("entity is nil")
	}
	if e.ID == "" {
		errs = append(errs, fmt.Errorf("entity: id is required"))
	}
	if _, err := ParseEntityType(string(e.Type)); err != nil {
		errs = append(errs, err)
	}
	if strings.TrimSpace(e.CanonicalName) == "" {
		errs = append(errs, fmt.Errorf("entity %s: canonical_name is required", e.ID))
	}
	if _, err := memory.ParseConfidenceLevel(string(e.Confidence)); err != nil {
		errs = append(errs, err)
	}

	seenAttrs := make(map[string]bool)
	seenNames := make(map[string]bool)
	if e.CanonicalName != "" {
		seenNames[foldName(e.CanonicalName)] = true
	}
	for _, a := range e.Aliases {
		if strings.TrimSpace(a) == "" {
			errs = append(errs, fmt.Errorf("entity %s: alias must not be empty", e.ID))
			continue
		}
		f := foldName(a)
		if seenNames[f] {
			errs = append(errs, fmt.Errorf("entity %s: alias %q duplicates canonical name or another alias", e.ID, a))
		}
		seenNames[f] = true
	}
	for _, a := range e.Attributes {
		if err := a.validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		if seenAttrs[normKey(a.Key)] {
			errs = append(errs, fmt.Errorf("entity %s: duplicate attribute key %q", e.ID, a.Key))
		}
		seenAttrs[normKey(a.Key)] = true
	}
	if e.CreatedAt.IsZero() {
		errs = append(errs, fmt.Errorf("entity %s: created_at is required", e.ID))
	}
	if e.UpdatedAt.IsZero() {
		errs = append(errs, fmt.Errorf("entity %s: updated_at is required", e.ID))
	}
	if e.CreatedAt.After(e.UpdatedAt) {
		errs = append(errs, fmt.Errorf("entity %s: created_at %s must not be after updated_at %s", e.ID, e.CreatedAt, e.UpdatedAt))
	}
	if e.LastVerifiedAt.After(e.UpdatedAt) {
		errs = append(errs, fmt.Errorf("entity %s: last_verified_at %s must not be after updated_at %s", e.ID, e.LastVerifiedAt, e.UpdatedAt))
	}
	return errors.Join(errs...)
}

// record builds the v0.7 MemoryRecord this entity persists as.
//
// The record's content is the entity's identity (Subject) plus its
// attributes, serialized into the KindEntity value payload. Each attribute
// is encoded as a JSON string inside EntityValue.Fields (the v0.7
// map[string]string shape), so the production sanitizer's redactValue path
// (which decodes EntityValue, sanitizes each Fields string, and re-encodes)
// preserves the attribute data. The v0.7 Freshness/Expiration blocks carry
// the identity-level verification stamp.
//
// The Store assigns the stable ID (content-addressed) on first write.
func (e *Entity) record(now time.Time) *memory.MemoryRecord {
	now = memory.TimeToUTC(now)
	rec := memory.NewMemoryRecord(now)
	rec.Kind = recordKind
	rec.Scope = recordScope
	rec.Subject = memory.EntityRef{
		Kind:        string(e.Type),
		Name:        e.CanonicalName,
		CanonicalID: e.ID,
		Aliases:     e.Aliases,
	}
	rec.Field = recordPrefix + e.ID
	rec.Confidence = e.Confidence
	rec.Volatility = memory.VolatileStatic // identity facts; attribute volatility lives on the attributes
	rec.Freshness = memory.Freshness{
		LastVerifiedAt: e.LastVerifiedAt,
	}
	rec.Provenance = memory.Provenance{
		VerifiedBy: e.VerifiedBy,
	}
	// Encode attributes into the v0.7 EntityValue.Fields (map[string]string):
	// each attribute is a JSON string keyed by its (original-case) key.
	fields := make(map[string]string, len(e.Attributes))
	for _, a := range e.Attributes {
		data, err := json.Marshal(a)
		if err != nil {
			// Unreachable for these types.
			panic("entitymemory: encode attribute: " + err.Error())
		}
		fields[a.Key] = string(data)
	}
	ev := memory.EntityValue{Fields: fields}
	_ = rec.EncodeValue(ev, now)
	return rec
}

// entityFromRecord projects a v0.7 memory record back into an Entity.
//
// It returns an error for records that are not entity records written by
// this package (wrong kind, or a record without the entity: field prefix).
//
// Attributes are decoded from EntityValue.Fields: each field value is a
// JSON-encoded Attribute (see record). Fields whose values do not decode as
// Attributes (written by other writers) are skipped.
func entityFromRecord(rec *memory.MemoryRecord) (*Entity, error) {
	if rec == nil {
		return nil, fmt.Errorf("entitymemory: nil record")
	}
	if rec.Kind != recordKind {
		return nil, fmt.Errorf("entitymemory: record %s has kind %s, want %s", rec.StableID, rec.Kind, recordKind)
	}
	id, ok := strings.CutPrefix(rec.Field, recordPrefix)
	if !ok || id == "" {
		return nil, fmt.Errorf("entitymemory: record %s is not an entity record (field %q)", rec.StableID, rec.Field)
	}
	ev := memory.EntityValue{}
	if v, err := rec.DecodeValue(); err == nil {
		if ent, ok2 := v.(memory.EntityValue); ok2 {
			ev = ent
		}
	}
	e := &Entity{
		ID:             rec.Subject.CanonicalID,
		Type:           EntityType(rec.Subject.Kind),
		CanonicalName:  rec.Subject.Name,
		Aliases:        append([]string(nil), rec.Subject.Aliases...),
		Attributes:     make([]Attribute, 0, len(ev.Fields)),
		Confidence:     rec.Confidence,
		VerifiedBy:     rec.Provenance.VerifiedBy,
		CreatedAt:      rec.CreatedAt,
		UpdatedAt:      rec.UpdatedAt,
		LastVerifiedAt: rec.Freshness.LastVerifiedAt,
		memoryID:       rec.StableID,
	}
	if e.ID == "" {
		e.ID = id
	}
	for _, k := range sortedKeysString(ev.Fields) {
		var a Attribute
		if err := json.Unmarshal([]byte(ev.Fields[k]), &a); err != nil {
			// Not an attribute written by this package; skip.
			continue
		}
		e.Attributes = append(e.Attributes, a)
	}
	return e, nil
}

// sortedKeysString returns the sorted keys of a string-keyed map.
func sortedKeysString[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// clone returns a deep copy of the entity (identity and attributes).
//
// Returned entities (Get, All, resolution candidates) are copies: callers
// may mutate them freely without affecting the store.
func (e *Entity) clone() *Entity {
	c := *e
	c.Aliases = append([]string(nil), e.Aliases...)
	c.Attributes = append([]Attribute(nil), e.Attributes...)
	for i := range c.Attributes {
		c.Attributes[i].Sources = append([]memory.MemorySource(nil), e.Attributes[i].Sources...)
	}
	return &c
}
