package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memorystore"
	"github.com/Mundo-Dolphins/local-newsroom/internal/patterns"
)

// patternsCmd is the "newsroom patterns" subcommand (v0.7, issue #148): it
// detects repeated editorial feedback patterns and manages pattern records.
//
// Pattern detection workflow:
//   - Query KindEditorialFeedback records from memory
//   - Group occurrences by author/profile/recipe/category
//   - Filter by minimum occurrence threshold
//   - Detect patterns (same feedback within threshold)
//   - Persist KindEditorialPattern records
//
// Patterns are advisory: they summarize recurring issues but don't bypass
// the verification pipeline or modify profiles automatically.
var patternsCmd = &cobra.Command{
	Use:   "patterns",
	Short: "Detect and manage repeated editorial feedback patterns",
	Long: `Detect and manage repeated editorial feedback patterns.

The patterns command provides subcommands for pattern detection and management:

  newsroom patterns detect  Detect patterns in editorial feedback
  newsroom patterns list    List detected patterns
  newsroom patterns show    Show details of a specific pattern

Pattern detection identifies recurring correction/edit patterns such as:
  - repeated removal of unsupported superlatives
  - recurring terminology changes
  - repeated preference for shorter intros
  - frequent restructuring of one recipe
  - repeated factual correction on the same entity field

The command requires the editorial memory database to exist. Run
"newsroom feedback" first to populate feedback records.
`,
	Example: `  newsroom patterns detect --category factual_correction --min-occurrences 2
  newsroom patterns list
  newsroom patterns show pattern-abc123`,
}

// patternsFlags holds the flags for the patterns command.
type patternsFlags struct {
	workspaceRoot string
	minOccurs     int
	timeWindow    string
	groupBy       string
	category      string
	format        string
}

var pf4 patternsFlags

func init() {
	f := patternsCmd.Flags()
	f.StringVar(&pf4.workspaceRoot, "workspace-root", ".newsroom", "workspace root directory")
	f.IntVar(&pf4.minOccurs, "min-occurrences", 2, "minimum number of occurrences before a pattern is reported")
	f.StringVar(&pf4.timeWindow, "time-window", "", "time window for occurrences (e.g., 72h, 168h)")
	f.StringVar(&pf4.groupBy, "group-by", "author", "group occurrences by: author, profile, recipe, category")
	f.StringVar(&pf4.category, "category", "", "filter by editorial change category")
	f.StringVar(&pf4.format, "format", "text", "output format: text or json")

	// Add subcommands
	patternsCmd.AddCommand(patternsDetectCmd)
	patternsCmd.AddCommand(patternsListCmd)
	patternsCmd.AddCommand(patternsShowCmd)

	rootCmd.AddCommand(patternsCmd)
}

// patternsDetectCmd detects patterns in editorial feedback.
var patternsDetectCmd = &cobra.Command{
	Use:   "detect",
	Short: "Detect patterns in editorial feedback",
	Long: `Detect repeated editorial feedback patterns.

Analysis:
  - Queries KindEditorialFeedback records from the memory store
  - Groups occurrences by author/profile/recipe/category
  - Identifies patterns where same feedback occurs multiple times
  - Filters by minimum occurrence threshold
  - Persists KindEditorialPattern records for detected patterns

The command outputs a summary of detected patterns and the record IDs
that were persisted to the memory database.`,
	Example: `  newsroom patterns detect
  newsroom patterns detect --min-occurrences 3
  newsroom patterns detect --category factual_correction --min-occurrences 2`,
	Args: cobra.ExactArgs(0),
	RunE: doPatternsDetect,
}

// patternsListCmd lists detected patterns.
var patternsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List detected patterns",
	Long: `List detected editorial feedback patterns.

Lists all KindEditorialPattern records from the memory store, with
filtering options for categorization and grouping.`,
	Example: `  newsroom patterns list
  newsroom patterns list --category factual_correction
  newsroom patterns list --group-by author`,
	Args: cobra.ExactArgs(0),
	RunE: doPatternsList,
}

// patternsShowCmd shows details of a specific pattern.
var patternsShowCmd = &cobra.Command{
	Use:   "show <pattern-id>",
	Short: "Show details of a specific pattern",
	Long: `Show details of a detected editorial feedback pattern.

Displays the pattern's metadata, occurrences, and evidence.`,
	Example: `  newsroom patterns show pattern-abc123`,
	Args:    cobra.ExactArgs(1),
	RunE:    doPatternsShow,
}

// patternReport is the output format for patterns.
type patternReport struct {
	ID               string              `json:"id"`
	Category         string              `json:"category"`
	GroupKey         string              `json:"group_key"`
	MinOccurrences   int                 `json:"min_occurrences"`
	OccurrencesCount int                 `json:"occurrences_count"`
	SampleFeedback   string              `json:"sample_feedback,omitempty"`
	Occurrences      []patternOccurrence `json:"occurrences,omitempty"`
	Notes            string              `json:"notes,omitempty"`
}

type patternOccurrence struct {
	RecordID    string `json:"record_id"`
	Feedback    string `json:"feedback"`
	Author      string `json:"author"`
	GivenAt     string `json:"given_at"`
	SubjectKind string `json:"subject_kind"`
	SubjectName string `json:"subject_name"`
	Category    string `json:"category"`
	Field       string `json:"field"`
}

// parseTimeWindow parses a time window string like "72h" or "168h" into a duration.
func parseTimeWindow(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "h") {
		n := strings.TrimSuffix(s, "h")
		var hours int64
		_, err := fmt.Sscanf(n, "%d", &hours)
		if err != nil {
			return 0, fmt.Errorf("invalid time window: %v", err)
		}
		return time.Duration(hours) * time.Hour, nil
	}
	if strings.HasSuffix(s, "d") {
		n := strings.TrimSuffix(s, "d")
		var days int64
		_, err := fmt.Sscanf(n, "%d", &days)
		if err != nil {
			return 0, fmt.Errorf("invalid time window: %v", err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("unsupported time window format: %s (use 'h' or 'd' suffix)", s)
}

// doPatternsDetect implements "newsroom patterns detect".
func doPatternsDetect(cmd *cobra.Command, args []string) error {
	if v := strings.ToLower(strings.TrimSpace(pf4.format)); v != "text" && v != "json" {
		return exitf(2, "invalid --format %q: use text or json", pf4.format)
	}

	_, err := filepath.Abs(pf4.workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolving workspace root: %w", err)
	}

	// Parse time window
	timeWindow, err := parseTimeWindow(pf4.timeWindow)
	if err != nil {
		return err
	}

	// Determine GroupBy value
	groupBy := patterns.GroupBy(pf4.groupBy)
	switch groupBy {
	case patterns.GroupByAuthor, patterns.GroupByProfile, patterns.GroupByRecipe, patterns.GroupByCategory:
		// valid
	default:
		return exitf(2, "invalid --group-by %q: use author, profile, recipe, or category", pf4.groupBy)
	}

	// Build pattern config
	cfg := patterns.Config{
		MinOccurrences: pf4.minOccurs,
		TimeWindow:     timeWindow,
		GroupBy:        groupBy,
		Category:       pf4.category,
	}

	// Get memory database path
	dbPath, _, err := appConfig.GetString(config.SettingMemoryDBPath, config.Override{})
	if err != nil {
		return fmt.Errorf("reading the memory database path: %w", err)
	}

	var backing memorystore.Store
	if dbPath == "" {
		backing, err = memorystore.NewSQLiteStore("", memorystore.DefaultDBName)
	} else {
		backing, err = memorystore.NewSQLiteStoreAt(dbPath)
	}
	if err != nil {
		return fmt.Errorf("opening the memory store: %w", err)
	}
	defer func() { _ = backing.Close() }()

	// Detect patterns
	patternStore := patterns.NewStore(backing, nil)
	patternRecords, err := patternStore.DetectPatterns(cmd.Context(), cfg)
	if err != nil {
		return fmt.Errorf("detecting patterns: %w", err)
	}

	if len(patternRecords) == 0 {
		if pf4.format == "json" {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), marshalJSON(struct {
				Count int `json:"count"`
			}{0}))
			if err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
		} else {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "No patterns detected.")
			if err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
		}
		return nil
	}

	// Persist patterns
	var ids []string
	for _, rec := range patternRecords {
		id := memorystore.DeriveStableID(rec)
		rec.StableID = id
		if err := backing.Write(cmd.Context(), rec); err != nil {
			return fmt.Errorf("persisting pattern record: %w", err)
		}
		ids = append(ids, string(id))
	}

	if pf4.format == "json" {
		// Build JSON report
		var reports []patternReport
		for _, rec := range patternRecords {
			decoded, err := rec.DecodeValue()
			if err != nil {
				return fmt.Errorf("decoding pattern record: %w", err)
			}

			if pr, ok := decoded.(memory.PatternRecord); ok {
				var occurrences []patternOccurrence
				for _, occ := range pr.Occurrences {
					occurrences = append(occurrences, patternOccurrence{
						RecordID:    occ.RecordID,
						Feedback:    occ.Feedback,
						Author:      occ.Author,
						GivenAt:     occ.GivenAt.Format(time.RFC3339),
						SubjectKind: occ.SubjectKind,
						SubjectName: occ.SubjectName,
						Category:    occ.Category,
						Field:       occ.Field,
					})
				}

				reports = append(reports, patternReport{
					ID:               string(rec.StableID),
					Category:         pr.Category,
					GroupKey:         pr.GroupKey,
					MinOccurrences:   pr.MinOccurrences,
					OccurrencesCount: len(pr.Occurrences),
					SampleFeedback:   pr.SampleFeedback,
					Occurrences:      occurrences,
					Notes:            pr.Notes,
				})
			}
		}

		// Output JSON
		data, err := json.MarshalIndent(reports, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling reports: %w", err)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	} else {
		// Text output
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Detected %d pattern(s):\n\n", len(patternRecords))
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
		for i, rec := range patternRecords {
			decoded, err := rec.DecodeValue()
			if err != nil {
				return fmt.Errorf("decoding pattern record: %w", err)
			}

			if pr, ok := decoded.(memory.PatternRecord); ok {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Pattern %d:\n", i+1)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  ID: %s\n", rec.StableID)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  Category: %s\n", pr.Category)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  Group: %s\n", pr.GroupKey)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  Min occurrences: %d\n", pr.MinOccurrences)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  Occurrences: %d\n", len(pr.Occurrences))
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  Sample feedback: %s\n", pr.SampleFeedback)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				if pr.Notes != "" {
					_, err = fmt.Fprintf(cmd.OutOrStdout(), "  Notes: %s\n", pr.Notes)
					if err != nil {
						return fmt.Errorf("writing output: %w", err)
					}
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout())
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
			}
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Persisted %d pattern record(s) to memory database.\n", len(ids))
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	}

	return nil
}

// doPatternsList implements "newsroom patterns list".
func doPatternsList(cmd *cobra.Command, args []string) error {
	if v := strings.ToLower(strings.TrimSpace(pf4.format)); v != "text" && v != "json" {
		return exitf(2, "invalid --format %q: use text or json", pf4.format)
	}

	_, err := filepath.Abs(pf4.workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolving workspace root: %w", err)
	}

	// Get memory database path
	dbPath, _, err := appConfig.GetString(config.SettingMemoryDBPath, config.Override{})
	if err != nil {
		return fmt.Errorf("reading the memory database path: %w", err)
	}

	var backing memorystore.Store
	if dbPath == "" {
		backing, err = memorystore.NewSQLiteStore("", memorystore.DefaultDBName)
	} else {
		backing, err = memorystore.NewSQLiteStoreAt(dbPath)
	}
	if err != nil {
		return fmt.Errorf("opening the memory store: %w", err)
	}
	defer func() { _ = backing.Close() }()

	// Query pattern records
	q := memorystore.Query{
		Kind: memory.KindEditorialPattern,
	}

	records, err := backing.Query(cmd.Context(), q)
	if err != nil {
		return fmt.Errorf("querying pattern records: %w", err)
	}

	if len(records) == 0 {
		if pf4.format == "json" {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), marshalJSON(struct {
				Count int `json:"count"`
			}{0}))
			if err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
		} else {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "No patterns found.")
			if err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
		}
		return nil
	}

	if pf4.format == "json" {
		var reports []patternReport
		for _, rec := range records {
			decoded, err := rec.DecodeValue()
			if err != nil {
				return fmt.Errorf("decoding pattern record: %w", err)
			}

			if pr, ok := decoded.(memory.PatternRecord); ok {
				var occurrences []patternOccurrence
				for _, occ := range pr.Occurrences {
					occurrences = append(occurrences, patternOccurrence{
						RecordID:    occ.RecordID,
						Feedback:    occ.Feedback,
						Author:      occ.Author,
						GivenAt:     occ.GivenAt.Format(time.RFC3339),
						SubjectKind: occ.SubjectKind,
						SubjectName: occ.SubjectName,
						Category:    occ.Category,
						Field:       occ.Field,
					})
				}

				reports = append(reports, patternReport{
					ID:               string(rec.StableID),
					Category:         pr.Category,
					GroupKey:         pr.GroupKey,
					MinOccurrences:   pr.MinOccurrences,
					OccurrencesCount: len(pr.Occurrences),
					SampleFeedback:   pr.SampleFeedback,
					Occurrences:      occurrences,
					Notes:            pr.Notes,
				})
			}
		}

		data, err := json.MarshalIndent(reports, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling reports: %w", err)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	} else {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Found %d pattern(s):\n\n", len(records))
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
		for i, rec := range records {
			decoded, err := rec.DecodeValue()
			if err != nil {
				return fmt.Errorf("decoding pattern record: %w", err)
			}

			if pr, ok := decoded.(memory.PatternRecord); ok {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Pattern %d:\n", i+1)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  ID: %s\n", rec.StableID)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  Category: %s\n", pr.Category)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  Group: %s\n", pr.GroupKey)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  Occurrences: %d\n", len(pr.Occurrences))
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  Notes: %s\n", pr.Notes)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout())
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
			}
		}
	}

	return nil
}

// doPatternsShow implements "newsroom patterns show".
func doPatternsShow(cmd *cobra.Command, args []string) error {
	if v := strings.ToLower(strings.TrimSpace(pf4.format)); v != "text" && v != "json" {
		return exitf(2, "invalid --format %q: use text or json", pf4.format)
	}

	patternID := memory.MemoryStableID(strings.TrimSpace(args[0]))

	// Get memory database path
	dbPath, _, err := appConfig.GetString(config.SettingMemoryDBPath, config.Override{})
	if err != nil {
		return fmt.Errorf("reading the memory database path: %w", err)
	}

	var backing memorystore.Store
	if dbPath == "" {
		backing, err = memorystore.NewSQLiteStore("", memorystore.DefaultDBName)
	} else {
		backing, err = memorystore.NewSQLiteStoreAt(dbPath)
	}
	if err != nil {
		return fmt.Errorf("opening the memory store: %w", err)
	}
	defer func() { _ = backing.Close() }()

	// Get pattern record
	rec, err := backing.Get(cmd.Context(), patternID)
	if err != nil {
		return fmt.Errorf("getting pattern record: %w", err)
	}
	if rec == nil {
		return exitf(4, "pattern record %s not found", patternID)
	}

	// Decode pattern
	decoded, err := rec.DecodeValue()
	if err != nil {
		return fmt.Errorf("decoding pattern record: %w", err)
	}

	pr, ok := decoded.(memory.PatternRecord)
	if !ok {
		return exitf(5, "record %s is not a pattern (kind: %s)", patternID, rec.Kind)
	}

	if pf4.format == "json" {
		var occurrences []patternOccurrence
		for _, occ := range pr.Occurrences {
			occurrences = append(occurrences, patternOccurrence{
				RecordID:    occ.RecordID,
				Feedback:    occ.Feedback,
				Author:      occ.Author,
				GivenAt:     occ.GivenAt.Format(time.RFC3339),
				SubjectKind: occ.SubjectKind,
				SubjectName: occ.SubjectName,
				Category:    occ.Category,
				Field:       occ.Field,
			})
		}

		report := patternReport{
			ID:               string(patternID),
			Category:         pr.Category,
			GroupKey:         pr.GroupKey,
			MinOccurrences:   pr.MinOccurrences,
			OccurrencesCount: len(pr.Occurrences),
			SampleFeedback:   pr.SampleFeedback,
			Occurrences:      occurrences,
			Notes:            pr.Notes,
		}

		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshaling report: %w", err)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	} else {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Pattern %s\n", patternID)
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "====================\n\n")
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Category:      %s\n", pr.Category)
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Group:         %s\n", pr.GroupKey)
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Min occurrences: %d\n", pr.MinOccurrences)
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Occurrences:     %d\n", len(pr.Occurrences))
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Sample feedback: %s\n", pr.SampleFeedback)
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
		if pr.Notes != "" {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Notes:           %s\n", pr.Notes)
			if err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout())
		if err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		if len(pr.Occurrences) > 0 {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Occurrences:\n")
			if err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout())
			if err != nil {
				return fmt.Errorf("writing output: %w", err)
			}
			for i, occ := range pr.Occurrences {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "  %d. %s\n", i+1, occ.Feedback)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "     Record: %s\n", occ.RecordID)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "     Author: %s\n", occ.Author)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "     Subject: %s / %s\n", occ.SubjectKind, occ.SubjectName)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "     Category: %s\n", occ.Category)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "     Field: %s\n", occ.Field)
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "     Given at: %s\n", occ.GivenAt.Format(time.RFC3339))
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout())
				if err != nil {
					return fmt.Errorf("writing output: %w", err)
				}
			}
		}
	}

	return nil
}
