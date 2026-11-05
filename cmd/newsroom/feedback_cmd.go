package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/editorialedit"
	"github.com/Mundo-Dolphins/local-newsroom/internal/feedback"
	"github.com/Mundo-Dolphins/local-newsroom/internal/memorystore"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// feedbackCmd is the "newsroom feedback" subcommand (v0.7, issue #147): it
// analyzes the generated vs human-edited final artifact pair of a completed
// run and records the analysis in the editorial memory.
//
// The analysis has two layers: a deterministic diff of the two persisted
// documents (always produced, always preserved on the records) and an
// optional classification of the diff hunks (a structured-output LLM call
// when --classify is set and an LLM is configured). The classifier is
// advisory: labels carry their own confidence and are never factual truth,
// and any classifier failure degrades the analysis to the bare diff.
//
// Records: one editorial-feedback record per classified, non-omitted diff
// hunk; one correction record per high-confidence fact-changing hunk; and
// one run-level summary record for every analyzed pair (including identical
// ones). All are content-addressed in the memory store, so re-running the
// command for an unedited pair is idempotent.
var feedbackCmd = &cobra.Command{
	Use:   "feedback <run-id>",
	Short: "Analyze the generated vs final artifact pair of a run and record the editorial feedback",
	Long: `Analyze the editorial changes between a run's generated output and its
human-edited final version, and record the analysis in the editorial memory.

  newsroom feedback <run-id> [--classify] [--format text|json]

The command requires the run's recorded final pair (record it first with
"newsroom final"). It diffs the two persisted documents deterministically;
with --classify it additionally labels each diff hunk with the editorial
change categories (factual correction, attribution, unsupported certainty,
tone, vocabulary, structure, omission, addition, length, headline,
formatting, thread split/merge) through a structured-output LLM call, and
records high-confidence factual corrections as correction records.

Deterministic facts (the diff, line and word counts) never depend on the
classifier; a classifier that fails leaves the analysis unclassified and the
command still records the deterministic diff.`,
	Example: `  newsroom feedback run-20260711T100000Z-abcdef0123
  newsroom feedback run-20260711T100000Z-abcdef0123 --classify --format json`,
	Args: cobra.ExactArgs(1),
	RunE: doFeedback,
}

// feedbackFlags holds the flags for the feedback command.
type feedbackFlags struct {
	workspaceRoot string
	classify      bool
	format        string
}

var fl4 feedbackFlags

func init() {
	f := feedbackCmd.Flags()
	f.StringVar(&fl4.workspaceRoot, "workspace-root", ".newsroom", "workspace root directory")
	f.BoolVar(&fl4.classify, "classify", false, "classify the diff hunks with the LLM (requires an LLM to be configured)")
	f.StringVar(&fl4.format, "format", "text", "output format: text or json")
	rootCmd.AddCommand(feedbackCmd)
}

// feedbackHunkReport is the per-hunk projection of one analysis in JSON mode.
type feedbackHunkReport struct {
	Index      int      `json:"index"`
	Diff       string   `json:"diff"`
	Categories []string `json:"categories,omitempty"`
	Note       string   `json:"note,omitempty"`
	Confidence float64  `json:"confidence"`
	Factual    bool     `json:"factual,omitempty"`
}

// feedbackReport is the JSON document emitted in --format json mode.
type feedbackReport struct {
	RunID           string               `json:"run_id"`
	GeneratedPath   string               `json:"generated_path"`
	FinalPath       string               `json:"final_path"`
	GeneratedHash   string               `json:"generated_hash"`
	FinalHash       string               `json:"final_hash"`
	Identical       bool                 `json:"identical"`
	AddedLines      int                  `json:"added_lines"`
	DeletedLines    int                  `json:"deleted_lines"`
	BeforeWords     int                  `json:"before_words"`
	AfterWords      int                  `json:"after_words"`
	HeadlineChanged bool                 `json:"headline_changed"`
	Classified      bool                 `json:"classified"`
	Classifier      string               `json:"classifier,omitempty"`
	FactualChange   bool                 `json:"factual_change"`
	HighConfidence  bool                 `json:"high_confidence"`
	Hunks           []feedbackHunkReport `json:"hunks"`
	Records         []string             `json:"record_ids"`
	AnalyzeError    string               `json:"analyze_error,omitempty"`
}

// doFeedback implements "newsroom feedback".
func doFeedback(cmd *cobra.Command, args []string) error {
	if v := strings.ToLower(strings.TrimSpace(fl4.format)); v != "text" && v != "json" {
		return exitf(2, "invalid --format %q: use text or json", fl4.format)
	}
	id := run.RunID(strings.TrimSpace(args[0]))
	if err := id.Validate(); err != nil {
		return exitf(2, "invalid run ID %q: %v", args[0], err)
	}

	root, err := filepath.Abs(fl4.workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolving workspace root: %w", err)
	}
	// The sanitizer is seeded from the current configuration only (like
	// final/publish): every byte crossing to disk passes the same boundary.
	store, err := workspace.NewStoreWithSanitizer(root, run.NewSanitizer(appConfig.SecretValues(nil)...))
	if err != nil {
		return fmt.Errorf("creating workspace store at %s: %w", root, err)
	}

	m, _, err := store.LoadManifest(id)
	if err != nil {
		if errors.Is(err, workspace.ErrRunNotFound) {
			if fl4.format == "json" {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), marshalJSON(feedbackReport{RunID: string(id)}))
			}
			return exitf(8, "run %s not found in workspace %s", id, root)
		}
		return fmt.Errorf("loading run %s: %w", id, err)
	}
	if m.FinalState == nil {
		if fl4.format == "json" {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), marshalJSON(feedbackReport{RunID: string(id)}))
		}
		return exitf(7, "run %s has no recorded final artifact pair: run 'newsroom final %s --final <file> --editor <who>' first", id, id)
	}
	fs := *m.FinalState

	// Load the persisted pair through the workspace store (the sanitization
	// boundary; the same path every run file crosses).
	genData, err := store.LoadArtifact(id, fs.GeneratedPath)
	if err != nil {
		return fmt.Errorf("loading the generated document %s: %w", fs.GeneratedPath, err)
	}
	finalData, err := store.LoadArtifact(id, fs.FinalPath)
	if err != nil {
		return fmt.Errorf("loading the final document %s: %w", fs.FinalPath, err)
	}

	classifier, err := buildFeedbackClassifier(cmd)
	if err != nil {
		return err
	}

	// The analysis: deterministic diff unconditionally; classification when a
	// classifier was built.
	res, analyzeErr := editorialedit.Analyze(cmd.Context(), classifier, fs.GeneratedPath, string(genData), fs.FinalPath, string(finalData), time.Now().UTC())
	if analyzeErr != nil {
		// Advisory-layer failure: the result is the bare deterministic diff
		// (Analyze leaves Classified false on error); keep going and record
		// it, with the warning surfaced in the output.
		res.Classified = false
	}

	report := buildFeedbackReport(id, &fs, res)
	if analyzeErr != nil {
		report.AnalyzeError = analyzeErr.Error()
	}

	// Persist the records (the analysis is in hand regardless).
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

	src := feedback.Source{
		RunID:           string(m.ID),
		GeneratedPath:   fs.GeneratedPath,
		FinalPath:       fs.FinalPath,
		GeneratorDigest: res.BeforeDigest,
		FinalDigest:     res.AfterDigest,
		Editor:          fs.Editor,
	}
	if m.Recipe != nil {
		src.Recipe = m.Recipe.ID
	}
	if m.Profile != nil {
		src.Profile = m.Profile.ID
	}
	ids, err := feedback.NewStore(backing, nil).StoreAnalysis(cmd.Context(), res, src, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("storing the feedback records: %w", err)
	}
	report.Records = ids

	printFeedback(cmd, report, analyzeErr)
	return nil
}

// buildFeedbackClassifier builds the LLM classifier when --classify is set
// (an LLM must be configured then), or nil otherwise (deterministic-only
// analysis).
func buildFeedbackClassifier(cmd *cobra.Command) (editorialedit.Classifier, error) {
	if !fl4.classify {
		return nil, nil
	}
	client, err := buildLLMClient(cmd)
	if err != nil {
		return nil, fmt.Errorf("--classify requires an LLM: %w", err)
	}
	model, _, err := appConfig.GetString(config.SettingLLMModel, flagOverride(cmd, "llm-model"))
	if err != nil {
		return nil, fmt.Errorf("resolving the LLM model: %w", err)
	}
	return editorialedit.NewClassifyLLM(client, model, ""), nil
}

// buildFeedbackReport projects the analysis onto the report shape.
func buildFeedbackReport(id run.RunID, fs *run.FinalState, res *editorialedit.Result) *feedbackReport {
	r := &feedbackReport{
		RunID:           string(id),
		GeneratedPath:   fs.GeneratedPath,
		FinalPath:       fs.FinalPath,
		GeneratedHash:   fs.GeneratedHash,
		FinalHash:       fs.FinalHash,
		Identical:       res.Identical,
		AddedLines:      res.AddedLines,
		DeletedLines:    res.DeletedLines,
		BeforeWords:     res.BeforeWords,
		AfterWords:      res.AfterWords,
		HeadlineChanged: res.HeadlineChanged,
		Classified:      res.Classified,
		Classifier:      res.Classifier,
		FactualChange:   res.FactualChange,
		HighConfidence:  res.HighConfidence,
	}
	for i, cl := range res.Classifications {
		h := feedbackHunkReport{
			Index:      i,
			Diff:       res.DiffTextForHunk(i),
			Note:       cl.Note,
			Confidence: cl.Confidence,
			Factual:    editorialedit.HasFactualChange(cl.Categories),
		}
		for _, c := range cl.Categories {
			h.Categories = append(h.Categories, string(c))
		}
		r.Hunks = append(r.Hunks, h)
	}
	return r
}

// printFeedback renders the text-mode output.
// printFeedback renders the text-mode output.
func printFeedback(cmd *cobra.Command, report *feedbackReport, analyzeErr error) {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "Run:          %s\n", report.RunID)
	_, _ = fmt.Fprintf(out, "Documents:    %s  vs  %s\n", report.GeneratedPath, report.FinalPath)
	if report.Identical {
		_, _ = fmt.Fprintln(out, "Result:       generated and final are identical — no editorial change")
	} else {
		_, _ = fmt.Fprintf(out, "Diff:         %d lines added, %d lines deleted (%d → %d words)",
			report.AddedLines, report.DeletedLines, report.BeforeWords, report.AfterWords)
		if report.HeadlineChanged {
			_, _ = fmt.Fprint(out, ", headline changed")
		}
		_, _ = fmt.Fprintln(out)
		if report.Classified {
			_, _ = fmt.Fprintf(out, "Classification: %s — factual change: %v (high confidence: %v)\n",
				orDash(report.Classifier), report.FactualChange, report.HighConfidence)
		} else {
			_, _ = fmt.Fprintln(out, "Classification: none (deterministic diff only)")
			if analyzeErr != nil {
				_, _ = fmt.Fprintf(out, "  note: classifier failed: %v\n", analyzeErr)
			}
		}
	}
	if len(report.Records) > 0 {
		_, _ = fmt.Fprintf(out, "Memory records: %d stored\n", len(report.Records))
	}
}

// orDash renders a possibly-empty string as itself or "-".
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

var _ = json.Marshal
