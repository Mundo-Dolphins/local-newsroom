package entitymemory

import (
	"sort"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

// AttributeFreshness is one attribute's freshness assessment under the
// store's freshness policy at the reference time.
type AttributeFreshness struct {
	// Key is the attribute name as stored (original case).
	Key string

	// Value is the attribute's current value.
	Value string

	// Volatility is the attribute's volatility band.
	Volatility memory.Volatility

	// Status is the freshness status at the reference time (fresh, stale,
	// or expired).
	Status memory.FreshnessStatus

	// StaleAfter is the staleness window that applied (the attribute's own
	// window when set, else the policy's band window; zero = no routine
	// staleness).
	StaleAfter time.Duration

	// ExpiresAt is the absolute expiry moment, if the policy's band expiry
	// applies (zero = no absolute expiry).
	ExpiresAt time.Time

	// LastVerifiedAt is when the attribute was last verified (zero = never
	// verified; such attributes are always stale).
	LastVerifiedAt time.Time

	// EntityName is the owning entity's canonical name. It is set by the
	// hint projection (BuildPromptBlock) so renderers can prefix attribute
	// lines with the entity; it is empty when the freshness was produced
	// outside a block (e.g. AttributeFreshnesses) and renderers then fall
	// back to the key alone.
	//
	// EntityName is presentation metadata, not part of any contract
	// validation.
	EntityName string
}

// ReVerify reports whether the attribute must be re-verified before its
// value is used as current factual context (stale or expired).
func (a AttributeFreshness) ReVerify() bool {
	return a.Status != memory.StatusFresh
}

// FreshnessStatus returns the freshness status of one attribute of the
// entity at time t under the store's freshness policy
// (FreshnessPolicy).
//
// It is the attribute-level projection of the generic volatility-aware
// policy: the attribute's volatility band drives the window, its own
// StaleAfter wins when set, and a never-verified attribute is always
// stale.
//
// Returns ErrEntityNotFound for unknown entity IDs, and an error if the
// attribute key does not exist on the entity.
func (s *Store) FreshnessStatus(id, key string, t time.Time) (memory.FreshnessStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entities[id]
	if !ok {
		return 0, ErrEntityNotFound
	}
	for _, a := range e.Attributes {
		if normKey(a.Key) == normKey(key) {
			return a.FreshnessStatus(s.policyLocked(), t).Status, nil
		}
	}
	return 0, ErrAttributeNotFound
}

// AttributeFreshnesses returns the freshness status of every attribute of
// the entity at time t under the store's freshness policy, ordered by
// attribute key (deterministic).
func (s *Store) AttributeFreshnesses(id string, t time.Time) ([]AttributeFreshness, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entities[id]
	if !ok {
		return nil, ErrEntityNotFound
	}
	p := s.policyLocked()
	out := make([]AttributeFreshness, 0, len(e.Attributes))
	for _, a := range e.Attributes {
		assess := a.FreshnessStatus(p, t)
		out = append(out, AttributeFreshness{
			Key:            a.Key,
			Value:          a.Value,
			Volatility:     a.Volatility,
			Status:         assess.Status,
			StaleAfter:     assess.StaleAfter,
			ExpiresAt:      assess.ExpiresAt,
			LastVerifiedAt: a.LastVerifiedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// StaleAttributes returns the attributes of the entity that the freshness
// policy considers stale or expired at time t — the input for a
// re-verification pass. Stale attributes may guide research; they must be
// re-verified before their values are used as current factual context.
//
// Returns ErrEntityNotFound for unknown entity IDs; the result is ordered
// by attribute key (deterministic).
func (s *Store) StaleAttributes(id string, t time.Time) ([]Attribute, error) {
	all, err := s.AttributeFreshnesses(id, t)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entities[id]
	if !ok {
		return nil, ErrEntityNotFound
	}
	byKey := make(map[string]Attribute, len(e.Attributes))
	for _, a := range e.Attributes {
		byKey[normKey(a.Key)] = a
	}
	var out []Attribute
	for _, f := range all {
		if f.ReVerify() {
			out = append(out, byKey[normKey(f.Key)])
		}
	}
	return out, nil
}
