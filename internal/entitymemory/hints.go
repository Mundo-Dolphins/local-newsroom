// This file implements the memory hint projection used by the research
// pipeline (issue #145).
//
// The memory is read-only context for the planner and the researcher:
//
//   - aliases, background facts, and known sources surface as hints in the
//     stage prompts;
//   - attribute values surface only when the freshness policy marks them
//     stale or expired, and always labeled as memory-only values that must
//     be re-verified;
//   - hints never cross the evidence boundary: they are not SourceReferences,
//     they carry no citations, and the downstream verifier (which sees only
//     the dossier) cannot mark a memory-derived claim as verified without
//     independent live-source evidence. The final-checker adds a
//     deterministic memory-provenance check on top.

package entitymemory

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// HintBudget is the bounded size of the projected hint block. The budget is
// fixed by the projection: a caller cannot request more entities than the
// budget allows (requesting fewer is allowed and clamps the block down).
type HintBudget struct {
	// MaxEntities caps the number of matched entities rendered.
	MaxEntities int

	// ReVerifyOnly limits the attribute projection to stale/expired
	// attributes (see PromptBlock.ReVerify). Identity lines always render.
	ReVerifyOnly bool
}

// DefaultBound bounds the number of entities rendered into a prompt block.
// The budget only narrows: callers may pass a smaller bound, never a larger
// one (bounds above DefaultBound are clamped to DefaultBound).
const DefaultBound = 10

// AliasLimit caps the number of aliases shown per entity in a prompt block,
// keeping the block bounded even for entities with long alias lists.
const AliasLimit = 5

// DefaultHintBudget is the projection budget used when none is supplied.
var DefaultHintBudget = HintBudget{
	MaxEntities:  DefaultBound,
	ReVerifyOnly: true,
}

// clamp narrows the budget; it never widens it beyond DefaultHintBudget.
func (b HintBudget) clamp() HintBudget {
	if b.MaxEntities <= 0 || b.MaxEntities > DefaultHintBudget.MaxEntities {
		b.MaxEntities = DefaultHintBudget.MaxEntities
	}
	b.ReVerifyOnly = true // values are only ever surfaced via ReVerify
	return b
}

// PromptBlock is the bounded, render-ready view of the entity memory for one
// stage. All lists are deterministically ordered (ID / key ascending) and
// length-bounded.
//
// A PromptBlock is passed by value to the renderers so callers cannot mutate
// shared state between renderings.
type PromptBlock struct {
	// Entities carries the bounded entity summaries, ID-ascending.
	Entities []EntitySummary

	// ReVerify lists the attributes that must be re-verified at the
	// reference time (stale or expired). These are the only hint lines that
	// carry a value; the value is flagged memory-only in every rendering.
	ReVerify []AttributeFreshness
}

// EntitySummary is one entity's identity plus its attribute key list,
// projected for prompt use.
//
// Attribute values are deliberately excluded from the normal render: values
// belong to the ReVerify projection (stale/expired attributes only). This
// keeps the common path — a fresh memory — value-free, so a hint block can
// only ever contribute names, aliases, and attribute names, and only stale
// values appear, always flagged as unverified context.
type EntitySummary struct {
	// ID is the entity's canonical ID.
	ID string

	// Type is the entity's category.
	Type EntityType

	// Name is the entity's canonical name.
	Name string

	// Aliases are the entity's known aliases (deduped, source order,
	// bounded by AliasLimit).
	Aliases []string

	// AttributeKeys lists the entity's attribute keys (canonical case,
	// sorted). It tells the planner what aspects of the entity are tracked.
	AttributeKeys []string
}

// BuildPromptBlock builds the render-ready hint block for a stage:
// the bounded entity summaries (ID-ascending) plus every stale or expired
// attribute of the matched entities, ordered by entity ID then attribute
// key.
//
// The bound applies to the number of entities (zero or negative uses
// DefaultBound; bounds above DefaultBound are clamped to it). The reference
// time t drives staleness: a zero t disables the ReVerify projection
// entirely (nothing is judged stale) and the block then carries identity
// hints only.
//
// Ambiguous mentions contribute every plausible match (they are still
// context: the names are exactly what disambiguates in the prompt).
func (s *Store) BuildPromptBlock(mentions []Mention, t time.Time, bound int) PromptBlock {
	if bound <= 0 || bound > DefaultBound {
		bound = DefaultBound
	}

	// Resolve each mention; collect the distinct matched entity IDs.
	byID := make(map[string]bool)
	for _, m := range mentions {
		res := s.Resolve(m)
		if res.Resolved != nil {
			byID[res.Resolved.ID] = true
			continue
		}
		for _, c := range res.Ambiguous {
			byID[c.Entity.ID] = true
		}
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > bound {
		ids = ids[:bound]
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	policy := s.policyLocked()
	block := PromptBlock{Entities: make([]EntitySummary, 0, len(ids))}
	for _, id := range ids {
		e, ok := s.entities[id]
		if !ok {
			continue
		}
		block.Entities = append(block.Entities, e.summaryLocked())

		if !t.IsZero() {
			for _, a := range e.Attributes {
				assess := a.FreshnessStatus(policy, t)
				if !assess.ReVerify() {
					continue
				}
				block.ReVerify = append(block.ReVerify, AttributeFreshness{
					Key:            a.Key,
					Value:          a.Value,
					Volatility:     a.Volatility,
					Status:         assess.Status,
					StaleAfter:     assess.StaleAfter,
					ExpiresAt:      assess.ExpiresAt,
					LastVerifiedAt: a.LastVerifiedAt,
					EntityName:     e.CanonicalName,
				})
			}
		}
	}
	return block
}

// ---------------------------------------------------------------------------
// summaryLocked
// ---------------------------------------------------------------------------

// summaryLocked projects the entity's identity for prompt use. The entity
// must be a live store object (the caller holds the store read lock) or a
// copy of one (the projection only reads).
//
// Aliases are deduped (folded form), keep source order, and are bounded by
// AliasLimit. Attribute keys are deduped (folded) and sorted ascending.
// Attribute values are never projected here (see EntitySummary).
func (e *Entity) summaryLocked() EntitySummary {
	s := EntitySummary{
		ID:   e.ID,
		Type: e.Type,
		Name: e.CanonicalName,
	}
	seen := make(map[string]bool, len(e.Aliases))
	for _, a := range e.Aliases {
		f := foldName(a)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		s.Aliases = append(s.Aliases, a)
		if len(s.Aliases) >= AliasLimit {
			break
		}
	}
	seenKeys := make(map[string]bool, len(e.Attributes))
	for _, a := range e.Attributes {
		k := normKey(a.Key)
		if k == "" || seenKeys[k] {
			continue
		}
		seenKeys[k] = true
		s.AttributeKeys = append(s.AttributeKeys, a.Key)
	}
	sort.Strings(s.AttributeKeys)
	return s
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// framingBlock is the mandatory invariant framing. Every rendered block
// carries it verbatim so the LLM sees the same safety statement in every
// stage that consumes hints.
const framingBlock = `HINTS FROM EDITORIAL MEMORY — CONTEXT, NEVER EVIDENCE.
- These lines may be used to recognize entities, refine search queries, and prioritize angles.
- A hint is NOT a source and must NOT be quoted, cited, or reported as fact.
- Values marked as stale memory must be re-verified against live sources before any use; if no live source confirms a value, do not report it.
- If research does not confirm a hinted fact, drop it or present it as an explicitly unconfirmed question.`

// RenderPlannerHints renders the block for the planner stage.
//
// The output is identity hints (names, aliases, attribute keys) plus the
// stale/expired list (entity + attribute names only — no values). Stale
// values deliberately do not appear in the planner block: the planner needs
// what to look for, and keeping every value out of its prompt is the
// strongest form of the no-values-by-default rule.
func (s *Store) RenderPlannerHints(mentions []Mention, t time.Time, bound int) string {
	block := s.BuildPromptBlock(mentions, t, bound)
	var b strings.Builder
	b.WriteString(framingBlock)
	b.WriteString("\n\nEntities matched for this run:\n")
	for _, e := range block.Entities {
		fmt.Fprintf(&b, "- %s (%s) ", e.Name, e.Type)
		if len(e.Aliases) > 0 {
			fmt.Fprintf(&b, "aka: %s", strings.Join(e.Aliases, ", "))
		}
		if len(e.AttributeKeys) > 0 {
			fmt.Fprintf(&b, " | tracked: %s", strings.Join(e.AttributeKeys, ", "))
		}
		b.WriteString("\n")
	}
	if len(block.ReVerify) > 0 {
		b.WriteString("\nMemory state flagged for this run:\n")
		for _, f := range block.ReVerify {
			fmt.Fprintf(&b, "- %s %s: %s — re-verify against live sources before relying on it\n",
				f.EntityName, f.Key, f.Status.String())
		}
	}
	return b.String()
}

// RenderResearcherHints renders the block for the researcher stage.
//
// Compared to the planner block this rendering additionally shows the
// last-known values of stale/expired attributes, each explicitly labeled as
// a memory value requiring re-verification. Fresh attributes contribute
// identity and attribute names only — no values — so a fresh memory can
// never leak a value into the research prompt.
func (s *Store) RenderResearcherHints(mentions []Mention, t time.Time, bound int) string {
	block := s.BuildPromptBlock(mentions, t, bound)
	var b strings.Builder
	b.WriteString(framingBlock)
	b.WriteString("\n\nEntities matched for this run:\n")
	for _, e := range block.Entities {
		fmt.Fprintf(&b, "- %s (%s) ", e.Name, e.Type)
		if len(e.Aliases) > 0 {
			fmt.Fprintf(&b, "aka: %s", strings.Join(e.Aliases, ", "))
		}
		if len(e.AttributeKeys) > 0 {
			fmt.Fprintf(&b, " | tracked: %s", strings.Join(e.AttributeKeys, ", "))
		}
		b.WriteString("\n")
	}
	if len(block.ReVerify) > 0 {
		b.WriteString("\nStale/expired memory values — verify before use, drop if unconfirmed:\n")
		for _, f := range block.ReVerify {
			lastVerified := "never"
			if !f.LastVerifiedAt.IsZero() {
				lastVerified = f.LastVerifiedAt.UTC().Format("2006-01-02")
			}
			fmt.Fprintf(&b, "- %s %s (last known %s, %s as of %s): %s\n",
				f.EntityName, f.Key, f.Value, f.Status.String(), lastVerified, f.Value)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// ResolveMentions
// ---------------------------------------------------------------------------

// ResolveMentions resolves each mention and returns the resolved entity for
// each (nil for mentions that did not resolve) plus the distinct matched
// entity IDs (mention order, deterministic).
//
// It is the public helper for the research pipeline (issue #145): the
// caller passes the mentions derived from the run topic and receives the
// entity IDs to project and record in the run manifest.
//
// Ambiguous mentions contribute no resolved entity; their candidate IDs are
// included in the ID list (they are still context).
func (s *Store) ResolveMentions(mentions []Mention) ([]*Entity, []string) {
	resolved := make([]*Entity, len(mentions))
	seen := make(map[string]bool)
	var ids []string
	for i, m := range mentions {
		res := s.Resolve(m)
		if res.Resolved == nil {
			resolved[i] = nil
			for _, c := range res.Ambiguous {
				if !seen[c.Entity.ID] {
					seen[c.Entity.ID] = true
					ids = append(ids, c.Entity.ID)
				}
			}
			continue
		}
		resolved[i] = res.Resolved
		if !seen[res.Resolved.ID] {
			seen[res.Resolved.ID] = true
			ids = append(ids, res.Resolved.ID)
		}
	}
	if len(ids) == 0 {
		ids = nil
	}
	return resolved, ids
}
