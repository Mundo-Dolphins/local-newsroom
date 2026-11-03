package memory

import (
	"errors"
	"fmt"
	"strings"
)

// SchemaVersion is the version of the editorial-memory record schema
// implemented by this package.
//
// The version is semantic (MAJOR.MINOR.PATCH) and is carried on every
// MemoryRecord. Consumers must reject records whose MAJOR version they do
// not understand; MINOR/PATCH changes are backward compatible.
const SchemaVersion = "0.7.0"

// SchemaMajorCurrent is the schema major version understood by this package.
const SchemaMajorCurrent = 0

// ErrUnsupportedSchemaVersion indicates that a memory record was written
// with a schema major version this package cannot read.
var ErrUnsupportedSchemaVersion = errors.New("unsupported editorial memory schema version")

// ErrMemoryNotEvidence indicates that a memory-backed value was consumed as
// verified factual evidence, which the subsystem does not permit.
var ErrMemoryNotEvidence = errors.New("memory is contextual knowledge, not verified evidence")

// SchemaMajor extracts the major version from a semantic version string.
//
// A valid version has exactly three non-negative integer components,
// e.g. "0.7.0".
func SchemaMajor(version string) (int, error) {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("%q must have MAJOR.MINOR.PATCH form", version)
	}
	for i, p := range parts {
		if p == "" {
			return 0, fmt.Errorf("%q must have MAJOR.MINOR.PATCH form", version)
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return 0, fmt.Errorf("%q: component %d must be numeric", version, i)
			}
		}
	}
	major, _ := atoiStrict(parts[0])
	return major, nil
}

// atoiStrict converts a string that is known to be all digits into an int.
// It never fails on the digit check; range is not a concern for version
// components.
func atoiStrict(s string) (int, error) {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n, nil
}
