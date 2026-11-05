package patterns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memorystore"
)

// GroupBy specifies how to group occurrences when detecting patterns.
type GroupBy string

const (
	// GroupByAuthor: group by editor identity
	GroupByAuthor GroupBy = "author"
	// GroupByProfile: group by profile ID
	GroupByProfile GroupBy = "profile"
	// GroupByRecipe: group by recipe ID
	GroupByRecipe GroupBy = "recipe"
	// GroupByCategory: group by editorial change category
	GroupByCategory GroupBy = "category"
)

// Config holds the configuration for pattern detection.
type Config struct {
	// MinOccurrences is the minimum number of occurrences before a pattern
	// is reported. Prevents overgeneralizing from one or two edits.
	MinOccurrences int

	// TimeWindow is the maximum age of occurrences to consider.
	// Zero means no time limit (all occurrences are considered).
	TimeWindow time.Duration

	// GroupBy specifies how to group occurrences for pattern detection.
	GroupBy GroupBy

	// Category filters occurrences to a specific editorial change category.
	// Empty means consider all categories.
	Category string
}

// Store writes pattern records to a memorystore.Store.
type Store struct {
	store memorystore.Store
	now   func() time.Time
}

// NewStore builds a Store over s. now (when non-nil) is the clock used for
// record timestamps; nil uses the real clock.
func NewStore(s memorystore.Store, now func() time.Time) *Store {
	if s == nil {
		panic("patterns: NewStore with a nil memorystore.Store")
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Store{store: s, now: now}
}

// DetectPatterns detects repeated editorial feedback patterns in the store.
//
// It queries KindEditorialFeedback records, applies pattern detection, and
// returns pattern records that can be persisted to the memory store.
func (s *Store) DetectPatterns(ctx context.Context, config Config) ([]*memory.MemoryRecord, error) {
	// Query all editorial feedback records
	q := memorystore.Query{
		Kind: memory.KindEditorialFeedback,
	}
	records, err := s.store.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("querying editorial feedback: %w", err)
	}

	// Build occurrences from records
	occurrences := s.buildOccurrences(records, config)
	if len(occurrences) == 0 {
		return nil, nil
	}

	// Detect patterns
	patterns := s.detectPatterns(occurrences, config)

	// Convert patterns to records
	var patternRecords []*memory.MemoryRecord
	for _, p := range patterns {
		rec := s.patternToRecord(p)
		patternRecords = append(patternRecords, rec)
	}

	return patternRecords, nil
}

// buildOccurrences filters and builds occurrences from records.
func (s *Store) buildOccurrences(records []*memory.MemoryRecord, config Config) []Occurrence {
	var occurrences []Occurrence
	now := time.Now().UTC()

	for _, rec := range records {
		// Decode the feedback record
		var fb memory.FeedbackRecord
		_, err := rec.DecodeValue()
		if err != nil {
			continue // Skip records that can't be decoded
		}
		// Unmarshal to the struct
		if err := json.Unmarshal(rec.Value, &fb); err != nil {
			continue
		}

		// Filter by time window
		if config.TimeWindow > 0 {
			if now.Sub(rec.CreatedAt) > config.TimeWindow {
				continue
			}
		}

		// Extract category from feedback text
		category := extractCategory(fb.Feedback)

		// Filter by category if specified
		if config.Category != "" && category != config.Category {
			continue
		}

		// Build occurrence
		occ := Occurrence{
			RecordID:    string(rec.StableID),
			Feedback:    fb.Feedback,
			Author:      fb.GivenBy,
			GivenAt:     fb.GivenAt,
			SubjectKind: rec.Subject.Kind,
			SubjectName: rec.Subject.Name,
			Category:    category,
			Field:       rec.Field,
		}

		occurrences = append(occurrences, occ)
	}

	return occurrences
}

// Occurrence represents one occurrence of a pattern.
type Occurrence struct {
	// RecordID is the stable ID of the editorial feedback record.
	RecordID string

	// Feedback is the full feedback text from the record.
	Feedback string

	// Author is the editor who gave the feedback (GivenBy field).
	Author string

	// GivenAt is when the feedback was given (UTC).
	GivenAt time.Time

	// SubjectKind is the kind of subject (recipe, profile, entity, etc.).
	SubjectKind string

	// SubjectName is the name of the subject.
	SubjectName string

	// Category is the editorial change category.
	Category string

	// Field is the record's field (e.g., "hunk-0", "summary").
	Field string
}

// detectPatterns detects patterns from occurrences.
func (s *Store) detectPatterns(occurrences []Occurrence, config Config) []Pattern {
	var patterns []Pattern

	// Group occurrences by key
	grouped := s.groupOccurrences(occurrences, config)

	// Detect patterns within each group
	for groupKey, groupOccurrences := range grouped {
		if len(groupOccurrences) < config.MinOccurrences {
			continue // Not enough occurrences for a pattern
		}

		// Group by feedback text (exact match for now)
		feedbackGroups := make(map[string][]Occurrence)
		for _, occ := range groupOccurrences {
			feedbackGroups[occ.Feedback] = append(feedbackGroups[occ.Feedback], occ)
		}

		// Check if any feedback group meets the threshold
		for _, group := range feedbackGroups {
			if len(group) >= config.MinOccurrences {
				// Sort occurrences by timestamp
				sorted := make([]Occurrence, len(group))
				copy(sorted, group)
				sort.Slice(sorted, func(i, j int) bool {
					return sorted[i].GivenAt.Before(sorted[j].GivenAt)
				})

				// Build pattern
				pattern := Pattern{
					MinOccurrences: config.MinOccurrences,
					Occurrences:    sorted,
					CreatedAt:      s.now().UTC(),
					SampleFeedback: sorted[0].Feedback,
					GroupKey:       groupKey,
				}

				// Set category from first occurrence
				if len(sorted) > 0 {
					pattern.Category = sorted[0].Category
				}

				patterns = append(patterns, pattern)
			}
		}
	}

	return patterns
}

// groupOccurrences groups occurrences by the configured key.
func (s *Store) groupOccurrences(occurrences []Occurrence, config Config) map[string][]Occurrence {
	grouped := make(map[string][]Occurrence)

	for _, occ := range occurrences {
		var key string
		switch config.GroupBy {
		case GroupByAuthor:
			key = occ.Author
		case GroupByProfile:
			if occ.SubjectKind == "profile" {
				key = occ.SubjectName
			} else {
				key = "non-profile"
			}
		case GroupByRecipe:
			if occ.SubjectKind == "recipe" {
				key = occ.SubjectName
			} else {
				key = "non-recipe"
			}
		case GroupByCategory:
			key = occ.Category
		default:
			key = "all"
		}

		grouped[key] = append(grouped[key], occ)
	}

	return grouped
}

// Pattern represents a detected repeated editorial feedback pattern.
type Pattern struct {
	// Category is the editorial change category for this pattern.
	Category string

	// GroupKey identifies the grouping (author/profile/recipe).
	GroupKey string

	// MinOccurrences is the minimum occurrences threshold used for detection.
	MinOccurrences int

	// Occurrences is the list of individual occurrences that form this pattern.
	Occurrences []Occurrence

	// CreatedAt is when this pattern record was created (UTC).
	CreatedAt time.Time

	// SampleFeedback is a sample feedback text from the pattern.
	SampleFeedback string
}

// patternToRecord converts a pattern to a memory record.
func (s *Store) patternToRecord(pattern Pattern) *memory.MemoryRecord {
	rec := memory.NewMemoryRecord(s.now())
	rec.Kind = memory.KindEditorialPattern
	rec.Scope = memory.ScopeNewsroom // Patterns are newsroom-wide
	rec.Subject = memory.EntityRef{
		Kind: "pattern",
		Name: s.patternID(pattern),
	}

	// Create pattern occurrences
	var occurrences []memory.PatternOccurrence
	for _, occ := range pattern.Occurrences {
		po := memory.PatternOccurrence{
			RecordID:    occ.RecordID,
			Feedback:    occ.Feedback,
			Author:      occ.Author,
			GivenAt:     occ.GivenAt,
			SubjectKind: occ.SubjectKind,
			SubjectName: occ.SubjectName,
			Category:    occ.Category,
			Field:       occ.Field,
		}
		occurrences = append(occurrences, po)
	}

	// Encode the pattern record
	err := rec.EncodeValue(memory.PatternRecord{
		Category:       pattern.Category,
		GroupKey:       pattern.GroupKey,
		MinOccurrences: pattern.MinOccurrences,
		Occurrences:    occurrences,
		SampleFeedback: pattern.SampleFeedback,
		Notes:          fmt.Sprintf("Detected pattern with %d occurrences", len(pattern.Occurrences)),
	}, s.now())
	if err != nil {
		panic(fmt.Sprintf("patterns: encode pattern: %v", err))
	}

	return rec
}

// patternID generates a stable ID for a pattern based on its content.
func (s *Store) patternID(pattern Pattern) string {
	var b strings.Builder
	b.WriteString(pattern.Category)
	b.WriteString("|")
	b.WriteString(pattern.GroupKey)
	b.WriteString("|")
	fmt.Fprintf(&b, "%d", pattern.MinOccurrences)
	b.WriteString("|")
	b.WriteString(pattern.SampleFeedback)

	// Add occurrences count
	fmt.Fprintf(&b, "|%d", len(pattern.Occurrences))

	// Hash the content
	h := sha256.Sum256([]byte(b.String()))
	return "pattern-" + hex.EncodeToString(h[:])[:16]
}

// DetectPatterns is a convenience function for direct use.
func DetectPatterns(ctx context.Context, store memorystore.Store, config Config) ([]*memory.MemoryRecord, error) {
	return NewStore(store, nil).DetectPatterns(ctx, config)
}

// extractCategory attempts to extract the editorial change category from
// feedback text. This is a heuristic and may need refinement.
func extractCategory(feedback string) string {
	// Try to extract category from the feedback text
	// Pattern: "categories: <category>" or similar

	lower := strings.ToLower(feedback)

	// Known categories (from editorialedit.Category)
	categories := []string{
		"factual_correction",
		"attribution_correction",
		"unsupported_certainty",
		"tone_style",
		"vocabulary_translation",
		"structure_order",
		"omission",
		"addition",
		"length_verbosity",
		"headline_title",
		"formatting",
		"thread_split_merge",
	}

	for _, cat := range categories {
		if strings.Contains(lower, cat) {
			return cat
		}
	}

	// Default to empty (no category detected)
	return ""
}
