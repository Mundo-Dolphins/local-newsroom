// Package entitymemory implements provenance-aware entity memory and safe
// entity resolution for the v0.7 editorial-memory subsystem (issue #143).
//
// It answers two questions the v0.7 contracts (#141, #142) deliberately left
// to consumers:
//
//  1. Entity memory: a recurring real-world entity — a player, coach, team,
//     reporter, or any other subject — is remembered as a single canonical
//     entity with a stable ID, a canonical name, a controlled type, a set of
//     aliases, and a set of attributes. Every attribute carries its own
//     volatility band and its own provenance (sources, last-verification
//     stamp), so consumers can weigh stable and volatile attributes
//     separately and know exactly where each value came from and when it was
//     last confirmed.
//
//  2. Entity resolution: incoming mentions (a name, alias, or stable ID
//     seen in source content) are resolved against the memory deterministically:
//     stable canonical IDs match first, then exact canonical names, then
//     exact aliases. Ambiguity is never resolved by guessing: when a mention
//     matches more than one entity, or matches none, the mention is reported
//     as ambiguous or unresolved and the caller decides what to do with it.
//     No fuzzy or LLM matching is part of the resolution path; nothing in
//     this package merges two entities that it cannot prove are the same.
//
// Persistence uses the v0.7 editorial-memory store (internal/memorystore)
// unchanged: each Entity is a KindEntity MemoryRecord with ScopeEntity. This
// package adds a thin attribute index on top of the generic Store so that
// alias lookups stay O(known-aliases) instead of a full scan per mention.
//
// The core memory rule is inherited and enforced: entity memory is
// contextual knowledge, not verified evidence. An entity's attributes may
// guide research and disambiguation; they must never flow into an artifact
// as verified claims, and they never overwrite current verified source data
// (see UpdateAttributes: verified source data always wins over stale memory).
package entitymemory

import (
	"fmt"
	"strings"
)

// EntityType is the category of a real-world entity.
//
// The vocabulary is controlled and publication-agnostic: these are the types
// the football-newsroom use case (and its natural generalizations) require.
// Resolution treats the type as a disambiguation dimension, so the set must
// stay small and explicit.
type EntityType string

const (
	// TypePlayer is a football player.
	TypePlayer EntityType = "player"

	// TypeCoach is a coach or front-office staff member.
	TypeCoach EntityType = "coach"

	// TypeTeam is a football team or organization that fields a roster.
	TypeTeam EntityType = "team"

	// TypeReporter is a journalist or other content producer.
	TypeReporter EntityType = "reporter"

	// TypePerson is a person that does not fall into a more specific type.
	TypePerson EntityType = "person"

	// TypeOrganization is a non-player organization (league, network, ...).
	TypeOrganization EntityType = "organization"
)

// AllEntityTypes lists every recognized EntityType in canonical order.
var AllEntityTypes = []EntityType{
	TypePlayer,
	TypeCoach,
	TypeTeam,
	TypeReporter,
	TypePerson,
	TypeOrganization,
}

// ParseEntityType converts a string into an EntityType. The match is
// case-insensitive and trims surrounding whitespace.
func ParseEntityType(s string) (EntityType, error) {
	c := EntityType(strings.TrimSpace(s))
	if len(c) != 0 {
		c = EntityType(strings.ToLower(string(c)))
	}
	for _, t := range AllEntityTypes {
		if c == t {
			return t, nil
		}
	}
	return "", fmt.Errorf("unknown entity type %q (expected one of %s)", s, entityTypesList())
}

func entityTypesList() string {
	parts := make([]string, len(AllEntityTypes))
	for i, t := range AllEntityTypes {
		parts[i] = string(t)
	}
	return strings.Join(parts, ", ")
}
