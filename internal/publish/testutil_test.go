package publish_test

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// baseTime is the fixed base time for deterministic run IDs and timestamps.
var baseTime = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

// testSecret is a high-entropy, mixed-class value the redaction detectors
// catch — the fixture behind every "no secret reaches disk" assertion.
const testSecret = "aB3dE7fH1jK4mN6pQ9rS2tU5vW8xY0zA1bC3dE6f"

// fixedClock returns an advancing clock: every call is one second after the
// last, so no persisted document can record a backwards history.
func fixedClock() func() time.Time {
	t := baseTime
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

// articleContent is the rendered article the demo run produces.
const articleContent = "# EU AI Act takes effect\n\nThe Act enters into force.\n"

// threadPosts are the canonical thread post texts (NUL-joined by
// publish.ThreadContent when a thread plan is built).
var threadPosts = []string{
	"EU AI Act enters into force. What it means for builders: https://example.com/ai-act",
	"High-risk systems get conformity assessments; general-purpose models get transparency duties.",
	"More: https://example.com/ai-act/guide",
}

// harness is one fully succeeded demo run on a real workspace store: a
// persisted manifest, the output files, and a READY review bundle. Every
// engine test starts from the same shape the real pipeline produces.
type harness struct {
	store    *workspace.Store
	runID    run.RunID
	manifest *run.Manifest
	bundle   *review.Bundle
	eng      *publish.Engine

	// clock is the single continuing clock shared by the runner, the engine,
	// and every reloaded engine (a restart keeps time moving forward): it
	// keeps the store's manifest-freshness rule (no older manifest may
	// overwrite a newer one) from tripping on fixtures.
	clock func() time.Time
}

// newHarness builds the demo run: all canonical stages succeeded (offline
// executors), final factual check passed, rendered output present.
func newHarness(t *testing.T) *harness {
	t.Helper()

	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "testws"},
		Topic:      "EU AI Act enters into force",
		SourceMode: run.SourceModeExplicit,
		StartedAt:  baseTime,
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}

	stages := []runner.Stage{
		{Name: run.CanonicalStages()[0], Fn: writeStage(runner.StageFile{Path: "sources/index.json", Kind: run.ArtifactKindSource, ID: "sources", Data: []byte(`{"urls":["https://example.com/a"]}`)})},
		{Name: run.CanonicalStages()[1], Fn: writeStage(runner.StageFile{Path: "fetch/index.json", Kind: run.ArtifactKindSource, ID: "fetch", Data: []byte(`{"fetched":1}`)})},
		{Name: run.CanonicalStages()[2], Fn: writeStage(runner.StageFile{Path: "documents/index.json", Kind: run.ArtifactKindDocument, ID: "documents", Data: []byte(`{"documents":1}`)})},
		{Name: run.CanonicalStages()[3], Fn: writeStage(runner.StageFile{Path: "research/dossier.json", Kind: run.ArtifactKindDossier, ID: "dossier", Data: []byte(`{"claims":[]}`)})},
		{Name: run.CanonicalStages()[4], Fn: writeStage(runner.StageFile{Path: "verify/verification.json", Kind: run.ArtifactKindVerification, ID: "verification", Data: []byte(`{"verdict":"verified"}`)})},
		{Name: run.CanonicalStages()[5], SkipReason: "no unresolved claims"},
		{Name: run.CanonicalStages()[6], Fn: writeStage(runner.StageFile{Path: "write/artifact.json", Kind: run.ArtifactKindEditorial, ID: "editorial", Data: []byte(`{"headline":"EU AI Act enters into force"}`)})},
		{Name: run.CanonicalStages()[7], Fn: writeStage(runner.StageFile{Path: "final_check/result.json", Kind: run.ArtifactKindFactualCheck, ID: "final-check", Data: []byte(`{"overall_status":"pass"}`)})},
		{Name: run.CanonicalStages()[8], Fn: writeStage(
			runner.StageFile{Path: "output/article.md", Kind: run.ArtifactKindRendered, ID: "article", Data: []byte(articleContent)},
			runner.StageFile{Path: "output/thread.txt", Kind: run.ArtifactKindRendered, ID: "thread", Data: []byte("thread")},
		)},
	}

	store := newStore(t)
	clock := fixedClock()
	res, err := runner.Run(context.Background(), runner.Config{
		Manifest: m,
		Store:    store,
		Stages:   stages,
		Clock:    clock,
	})
	if err != nil {
		t.Fatalf("runner.Run: %v", err)
	}
	if res.Status != run.RunStatusSucceeded {
		t.Fatalf("run status = %s, want succeeded (err=%v)", res.Status, err)
	}

	loaded, _, err := store.LoadManifest(m.ID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.Status != run.RunStatusSucceeded {
		t.Fatalf("on-disk status = %s, want succeeded", loaded.Status)
	}

	// The render stage must have persisted the final-check pass: readiness
	// requires it.
	data, err := store.LoadArtifact(m.ID, "final_check/result.json")
	if err != nil {
		t.Fatalf("final_check/result.json must be persisted: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("final_check/result.json is empty")
	}

	bundle, err := review.Build(store, loaded)
	if err != nil {
		t.Fatalf("review.Build: %v", err)
	}
	ready, _ := review.Readiness(bundle)
	if !ready {
		t.Fatalf("demo run must be READY for the publication tests (bundle status %s, final check %s, missing %v)",
			bundle.Status, bundle.FinalCheckStatus, bundle.OutputMissing)
	}

	eng := publish.NewWithClock(store, clock)

	return &harness{
		store:    store,
		runID:    m.ID,
		manifest: loaded,
		bundle:   bundle,
		eng:      eng,
		clock:    clock,
	}
}

// writeStage is an offline stage executor that persists the given files.
func writeStage(files ...runner.StageFile) runner.StageFunc {
	return func(_ context.Context, _ *runner.StageInput) (*runner.StageOutput, error) {
		return &runner.StageOutput{Files: files}, nil
	}
}

// persistCrashedPlan simulates the on-disk state after a process that died
// mid-cycle: it writes the (manipulated) plan document through the store
// boundary and then persists a manifest summary whose plan_hash matches the
// bytes that actually landed on disk — the fully-consistent state the
// engine's persistPlan would leave whenever its two writes both completed.
// That is the resume point a restarted process reads.
func (h *harness) persistCrashedPlan(t *testing.T, plan *publication.Plan) {
	t.Helper()
	data, err := publication.MarshalPlan(plan)
	if err != nil {
		t.Fatalf("MarshalPlan of crashed state: %v", err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("crashed-state plan is not a valid document: %v", err)
	}
	ref := run.ArtifactRef{
		Kind:       run.ArtifactKindCustom,
		ID:         "publication-plan-crashstate",
		Path:       publish.PlanPath(plan.ID),
		ProducedAt: plan.UpdatedAt,
	}
	if _, err := h.store.SaveArtifact(h.runID, ref, data); err != nil {
		t.Fatalf("persisting crashed plan document: %v", err)
	}
	// The manifest summary must hash exactly what the store wrote (the
	// store may sanitize on the way down).
	persisted, err := h.store.LoadArtifact(h.runID, publish.PlanPath(plan.ID))
	if err != nil {
		t.Fatalf("reading back crashed plan document: %v", err)
	}
	succeeded, failed := 0, 0
	for _, tt := range plan.Targets {
		switch tt.Status {
		case publication.StatusSucceeded:
			succeeded++
		case publication.StatusFailed:
			failed++
		}
	}
	m, _, err := h.store.LoadManifest(h.runID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	m.PublicationState = &run.PublicationState{
		PlanID:    string(plan.ID),
		PlanPath:  publish.PlanPath(plan.ID),
		PlanHash:  string(publication.DigestOf(persisted)),
		Status:    string(plan.Status),
		Targets:   len(plan.Targets),
		Succeeded: succeeded,
		Failed:    failed,
		UpdatedAt: plan.UpdatedAt,
	}
	m.UpdatedAt = plan.UpdatedAt
	if _, err := h.store.SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest of crashed state: %v", err)
	}
}

// newStore builds a workspace store in a temp directory.
func newStore(t *testing.T) *workspace.Store {
	t.Helper()
	store, err := workspace.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// mustPublish is Publish with test-fatal setup errors.
func (h *harness) mustPublish(t *testing.T, publishers map[publication.TargetID]publish.Publisher, opts publish.Options) *publication.Plan {
	t.Helper()
	plan, err := h.eng.Publish(context.Background(), h.runID, publishers, opts)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return plan
}

// target returns one target of a plan (test-fatal when absent).
func target(t *testing.T, plan *publication.Plan, id publication.TargetID) *publication.PublicationTarget {
	t.Helper()
	tt, ok := plan.Target(id)
	if !ok {
		t.Fatalf("plan %s has no target %q", plan.ID, id)
	}
	return tt
}

// errCode is the target's error code, or <nil> when the target carries no
// error summary (keeps test failure messages nil-safe).
func errCode(e *run.ErrorSummary) string {
	if e == nil {
		return "<nil>"
	}
	return e.Code
}

// testPlanID builds a well-formed publication plan ID for fixtures.
func testPlanID() publication.PublicationID {
	return publication.NewPublicationID(
		run.NewRunID("testws", "EU AI Act enters into force", baseTime),
		publication.NewArtifactIdentity("article", run.ArtifactKindRendered, "output/article.md", []byte(articleContent), "render/v1"),
		[]publication.TargetID{"site"},
		baseTime,
	)
}
