package memory

import (
	"fmt"
	"strings"
	"time"
)

// Volatility enumerates how quickly a memory record is expected to become
// stale. Volatility is an explicit, first-class property: consumers decide
// per-use whether a record's age and volatility are acceptable, instead of
// relying on implicit "freshness of the archive".
type Volatility string

const (
	// VolatileStatic is permanent / static: facts that do not change (e.g. a
	// person's draft class year, the year a team was founded).
	VolatileStatic Volatility = "static"

	// VolatileLow changes rarely (e.g. a player's college, a coach's tenure
	// start).
	VolatileLow Volatility = "low"

	// VolatileMedium changes at a moderate pace (e.g. a player's current
	// team, a season's standing context).
	VolatileMedium Volatility = "medium"

	// VolatileHigh changes often (e.g. injury status, weekly fantasy
	// outlooks).
	VolatileHigh Volatility = "high"

	// VolatileEphemeral is valid only for a short window (e.g. "out for
	// tonight's game").
	VolatileEphemeral Volatility = "ephemeral"
)

// AllVolatilities lists every recognized Volatility in canonical order,
// from most durable to least durable.
var AllVolatilities = []Volatility{
	VolatileStatic,
	VolatileLow,
	VolatileMedium,
	VolatileHigh,
	VolatileEphemeral,
}

// ParseVolatility converts a string into a Volatility.
func ParseVolatility(s string) (Volatility, error) {
	v := Volatility(strings.TrimSpace(s))
	for _, valid := range AllVolatilities {
		if v == valid {
			return v, nil
		}
	}
	return "", fmt.Errorf("unknown volatility %q (expected one of %s)", s, volatilityList())
}

func volatilityList() string {
	parts := make([]string, len(AllVolatilities))
	for i, v := range AllVolatilities {
		parts[i] = string(v)
	}
	return strings.Join(parts, ", ")
}

// IsMoreDurableThan reports whether v is at least as durable as other.
func (v Volatility) IsMoreDurableThan(other Volatility) bool {
	order := map[Volatility]int{
		VolatileStatic:    0,
		VolatileLow:       1,
		VolatileMedium:    2,
		VolatileHigh:      3,
		VolatileEphemeral: 4,
	}
	_, okV := order[v]
	_, okO := order[other]
	if !okV || !okO {
		return false
	}
	return order[v] <= order[other]
}

// Freshness captures the time-stamped currency of a memory record.
//
// A record is only as good as its last verification. Freshness is explicit
// (LastVerifiedAt) rather than derived, so a consumer can distinguish
// "verified three days ago" from "never verified since creation".
type Freshness struct {
	// LastVerifiedAt is when the record was last confirmed against a source
	// (or last set by a human correction). Zero means never verified.
	LastVerifiedAt time.Time `json:"last_verified_at,omitempty"`

	// StaleAfter is the interval after LastVerifiedAt beyond which the record
	// must be re-verified before it can be relied on. Zero means no fixed
	// staleness window; the volatility band alone guides consumers.
	StaleAfter time.Duration `json:"stale_after,omitempty"`
}

// IsStale reports whether the record is past its staleness window at time t.
// A record that was never verified is considered stale for consumers that
// require verification.
//
// The comparison is inclusive: a record is stale when its verification is
// *at or beyond* StaleAfter old. This keeps the staleness windows of
// overlapping policies (a record's own window vs. a policy's band window)
// monotonic under min-composition: a record that is stale under the
// narrower window is still stale when the broader one is applied, so the
// combined judgment equals the stricter of the two.
func (f Freshness) IsStale(t time.Time) bool {
	if f.LastVerifiedAt.IsZero() {
		return true
	}
	if f.StaleAfter <= 0 {
		return false
	}
	return !t.Before(f.LastVerifiedAt.Add(f.StaleAfter))
}

// Expiration models invalidation and expiry of a memory record.
//
// Expiry is distinct from staleness: a stale record may be *re-verified*;
// an expired record is no longer usable at all and should be suppressed
// (or archived) by the subsystem.
type Expiration struct {
	// ExpiresAt is an absolute time after which the record is expired.
	// Zero means "no absolute expiry".
	ExpiresAt time.Time `json:"expires_at,omitempty"`

	// InvalidationStatus is "active" by default, "superseded" when a newer
	// record replaces this one, and "invalidated" when the record was found
	// to be wrong.
	InvalidationStatus InvalidationStatus `json:"invalidation_status,omitempty"`

	// SupersededBy is the stable ID of the record that superseded this one,
	// set when InvalidationStatus is InvalidationSuperseded.
	SupersededBy string `json:"superseded_by,omitempty"`

	// InvalidationReason is a short human-readable explanation, set whenever
	// the record leaves the active state.
	InvalidationReason string `json:"invalidation_reason,omitempty"`
}

// InvalidationStatus enumerates the lifecycle states of a record with
// respect to invalidation.
type InvalidationStatus string

const (
	// InvalidationActive is the default: the record is currently usable.
	InvalidationActive InvalidationStatus = "active"

	// InvalidationSuperseded: a newer record replaced this one. The record
	// is retained for audit but must not inform current decisions.
	InvalidationSuperseded InvalidationStatus = "superseded"

	// InvalidationInvalidated: the record was found to be wrong (typically
	// by a human correction) and must not be used.
	InvalidationInvalidated InvalidationStatus = "invalidated"
)

// AllInvalidationStatuses lists every recognized InvalidationStatus.
var AllInvalidationStatuses = []InvalidationStatus{
	InvalidationActive,
	InvalidationSuperseded,
	InvalidationInvalidated,
}

// ParseInvalidationStatus converts a string into an InvalidationStatus.
func ParseInvalidationStatus(s string) (InvalidationStatus, error) {
	st := InvalidationStatus(strings.TrimSpace(s))
	for _, valid := range AllInvalidationStatuses {
		if st == valid {
			return st, nil
		}
	}
	return "", fmt.Errorf("unknown invalidation status %q", s)
}

// IsExpired reports whether the record is expired or otherwise non-usable at
// time t.
func (e Expiration) IsExpired(t time.Time) bool {
	if e.InvalidationStatus == InvalidationSuperseded || e.InvalidationStatus == InvalidationInvalidated {
		return true
	}
	return !e.ExpiresAt.IsZero() && !t.Before(e.ExpiresAt)
}

// EntityRef is a publication-agnostic reference to a real-world entity.
//
// It intentionally does not inherit any specific entity model: a newsroom
// may resolve the same EntityRef against its own entity store, a dossier,
// or nothing at all.
type EntityRef struct {
	// Kind is the entity category, e.g. "person", "team", "organization",
	// "place", "competition". Free-form but conventional; consumers that
	// care should match against a controlled vocabulary.
	Kind string `json:"kind"`

	// Name is the preferred display name.
	Name string `json:"name"`

	// CanonicalID is a stable identifier within the consumer's entity
	// namespace (e.g. a dossier entity ID or an archive person ID). Empty
	// means the entity is referenced by name only.
	CanonicalID string `json:"canonical_id,omitempty"`

	// Aliases lists known alternative names or identifiers.
	Aliases []string `json:"aliases,omitempty"`
}

// Validate checks the reference's invariants.
func (r EntityRef) Validate() error {
	if r.Kind == "" {
		return fmt.Errorf("entity ref: kind is required")
	}
	if r.Name == "" {
		return fmt.Errorf("entity ref: name is required")
	}
	return nil
}

// Matches reports whether other refers to the same entity, i.e. both carry a
// non-empty CanonicalID and the IDs are equal. Name/alias matching is the
// consumer's concern, not the contract's.
func (r EntityRef) Matches(other EntityRef) bool {
	return r.CanonicalID != "" && r.CanonicalID == other.CanonicalID
}
