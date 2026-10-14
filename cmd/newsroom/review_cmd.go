package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// reviewCmd is the "newsroom review" subcommand: it shows (and refreshes)
// the deterministic human review bundle of a run — the single document a
// person reads to decide what a run produced and whether it can be used.
var reviewCmd = &cobra.Command{
	Use:   "review <run-id>",
	Short: "Show the human review bundle for a run",
	Long: `Show the human review bundle for a run: a single, human-readable
Markdown document that summarizes the run (topic, sources, claims,
verification results, follow-up research, final factual check, output
files, and the run's profile and model) and states plainly whether the
output is ready to use.

The bundle is a deterministic projection of the run's persisted record:
it never calls an LLM, and re-running it always produces the same document.
By default it also refreshes review.md inside the run directory so the
copy on disk matches what is printed.

  newsroom review <run-id>

Review bundles exist for every terminal run — succeeded, failed, and
cancelled alike — because a failed run is exactly when a human needs to
know what happened.`,
	Example: `  newsroom review run-20260711T100000Z-abcdef0123
  newsroom review run-20260711T100000Z-abcdef0123 --no-save
  newsroom review run-20260711T100000Z-abcdef0123 --workspace-root .newsroom`,
	Args: cobra.ExactArgs(1),
	RunE: doReview,
}

// reviewFlags holds the flags for the review command.
type reviewFlags struct {
	workspaceRoot string
	noSave        bool
}

// rf2 are the review command flags (the run command keeps the historical name).
var rf2 reviewFlags

func init() {
	f := reviewCmd.Flags()
	f.StringVar(&rf2.workspaceRoot, "workspace-root", ".newsroom", "workspace root directory")
	f.BoolVar(&rf2.noSave, "no-save", false, "print the bundle without refreshing review.md")
	rootCmd.AddCommand(reviewCmd)
}

// doReview implements "newsroom review <run-id>".
func doReview(cmd *cobra.Command, args []string) error {
	id := run.RunID(args[0])
	if err := id.Validate(); err != nil {
		return err
	}
	root, err := filepath.Abs(rf2.workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolving workspace root: %w", err)
	}
	store, err := workspace.NewStoreWithSanitizer(root, run.NewSanitizer(appConfig.SecretValues(nil)...))
	if err != nil {
		return fmt.Errorf("creating workspace store: %w", err)
	}

	m, _, err := store.LoadManifest(id)
	if err != nil {
		if errors.Is(err, workspace.ErrRunNotFound) {
			return fmt.Errorf("run %s not found in workspace %s", id, root)
		}
		return fmt.Errorf("loading run %s: %w", id, err)
	}

	b, err := review.Build(store, m)
	if err != nil {
		return fmt.Errorf("building the review bundle: %w", err)
	}

	fmt.Print(b.Render())

	if rf2.noSave {
		return nil
	}
	path, err := review.Save(store, b)
	if err != nil {
		// The bundle is a convenience: a failure to persist it must not
		// change the command's outcome (the document was already printed).
		fmt.Fprintf(os.Stderr, "warning: could not refresh %s: %v\n", run.ReviewFileName, err)
		return nil
	}
	fmt.Fprintf(os.Stderr, "review bundle saved: %s\n", path)
	return nil
}
