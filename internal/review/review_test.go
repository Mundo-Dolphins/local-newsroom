package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// ---------------------------------------------------------------------------
// Build: scenarios
// ---------------------------------------------------------------------------

func TestBuildSucceededRun(t *testing.T) {
	_, b, m := runSucceeded(t)

	if b.Status != run.RunStatusSucceeded {
		t.Fatalf("status = %s, want succeeded", b.Status)
	}
	if len(b.BuildNotes) != 0 {
		t.Fatalf("build notes = %v, want none", b.BuildNotes)
	}
	if got := len(b.SourcesSelected); got != 2 {
		t.Fatalf("sources = %d, want 2", got)
	}
	if b.FetchSuccesses != 2 || b.FetchFailed != 0 {
		t.Fatalf("fetch = %d/%d, want 2/0", b.FetchSuccesses, b.FetchFailed)
	}
	if b.DocumentsCount != 2 {
		t.Fatalf("documents = %d, want 2", b.DocumentsCount)
	}
	if b.DossierClaims != 4 {
		t.Fatalf("dossier claims = %d, want 4", b.DossierClaims)
	}
	if b.ClaimsTotal != 4 || b.ClaimsSupported != 4 {
		t.Fatalf("claims = %d total / %d supported, want 4/4", b.ClaimsTotal, b.ClaimsSupported)
	}
	if b.ClaimsContradicted != 0 || b.ClaimsUncertain != 0 || b.ClaimsInsufficient != 0 {
		t.Fatalf("unwanted claim counts: %+v", b)
	}
	if !b.QualityScoreSet || b.QualityScore != 90 {
		t.Fatalf("quality = %v/%v, want 90", b.QualityScore, b.QualityScoreSet)
	}
	if b.FinalCheckStatus != contracts.FactualCheckStatusPass {
		t.Fatalf("final check = %s, want pass", b.FinalCheckStatus)
	}
	if !b.FinalCheckPresent {
		t.Fatal("final check present = false")
	}
	if !b.ArtifactPresent || b.ArtifactTitle != "Unit headline" {
		t.Fatalf("artifact = present=%v title=%q", b.ArtifactPresent, b.ArtifactTitle)
	}
	if len(b.OutputPaths) != 1 || len(b.OutputMissing) != 0 {
		t.Fatalf("output = %v / missing %v", b.OutputPaths, b.OutputMissing)
	}
	ready, why := Readiness(b)
	if !ready {
		t.Fatalf("readiness = false: %s", why)
	}
	// The bundle's identity must mirror the manifest.
	if b.RunID != m.ID || b.Topic != m.Topic {
		t.Fatalf("identity = %s/%s, want %s/%s", b.RunID, b.Topic, m.ID, m.Topic)
	}
}

func TestBuildFailedFinalCheckRun(t *testing.T) {
	_, b := runFailedFinalCheck(t)

	if b.Status != run.RunStatusFailed {
		t.Fatalf("status = %s, want failed", b.Status)
	}
	if b.FailedStage != string(run.StageFinalCheck) {
		t.Fatalf("failed stage = %q, want final_check", b.FailedStage)
	}
	if b.FinalCheckStatus != contracts.FactualCheckStatusFail {
		t.Fatalf("final check = %s, want fail", b.FinalCheckStatus)
	}
	if b.ClaimsTotal != 3 || b.ClaimsSupported != 1 || b.ClaimsContradicted != 1 || b.ClaimsInsufficient != 1 {
		t.Fatalf("claim counts = %d/%d/%d/%d, want 3/1/1/1", b.ClaimsTotal, b.ClaimsSupported, b.ClaimsContradicted, b.ClaimsInsufficient)
	}
	if len(b.Contradicted) != 1 || b.Contradicted[0].ID != "claim-002" {
		t.Fatalf("contradicted = %+v", b.Contradicted)
	}
	if len(b.Unresolved) != 1 || b.Unresolved[0].ID != "claim-003" {
		t.Fatalf("unresolved = %+v", b.Unresolved)
	}
	if b.FollowUpPresent != true || b.FollowUpRounds != 2 || len(b.FollowUpUnresolved) != 1 {
		t.Fatalf("follow-up = %+v", b)
	}
	ready, why := Readiness(b)
	if ready {
		t.Fatalf("readiness = true for a failed run: %s", why)
	}

	doc := b.Render()
	if !strings.Contains(doc, "**NOT READY**") {
		t.Fatal("rendered doc lacks NOT READY")
	}
	if strings.Contains(doc, "**READY**") {
		t.Fatal("rendered doc presents a failed run as READY")
	}
	if !strings.Contains(doc, "final_check") {
		t.Fatal("rendered doc does not name the failed stage")
	}
	if !strings.Contains(doc, "claim-002") || !strings.Contains(doc, "claim-003") {
		t.Fatal("rendered doc does not surface the problem claims")
	}
}

func TestBuildFailedResearchRun(t *testing.T) {
	_, b := runFailedResearch(t)

	if b.Status != run.RunStatusFailed || b.FailedStage != string(run.StageResearch) {
		t.Fatalf("status/stage = %s/%s, want failed/research", b.Status, b.FailedStage)
	}
	if b.DossierPresent || b.Verified || b.FinalCheckPresent || b.ArtifactPresent {
		t.Fatalf("downstream sections present for a run that stopped at research: %+v", b)
	}
	if len(b.BuildNotes) != 0 {
		t.Fatalf("build notes = %v, want none (missing files are expected after a failed stage)", b.BuildNotes)
	}
	// One of the two fetched sources failed: the bundle must carry it.
	if b.FetchFailed != 1 {
		t.Fatalf("fetch failed = %d, want 1", b.FetchFailed)
	}
	ready, _ := Readiness(b)
	if ready {
		t.Fatal("readiness = true for a failed run")
	}
	doc := b.Render()
	for _, want := range []string{"**NOT READY**", "research", "connection refused"} {
		if !strings.Contains(doc, want) {
			t.Errorf("rendered doc lacks %q", want)
		}
	}
	if strings.Contains(doc, "## Final factual check") {
		t.Error("rendered doc includes a final-check section that was never reached")
	}
}

func TestBuildCancelledRun(t *testing.T) {
	f := newFixture(t, "unit topic")
	f.writeStage(t, run.StageDiscovery, 5, run.ArtifactKindSource, "sources-index", run.ArtifactPathSourcesIndex,
		sourcesFixture(t, []string{"https://alpha.example.com/post"}, nil, 1))
	f.failStage(t, run.StageFetch, 10, "fetch.error", "fetch aborted")
	// A cancelled stage must be one that was running when the stop arrived.
	if err := f.m.StartStage(run.StageExtract, tAt(12)); err != nil {
		t.Fatalf("start extract: %v", err)
	}
	if err := f.m.CancelStage(run.StageExtract, tAt(13)); err != nil {
		t.Fatalf("cancel stage: %v", err)
	}
	for _, s := range []run.StageName{run.StageResearch, run.StageVerify, run.StageFollowUp, run.StageWrite, run.StageFinalCheck, run.StageRender} {
		f.skipStage(t, s, 20, "run cancelled")
	}
	f.cancelRun(t, 25, "user requested stop")

	b, err := Build(f.store, f.m)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if b.Status != run.RunStatusCancelled {
		t.Fatalf("status = %s, want cancelled", b.Status)
	}
	ready, why := Readiness(b)
	if ready {
		t.Fatalf("readiness = true for a cancelled run: %s", why)
	}
	doc := b.Render()
	if !strings.Contains(doc, "CANCELLED") || !strings.Contains(doc, "**NOT READY**") {
		t.Fatal("rendered doc must present a cancelled run as NOT READY")
	}
}

// ---------------------------------------------------------------------------
// Build: integrity and robustness
// ---------------------------------------------------------------------------

func TestBuildMissingSucceededArtifactIsANote(t *testing.T) {
	f, _, _ := runSucceeded(t)
	// Simulate a corrupted workspace: the succeeded verify stage's file is gone.
	if err := os.Remove(filepath.Join(f.runDir, "verify", "verification.json")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	m, _, err := f.store.LoadManifest(f.m.ID)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	b, err := Build(f.store, m)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if b.Verified {
		t.Fatal("verified = true although the file is missing")
	}
	found := false
	for _, n := range b.BuildNotes {
		if strings.Contains(n, "verification.json") {
			found = true
		}
	}
	if !found {
		t.Fatalf("build notes = %v, want a missing-artifact note", b.BuildNotes)
	}
	if !strings.Contains(b.Render(), "integrity") {
		t.Fatal("rendered doc must surface the integrity note prominently")
	}
}

func TestBuildSucceededRunWithoutCheckFile(t *testing.T) {
	// A succeeded run whose final-check file was deleted must never render
	// as READY: the gate evidence is gone.
	f := newFixture(t, "unit topic")
	urls := []string{"https://alpha.example.com/post"}
	f.writeStage(t, run.StageDiscovery, 5, run.ArtifactKindSource, "sources-index", run.ArtifactPathSourcesIndex,
		sourcesFixture(t, urls, nil, 1))
	f.writeStage(t, run.StageFetch, 10, run.ArtifactKindDocument, "fetch-index", run.ArtifactPathFetchIndex,
		fetchFixture(t, urls, true))
	f.writeStage(t, run.StageExtract, 15, run.ArtifactKindDocument, "documents-index", run.ArtifactPathDocumentsIndex,
		documentsFixture(t, 1))
	f.writeStage(t, run.StageResearch, 20, run.ArtifactKindDossier, "dossier", run.ArtifactPathDossier,
		dossierFixture(t, claim("claim-001", "first supported statement")))
	f.writeStage(t, run.StageVerify, 25, run.ArtifactKindVerification, "verification", run.ArtifactPathVerification,
		verificationFixture(t, map[string]contracts.VerificationStatus{"claim-001": contracts.VerificationStatusSupported}, 85))
	f.skipStage(t, run.StageFollowUp, 30, "no unresolved claims")
	f.writeStage(t, run.StageWrite, 35, run.ArtifactKindEditorial, "artifact", run.ArtifactPathArtifact,
		artifactFixture(t))
	f.writeStage(t, run.StageFinalCheck, 40, run.ArtifactKindFactualCheck, "final-check", run.ArtifactPathFinalCheck,
		finalCheckFixture(t, contracts.FactualCheckStatusPass))
	// Corrupt the workspace: delete the check file after the run ended.
	if err := os.Remove(filepath.Join(f.runDir, "final_check", "result.json")); err != nil {
		t.Fatalf("remove check file: %v", err)
	}
	// Re-load the manifest from disk (the on-disk record the review reads).
	m, _, err := f.store.LoadManifest(f.m.ID)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	b, err := Build(f.store, m)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if b.FinalCheckPresent {
		t.Fatal("final check present = true for a missing file")
	}
	if len(b.BuildNotes) == 0 {
		t.Fatal("missing succeeded-stage artifact must produce a build note")
	}
	ready, why := Readiness(b)
	if ready {
		t.Fatalf("readiness = true without gate evidence: %s", why)
	}
	doc := b.Render()
	if !strings.Contains(doc, "**NOT READY**") || strings.Contains(doc, "**READY**") {
		t.Fatal("a succeeded run without its check file must render NOT READY")
	}
}

func TestBuildCorruptArtifactIsANotableBuildNote(t *testing.T) {
	f := newFixture(t, "unit topic")
	f.writeStage(t, run.StageDiscovery, 5, run.ArtifactKindSource, "sources-index", run.ArtifactPathSourcesIndex,
		sourcesFixture(t, []string{"https://alpha.example.com/post"}, nil, 1))
	f.failStage(t, run.StageFetch, 10, "fetch.error", "fetch aborted")
	for _, s := range []run.StageName{run.StageExtract, run.StageResearch, run.StageVerify, run.StageFollowUp, run.StageWrite, run.StageFinalCheck, run.StageRender} {
		f.skipStage(t, s, 15, "upstream stage failed")
	}
	f.failRun(t, 20, "run.failed", "fetch failed")
	// Overwrite the sources index with garbage JSON.
	p := filepath.Join(f.runDir, "sources", "index.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	m, _, err := f.store.LoadManifest(f.m.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	b, err := Build(f.store, m)
	if err != nil {
		t.Fatalf("build must not fail on a corrupt stage file: %v", err)
	}
	found := false
	for _, n := range b.BuildNotes {
		if strings.Contains(n, "sources/index.json") && strings.Contains(n, "parsed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("build notes = %v, want a parse-failure note for sources/index.json", b.BuildNotes)
	}
	if len(b.SourcesSelected) != 0 {
		t.Fatalf("sources selected = %v, want empty for a corrupt file", b.SourcesSelected)
	}
}

func TestBuildRejectsNilInputs(t *testing.T) {
	if _, err := Build(nil, &run.Manifest{}); err == nil {
		t.Fatal("nil store must be rejected")
	}
	if _, err := Build(newFixture(t, "t").store, nil); err == nil {
		t.Fatal("nil manifest must be rejected")
	}
}

// ---------------------------------------------------------------------------
// Save
// ---------------------------------------------------------------------------

func TestSaveWritesReviewMDThroughStore(t *testing.T) {
	f, b, _ := runSucceeded(t)
	path, err := Save(f.store, b)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	want := filepath.Join(f.runDir, "review.md")
	if path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	wantDoc := b.Render()
	if string(data) != wantDoc {
		t.Fatal("saved document differs from rendered document (sanitization changed clean content?)")
	}
	// The manifest on disk must not reference the bundle.
	m, _, err := f.store.LoadManifest(f.m.ID)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	for _, a := range m.Artifacts {
		if a.Path == "review.md" {
			t.Fatal("review.md must not be referenced on the manifest")
		}
	}
	// Re-saving is idempotent (deterministic content, same file).
	if _, err := Save(f.store, b); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	data2, _ := os.ReadFile(want)
	if string(data2) != wantDoc {
		t.Fatal("re-saved document changed (must be deterministic)")
	}
}

func TestSaveFailedRunWithoutManifestReference(t *testing.T) {
	f, b := runFailedFinalCheck(t)
	path, err := Save(f.store, b)
	if err != nil {
		t.Fatalf("saving a failed run's review must work: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("review.md not on disk: %v", err)
	}
}

func TestSaveRedactsExactSecrets(t *testing.T) {
	// A store configured with an exact secret value must redact it from the
	// bundle file even when the pattern detectors would not catch it.
	secret := "hunter2hunter2hunter2"
	root := t.TempDir()
	store, err := workspace.NewStoreWithSanitizer(root, run.NewSanitizer(secret))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "unit", Path: root},
		Topic:      "secret topic",
		SourceMode: run.SourceModeExplicit,
		StartedAt:  testTime,
	})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if _, err := store.CreateRun(m); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := m.Start(tAt(1)); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := store.SaveManifest(m); err != nil {
		t.Fatalf("save: %v", err)
	}
	// A dossier whose claim statement embeds the secret.
	claims := []researcher.Claim{claim("claim-001", "the key was "+secret+" according to the source")}
	data := mustJSON(t, dossierFixture(t, claims...))
	ref := run.ArtifactRef{
		Kind:       run.ArtifactKindDossier,
		ID:         "dossier",
		Stage:      run.StageResearch,
		Path:       run.ArtifactPathDossier,
		ProducedAt: run.NewTimestamp(tAt(5)),
	}
	if err := m.StartStage(run.StageResearch, tAt(2)); err != nil {
		t.Fatalf("start stage: %v", err)
	}
	// The store sanitizes on the way in, so the persisted dossier is clean.
	if _, err := store.SaveArtifact(m.ID, ref, data); err != nil {
		t.Fatalf("save dossier: %v", err)
	}
	if err := m.SucceedStage(run.StageResearch, tAt(3)); err != nil {
		t.Fatalf("succeed stage: %v", err)
	}
	// Fail the rest so the run is terminal.
	if err := m.StartStage(run.StageVerify, tAt(4)); err != nil {
		t.Fatalf("start verify: %v", err)
	}
	if err := m.FailStage(run.StageVerify, tAt(6), "verify.error", "boom"); err != nil {
		t.Fatalf("fail stage: %v", err)
	}
	if err := m.Fail(tAt(7), "run.failed", "verify failed"); err != nil {
		t.Fatalf("fail run: %v", err)
	}
	if _, err := store.SaveManifest(m); err != nil {
		t.Fatalf("final save: %v", err)
	}

	b, err := Build(store, m)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	path, err := Save(store, b)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	data2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data2), secret) {
		t.Fatal("review.md contains the configured exact secret")
	}
}

// ---------------------------------------------------------------------------
// Render: safety rules
// ---------------------------------------------------------------------------

func TestReadinessMatrix(t *testing.T) {
	cases := []struct {
		name          string
		status        run.RunStatus
		checkPresent  bool
		checkStatus   contracts.FactualCheckStatus
		outputMissing bool
		wantReady     bool
	}{
		{"succeeded pass", run.RunStatusSucceeded, true, contracts.FactualCheckStatusPass, false, true},
		{"succeeded pass output missing", run.RunStatusSucceeded, true, contracts.FactualCheckStatusPass, true, false},
		{"succeeded fail", run.RunStatusSucceeded, true, contracts.FactualCheckStatusFail, false, false},
		{"succeeded review", run.RunStatusSucceeded, true, contracts.FactualCheckStatusReview, false, false},
		{"succeeded no check file", run.RunStatusSucceeded, false, "", false, false},
		{"failed", run.RunStatusFailed, true, contracts.FactualCheckStatusPass, false, false},
		{"cancelled", run.RunStatusCancelled, true, contracts.FactualCheckStatusPass, false, false},
		{"pending", run.RunStatusPending, false, "", false, false},
		{"running", run.RunStatusRunning, false, "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &Bundle{
				Status:            tc.status,
				RunID:             "run-20260711T100000Z-abcdef01",
				Topic:             "topic",
				FinalCheckPresent: tc.checkPresent,
				FinalCheckStatus:  tc.checkStatus,
			}
			if tc.outputMissing {
				b.OutputMissing = []string{"/x/write/artifact.brief.md"}
			}
			ready, why := Readiness(b)
			if ready != tc.wantReady {
				t.Fatalf("readiness = %v (%s), want %v", ready, why, tc.wantReady)
			}
			if !tc.wantReady && why == "" {
				t.Fatal("a not-ready result must carry a reason")
			}
		})
	}
}

func TestRenderNeverReadyWhenCheckFailed(t *testing.T) {
	b := &Bundle{
		Status:            run.RunStatusSucceeded,
		RunID:             "run-20260711T100000Z-abcdef01",
		Topic:             "topic",
		FinalCheckPresent: true,
		FinalCheckStatus:  contracts.FactualCheckStatusFail,
		Findings:          []contracts.FactualFinding{{Statement: "the number changed"}},
		OutputPaths:       []string{"/x/write/artifact.brief.md"},
	}
	doc := b.Render()
	if strings.Contains(doc, "**READY**") {
		t.Fatal("a run with a failed final check must not render READY")
	}
	if !strings.Contains(doc, "**NOT READY**") {
		t.Fatal("expected a NOT READY statement")
	}
}

// ---------------------------------------------------------------------------
// Render: escaping, redaction, determinism
// ---------------------------------------------------------------------------

func TestRenderEscapesAndTruncatesFields(t *testing.T) {
	long := strings.Repeat("x", 500)
	b := &Bundle{
		Status:      run.RunStatusFailed,
		RunID:       "run-20260711T100000Z-abcdef01",
		Topic:       "a | b\nc | d",
		FailedStage: "final_check",
		Reason:      "it broke",
		Unresolved: []ClaimInfo{{
			ID:        "claim-001",
			Statement: "first | second\nthird\n\n" + long,
		}},
	}
	doc := b.Render()
	// Pipe escaping and newline flattening in list contexts.
	if strings.Contains(doc, "a | b\nc") {
		t.Fatal("raw pipes/newlines must not reach the rendered document")
	}
	if !strings.Contains(doc, "a \\| b c \\| d") {
		t.Fatalf("topic not escaped/flattened as expected: %q", firstLines(doc, 6))
	}
	// Deterministic truncation.
	if !strings.Contains(doc, "…") {
		t.Fatal("long fields must be truncated with an ellipsis")
	}
	for _, line := range strings.Split(doc, "\n") {
		if len(line) > maxFieldLen+80 {
			t.Fatalf("line exceeds the field bound: %d chars", len(line))
		}
	}
}

func TestRenderRedactsSecretsInContent(t *testing.T) {
	b := &Bundle{
		Status: run.RunStatusFailed,
		RunID:  "run-20260711T100000Z-abcdef01",
		Topic:  "topic",
		Reason: "api_key=sk-abcdef123456789abcdef",
		Findings: []contracts.FactualFinding{
			{Statement: "authorization header was Authorization: Bearer abcdef1234567890abcdef"},
		},
		SourcesSelected: []string{"https://user:pass123456@example.com/page"},
	}
	doc := b.Render()
	for _, secret := range []string{"sk-abcdef123456789abcdef", "abcdef1234567890abcdef", "pass123456"} {
		if strings.Contains(doc, secret) {
			t.Fatalf("rendered document contains secret %q", secret)
		}
	}
	if !strings.Contains(doc, "[REDACTED]") {
		t.Fatal("expected redaction placeholders in the rendered document")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	f, b, _ := runSucceeded(t)
	d1 := b.Render()
	d2 := b.Render()
	if d1 != d2 {
		t.Fatal("Render is not deterministic for the same bundle")
	}
	// Rebuilding from the on-disk record must reproduce the same document.
	m, _, err := f.store.LoadManifest(f.m.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	b2, err := Build(f.store, m)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if b2.Render() != d1 {
		t.Fatal("rebuilding the bundle changed the document")
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) < n {
		return s
	}
	return strings.Join(lines[:n], "\n")
}
