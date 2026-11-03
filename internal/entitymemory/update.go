package entitymemory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

// SourceClaim is one attribute update attributed to a source.
//
// It is the unit of "current verified source data" as far as entity memory
// is concerned: what the source says, when it was consulted, and how fast
// this attribute is expected to change. When the caller has already run the
// claim through the verification pipeline, mark it Verified.
type SourceClaim struct {
	// Key is the attribute name to update (e.g. "current_team").
	Key string

	// Value is what the source says.
	Value string

	// Source is the provenance of this claim.
	Source memory.MemorySource

	// Volatility overrides the attribute's volatility band. Zero (the zero
	// value "") keeps the attribute's existing band when the attribute
	// exists, and falls back to VolatileMedium for new attributes.
	Volatility memory.Volatility

	// StaleAfter overrides the attribute's staleness window. Zero keeps the
	// existing window, or leaves the attribute window-less (volatility
	// guidance only) for new attributes.
	StaleAfter time.Duration

	// Verified is true when the caller has confirmed the claim through the
	// verification pipeline (or equivalent). Verified claims beat stale
	// memory for the same attribute (see UpdateAttributes); unverified
	// claims never overwrite an unexpired, verified attribute value.
	Verified bool
}

// UpdateAttributes updates attributes of an existing entity from claims and
// persists the result. It implements the ticket's safety rule:
//
//   - Current verified source data always wins over memory: a claim that is
//     Verified and newer than (or equal to) the attribute's last
//     verification replaces the value, updating provenance and freshness.
//   - Stale memory loses to current source data: an unverified claim
//     replaces the existing value only when the existing attribute is stale
//     at now.
//   - Otherwise a conflict: the existing (fresh, verified) value is kept,
//     the claim is not applied, and an ErrConflict is returned naming the
//     attribute and both values. Nothing is silently overwritten.
//
// Applied claims update the attribute's per-field provenance (its Sources
// become the claim's sources) and its freshness stamp. The entity record is
// rewritten through the backing store on success.
//
// now is the reference time for all freshness comparisons.
//
// The call is all-or-nothing: every claim is validated first, and when any
// claim is malformed or conflicts with existing (fresh, verified) memory,
// none of the claims are applied and the joined error is returned, leaving
// the entity exactly as it was.
func (s *Store) UpdateAttributes(ctx context.Context, now time.Time, id string, claims ...SourceClaim) error {
	now = memory.TimeToUTC(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entities[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrEntityNotFound, id)
	}
	working := e.clone()
	byKey := make(map[string]*Attribute, len(working.Attributes))
	for i := range working.Attributes {
		a := &working.Attributes[i]
		byKey[normKey(a.Key)] = a
	}

	// Phase 1: validate every claim before touching anything.
	var conflicts []error
	for _, c := range claims {
		if normKey(c.Key) == "" {
			conflicts = append(conflicts, fmt.Errorf("claim: key is required"))
			continue
		}
		if c.Value == "" {
			conflicts = append(conflicts, fmt.Errorf("claim %q: value is required", c.Key))
			continue
		}
		if err := c.Source.Validate(); err != nil {
			conflicts = append(conflicts, fmt.Errorf("claim %q: %w", c.Key, err))
			continue
		}
		if c.Volatility != "" {
			if _, err := memory.ParseVolatility(string(c.Volatility)); err != nil {
				conflicts = append(conflicts, fmt.Errorf("claim %q: %w", c.Key, err))
				continue
			}
		}
	}
	if len(conflicts) > 0 {
		return errors.Join(conflicts...)
	}

	// Phase 2: apply the pre-validated claims to the working copy.
	applied := 0
	for _, c := range claims {
		key := normKey(c.Key)
		a, exists := byKey[key]
		if !exists {
			a = &Attribute{Key: c.Key}
			working.Attributes = append(working.Attributes, *a)
			a = &working.Attributes[len(working.Attributes)-1]
			byKey[key] = a
		}

		existingStale := a.isStale(now)
		switch {
		case c.Verified:
			// Verified source data always wins over memory — it is current
			// source data, and memory must never sit on top of it.
			a.Value = c.Value
			a.LastVerifiedAt = c.Source.RetrievedAt
			a.Sources = append([]memory.MemorySource(nil), c.Source)
		case !c.Verified && existingStale:
			// Stale memory loses to current source data — even unverified
			// source data is better than a stale memory.
			a.Value = c.Value
			a.LastVerifiedAt = c.Source.RetrievedAt
			a.Sources = append([]memory.MemorySource(nil), c.Source)
		case !c.Verified && !existingStale:
			// Fresh, verified memory beats unverified source data: conflict.
			conflicts = append(conflicts, &ErrConflict{
				EntityID:    id,
				Key:         a.Key,
				Existing:    a.Value,
				ExistingAt:  a.LastVerifiedAt,
				New:         c.Value,
				NewVerified: c.Source.RetrievedAt,
				Reason:      "existing attribute is fresh and verified; the unverified claim does not overwrite it",
			})
			continue
		}

		// Volatility / staleness overrides.
		if c.Volatility != "" {
			a.Volatility = c.Volatility
		}
		if c.StaleAfter != 0 {
			a.StaleAfter = c.StaleAfter
		}
		if a.StaleAfter > 0 && a.LastVerifiedAt.IsZero() {
			// A window without a stamp is meaningless; the attribute is
			// unconditionally stale. Drop the window to keep invariants.
			a.StaleAfter = 0
		}
		applied++
	}

	if len(conflicts) > 0 {
		// All-or-nothing: the working copy is dropped; the stored entity is
		// left exactly as it was.
		return errors.Join(conflicts...)
	}
	if applied == 0 {
		return fmt.Errorf("no claims to apply to entity %s", id)
	}

	// All invariants must hold before persisting.
	if err := working.Validate(); err != nil {
		return fmt.Errorf("entity %s after update: %w", id, err)
	}
	working.memoryID = e.memoryID
	working.UpdatedAt = now
	if err := s.persistLocked(ctx, now, working); err != nil {
		return err
	}
	return nil
}

// AddAlias adds an alias to an entity, persisting the result.
//
// Safety rules:
//   - adding the canonical name or an existing alias back is a no-op
//     success (idempotent);
//   - if the name is already used as a canonical name or alias by a
//     *different* entity, ErrDuplicateName is returned and nothing changes:
//     the alias is not stolen and the two entities are not merged — the
//     caller must disambiguate.
//   - if the name equals the entity's own *canonical* name it is rejected
//     (aliases must differ from the canonical name).
func (s *Store) AddAlias(ctx context.Context, now time.Time, id, alias string) error {
	now = memory.TimeToUTC(now)
	alias = strings.TrimSpace(alias)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entities[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrEntityNotFound, id)
	}
	if foldName(alias) == "" {
		return fmt.Errorf("alias must not be empty (entity %s)", id)
	}
	fa := foldName(alias)
	if fa == foldName(e.CanonicalName) {
		return fmt.Errorf("alias %q equals the entity's canonical name (entity %s); use the canonical name or choose a different alias", alias, id)
	}
	for _, existing := range e.Aliases {
		if foldName(existing) == fa {
			return nil // idempotent: already present
		}
	}
	for _, otherID := range s.byName[fa] {
		if otherID != id {
			return fmt.Errorf("%w: %q is used by %s; refuse to add it to %s", ErrDuplicateName, alias, otherID, id)
		}
	}
	e.Aliases = append(e.Aliases, alias)
	e.UpdatedAt = now
	return s.persistLocked(ctx, now, e)
}

// RemoveAlias removes an alias from an entity (case-insensitive after
// folding), persisting the result. Removing a name that is not an alias is
// an error; removing the canonical name is impossible (it is not an alias).
func (s *Store) RemoveAlias(ctx context.Context, now time.Time, id, alias string) error {
	now = memory.TimeToUTC(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entities[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrEntityNotFound, id)
	}
	folded := foldName(alias)
	kept := make([]string, 0, len(e.Aliases))
	found := false
	for _, a := range e.Aliases {
		if foldName(a) == folded {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		return fmt.Errorf("alias %q is not an alias of entity %s", alias, id)
	}
	working := e.clone()
	working.Aliases = kept
	working.UpdatedAt = now
	return s.persistLocked(ctx, now, working)
}

// Verify stamps the entity's identity as verified at now, with optional
// provenance, and persists the result.
//
// Verification of *attributes* is done per-attribute by UpdateAttributes
// (verified source claims); this stamps the entity's identity (its
// existence, type, and canonical name) as confirmed.
func (s *Store) Verify(ctx context.Context, now time.Time, id, verifiedBy string, sources ...memory.MemorySource) error {
	now = memory.TimeToUTC(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, src := range sources {
		if err := src.Validate(); err != nil {
			return fmt.Errorf("source: %w", err)
		}
	}
	e, ok := s.entities[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrEntityNotFound, id)
	}
	working := e.clone()
	working.LastVerifiedAt = now
	if verifiedBy != "" {
		working.VerifiedBy = verifiedBy
	}
	working.UpdatedAt = now
	return s.persistLocked(ctx, now, working)
}
