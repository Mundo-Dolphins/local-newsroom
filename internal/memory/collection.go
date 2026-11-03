package memory

import (
	"encoding/json"
	"errors"
	"fmt"
)

// MemoryCollection is a persistence-neutral set of memory records, the unit
// of serialization for the subsystem.
//
// It mirrors how the publication plan is versioned: the collection carries
// the schema version, the records are stored verbatim, and unknown major
// versions are rejected on unmarshal.
type MemoryCollection struct {
	// SchemaVersion is the schema version of this collection.
	SchemaVersion string `json:"schema_version"`

	// Records is the set of memory records.
	Records []*MemoryRecord `json:"records"`
}

// NewCollection builds a collection at the current schema version.
func NewCollection(records ...*MemoryRecord) *MemoryCollection {
	return &MemoryCollection{
		SchemaVersion: SchemaVersion,
		Records:       records,
	}
}

// Validate checks the collection invariants:
//
//   - the schema version is a semantic version this package understands;
//   - every record validates individually;
//   - stable IDs are unique within the collection.
func (c *MemoryCollection) Validate() error {
	if c == nil {
		return fmt.Errorf("memory collection is nil")
	}
	var errs []error

	if c.SchemaVersion == "" {
		errs = append(errs, fmt.Errorf("schema_version is required"))
	} else {
		major, err := SchemaMajor(c.SchemaVersion)
		if err != nil {
			errs = append(errs, fmt.Errorf("schema_version %q is not a semantic version: %v", c.SchemaVersion, err))
		} else if major != SchemaMajorCurrent {
			errs = append(errs, fmt.Errorf("%w: collection is %s, this version understands major %d", ErrUnsupportedSchemaVersion, c.SchemaVersion, SchemaMajorCurrent))
		}
	}

	seen := make(map[MemoryStableID]bool)
	for i, r := range c.Records {
		if r == nil {
			errs = append(errs, fmt.Errorf("record[%d] is nil", i))
			continue
		}
		if err := r.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("record[%d] (%s): %w", i, r.StableID, err))
			continue
		}
		if seen[r.StableID] {
			errs = append(errs, fmt.Errorf("duplicate stable_id in collection: %q", r.StableID))
		}
		seen[r.StableID] = true
	}
	return errors.Join(errs...)
}

// Marshal serializes the collection to canonical JSON. The schema version is
// stamped at the current version on write.
func (c *MemoryCollection) Marshal() ([]byte, error) {
	if c == nil {
		return nil, fmt.Errorf("memory collection is nil")
	}
	out := *c
	out.SchemaVersion = SchemaVersion
	return json.Marshal(&out)
}

// UnmarshalMemoryCollection decodes a collection from JSON.
//
// A document whose MAJOR schema version is not understood returns
// ErrUnsupportedSchemaVersion and no collection; MINOR/PATCH differences
// are accepted.
func UnmarshalMemoryCollection(data []byte) (*MemoryCollection, error) {
	var c MemoryCollection
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("decode memory collection: %w", err)
	}
	if c.SchemaVersion == "" {
		return nil, fmt.Errorf("memory collection: schema_version is required")
	}
	major, err := SchemaMajor(c.SchemaVersion)
	if err != nil {
		return nil, fmt.Errorf("memory collection: %w", err)
	}
	if major != SchemaMajorCurrent {
		return nil, fmt.Errorf("%w: collection is %s, this version understands major %d", ErrUnsupportedSchemaVersion, c.SchemaVersion, SchemaMajorCurrent)
	}
	return &c, nil
}
