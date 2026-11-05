package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// finalCmd is the "newsroom final" subcommand (v0.7, issue #146): the
// explicit user action that records the generated vs human-edited final
// artifact pair of a completed run.
//
// The run's output/ originals are never touched: the generated output is
// copied verbatim into <run dir>/final/generated/ and the human's edited
// file into <run dir>/final/, both baselined by content digest and recorded
// on the manifest's final_state summary. Nothing is inferred from
// publication state — the pair is recorded only because the user asked for
// it.
var finalCmd = &cobra.Command{
	Use:   "final <run-id> --final <file> --editor <who>",
	Short: "Record the human-edited final artifact of a completed run",
	Long: `Record the generated vs human-edited final artifact pair of a completed run.

A final artifact pair is the editorial loop made durable: the run's
generated output (the rendered article or thread under output/) is copied
verbatim into the run directory (final/generated/), the human-edited final
file is stored beside it (final/), and both are baselined by content digest
on the manifest. The originals under output/ are never overwritten, and the
record is written only through this explicit command — never inferred from
publication state.

  newsroom final <run-id> --final <edited-file> --editor <who>
                    [--published-uri <url>] [--format text|json]

The run must be completed (succeeded) and READY (final factual check
passed); otherwise the command refuses.`,
	Example: `  newsroom final run-20260711T100000Z-abcdef0123 --final article.md --editor javi
  newsroom final run-20260711T100000Z-abcdef0123 --final ./edited/thread.txt --editor javi --published-uri https://bsky.app/profile/me/post/1`,
	Args: cobra.ExactArgs(1),
	RunE: doFinal,
}

// finalFlags holds the flags for the final command.
type finalFlags struct {
	workspaceRoot string
	finalPath     string
	editor        string
	publishedURI  string
	format        string
}

// fl3 are the final command flags (the run and review commands keep the
// historical names).
var fl3 finalFlags

func init() {
	f := finalCmd.Flags()
	f.StringVar(&fl3.workspaceRoot, "workspace-root", ".newsroom", "workspace root directory")
	f.StringVar(&fl3.finalPath, "final", "", "path to the human-edited final artifact (run-relative or absolute)")
	f.StringVar(&fl3.editor, "editor", "", "identity of the human editor (required)")
	f.StringVar(&fl3.publishedURI, "published-uri", "", "optional external reference of the publication result (http(s) URL)")
	f.StringVar(&fl3.format, "format", "text", "output format: text or json")
	_ = finalCmd.MarkFlagRequired("final")
	_ = finalCmd.MarkFlagRequired("editor")
	rootCmd.AddCommand(finalCmd)
}

// finalRecord is the machine-readable projection of a recorded pair.
type finalRecord struct {
	GeneratedPath string `json:"generated_path"`
	GeneratedHash string `json:"generated_hash"`
	FinalPath     string `json:"final_path"`
	FinalHash     string `json:"final_hash"`
	Editor        string `json:"editor"`
	EditedAt      string `json:"edited_at"`
	PublishedURI  string `json:"published_uri,omitempty"`
}

// finalReport is the JSON document emitted in --format json mode.
type finalReport struct {
	RunID  string       `json:"run_id"`
	Status string       `json:"status"`
	Topic  string       `json:"topic"`
	Record *finalRecord `json:"record"`
}

func finalReportOf(m *run.Manifest, r *publish.RecordFinalResult, req publish.RecordFinalRequest) finalReport {
	rep := finalReport{
		RunID:  string(m.ID),
		Status: string(m.Status),
		Topic:  m.Topic,
	}
	fs := m.FinalState
	if fs == nil {
		return rep
	}
	rep.Record = &finalRecord{
		GeneratedPath: fs.GeneratedPath,
		GeneratedHash: fs.GeneratedHash,
		FinalPath:     fs.FinalPath,
		FinalHash:     fs.FinalHash,
		Editor:        fs.Editor,
		EditedAt:      fs.EditedAt.String(),
		PublishedURI:  fs.PublishedURI,
	}
	return rep
}

// doFinal implements "newsroom final".
func doFinal(cmd *cobra.Command, args []string) error {
	if v := strings.ToLower(strings.TrimSpace(fl3.format)); v != "text" && v != "json" {
		return exitf(2, "invalid --format %q: use text or json", fl3.format)
	}
	id := run.RunID(strings.TrimSpace(args[0]))
	if err := id.Validate(); err != nil {
		return exitf(2, "invalid run ID %q: %v", args[0], err)
	}

	root, err := filepath.Abs(fl3.workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolving workspace root: %w", err)
	}
	// The sanitizer is seeded from the current configuration only (like
	// publish): every byte crossing to disk passes the same boundary.
	store, err := workspace.NewStoreWithSanitizer(root, run.NewSanitizer(appConfig.SecretValues(nil)...))
	if err != nil {
		return fmt.Errorf("creating workspace store at %s: %w", root, err)
	}

	m, _, err := store.LoadManifest(id)
	if err != nil {
		if errors.Is(err, workspace.ErrRunNotFound) {
			if fl3.format == "json" {
				if _, werr := fmt.Fprintln(cmd.OutOrStdout(), marshalJSON(finalReport{RunID: string(id), Status: "unknown"})); werr != nil {
					return fmt.Errorf("writing JSON output: %w", werr)
				}
			}
			return exitf(8, "run %s not found in workspace %s", id, root)
		}
		return fmt.Errorf("loading run %s: %w", id, err)
	}

	bundle, err := review.Build(store, m)
	if err != nil {
		return fmt.Errorf("building the review bundle for run %s: %w", id, err)
	}

	eng := publish.New(store)
	req := publish.RecordFinalRequest{
		FinalPath:    fl3.finalPath,
		Editor:       fl3.editor,
		PublishedURI: fl3.publishedURI,
	}
	res, err := eng.RecordFinal(cmd.Context(), id, req, bundle)
	if err != nil {
		if fl3.format == "json" {
			if _, werr := fmt.Fprintln(cmd.OutOrStdout(), marshalJSON(finalReportOf(m, nil, req))); werr != nil {
				return fmt.Errorf("writing JSON output: %w", werr)
			}
		}
		code := 8
		if errors.Is(err, publish.ErrFinalNotCompleted) {
			code = 7
		}
		return exitf(code, "%v", err)
	}

	rep := finalReportOf(m, res, req)
	if fl3.format == "json" {
		if _, werr := fmt.Fprintln(cmd.OutOrStdout(), marshalJSON(rep)); werr != nil {
			return fmt.Errorf("writing JSON output: %w", werr)
		}
		return nil
	}
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "Recorded the generated vs human-edited final artifact pair for run %s.\n", id); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}
	if _, err := fmt.Fprintf(out, "  generated: %s (%s)\n", res.GeneratedPath, res.GeneratedHash); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}
	if _, err := fmt.Fprintf(out, "  final:     %s (%s)\n", res.FinalPath, res.FinalHash); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}
	if _, err := fmt.Fprintf(out, "  editor:    %s\n", fl3.editor); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}
	if fl3.publishedURI != "" {
		if _, err := fmt.Fprintf(out, "  published: %s\n", fl3.publishedURI); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}
	}
	if _, err := fmt.Fprintf(out, "Both versions are preserved; the original output is unchanged.\n"); err != nil {
		return fmt.Errorf("writing output: %w", err)
	}
	return nil
}

// marshalJSON renders v as a single indented JSON line.
func marshalJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}
