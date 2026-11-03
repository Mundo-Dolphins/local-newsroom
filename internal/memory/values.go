package memory

import (
	"fmt"
	"time"
)

// FactualValue is the Value payload for KindFactual records.
//
// A factual memory is a claim about the world. It is contextual knowledge:
// consumers may use it to prioritize verification or to seed research, but
// it must never flow into an artifact as verified evidence.
type FactualValue struct {
	// Statement is the claim itself, in plain language.
	Statement string `json:"statement"`

	// Data is an optional structured payload for non-prose facts, e.g.
	// {"position": "WR", "team": "..."} for a roster fact.
	Data map[string]string `json:"data,omitempty"`
}

// Validate checks the payload's invariants.
func (v FactualValue) Validate() error {
	if v.Statement == "" {
		return fmt.Errorf("factual value: statement is required")
	}
	return nil
}

// EntityValue is the Value payload for KindEntity records.
//
// Entity memory resolves and disambiguates entities: aliases, roles, and
// relationships. Fields is an open-ended bag of static-ish attributes
// (e.g. "draft_year": "2023", "college": "...") whose volatility belongs on
// the record, not on the value.
type EntityValue struct {
	// Fields is a set of attribute name/value pairs about the subject.
	Fields map[string]string `json:"fields,omitempty"`

	// Relationships lists known relationships to other entities, e.g.
	// {"type": "plays_for", "entity": "<name or canonical id>"}.
	Relationships []EntityRelationship `json:"relationships,omitempty"`
}

// EntityRelationship is one relationship from the subject to another entity.
type EntityRelationship struct {
	// Type is the relationship kind, e.g. "plays_for", "attended", "works_at".
	Type string `json:"type"`

	// Target is the related entity: a name or a canonical ID.
	Target string `json:"target"`

	// Since is when the relationship was observed to hold.
	Since time.Time `json:"since,omitempty"`

	// Note is optional free-text context.
	Note string `json:"note,omitempty"`
}

// Validate checks the payload's invariants.
func (v EntityValue) Validate() error {
	for _, r := range v.Relationships {
		if r.Type == "" {
			return fmt.Errorf("entity value: relationship type is required")
		}
		if r.Target == "" {
			return fmt.Errorf("entity value: relationship target is required (got type %q)", r.Type)
		}
	}
	return nil
}
