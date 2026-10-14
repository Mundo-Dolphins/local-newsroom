package review

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/discovery"
	"github.com/Mundo-Dolphins/local-newsroom/internal/followup"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// testTime is the fixed base time for deterministic fixtures.
var testTime = time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)

func tAt(i int) time.Time { return testTime.Add(time.Duration(i) * time.Second) }

// fixture is one persisted run on a real workspace store.
type fixture struct {
	store  *workspace.Store
	m      *run.Manifest
	runDir string
}

// newFixture creates a run in a fresh temporary workspace and returns it with
// a helper for writing stage files through the real store.
func newFixture(t *testing.T, topic string) *fixture {
	t.Helper()
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "unit", Path: root},
		Topic:      topic,
		SourceMode: run.SourceModeExplicit,
		Profile: &run.Profile{
			ID:   "unit-profile",
			Hash: strings.Repeat("ab", 32),
		},
		Model:     run.NewModelMetadata("test-provider", "test-model", "http://127.0.0.1:1/v1"),
		StartedAt: testTime,
	})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if err := m.Config.Set("run.format", "brief"); err != nil {
		t.Fatalf("config: %v", err)
	}
	if err := m.Config.Set("run.quality_threshold", "60"); err != nil {
		t.Fatalf("config: %v", err)
	}
	path, err := store.CreateRun(m)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	// CreateRun returns the run directory itself.
	dir := path
	if err := m.Start(tAt(1)); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := store.SaveManifest(m); err != nil {
		t.Fatalf("save: %v", err)
	}
	return &fixture{store: store, m: m, runDir: dir}
}

// writeStage runs one stage to success, persisting its file(s) through the
// store and referencing them on the manifest — the same sequence a real run
// performs.
func (f *fixture) writeStage(t *testing.T, name run.StageName, step int, kind run.ArtifactKind, id, rel string, data []byte) {
	t.Helper()
	if err := f.m.StartStage(name, tAt(step)); err != nil {
		t.Fatalf("start stage %s: %v", name, err)
	}
	ref := run.ArtifactRef{
		Kind:       kind,
		ID:         id,
		Stage:      name,
		Path:       rel,
		ProducedAt: run.NewTimestamp(tAt(step)),
	}
	if err := f.m.AddArtifact(ref); err != nil {
		t.Fatalf("add artifact %s: %v", rel, err)
	}
	if _, err := f.store.SaveArtifact(f.m.ID, ref, data); err != nil {
		t.Fatalf("save artifact %s: %v", rel, err)
	}
	if err := f.m.SucceedStage(name, tAt(step+1)); err != nil {
		t.Fatalf("succeed stage %s: %v", name, err)
	}
	if _, err := f.store.SaveManifest(f.m); err != nil {
		t.Fatalf("save manifest after %s: %v", name, err)
	}
}

// writeStageFailed fails one stage after persisting its result file (the
// final-check stage behaves this way: the result is written even when the
// gate fails, and only the manifest reference is withheld).
func (f *fixture) writeStageFailed(t *testing.T, name run.StageName, step int, kind run.ArtifactKind, id, rel string, data []byte, code, msg string) {
	t.Helper()
	if err := f.m.StartStage(name, tAt(step)); err != nil {
		t.Fatalf("start stage %s: %v", name, err)
	}
	ref := run.ArtifactRef{
		Kind:       kind,
		ID:         id,
		Stage:      name,
		Path:       rel,
		ProducedAt: run.NewTimestamp(tAt(step)),
	}
	if _, err := f.store.SaveArtifact(f.m.ID, ref, data); err != nil {
		t.Fatalf("save artifact %s: %v", rel, err)
	}
	if err := f.m.FailStage(name, tAt(step+1), code, msg); err != nil {
		t.Fatalf("fail stage %s: %v", name, err)
	}
}

// failStage fails one stage (no artifact).
func (f *fixture) failStage(t *testing.T, name run.StageName, step int, code, msg string) {
	t.Helper()
	if err := f.m.StartStage(name, tAt(step)); err != nil {
		t.Fatalf("start stage %s: %v", name, err)
	}
	if err := f.m.FailStage(name, tAt(step+1), code, msg); err != nil {
		t.Fatalf("fail stage %s: %v", name, err)
	}
}

// skipStage records one stage as skipped.
func (f *fixture) skipStage(t *testing.T, name run.StageName, step int, reason string) {
	t.Helper()
	if err := f.m.SkipStage(name, tAt(step), reason); err != nil {
		t.Fatalf("skip stage %s: %v", name, err)
	}
}

// failRun terminates the run as failed after a failed stage.
func (f *fixture) failRun(t *testing.T, step int, code, msg string) {
	t.Helper()
	if err := f.m.Fail(tAt(step), code, msg); err != nil {
		t.Fatalf("fail run: %v", err)
	}
	if _, err := f.store.SaveManifest(f.m); err != nil {
		t.Fatalf("save final manifest: %v", err)
	}
}

// succeedRun terminates the run as succeeded (all stages done).
func (f *fixture) succeedRun(t *testing.T, step int) {
	t.Helper()
	if err := f.m.Succeed(tAt(step)); err != nil {
		t.Fatalf("succeed run: %v", err)
	}
	if _, err := f.store.SaveManifest(f.m); err != nil {
		t.Fatalf("save final manifest: %v", err)
	}
}

// cancelRun terminates the run as cancelled.
func (f *fixture) cancelRun(t *testing.T, step int, reason string) {
	t.Helper()
	if err := f.m.Cancel(tAt(step), reason); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	if _, err := f.store.SaveManifest(f.m); err != nil {
		t.Fatalf("save final manifest: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Artifact content builders (marshalled from the same types the review
// unmarshals, so the fixtures are shape-compatible with real runs).
// ---------------------------------------------------------------------------

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func sourcesFixture(t *testing.T, selected, queries []string, candidates int) []byte {
	si := sourcesIndex{
		Mode:     run.SourceModeExplicit,
		Topic:    "unit topic",
		Selected: selected,
	}
	for _, q := range queries {
		si.Queries = append(si.Queries, planner.SearchQuery{Query: q})
	}
	for i := 0; i < candidates; i++ {
		si.Candidates = append(si.Candidates, discovery.Candidate{
			CandidateURL:    "https://candidate.example.com/" + strconv.Itoa(i),
			CandidateDomain: "candidate.example.com",
			BestRank:        i + 1,
		})
	}
	return mustJSON(t, si)
}

func fetchFixture(t *testing.T, urls []string, allOK bool) []byte {
	fi := fetchIndex{}
	for i, u := range urls {
		ok := allOK || i < len(urls)-1
		f := SourceFetch{URL: u, Title: "title-" + strconv.Itoa(i), OK: ok, Status: 200, RetrievedAt: testTime}
		if !ok {
			f.Status = 0
			f.Error = "get: connection refused"
		}
		fi.Attempted = append(fi.Attempted, f)
		if ok {
			fi.Successes++
		} else {
			fi.Failed++
		}
	}
	return mustJSON(t, fi)
}

func documentsFixture(t *testing.T, n int) []byte {
	di := documentsIndex{Count: n}
	for i := 0; i < n; i++ {
		di.SourceIDs = append(di.SourceIDs, "src-"+strconv.Itoa(i))
		di.Paths = append(di.Paths, "documents/doc-"+strconv.Itoa(i)+".txt")
	}
	return mustJSON(t, di)
}

func dossierFixture(t *testing.T, claims ...researcher.Claim) []byte {
	d := researcher.ResearchDossier{
		StableID:    "dossier-1",
		Topic:       "unit topic",
		GeneratedAt: testTime,
		Sources: []researcher.SourceReference{
			{StableID: "src-0", OriginalURL: "https://alpha.example.com/post", Title: "Alpha post"},
			{StableID: "src-1", OriginalURL: "https://beta.example.com/story", Title: "Beta story"},
		},
		Claims: claims,
	}
	return mustJSON(t, d)
}

func claim(id, statement string) researcher.Claim {
	return researcher.Claim{
		ID:            id,
		Statement:     statement,
		Confidence:    researcher.ConfidenceHigh,
		IsUnsupported: false,
		Evidence: []researcher.Evidence{
			{SourceID: "src-0", Excerpts: []researcher.Excerpt{{Text: "supporting excerpt"}}},
		},
	}
}

func verificationFixture(t *testing.T, statuses map[string]contracts.VerificationStatus, quality float64) []byte {
	vres := contracts.VerificationResult{
		StableID:             "verification-1",
		InputDossierID:       "dossier-1",
		VerifiedAt:           testTime,
		VerificationStatuses: statuses,
		ClaimDetails:         map[string]contracts.ClaimVerificationDetails{},
		VerificationSummary:  "claims assessed against recorded evidence",
		QualityScore:         quality,
	}
	for id, status := range statuses {
		details := contracts.ClaimVerificationDetails{
			ClaimID:            id,
			Statement:          "statement for " + id,
			VerificationStatus: status,
			ConfidenceLevel:    contracts.ConfidenceHigh,
		}
		if status == contracts.VerificationStatusContradicted {
			details.ConflictingEvidence = []contracts.EvidenceReference{{SourceID: "src-1"}}
			details.Limitations = "source conflicts"
		}
		if status == contracts.VerificationStatusInsufficientEvidence {
			details.Limitations = "only one source"
		}
		vres.ClaimDetails[id] = details
	}
	return mustJSON(t, vres)
}

func followUpFixture(t *testing.T, resolved, unresolved []string) []byte {
	return mustJSON(t, followup.FollowUpResult{
		RoundNumber:           2,
		QueryCount:            3,
		FetchedCount:          2,
		ResolvedClaims:        resolved,
		UnresolvedClaimsAfter: unresolved,
		Warnings:              []string{"one search degraded"},
		Errors:                []string{"one source 404"},
	})
}

func artifactFixture(t *testing.T, warnings ...contracts.Warning) []byte {
	art := contracts.EditorialArtifact{
		StableID:     "artifact-1",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Unit headline",
		Body:         "Unit body text.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-001": {},
			"claim-002": {},
		},
		Warnings: warnings,
	}
	return mustJSON(t, art)
}

func finalCheckFixture(t *testing.T, status contracts.FactualCheckStatus, findings ...contracts.FactualFinding) []byte {
	cr := contracts.FactualCheckResult{
		StableID:        "check-1",
		InputArtifactID: "artifact-1",
		CheckedAt:       testTime,
		OverallStatus:   status,
		ConfidenceLevel: contracts.ConfidenceHigh,
		Summary:         "checked every statement against the dossier",
		FactualFindings: findings,
	}
	return mustJSON(t, cr)
}

// ---------------------------------------------------------------------------
// Full-pipeline fixtures
// ---------------------------------------------------------------------------

// runSucceeded drives a complete, clean run: every stage succeeds, all
// claims supported, final check passes, output rendered.
func runSucceeded(t *testing.T) (*fixture, *Bundle, *run.Manifest) {
	t.Helper()
	f := newFixture(t, "unit topic")
	urls := []string{"https://alpha.example.com/post", "https://beta.example.com/story"}
	f.writeStage(t, run.StageDiscovery, 5, run.ArtifactKindSource, "sources-index", run.ArtifactPathSourcesIndex,
		sourcesFixture(t, urls, []string{"unit topic"}, 4))
	f.writeStage(t, run.StageFetch, 10, run.ArtifactKindDocument, "fetch-index", run.ArtifactPathFetchIndex,
		fetchFixture(t, urls, true))
	f.writeStage(t, run.StageExtract, 15, run.ArtifactKindDocument, "documents-index", run.ArtifactPathDocumentsIndex,
		documentsFixture(t, 2))
	claims := []researcher.Claim{
		claim("claim-001", "first supported statement"),
		claim("claim-002", "second supported statement"),
		claim("claim-003", "third supported statement"),
		claim("claim-004", "fourth supported statement"),
	}
	f.writeStage(t, run.StageResearch, 20, run.ArtifactKindDossier, "dossier", run.ArtifactPathDossier,
		dossierFixture(t, claims...))
	f.writeStage(t, run.StageVerify, 25, run.ArtifactKindVerification, "verification", run.ArtifactPathVerification,
		verificationFixture(t, map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusSupported,
			"claim-003": contracts.VerificationStatusSupported,
			"claim-004": contracts.VerificationStatusSupported,
		}, 90))
	f.writeStage(t, run.StageFollowUp, 30, run.ArtifactKindCustom, "followup-result", run.ArtifactPathFollowUp,
		followUpFixture(t, []string{}, nil))
	f.writeStage(t, run.StageWrite, 35, run.ArtifactKindEditorial, "artifact", run.ArtifactPathArtifact,
		artifactFixture(t))
	f.writeStage(t, run.StageFinalCheck, 40, run.ArtifactKindFactualCheck, "final-check", run.ArtifactPathFinalCheck,
		finalCheckFixture(t, contracts.FactualCheckStatusPass))
	// Rendered output.
	if err := f.m.StartStage(run.StageRender, tAt(45)); err != nil {
		t.Fatalf("start render: %v", err)
	}
	brief := "Brief body for the unit topic."
	ref := run.ArtifactRef{
		Kind:       run.ArtifactKindRendered,
		ID:         "rendered-brief",
		Stage:      run.StageRender,
		Path:       "write/artifact.brief.md",
		ProducedAt: run.NewTimestamp(tAt(46)),
	}
	if err := f.m.AddArtifact(ref); err != nil {
		t.Fatalf("add rendered: %v", err)
	}
	if _, err := f.store.SaveArtifact(f.m.ID, ref, []byte(brief)); err != nil {
		t.Fatalf("save rendered: %v", err)
	}
	if err := f.m.SucceedStage(run.StageRender, tAt(47)); err != nil {
		t.Fatalf("succeed render: %v", err)
	}
	f.succeedRun(t, 48)

	b, err := Build(f.store, f.m)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return f, b, f.m
}

// runFailedFinalCheck is a run that completed every stage through the final
// check, where the check FAILED: the run is failed, output not ready.
func runFailedFinalCheck(t *testing.T) (*fixture, *Bundle) {
	t.Helper()
	f := newFixture(t, "unit topic")
	urls := []string{"https://alpha.example.com/post"}
	f.writeStage(t, run.StageDiscovery, 5, run.ArtifactKindSource, "sources-index", run.ArtifactPathSourcesIndex,
		sourcesFixture(t, urls, nil, 1))
	f.writeStage(t, run.StageFetch, 10, run.ArtifactKindDocument, "fetch-index", run.ArtifactPathFetchIndex,
		fetchFixture(t, urls, true))
	f.writeStage(t, run.StageExtract, 15, run.ArtifactKindDocument, "documents-index", run.ArtifactPathDocumentsIndex,
		documentsFixture(t, 1))
	f.writeStage(t, run.StageResearch, 20, run.ArtifactKindDossier, "dossier", run.ArtifactPathDossier,
		dossierFixture(t,
			claim("claim-001", "first supported statement"),
			claim("claim-002", "statement the evidence contradicts"),
			claim("claim-003", "statement with thin evidence"),
		))
	f.writeStage(t, run.StageVerify, 25, run.ArtifactKindVerification, "verification", run.ArtifactPathVerification,
		verificationFixture(t, map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusContradicted,
			"claim-003": contracts.VerificationStatusInsufficientEvidence,
		}, 40))
	f.writeStage(t, run.StageFollowUp, 30, run.ArtifactKindCustom, "followup-result", run.ArtifactPathFollowUp,
		followUpFixture(t, []string{"claim-003"}, []string{"claim-002"}))
	f.writeStage(t, run.StageWrite, 35, run.ArtifactKindEditorial, "artifact", run.ArtifactPathArtifact,
		artifactFixture(t, contracts.Warning{Severity: contracts.WarningSeverityHigh, Message: "article makes a claim the verification contradicts"}))
	// The final check persists its result even when the gate fails.
	f.writeStageFailed(t, run.StageFinalCheck, 40, run.ArtifactKindFactualCheck, "final-check", run.ArtifactPathFinalCheck,
		finalCheckFixture(t, contracts.FactualCheckStatusFail,
			contracts.FactualFinding{
				FindingID:       "finding-1",
				FindingType:     contracts.FindingTypeError,
				Category:        contracts.FindingCategoryAccuracy,
				Statement:       "the headline doubles the figure reported by the source",
				Severity:        contracts.WarningSeverityHigh,
				RelatedClaimIDs: []string{"claim-001"},
			},
			contracts.FactualFinding{
				FindingID:   "finding-2",
				FindingType: contracts.FindingTypeConcern,
				Category:    contracts.FindingCategoryClaim,
				Statement:   "an extra claim has no evidence in the dossier",
				Severity:    contracts.WarningSeverityCritical,
			}),
		"final_check.gate", "final check failed: 2 findings")
	f.skipStage(t, run.StageRender, 45, "final check failed")
	f.failRun(t, 46, "run.failed", "final check failed: 2 findings")

	b, err := Build(f.store, f.m)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return f, b
}

// runFailedResearch stops the run at the research stage (no dossier onward).
func runFailedResearch(t *testing.T) (*fixture, *Bundle) {
	t.Helper()
	f := newFixture(t, "unit topic")
	urls := []string{"https://alpha.example.com/post", "https://beta.example.com/story"}
	f.writeStage(t, run.StageDiscovery, 5, run.ArtifactKindSource, "sources-index", run.ArtifactPathSourcesIndex,
		sourcesFixture(t, urls, nil, 2))
	f.writeStage(t, run.StageFetch, 10, run.ArtifactKindDocument, "fetch-index", run.ArtifactPathFetchIndex,
		fetchFixture(t, urls, false)) // last URL failed
	f.writeStage(t, run.StageExtract, 15, run.ArtifactKindDocument, "documents-index", run.ArtifactPathDocumentsIndex,
		documentsFixture(t, 1))
	f.failStage(t, run.StageResearch, 20, "research.llm", "llm request failed")
	for _, s := range []run.StageName{run.StageVerify, run.StageFollowUp, run.StageWrite, run.StageFinalCheck, run.StageRender} {
		f.skipStage(t, s, 25, "upstream stage failed")
	}
	f.failRun(t, 30, "run.failed", "research failed: llm request failed")

	b, err := Build(f.store, f.m)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return f, b
}
