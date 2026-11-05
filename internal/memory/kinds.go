package memory

import (
	"fmt"
	"strings"
	"time"
)

// MemoryKind enumerates the categories of editorial memory.
//
// The taxonomy is the set of stable kinds the subsystem recognizes. Kinds
// are deliberately orthogonal to subjects: the subject of a record is
// carried by EntityRef (and Scope), while Kind describes what the record is
// *about* in operational terms.
type MemoryKind string

const (
	// KindEntity tracks identity facts about a real-world entity (people,
	// organizations, teams, places): aliases, roles, relationships.
	KindEntity MemoryKind = "entity"

	// KindFactual tracks claims about the world that the newsroom believes to
	// be true. These are the only kind whose records must carry provenance
	// (see Provenance) answering where the fact came from and when it was
	// last verified.
	KindFactual MemoryKind = "factual"

	// KindEditorialFeedback records corrections and preferences expressed by
	// humans (editors, operators) about the newsroom's own output.
	KindEditorialFeedback MemoryKind = "editorial_feedback"

	// KindRecipeExecution tracks how a recipe performed when executed:
	// outcomes, failure modes, timings, lessons.
	KindRecipeExecution MemoryKind = "recipe_execution"

	// KindCorrection tracks corrections to previously published or persisted
	// content, including the original claim, the amended form, and the
	// reason.
	KindCorrection MemoryKind = "correction"

	// KindSuggestion captures machine- or human-generated proposals to refine
	// profiles or recipes. Suggestions are never applied automatically.
	KindSuggestion MemoryKind = "suggestion"

	// KindEditorialPattern records detected repeated editorial feedback
	// patterns. Patterns are advisory: they summarize recurring issues
	// but don't bypass the verification pipeline or modify profiles
	// automatically.
	KindEditorialPattern MemoryKind = "editorial_pattern"
)

// AllKinds lists every recognized MemoryKind in canonical order.
var AllKinds = []MemoryKind{
	KindEntity,
	KindFactual,
	KindEditorialFeedback,
	KindRecipeExecution,
	KindCorrection,
	KindSuggestion,
	KindEditorialPattern,
}

// ParseMemoryKind converts a string into a MemoryKind.
func ParseMemoryKind(s string) (MemoryKind, error) {
	k := MemoryKind(strings.TrimSpace(s))
	for _, valid := range AllKinds {
		if k == valid {
			return k, nil
		}
	}
	return "", fmt.Errorf("unknown memory kind %q (expected one of %s)", s, kindsList())
}

func kindsList() string {
	parts := make([]string, len(AllKinds))
	for i, k := range AllKinds {
		parts[i] = string(k)
	}
	return strings.Join(parts, ", ")
}

// MemoryScope indicates where a memory record applies.
//
// Scopes let consumers filter and merge records by breadth of applicability
// without encoding that policy into the record content.
type MemoryScope string

const (
	// ScopeRun is scoped to a single newsroom run (ephemeral by nature).
	ScopeRun MemoryScope = "run"

	// ScopeSource is scoped to a single source (URL, feed, document).
	ScopeSource MemoryScope = "source"

	// ScopeEntity is scoped to a single entity (see EntityRef).
	ScopeEntity MemoryScope = "entity"

	// ScopeProfile is scoped to an author/editorial profile.
	ScopeProfile MemoryScope = "profile"

	// ScopeRecipe is scoped to a specific recipe.
	ScopeRecipe MemoryScope = "recipe"

	// ScopePublication is scoped to a publication channel or outlet.
	ScopePublication MemoryScope = "publication"

	// ScopeNewsroom is newsroom-wide: applies to any subject matter.
	ScopeNewsroom MemoryScope = "newsroom"
)

// AllScopes lists every recognized MemoryScope in canonical order.
var AllScopes = []MemoryScope{
	ScopeRun,
	ScopeSource,
	ScopeEntity,
	ScopeProfile,
	ScopeRecipe,
	ScopePublication,
	ScopeNewsroom,
}

// ParseMemoryScope converts a string into a MemoryScope.
func ParseMemoryScope(s string) (MemoryScope, error) {
	scp := MemoryScope(strings.TrimSpace(s))
	for _, valid := range AllScopes {
		if scp == valid {
			return scp, nil
		}
	}
	return "", fmt.Errorf("unknown memory scope %q", s)
}

// MemorySource identifies where a memory record came from.
//
// A record may have multiple sources (e.g. a fact corroborated by two
// independent fetches). Sources are provenance inputs: they never make the
// record itself evidence.
type MemorySource struct {
	// SourceType is the kind of source: "web", "rss", "json", "csv", "pdf",
	// "live", "archive", "user", "llm", "inferred", or another
	// publication-agnostic tag. "user" and "llm" originate from people or
	// models, not from the web.
	SourceType string `json:"source_type"`

	// SourceID is a stable identifier for the source: a URL for web sources,
	// a feed ID for RSS, a document StableID for archive sources, a run ID
	// for derived sources, or an operator identifier for user input.
	SourceID string `json:"source_id"`

	// Title is an optional human-readable label for the source.
	Title string `json:"title,omitempty"`

	// RetrievedAt is when the source was last consulted for this record.
	RetrievedAt time.Time `json:"retrieved_at"`

	// Note is an optional free-text remark (e.g. which page or section of the
	// source informed the record).
	Note string `json:"note,omitempty"`
}

// Validate checks the source's invariants.
func (s MemorySource) Validate() error {
	if s.SourceType == "" {
		return fmt.Errorf("memory source: source_type is required")
	}
	if s.SourceID == "" {
		return fmt.Errorf("memory source: source_id is required")
	}
	if s.RetrievedAt.IsZero() {
		return fmt.Errorf("memory source: retrieved_at is required")
	}
	return nil
}
