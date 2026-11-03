package entitymemory

import (
	"strings"
	"time"
)

// Mention is an incoming reference to an entity seen in source content:
// whatever string the source uses for it, at most with a hint about what
// kind of thing it is.
type Mention struct {
	// Text is the name, alias, or ID as it appears. Matching is exact
	// (case-insensitive after whitespace folding); fuzzy or LLM matching is
	// deliberately not part of the resolution path.
	Text string

	// TypeHint is an optional expected entity type. When set, a candidate
	// entity of a *different* type is a disqualifier for single-entity
	// matches (see the disambiguation rules below); it never downgrades a
	// matching-type candidate.
	TypeHint EntityType
}

// Candidate is one entity a mention matched, with the strength of the match
// and enough context to explain an ambiguous outcome.
type Candidate struct {
	// Entity is the matched entity (a live copy).
	Entity *Entity

	// MatchedVia is which tier produced the match (see MatchTier).
	MatchedVia MatchTier

	// MatchedOn is the exact name or ID that matched: the mention text for
	// name/alias/ID matches.
	MatchedOn string
}

// MatchTier ranks how strongly a candidate matches a mention, strongest
// first. Tiers are the disambiguation rules:
//
//  1. TierStableID — the mention is (case-insensitively) an entity's
//     canonical ID. IDs are chosen by the newsroom and stable, so a match
//     here is definitive.
//  2. TierCanonicalName — the mention is an exact (folded) match of an
//     entity's canonical name.
//  3. TierAlias — the mention is an exact (folded) match of an entity's
//     alias.
//
// Candidates from different tiers never resolve to a single entity: an alias
// match that collides with another entity's canonical-name match is
// reported as ambiguous, not silently preferred.
type MatchTier int

const (
	// TierStableID: matched on the entity's canonical ID.
	TierStableID MatchTier = iota
	// TierCanonicalName: matched on the entity's canonical name.
	TierCanonicalName
	// TierAlias: matched on an alias.
	TierAlias
)

// String returns the tier name.
func (t MatchTier) String() string {
	switch t {
	case TierStableID:
		return "stable_id"
	case TierCanonicalName:
		return "canonical_name"
	case TierAlias:
		return "alias"
	}
	return "unknown"
}

// StaleAt returns the staleness window of a match at this tier: how long a
// match may rely on the entity's last identity verification before it must
// be re-verified. Weaker tiers are less trustworthy and expire faster.
//
// These are defaults; callers with a custom freshness policy apply their own
// on top of the reported staleness.
func (t MatchTier) StaleAt() time.Duration {
	switch t {
	case TierStableID:
		return 90 * 24 * time.Hour
	case TierCanonicalName:
		return 30 * 24 * time.Hour
	case TierAlias:
		return 7 * 24 * time.Hour
	}
	return 24 * time.Hour
}

// Resolution is the outcome of resolving one mention.
//
// Exactly one of Resolved, Ambiguous, or Unresolved describes the result —
// the safety rule from the ticket: ambiguity is surfaced, never resolved by
// guessing; no LLM fuzzy match participates.
type Resolution struct {
	// Mention is the mention that was resolved.
	Mention Mention

	// Resolved is non-nil exactly when the mention matched one entity with
	// confidence. Resolving never mutates the store.
	Resolved *Entity

	// MatchedVia / MatchedOn describe the winning match (set when Resolved
	// is non-nil).
	MatchedVia MatchTier
	MatchedOn  string

	// Stale, when the resolution is a single-entity match, reports whether
	// the entity's identity verification is missing or past the tier's
	// staleness window (MatchTier.StaleAt) at the store's reference time.
	// Stale matches are still returned (memory is context, not evidence),
	// but consumers that require verification must treat them as unverified.
	// Stale is never set when the reference time is zero (no judgment).
	Stale bool

	// Ambiguous is non-empty when the mention matched more than one entity
	// (or matched one entity through a tier the tie-break cannot break). A
	// non-empty Ambiguous list means the caller must surface the ambiguity;
	// the matched entities are all kept separate.
	Ambiguous []Candidate

	// Unresolved is true when the mention matched nothing. The mention may
	// still deserve a new entity; the caller decides (see
	// Create for the name-conflict guarantees that keep the existing
	// entities intact).
	Unresolved bool
}

// foldName canonicalizes a name for matching: Unicode case-fold to lower
// case and collapse internal whitespace runs to a single space.
//
// The match stays exact apart from case and whitespace — the deterministic
// core the ticket requires.
func foldName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// Resolve resolves a mention against the entity memory deterministically.
//
// Disambiguation rules, in order:
//
//  1. The mention is matched on every tier it hits (stable ID, canonical
//     name, alias). Each distinct matched entity is a Candidate, at the
//     strongest tier it matched on.
//  2. Zero candidates -> Unresolved.
//  3. One candidate -> Resolved, unless a TypeHint is set and the
//     candidate's type differs from the hint (in which case the result is
//     Ambiguous with that single candidate: a type mismatch is a red flag,
//     not a silent override).
//  4. More than one candidate -> Ambiguous with all of them, ordered by
//     tier then entity ID. If exactly one candidate is *fresh* at the
//     store's reference time (see WithNow) and all others are stale, the
//     fresh one resolves (fresh memory beats stale memory; see
//     entityStaleAt). If the hint disqualifies the fresh candidate, the
//     ambiguity stands.
//
// Ties inside one tier (two entities sharing a name/alias — possible only
// through external writes, since Create/AddAlias refuse duplicates) also
// stay ambiguous.
//
// Staleness is judged at the store's reference time: pass one via WithNow
// before calling, or leave it zero to skip staleness judgments entirely
// (pure deterministic tier logic).
func (s *Store) Resolve(m Mention) Resolution {
	res := Resolution{Mention: m}
	now := s.now()
	mtext := foldName(m.Text)
	if mtext == "" {
		res.Unresolved = true
		return res
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Collect candidates per entity, keeping the strongest tier per entity.
	type cand struct {
		tier MatchTier
		on   string
	}
	best := make(map[string]cand)
	add := func(id string, tier MatchTier, on string) {
		cur, ok := best[id]
		if !ok || tier < cur.tier {
			best[id] = cand{tier: tier, on: on}
		}
	}

	// Tier 1: stable IDs. IDs are chosen by the newsroom and stable: the
	// match is exact (a canonical ID is its own token; a folded mention that
	// happens to look like an ID never beats the ID itself). Exact match
	// first, then case-insensitive for IDs only (IDs conventionally carry
	// no case-sensitive meaning, unlike names).
	if e, ok := s.entities[m.Text]; ok {
		add(e.ID, TierStableID, m.Text)
	} else if e, ok := s.entities[strings.ToLower(m.Text)]; ok {
		add(e.ID, TierStableID, m.Text)
	}
	// Tier 2/3: names and aliases through the index (folded, exact).
	for _, id := range s.byName[mtext] {
		e := s.entities[id]
		if e == nil {
			continue
		}
		if foldName(e.CanonicalName) == mtext {
			add(e.ID, TierCanonicalName, e.CanonicalName)
		} else {
			add(e.ID, TierAlias, m.Text)
		}
	}

	candidates := make([]Candidate, 0, len(best))
	for _, id := range sortedKeysString(best) {
		c := best[id]
		e := s.entities[id]
		candidates = append(candidates, Candidate{
			Entity:     e.clone(),
			MatchedVia: c.tier,
			MatchedOn:  c.on,
		})
	}

	switch len(candidates) {
	case 0:
		res.Unresolved = true
		return res
	case 1:
		c := candidates[0]
		if m.TypeHint != "" && c.Entity.Type != m.TypeHint {
			res.Ambiguous = candidates
			return res
		}
		res.Resolved = c.Entity
		res.MatchedVia = c.MatchedVia
		res.MatchedOn = c.MatchedOn
		res.Stale = entityStaleAt(c.Entity, c.MatchedVia, now)
		return res
	default:
		// Stale-aware tie-break: exactly one fresh candidate resolves.
		fresh := make([]Candidate, 0, len(candidates))
		for _, c := range candidates {
			if !entityStaleAt(c.Entity, c.MatchedVia, now) {
				fresh = append(fresh, c)
			}
		}
		if len(fresh) == 1 && len(fresh) < len(candidates) {
			c := fresh[0]
			if m.TypeHint == "" || c.Entity.Type == m.TypeHint {
				res.Resolved = c.Entity
				res.MatchedVia = c.MatchedVia
				res.MatchedOn = c.MatchedOn
				res.Stale = false
				return res
			}
		}
		res.Ambiguous = candidates
		return res
	}
}

// entityStaleAt reports whether the entity's identity verification is stale
// for a match at tier tier at time t0:
//
//   - a never-verified entity is always stale (mirrors memory.Freshness);
//   - with t0 zero, no window check is performed (a verified entity is
//     fresh); otherwise the entity is stale when the age of the verification
//     exceeds tier.StaleAt().
func entityStaleAt(e *Entity, tier MatchTier, t0 time.Time) bool {
	if e.LastVerifiedAt.IsZero() {
		return true
	}
	if t0.IsZero() {
		return false
	}
	return t0.Sub(e.LastVerifiedAt) > tier.StaleAt()
}

// ResolveMany resolves a batch of mentions in one pass.
//
// It applies the same deterministic rules as Resolve. Mentions that
// resolve independently do not influence each other; each resolution is
// computed against the same store state, so the batch is deterministic for
// a given store.
func (s *Store) ResolveMany(mentions []Mention) []Resolution {
	out := make([]Resolution, 0, len(mentions))
	for _, m := range mentions {
		out = append(out, s.Resolve(m))
	}
	return out
}
