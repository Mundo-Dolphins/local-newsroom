package review

import (
	"fmt"
	"path/filepath"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
)

// Save renders the bundle and persists it in the run directory as review.md,
// through the store's artifact persistence path — the same single
// sanitization boundary every other run file crosses (content sanitized,
// atomic write, redaction metadata synced) — so the file on disk is
// guaranteed secret-free.
//
// The file is deliberately NOT referenced on the manifest: a failed or
// cancelled run cannot carry new artifact references, yet the review bundle
// must exist for exactly those runs. It is a local convenience file in the
// run directory — listed in the "Evidence files" section of every review —
// not a pipeline artifact.
//
// It returns the absolute path of the written file.
func Save(store runner.Store, b *Bundle) (string, error) {
	if store == nil {
		return "", fmt.Errorf("review: store is required")
	}
	if b == nil {
		return "", fmt.Errorf("review: bundle is required")
	}
	if b.RunID == "" {
		return "", fmt.Errorf("review: bundle has no run id")
	}
	dir, err := store.RunDir(b.RunID)
	if err != nil {
		return "", fmt.Errorf("review: run directory: %w", err)
	}

	ref := run.ArtifactRef{
		Kind:       run.ArtifactKindCustom,
		ID:         "review-bundle",
		Stage:      "", // not a stage artifact: never recorded on the manifest
		Path:       run.ReviewFileName,
		ProducedAt: bundleProducedAt(b),
	}
	rel, err := store.SaveArtifact(b.RunID, ref, []byte(b.Render()))
	if err != nil {
		return "", fmt.Errorf("review: saving %s: %w", run.ReviewFileName, err)
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), nil
}

// bundleProducedAt derives the bundle's recorded production time from the
// run's own timestamps (deterministic: never the wall clock). A review
// written after the run finished is stamped with the run's last update so
// re-generating the bundle does not churn the record.
func bundleProducedAt(b *Bundle) run.Timestamp {
	if b.FinishedAt != nil && !b.FinishedAt.IsZero() {
		return *b.FinishedAt
	}
	if !b.UpdatedAt.IsZero() {
		return b.UpdatedAt
	}
	return b.StartedAt
}
