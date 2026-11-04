package entitymemory

import (
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

// Attribute is one attribute of an entity with its own volatility, freshness,
// and provenance.
//
// Attributes — not entities — are the unit of provenance and freshness. An
// entity's "college" (rarely changing) and its "current team" (changing
// weekly) are different attributes with different volatility bands and
// different staleness windows, even though they live on the same entity.
// This keeps the per-field provenance/freshness required by the ticket
// without fragmenting one entity into many memory records.
type Attribute struct {
	// Key is the attribute name, e.g. "current_team", "position",
	// "college", "tenure_start". It is the stable identifier of the
	// attribute within the entity; key comparison is case-insensitive.
	Key string `json:"key"`

	// Value is the attribute's current value.
	Value string `json:"value"`

	// Volatility is the expected change rate of this attribute
	// (see memory.Volatility: static, low, medium, high, ephemeral).
	Volatility memory.Volatility `json:"volatility"`

	// LastVerifiedAt is when this attribute was last confirmed against a
	// source (or last set by a human correction). Zero means never
	// verified: the attribute is stale for consumers that require
	// verification.
	LastVerifiedAt time.Time `json:"last_verified_at,omitempty"`

	// StaleAfter is the interval after LastVerifiedAt beyond which the
	// attribute must be re-verified before it can be relied on. Zero means
	// no fixed staleness window; the volatility band alone guides consumers.
	StaleAfter time.Duration `json:"stale_after,omitempty"`

	// Sources records where the value came from (per-field provenance).
	Sources []memory.MemorySource `json:"sources,omitempty"`
}

// normKey is the canonical form of an attribute key.
func normKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// isStale reports whether the attribute is past its staleness window at t.
// An attribute that was never verified is stale (mirrors memory.Freshness).
// The window boundary is inclusive (fresh at exactly StaleAfter, stale
// immediately after), mirroring memory.Freshness.IsStale.
func (a Attribute) isStale(t time.Time) bool {
	if a.LastVerifiedAt.IsZero() {
		return true
	}
	if a.StaleAfter <= 0 {
		return false
	}
	return !t.Before(a.LastVerifiedAt.Add(a.StaleAfter))
}

// FreshnessStatus returns the attribute's freshness status (fresh, stale,
// or expired) at time t under the policy:
//
//   - a never-verified attribute is always stale (verification currency is
//     explicit, mirroring memory.Freshness);
//   - the attribute's own StaleAfter window, when set, always wins over the
//     policy's band window;
//   - the policy's volatility-band expiry window, when set, bounds the
//     attribute's lifetime from its last verification; past it, the
//     attribute is expired (no longer usable, not merely stale).
//
// The calculation is deterministic: the same attribute, policy, and time
// always produce the same status.
func (a Attribute) FreshnessStatus(policy memory.FreshnessPolicy, t time.Time) memory.FreshnessAssessment {
	assess := memory.FreshnessAssessment{
		Status:     memory.StatusFresh,
		StaleAfter: policy.StalenessWindow(a.Volatility),
	}
	if a.StaleAfter > 0 {
		assess.StaleAfter = a.StaleAfter
	}
	if a.LastVerifiedAt.IsZero() {
		assess.Status = memory.StatusStale
		return assess
	}
	// Absolute expiry: the policy's band-bound lifetime from the last
	// verification bounds the attribute's usability.
	if w := policy.ExpiryWindow(a.Volatility); w > 0 && !t.Before(a.LastVerifiedAt.Add(w)) {
		assess.ExpiresAt = a.LastVerifiedAt.Add(w)
		assess.Status = memory.StatusExpired
		return assess
	}
	// Staleness: the attribute's own window is the stricter of the two and
	// always wins; the policy's band window applies to any age left over.
	// The boundary is inclusive, mirroring memory.Freshness.IsStale.
	stale := a.isStale(t)
	if !stale && assess.StaleAfter > 0 {
		stale = !t.Before(a.LastVerifiedAt.Add(assess.StaleAfter))
	}
	if stale {
		assess.Status = memory.StatusStale
		return assess
	}
	return assess
}

// IsStaleAt reports whether the attribute is stale or expired at time t
// under the policy (i.e. it must be re-verified before its value is used as
// current factual context).
func (a Attribute) IsStaleAt(policy memory.FreshnessPolicy, t time.Time) bool {
	return a.FreshnessStatus(policy, t).ReVerify()
}

// validate checks the attribute's invariants.
func (a Attribute) validate() error {
	if normKey(a.Key) == "" {
		return fmt.Errorf("attribute: key is required")
	}
	if a.Value == "" {
		return fmt.Errorf("attribute %q: value is required", a.Key)
	}
	if _, err := memory.ParseVolatility(string(a.Volatility)); err != nil {
		return fmt.Errorf("attribute %q: %w", a.Key, err)
	}
	for i, s := range a.Sources {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("attribute %q: source[%d]: %w", a.Key, i, err)
		}
	}
	if a.StaleAfter < 0 {
		return fmt.Errorf("attribute %q: stale_after must not be negative", a.Key)
	}
	if a.LastVerifiedAt.IsZero() && a.StaleAfter > 0 {
		return fmt.Errorf("attribute %q: stale_after without last_verified_at is meaningless (a never-verified attribute is stale regardless)", a.Key)
	}
	return nil
}
