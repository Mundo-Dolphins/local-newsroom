package contracts

import "time"

// TimeToUTC converts a time.Time to UTC if it's not already in UTC.
func TimeToUTC(t time.Time) time.Time {
	if t.Location() != time.UTC {
		return t.UTC()
	}
	return t
}

// MustTimeToUTC panics if the conversion fails (useful for testing).
func MustTimeToUTC(t time.Time) time.Time {
	if t.Location() != time.UTC {
		return t.UTC()
	}
	return t
}

// TimeReference provides a fixed time for testing determinism.
var TimeReference = time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

// AssertValid ensures the given time is valid.
func AssertValid(t time.Time) bool {
	return !t.IsZero()
}

// IDGenerator is a simple interface for generating stable IDs.
// Implementations should be deterministic for testing.
type IDGenerator interface {
	// GenerateID creates a new unique ID.
	GenerateID() string

	// GenerateIDWithPrefix creates a new unique ID with the given prefix.
	GenerateIDWithPrefix(prefix string) string
}

// UUIDGenerator implements IDGenerator using simple counters for testing.
type UUIDGenerator struct {
	counter int
}

// NewUUIDGenerator creates a new IDGenerator for testing.
func NewUUIDGenerator() *UUIDGenerator {
	return &UUIDGenerator{counter: 0}
}

// GenerateID implements IDGenerator.
func (g *UUIDGenerator) GenerateID() string {
	g.counter++
	return "id-" + string(rune('0'+g.counter%10))
}

// GenerateIDWithPrefix implements IDGenerator.
func (g *UUIDGenerator) GenerateIDWithPrefix(prefix string) string {
	g.counter++
	return prefix + "-" + string(rune('0'+g.counter%10))
}

// VerificationStatusMap is a map of claim IDs to verification status.
// This is a type alias for clarity in function signatures.
type VerificationStatusMap map[string]VerificationStatus

// ClaimDetailsMap is a map of claim IDs to verification details.
type ClaimDetailsMap map[string]ClaimVerificationDetails

// ClaimUsageMap is a map of claim IDs to usage information.
type ClaimUsageMap map[string]ClaimUsage

// SourceUsageMap is a map of source IDs to usage information.
type SourceUsageMap map[string]SourceUsage

// ClaimVerificationStatusMap is a map of claim IDs to their verification status.
// This mirrors VerificationResult for cross-referencing.
type ClaimVerificationStatusMap map[string]VerificationStatus

// TimeSlice returns a copy of the given time slice with times converted to UTC.
func TimeSlice(tss []time.Time) []time.Time {
	result := make([]time.Time, len(tss))
	for i, t := range tss {
		result[i] = TimeToUTC(t)
	}
	return result
}

// IsEmpty checks if a string slice is empty or nil.
func IsEmpty(s []string) bool {
	return len(s) == 0
}

// Contains checks if a string is in a string slice.
func Contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}
	return false
}

// UniqueStrings returns a new slice with duplicates removed, preserving order.
func UniqueStrings(s []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(s))
	for _, str := range s {
		if !seen[str] {
			seen[str] = true
			result = append(result, str)
		}
	}
	return result
}

// MergeStringSlices merges multiple string slices, removing duplicates.
func MergeStringSlices(slices ...[]string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range slices {
		for _, str := range s {
			if !seen[str] {
				seen[str] = true
				result = append(result, str)
			}
		}
	}
	return result
}

// EqualStringSlices checks if two string slices contain the same elements (order-independent).
func EqualStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int)
	for _, str := range a {
		seen[str]++
	}
	for _, str := range b {
		if seen[str] == 0 {
			return false
		}
		seen[str]--
	}
	return true
}
