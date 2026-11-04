package memory

import (
	"fmt"
	"time"
)

// FreshnessStatus is the trichotomy every freshness calculation produces:

// a record is either fresh (no action needed), stale (still usable as a
// research pointer, but must be re-verified before it is used as current
// factual context), or expired (no longer usable at all).
type FreshnessStatus int

const (
	// StatusFresh: the record is within its freshness window.
	StatusFresh FreshnessStatus = iota

	// StatusStale: the record is past its staleness window. Stale records
	// may guide research and prioritization, but must be re-verified
	// before they are used as current factual context.
	StatusStale

	// StatusExpired: the record is no longer usable (past its absolute
	// expiry, superseded, or invalidated).
	StatusExpired
)

// String returns the status name.
func (s FreshnessStatus) String() string {
	switch s {
	case StatusFresh:
		return "fresh"
	case StatusStale:
		return "stale"
	case StatusExpired:
		return "expired"
	}
	return "unknown"
}

// FreshnessPolicy is the generic, volatility-aware freshness policy for
// memory facts.
//
// It is domain-agnostic by design: it speaks only in Volatility bands and
// durations, and carries no field names, entity types, or other
// domain vocabulary. A newsroom maps its own attributes onto volatility
// bands (e.g. a rostership onto high, a draft year onto static) and then
// applies the policy; the policy itself never learns what the attributes
// mean.
//
// A policy is immutable: NewFreshnessPolicy returns a ready-to-use value
// that may be shared across goroutines without synchronization.
type FreshnessPolicy struct {
	// Durations maps each Volatility band to the interval after
	// LastVerifiedAt beyond which a record of that band is stale.
	// Zero or missing entries mean "no routine staleness": records in that
	// band never become stale on a timer (a never-verified record is still
	// stale, so verification currency is always explicit).
	Durations map[Volatility]time.Duration

	// Expiry maps each Volatility band to the maximum lifetime of a record
	// in that band: LastVerifiedAt + Expiry[v] is the absolute moment the
	// record expires (is no longer usable, not merely stale). Zero or
	// missing entries mean no absolute expiry for that band.
	Expiry map[Volatility]time.Duration
}

// DefaultFreshnessPolicy is the out-of-the-box policy. Its durations are
// reasonable defaults for a newsroom workflow, not domain constants:
// every entry can be replaced by the operator without touching the engine.
var DefaultFreshnessPolicy = FreshnessPolicy{
	Durations: map[Volatility]time.Duration{
		VolatileStatic:    0, // no routine expiry
		VolatileLow:       90 * 24 * time.Hour,
		VolatileMedium:    14 * 24 * time.Hour,
		VolatileHigh:      24 * time.Hour,
		VolatileEphemeral: 12 * time.Hour,
	},
	Expiry: map[Volatility]time.Duration{
		VolatileStatic:    0, // no routine expiry
		VolatileLow:       365 * 24 * time.Hour,
		VolatileMedium:    180 * 24 * time.Hour,
		VolatileHigh:      30 * 24 * time.Hour,
		VolatileEphemeral: 2 * 24 * time.Hour,
	},
}

// NewFreshnessPolicy returns a policy built from the two maps, copying them
// so callers cannot mutate the policy afterwards.
//
// The maps must not contain unknown Volatility values; NewFreshnessPolicy
// returns an error otherwise (the policy is validated once, at
// construction, so per-record application is a cheap lookup).
func NewFreshnessPolicy(durations, expiry map[Volatility]time.Duration) (FreshnessPolicy, error) {
	copyDurations := make(map[Volatility]time.Duration, len(durations))
	for v, d := range durations {
		copyDurations[v] = d
	}
	copyExpiry := make(map[Volatility]time.Duration, len(expiry))
	for v, d := range expiry {
		copyExpiry[v] = d
	}
	for v, d := range copyDurations {
		if _, err := ParseVolatility(string(v)); err != nil {
			return FreshnessPolicy{}, fmt.Errorf("freshness policy: %w", err)
		}
		if d < 0 {
			return FreshnessPolicy{}, fmt.Errorf("freshness policy: stale_after duration for %q must not be negative", v)
		}
	}
	for v, d := range copyExpiry {
		if _, err := ParseVolatility(string(v)); err != nil {
			return FreshnessPolicy{}, fmt.Errorf("freshness policy: %w", err)
		}
		if d < 0 {
			return FreshnessPolicy{}, fmt.Errorf("freshness policy: expiry duration for %q must not be negative", v)
		}
	}
	return FreshnessPolicy{Durations: copyDurations, Expiry: copyExpiry}, nil
}

// StalenessWindow returns the policy's staleness interval for the given
// volatility band. A zero result means the record never becomes stale on a
// timer (it can still be stale through its own explicit window, or because
// it was never verified). The policy's maps are immutable by contract; new
// policies are constructed with NewFreshnessPolicy.
func (p FreshnessPolicy) StalenessWindow(v Volatility) time.Duration {
	if p.Durations == nil {
		return 0
	}
	return p.Durations[v]
}

// ExpiryWindow returns the policy's maximum lifetime for the given
// volatility band. A zero result means the band has no absolute expiry.
// The policy's maps are immutable by contract; new policies are constructed
// with NewFreshnessPolicy.
func (p FreshnessPolicy) ExpiryWindow(v Volatility) time.Duration {
	if p.Expiry == nil {
		return 0
	}
	return p.Expiry[v]
}

// FreshnessAssessment is the outcome of applying a FreshnessPolicy to a
// record at a point in time.
type FreshnessAssessment struct {
	// Status is the record's freshness status (fresh, stale, or expired).
	Status FreshnessStatus

	// StaleAfter is the staleness window that applied to the record
	// (zero = no routine staleness for its volatility band).
	StaleAfter time.Duration

	// ExpiresAt is the absolute moment the record expires. It is the
	// later of the record's own absolute expiry (Expiration.ExpiresAt)
	// and the policy's band-bound lifetime derived from the last
	// verification (LastVerifiedAt + ExpiryWindow). Zero means the
	// record has no absolute expiry.
	ExpiresAt time.Time
}

// IsExpired reports whether the assessment is expired.
func (a FreshnessAssessment) IsExpired() bool { return a.Status == StatusExpired }

// IsStale reports whether the assessment is stale (and not expired).
func (a FreshnessAssessment) IsStale() bool { return a.Status == StatusStale }

// IsFresh reports whether the assessment is fresh.
func (a FreshnessAssessment) IsFresh() bool { return a.Status == StatusFresh }

// ReVerify reports whether the record must be re-verified before it is used
// as current factual context: true for stale and expired records, false for
// fresh ones. Stale records may still guide research; they may not be
// presented as current.
func (a FreshnessAssessment) ReVerify() bool { return a.Status != StatusFresh }

// evaluate computes a record's status against the policy at time t.
//
// The calculation is deterministic: it depends only on the record's
// volatility band, its freshness timestamps, its expiration block, and the
// policy. The same inputs always produce the same assessment.
//
// Precedence: invalidation (superseded/invalidated) and absolute expiry
// (the record's own expiry or the policy's band lifetime) dominate; staleness
// is judged only for records that are still within their lifetime. A
// never-verified record is always stale (verification currency is explicit,
// mirroring Freshness.IsStale).
func (p FreshnessPolicy) evaluate(rec *MemoryRecord, t time.Time) FreshnessAssessment {
	a := FreshnessAssessment{
		StaleAfter: p.StalenessWindow(rec.Volatility),
	}

	// Absolute expiry: the later of the record's own expiry and the
	// policy's band-bound lifetime from the last verification. Expired
	// dominates stale (a record past its lifetime is done, not merely
	// due for re-verification).
	expiries := []time.Time{}
	if !rec.Expiration.ExpiresAt.IsZero() {
		expiries = append(expiries, rec.Expiration.ExpiresAt)
	}
	if w := p.ExpiryWindow(rec.Volatility); w > 0 && !rec.Freshness.LastVerifiedAt.IsZero() {
		expiries = append(expiries, rec.Freshness.LastVerifiedAt.Add(w))
	}
	for _, e := range expiries {
		if a.ExpiresAt.IsZero() || e.After(a.ExpiresAt) {
			a.ExpiresAt = e
		}
	}

	// Invalidation invalidates regardless of time.
	if rec.Expiration.IsExpired(t) {
		a.Status = StatusExpired
		return a
	}

	// Staleness is judged before expiry precedence only in the fresh region:
	// a record past both its staleness window and its lifetime is expired
	// (expired dominates stale), so the expiry check comes first.
	if !a.ExpiresAt.IsZero() && !t.Before(a.ExpiresAt) {
		a.Status = StatusExpired
		return a
	}

	// Staleness: the record's own window is the stricter of the two and
	// always wins; the policy's band window applies to any age left over.
	// (Freshness.IsStale covers the explicit window, including
	// never-verified records, with inclusive boundaries so the combined
	// judgment is monotonic.)
	stale := rec.Freshness.IsStale(t)
	if !stale && a.StaleAfter > 0 {
		if rec.Freshness.LastVerifiedAt.IsZero() {
			stale = true
		} else {
			stale = !t.Before(rec.Freshness.LastVerifiedAt.Add(a.StaleAfter))
		}
	}
	if stale {
		a.Status = StatusStale
		return a
	}
	a.Status = StatusFresh
	return a
}

// StatusOf applies the policy to a record at time t.
//
// A nil record is reported as expired.
func (p FreshnessPolicy) StatusOf(rec *MemoryRecord, t time.Time) FreshnessAssessment {
	if rec == nil {
		return FreshnessAssessment{Status: StatusExpired}
	}
	return p.evaluate(rec, t)
}

// IsStaleOf applies the policy's staleness judgment to a record at time t
// (expired records count as stale for the caller's purposes: they also need
// re-verification).
func (p FreshnessPolicy) IsStaleOf(rec *MemoryRecord, t time.Time) bool {
	return p.StatusOf(rec, t).ReVerify()
}

// StaleRecords returns the records in recs that the policy considers stale
// or expired at time t, in input order. This is the standard input for a
// re-verification pass.
func (p FreshnessPolicy) StaleRecords(recs []*MemoryRecord, t time.Time) []*MemoryRecord {
	var out []*MemoryRecord
	for _, r := range recs {
		if p.IsStaleOf(r, t) {
			out = append(out, r)
		}
	}
	return out
}

// FreshRecords returns the records in recs that the policy considers fresh
// at time t, in input order.
func (p FreshnessPolicy) FreshRecords(recs []*MemoryRecord, t time.Time) []*MemoryRecord {
	var out []*MemoryRecord
	for _, r := range recs {
		if p.StatusOf(r, t).IsFresh() {
			out = append(out, r)
		}
	}
	return out
}

// RecordStatusOf is a convenience wrapper that applies the default policy.
//
// It exists so call sites that have no operator configuration can still get
// the volatility-aware trichotomy with zero wiring; it is not a substitute
// for passing an explicit policy where one is available.
func RecordStatusOf(rec *MemoryRecord, t time.Time) FreshnessAssessment {
	return DefaultFreshnessPolicy.StatusOf(rec, t)
}
