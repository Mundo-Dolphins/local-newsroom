package review

import (
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// kitchenSinkBundle fills every branch of the renderer so the sweep covers
// all sections, including the rarely-hit warning/error/category paths.
func kitchenSinkBundle() *Bundle {
	start := time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)
	b := &Bundle{
		RunID:         "run-20260711T100000Z-abcdef01",
		WorkspaceName: "unit",
		WorkspacePath: "/tmp/ws",
		Status:        run.RunStatusFailed,
		Topic:         "a topic about things",
		SourceMode:    run.SourceModeDiscovered,
		StartedAt:     run.NewTimestamp(start),
		UpdatedAt:     run.NewTimestamp(start.Add(42 * time.Second)),
		FailedStage:   "final_check",
		Reason:        "the gate failed",
		ProfileID:     "dolphin",
		ProfileHash:   strings.Repeat("cd", 32),
		ProfilePath:   "/profiles/dolphin",
		ModelProvider: "provider-x",
		ModelName:     "model-y",
		ModelEndpoint: "http://127.0.0.1:9/v1",
		Config:        map[string]string{"run.format": "thread", "run.quality_threshold": "60", "run.language": "en"},
		RunDir:        "/tmp/ws/runs/run-20260711T100000Z-abcdef01",

		SourcesSelected: []string{"https://alpha.example.com/post", "https://user:pass123456@example.com/x"},
		Queries:         []string{"q one", "q two"},
		Candidates:      9,
		FetchResults: []SourceFetch{
			{URL: "https://alpha.example.com/post", Title: "Alpha", Status: 200, OK: true, RetrievedAt: start},
			{URL: "https://beta.example.com/x", Status: 0, OK: false, Error: "connection refused", RetrievedAt: start},
		},
		FetchSuccesses: 1,
		FetchFailed:    1,
		DocumentsCount: 3,

		DossierPresent: true,
		DossierClaims:  5,
		DossierSources: []researcher.SourceReference{
			{StableID: "src-1", OriginalURL: "https://alpha.example.com/post", Title: "Alpha"},
			{StableID: "src-2", OriginalURL: "https://beta.example.com/x"},
		},

		Verified:            true,
		ClaimsTotal:         5,
		ClaimsSupported:     2,
		ClaimsContradicted:  1,
		ClaimsUncertain:     1,
		ClaimsInsufficient:  1,
		QualityScore:        40,
		QualityScoreSet:     true,
		ContradictionCount:  1,
		VerificationSummary: "mixed results; one conflict documented",
		Contradicted:        []ClaimInfo{{ID: "c-2", Statement: "the doubled figure", Status: contracts.VerificationStatusContradicted, Confidence: contracts.ConfidenceLow, Limitations: "conflict", HasConflicts: true}},
		Unresolved:          []ClaimInfo{{ID: "c-3", Statement: "thin evidence", Status: contracts.VerificationStatusUncertain}, {ID: "c-4", Statement: "one source only", Status: contracts.VerificationStatusInsufficientEvidence, Limitations: "only one source"}},

		FollowUpPresent:    true,
		FollowUpRounds:     2,
		FollowUpQueries:    4,
		FollowUpSources:    3,
		FollowUpResolved:   []string{"c-4"},
		FollowUpUnresolved: []string{"c-3"},
		FollowUpWarnings:   []string{"search degraded"},
		FollowUpErrors:     []string{"one 404"},

		FinalCheckPresent:    true,
		FinalCheckStatus:     contracts.FactualCheckStatusFail,
		FinalCheckConfidence: contracts.ConfidenceMedium,
		FinalCheckSummary:    "two statements fail",
		Findings: []contracts.FactualFinding{
			{FindingID: "f-1", FindingType: contracts.FindingTypeError, Category: contracts.FindingCategoryAccuracy, Statement: "figure doubled", Severity: contracts.WarningSeverityHigh, RelatedClaimIDs: []string{"c-2"}},
		},
		Unsupported: []contracts.UnsupportedStatement{
			{Statement: "an unsupported claim", StatementType: contracts.UnsupportedStatementNewFacts, Severity: contracts.WarningSeverityHigh},
		},
		Altered: []contracts.AlteredDetail{
			{DetailType: contracts.AlteredDetailTypeNumber, OriginalValue: "10", AlteredImageValue: "20", Significance: contracts.AlterationSignificanceCritical},
		},
		OutOfContext: []contracts.OutOfContextStatement{
			{Statement: "quote moved", OriginalContext: "interview", Misrepresentation: "reads as fact", Severity: contracts.WarningSeverityMedium},
		},
		Recommendations: []contracts.Recommendation{
			{RecommendationID: "r-1", RecommendationType: contracts.RecommendationTypeFix, Description: "restore the source figure", Priority: contracts.RecommendationPriorityHigh},
		},

		ArtifactPresent:  true,
		ArtifactTitle:    "The headline",
		ArtifactType:     contracts.ArtifactTypeThread,
		ArtifactClaims:   5,
		ArtifactWarnings: []contracts.Warning{{WarningType: contracts.WarningTypeLowConfidence, Severity: contracts.WarningSeverityMedium, Message: "low-confidence claim included"}},

		OutputPaths:   []string{"/tmp/ws/runs/x/write/artifact.thread.txt"},
		OutputMissing: []string{"/tmp/ws/runs/x/write/artifact.thread.txt"},

		Stages: []StageInfo{
			{Name: run.StageDiscovery, Status: run.StageStatusSucceeded, Attempts: 1},
			{Name: run.StageFetch, Status: run.StageStatusSucceeded, Attempts: 2},
			{Name: run.StageExtract, Status: run.StageStatusSkipped, SkipReason: "no documents", Attempts: 0},
			{Name: run.StageResearch, Status: run.StageStatusSucceeded, Attempts: 1},
			{Name: run.StageVerify, Status: run.StageStatusSucceeded, Attempts: 3},
			{Name: run.StageFollowUp, Status: run.StageStatusSucceeded, Attempts: 1},
			{Name: run.StageWrite, Status: run.StageStatusSucceeded, Attempts: 1},
			{Name: run.StageFinalCheck, Status: run.StageStatusFailed, Attempts: 1, Error: "the gate failed"},
			{Name: run.StageRender, Status: run.StageStatusSkipped, SkipReason: "final check failed", Attempts: 0},
		},

		Warnings: []run.Warning{run.NewWarning("verification.unresolved", "two claims unresolved", start)},
		Errors:   []run.ErrorSummary{run.NewErrorSummary("final_check.gate", "the gate failed", start)},
		BuildNotes: []string{
			"stage verify is recorded as succeeded but its artifact verify/verification.json is missing from the run directory",
		},
	}
	return b
}

// TestRenderKitchenSink exercises every section of the renderer and checks
// that the expected content (and no unsafe content) appears.
func TestRenderKitchenSink(t *testing.T) {
	doc := kitchenSinkBundle().Render()

	mustContain := []string{
		"# Run Review: a topic about things",
		"**Status: FAILED** (stopped at stage final_check)",
		"| **Run ID** | `run-20260711T100000Z-abcdef01` |",
		"| **Profile** | dolphin (hash",
		"| **Model** | provider-x / model-y @ `http://127.0.0.1:9/v1` |",
		"| **Output format** | thread |",
		"| **Quality threshold** | 60 |",
		"| **Run directory** |",
		"## Unresolved issues",
		"Final factual check: **FAIL**",
		"1 claim(s) contradicted by evidence",
		"2 claim(s) not fully supported",
		"the run failed at stage final_check",
		"verification quality score 40.0 is below the 60.0 gate",
		"### Contradicted claims (1)",
		"`c-2` [contradicted]",
		"conflicting evidence recorded",
		"### Unresolved claims (2)",
		"### Run warnings (1)",
		"`verification.unresolved`",
		"### Bundle integrity notes (1)",
		"verify/verification.json is missing",
		"## Pipeline",
		"| `extract` | skipped | 0 | no documents |",
		"| `final_check` | failed | 1 | the gate failed |",
		"## Sources",
		"Mode: discovered — 2 source(s) selected (from 9 candidate(s))",
		"`q one`",
		"Fetched: 1 of 2 succeeded, 1 failed",
		"[alpha.example.com/post](https://alpha.example.com/post)",
		"ok (HTTP 200)",
		"connection refused",
		"Documents extracted: 3",
		"## Research dossier",
		"Claims produced: 5",
		"Sources used (2):",
		"[alpha.example.com/post](https://alpha.example.com/post)",
		"[beta.example.com/x](https://beta.example.com/x)",
		"## Claims & verification",
		"| supported | 2 |",
		"| contradicted | 1 |",
		"| uncertain | 1 |",
		"| insufficient evidence | 1 |",
		"| **total** | **5** |",
		"Quality score: 40.0 / 100",
		"Documented contradictions: 1",
		"Verifier summary: mixed results; one conflict documented",
		"## Follow-up research",
		"Rounds: 2 · queries: 4 · sources fetched: 3",
		"Resolved: `c-4`",
		"Still unresolved: `c-3`",
		"## Final factual check",
		"Overall: **FAIL**",
		"**the artifact failed the final gate and must not be used**",
		"confidence: medium",
		"Findings (1):",
		"- `f-1` [high / error / accuracy] figure doubled (claims `c-2`)",
		"Unsupported statements (1):",
		"an unsupported claim",
		"Altered details (1):",
		"10 → 20",
		"Out-of-context statements (1):",
		"quote moved",
		"Recommendations (1):",
		"[high / fix] restore the source figure",
		"## Editorial artifact",
		"The headline (thread)",
		"Claim references: 5",
		"[medium] low-confidence claim included",
		"## Output artifacts",
		"**NOT READY**",
		"`/tmp/ws/runs/x/write/artifact.thread.txt` — **missing**",
		"## Evidence files",
		"| run manifest | `manifest.json` | no |",
		"## Run errors",
		"`final_check.gate` — the gate failed",
		"*Generated deterministically by local-newsroom v0.5 (no LLM calls). Regenerate with `newsroom review run-20260711T100000Z-abcdef01`.*",
	}
	for _, want := range mustContain {
		if !strings.Contains(doc, want) {
			t.Errorf("rendered document lacks %q\n---\n%s", want, doc)
		}
	}
	// Secret backstop: the redacted URL must not leak its userinfo.
	if strings.Contains(doc, "pass123456") {
		t.Fatal("rendered document leaks URL userinfo")
	}
	// The severity for the issue must not claim readiness anywhere.
	if strings.Contains(doc, "**READY**") {
		t.Fatal("a failed run must not render READY")
	}
}

// TestRenderSkippedFollowUpRendersSkipReason covers the follow-up section's
// skipped branch (stage planned but not run).
func TestRenderSkippedFollowUpRendersSkipReason(t *testing.T) {
	b := &Bundle{
		Status:                run.RunStatusFailed,
		RunID:                 "run-20260711T100000Z-abcdef01",
		Topic:                 "t",
		FollowUpSkippedReason: "verification had no unresolved claims",
	}
	doc := b.Render()
	if !strings.Contains(doc, "## Follow-up research") || !strings.Contains(doc, "Skipped: verification had no unresolved claims") {
		t.Fatalf("skipped follow-up must render its reason:\n%s", doc)
	}
}

// TestRenderMinimalBundle has nothing but identity: it must still render the
// header, the issue verdict, and the footer without panicking or inventing
// sections.
func TestRenderMinimalBundle(t *testing.T) {
	b := &Bundle{
		Status: run.RunStatusFailed,
		RunID:  "run-20260711T100000Z-abcdef01",
		Topic:  "t",
		Reason: "kaboom",
	}
	doc := b.Render()
	for _, want := range []string{
		"# Run Review: t",
		"**NOT READY**",
		"kaboom",
		"## Unresolved issues",
		"No rendered output was produced for this run.",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("minimal render lacks %q:\n%s", want, doc)
		}
	}
	for _, absent := range []string{
		"## Sources",
		"## Research dossier",
		"## Claims & verification",
		"## Final factual check",
		"## Editorial artifact",
		"## Evidence files",
		"## Pipeline",
	} {
		if strings.Contains(doc, absent) {
			t.Errorf("minimal render must not include %q", absent)
		}
	}
}

// TestRenderRunningRunPresentsAsPartial covers the non-terminal header.
func TestRenderRunningRunPresentsAsPartial(t *testing.T) {
	b := &Bundle{
		Status: run.RunStatusRunning,
		RunID:  "run-20260711T100000Z-abcdef01",
		Topic:  "t",
		Stages: []StageInfo{{Name: run.StageResearch, Status: run.StageStatusRunning, Attempts: 1}},
	}
	doc := b.Render()
	if !strings.Contains(doc, "RUNNING") || !strings.Contains(doc, "partial") {
		t.Fatalf("running run must render as partial:\n%s", doc)
	}
}

// TestRenderSucceededRunHeaderReady checks the happy-path header wording.
func TestRenderSucceededRunHeaderReady(t *testing.T) {
	b := &Bundle{
		Status:            run.RunStatusSucceeded,
		RunID:             "run-20260711T100000Z-abcdef01",
		Topic:             "t",
		FinalCheckPresent: true,
		FinalCheckStatus:  contracts.FactualCheckStatusPass,
		OutputPaths:       []string{"/x/output/article.md"},
	}
	doc := b.Render()
	if !strings.Contains(doc, "ready for local use") {
		t.Fatalf("succeeded+pass header must say ready:\n%s", doc)
	}
	if !strings.Contains(doc, "**READY**") {
		t.Fatalf("succeeded+pass output must be READY:\n%s", doc)
	}
}

// TestRenderRecipeLine covers the v0.6 recipe row: rendered for recipe
// runs, absent (empty value skips the row) for standard runs.
func TestRenderRecipeLine(t *testing.T) {
	b := &Bundle{
		Status:   run.RunStatusSucceeded,
		RunID:    "run-20260711T100000Z-abcdef01",
		Topic:    "t",
		RecipeID: "afc-east-weekly",
	}
	doc := b.Render()
	if !strings.Contains(doc, "| **Recipe** | `afc-east-weekly` |") {
		t.Errorf("recipe row missing:\n%s", doc)
	}

	b.RecipeHash = "deadbeef"
	doc = b.Render()
	if !strings.Contains(doc, "(hash `deadbeef`)") && !strings.Contains(doc, "(hash deadbeef)") {
		t.Errorf("recipe hash missing:\n%s", doc)
	}

	// Standard run: no recipe row at all.
	standard := &Bundle{Status: run.RunStatusSucceeded, RunID: "run-20260711T100000Z-abcdef02", Topic: "t"}
	if strings.Contains(standard.Render(), "**Recipe**") {
		t.Error("standard run must not render a recipe row")
	}
}
