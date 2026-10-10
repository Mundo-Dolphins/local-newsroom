package runner_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// Unmistakable sentinels (same contract as the workspace suite): any of
// these strings appearing anywhere under the run directory is a failure.
//
//   - sentinelA is low-entropy: only the exact-value path and high-confidence
//     pattern contexts (Bearer) catch it.
//   - sentinelB is high-entropy: pattern detectors catch it in bare prose too.
const (
	sentinelA = "TEST_SECRET_DO_NOT_PERSIST_123456"
	sentinelB = "TEST_SECRET_DO_NOT_PERSIST_xK9mQ2vN8pR5tW1yZ4cD7fG0hJ"
)

// newSentinelStore builds a store whose sanitizer knows both sentinels as
// exact configured secret values (the OMLX_API_KEY / SEARXNG_API_KEY path).
func newSentinelStore(t *testing.T) *workspace.Store {
	t.Helper()
	store, err := workspace.NewStoreWithSanitizer(t.TempDir(), run.NewSanitizer(sentinelA, sentinelB))
	if err != nil {
		t.Fatalf("NewStoreWithSanitizer: %v", err)
	}
	return store
}

// requireNoSentinelsInRunTree recursively scans the run directory and fails
// if any persisted file (artifact, sidecar, manifest) contains either
// sentinel.
func requireNoSentinelsInRunTree(t *testing.T, store *workspace.Store, id run.RunID) {
	t.Helper()
	runDir := filepath.Join(store.RunsDir(), id.String())
	walkRunDir(t, runDir, func(path string, data []byte) {
		if bytes.Contains(data, []byte(sentinelA)) || bytes.Contains(data, []byte(sentinelB)) {
			t.Errorf("persisted file %s contains a sentinel:\n%s", path, data)
		}
	})
}

// TestRunSucceededRunTreeContainsNoSentinel sweeps a full successful run
// whose source page, document, dossier, and editorial artifacts all embed
// sentinels in different contexts. The run must succeed (redaction is not an
// error) and the entire persisted tree must be sentinel-free.
func TestRunSucceededRunTreeContainsNoSentinel(t *testing.T) {
	store := newSentinelStore(t)
	m := newTestManifest(t)

	plan := run.CanonicalStages()
	write := func(files ...runner.StageFile) runner.StageFunc {
		return func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
			return &runner.StageOutput{Files: files}, nil
		}
	}
	stages := []runner.Stage{
		{Name: plan[0], Fn: write(
			runner.StageFile{Path: "search-plan.json", Kind: run.ArtifactKindCustom, ID: "search-plan",
				Data: []byte(`{"queries":["EU AI Act","omlx key ` + sentinelA + `"]}`)},
			runner.StageFile{Path: "discovery.json", Kind: run.ArtifactKindCustom, ID: "discovery",
				Data: []byte(`{"candidates":["https://example.com/a?api_key=` + sentinelB + `"]}`)},
		)},
		{Name: plan[1], Fn: write(
			runner.StageFile{Path: "sources/source-1.md", Kind: run.ArtifactKindSource, ID: "source-1",
				Data: []byte("page body " + sentinelA + " and " + sentinelB + " end\n")},
		)},
		{Name: plan[2], Fn: write(
			runner.StageFile{Path: "documents/doc-1.md", Kind: run.ArtifactKindDocument, ID: "doc-1",
				Data: []byte("# Doc\n\nAuthorization: Bearer " + sentinelB + "\n")},
		)},
		{Name: plan[3], Fn: write(
			runner.StageFile{Path: "dossier.json", Kind: run.ArtifactKindDossier, ID: "dossier",
				Data: []byte(`{"claims":[{"text":"the page leaked ` + sentinelA + `"}]}`)},
		)},
		{Name: plan[4], Fn: write(
			runner.StageFile{Path: "verification.json", Kind: run.ArtifactKindVerification, ID: "verification",
				Data: []byte(`{"verdict":"verified","access_token":"` + sentinelB + `"}`)},
		)},
		{Name: plan[5], SkipReason: "no unresolved claims"},
		{Name: plan[6], Fn: write(
			runner.StageFile{Path: "artifact.json", Kind: run.ArtifactKindEditorial, ID: "editorial",
				Data: []byte(`{"headline":"EU AI Act takes effect","notes":"bearer ` + sentinelA + ` seen"}`)},
		)},
		{Name: plan[7], Fn: write(
			runner.StageFile{Path: "final-check.json", Kind: run.ArtifactKindFactualCheck, ID: "final-check",
				Data: []byte(`{"ok":true,"password":"` + sentinelB + `"}`)},
		)},
		{Name: plan[8], Fn: write(
			runner.StageFile{Path: "output/article.md", Kind: run.ArtifactKindRendered, ID: "article",
				Data: []byte("# EU AI Act takes effect\n")},
		)},
	}

	res, err := mustRun(t, context.Background(), m, store, stages, fixedClock())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != run.RunStatusSucceeded {
		t.Fatalf("status = %s, want succeeded", res.Status)
	}
	requireNoSentinelsInRunTree(t, store, m.ID)
}

// TestRunFailedRunTreeContainsNoSentinel sweeps a run whose stage fails with
// an error that quotes a credential. The failure diagnostics must be
// persisted (redacted) and the whole tree must stay sentinel-free.
func TestRunFailedRunTreeContainsNoSentinel(t *testing.T) {
	store := newSentinelStore(t)
	m := newTestManifest(t)

	plan := run.CanonicalStages()
	stages := []runner.Stage{
		{Name: plan[0], Fn: func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
			return &runner.StageOutput{Files: []runner.StageFile{
				{Path: "search-plan.json", Kind: run.ArtifactKindCustom, ID: "search-plan",
					Data: []byte(`{"queries":["EU AI Act"]}`)},
			}}, nil
		}},
		{Name: plan[1], Fn: func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
			// The stage reports a failure whose message quotes a live
			// credential in Bearer context.
			return nil, errors.New("fetch failed: server echoed Authorization: Bearer " + sentinelA)
		}},
		// The remaining stages are never reached.
		{Name: plan[2], SkipReason: "not reached"}, {Name: plan[3], SkipReason: "not reached"},
		{Name: plan[4], SkipReason: "not reached"}, {Name: plan[5], SkipReason: "not reached"},
		{Name: plan[6], SkipReason: "not reached"}, {Name: plan[7], SkipReason: "not reached"},
		{Name: plan[8], SkipReason: "not reached"},
	}

	res, err := mustRun(t, context.Background(), m, store, stages, fixedClock())
	if err == nil {
		t.Fatal("Run returned no error for a failed run")
	}
	if res.Status != run.RunStatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if strings.Contains(res.Err.Error(), sentinelA) {
		t.Errorf("result error leaks the sentinel: %v", res.Err)
	}
	if len(res.Manifest.Errors) == 0 || strings.Contains(res.Manifest.Errors[len(res.Manifest.Errors)-1].Message, sentinelA) {
		t.Errorf("persisted failure summary leaks the sentinel: %+v", res.Manifest.Errors)
	}
	if len(res.Manifest.Errors) > 0 && !strings.Contains(res.Manifest.Errors[len(res.Manifest.Errors)-1].Message, run.RedactedPlaceholder) {
		t.Errorf("persisted failure summary was not redacted: %+v", res.Manifest.Errors)
	}
	requireNoSentinelsInRunTree(t, store, m.ID)
}

// TestRunErrorQuotingConfiguredValueIsSanitized covers the low-entropy
// edge: a stage error that quotes a configured exact secret value in bare
// prose is invisible to the pattern detectors, so the runner sanitizes the
// failure message with the store's sanitizer before recording it. The run
// fails (as it did), the record is persisted redacted, and neither the
// returned error nor the tree contains the value.
func TestRunErrorQuotingConfiguredValueIsSanitized(t *testing.T) {
	store := newSentinelStore(t)
	m := newTestManifest(t)

	plan := run.CanonicalStages()
	stages := []runner.Stage{
		{Name: plan[0], Fn: func(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
			// Bare low-entropy exact value: only the exact-value path detects
			// it.
			return nil, errors.New("request rejected, session id " + sentinelA)
		}},
		{Name: plan[1], SkipReason: "not reached"}, {Name: plan[2], SkipReason: "not reached"},
		{Name: plan[3], SkipReason: "not reached"}, {Name: plan[4], SkipReason: "not reached"},
		{Name: plan[5], SkipReason: "not reached"}, {Name: plan[6], SkipReason: "not reached"},
		{Name: plan[7], SkipReason: "not reached"}, {Name: plan[8], SkipReason: "not reached"},
	}

	res, err := mustRun(t, context.Background(), m, store, stages, fixedClock())
	if err == nil {
		t.Fatal("Run succeeded, want a recorded failure")
	}
	if strings.Contains(err.Error(), sentinelA) {
		t.Errorf("returned error leaks the sentinel: %v", err)
	}
	if res == nil || res.Status != run.RunStatusFailed {
		t.Fatalf("result status = %v, want failed", res)
	}
	if len(res.Manifest.Errors) == 0 {
		t.Fatal("no failure summary recorded")
	}
	if !strings.Contains(res.Manifest.Errors[len(res.Manifest.Errors)-1].Message, run.RedactedPlaceholder) {
		t.Errorf("failure summary was not redacted: %+v", res.Manifest.Errors)
	}
	requireNoSentinelsInRunTree(t, store, m.ID)
}
