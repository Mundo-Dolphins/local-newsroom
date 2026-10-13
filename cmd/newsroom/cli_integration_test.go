package main

// In-process integration tests for the CLI command handlers (do* functions).
//
// Every test pins the environment (setHermeticEnv) and drives the real cobra
// tree through runCommand, exactly the way main() does (rootCmd.Execute with
// the full command path). This covers the flag/precedence resolution chain,
// the do* handlers, and the internal packages they orchestrate, all inside
// the test binary so the coverage profile counts them.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archive"
	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// =============================================================================
// Shared fixtures (mirrors the shapes the internal packages produce/accept)
// =============================================================================

func cliBaseTime() time.Time {
	return time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
}

// cliDossierJSON builds a valid ResearchDossier file for doVerify.
func cliDossierJSON(t *testing.T) string {
	t.Helper()
	dossier := researcher.ResearchDossier{
		StableID:    "dossier-cli-001",
		Topic:       "CLI Integration Topic",
		GeneratedAt: cliBaseTime(),
		Sources: []researcher.SourceReference{
			{StableID: "source-001", OriginalURL: "https://example.com/a1", SourceType: types.SourceTypeWeb, RetrievedAt: cliBaseTime(), Title: "Source 1"},
			{StableID: "source-002", OriginalURL: "https://example.com/a2", SourceType: types.SourceTypeWeb, RetrievedAt: cliBaseTime(), Title: "Source 2"},
		},
		Claims: []researcher.Claim{
			{
				ID:         "claim-001",
				Statement:  "The council approved the budget on 2024-01-10.",
				Confidence: researcher.ConfidenceHigh,
				Evidence:   []researcher.Evidence{{SourceID: "source-001", Excerpts: []researcher.Excerpt{{Text: "approved the budget", StartOffset: 0, EndOffset: 17}}}},
			},
			{
				ID:         "claim-002",
				Statement:  "Renewable capacity grew 15% by 2025.",
				Confidence: researcher.ConfidenceMedium,
				Evidence:   []researcher.Evidence{{SourceID: "source-002", Excerpts: []researcher.Excerpt{{Text: "grew 15 percent", StartOffset: 0, EndOffset: 14}}}},
			},
		},
		Contradictions: []researcher.Contradiction{},
		ResearchNotes:  []researcher.ResearchNote{},
	}
	b, err := json.Marshal(dossier)
	if err != nil {
		t.Fatalf("marshal dossier: %v", err)
	}
	return string(b)
}

// cliVerificationJSON builds a valid VerificationResult (used both as a file
// for doWrite/doFinalCheck and as the canned LLM response for doVerify).
func cliVerificationJSON(t *testing.T, statuses map[string]contracts.VerificationStatus) string {
	t.Helper()
	details := make(map[string]contracts.ClaimVerificationDetails, len(statuses))
	for id, status := range statuses {
		details[id] = contracts.ClaimVerificationDetails{
			ClaimID:            id,
			Statement:          "Statement for " + id,
			VerificationStatus: status,
			ConfidenceLevel:    contracts.ConfidenceHigh,
		}
	}
	result := contracts.VerificationResult{
		StableID:             "ver-cli-001",
		InputDossierID:       "dossier-cli-001",
		VerifiedAt:           cliBaseTime(),
		VerificationStatuses: statuses,
		ClaimDetails:         details,
		Contradictions:       []contracts.VerifiedContradiction{},
		QualityScore:         85.0,
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal verification: %v", err)
	}
	return string(b)
}

// cliArtifactJSON builds a valid EditorialArtifact (file for doFinalCheck,
// canned LLM response for doWrite).
func cliArtifactJSON(t *testing.T, body string) string {
	t.Helper()
	artifact := contracts.EditorialArtifact{
		StableID:     "artifact-cli-001",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "CLI Integration Article",
		Body:         body,
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-001": {ClaimID: "claim-001", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
			"claim-002": {ClaimID: "claim-002", UsageType: contracts.ClaimUsageSupporting, Paraphrased: false},
		},
		SourceReferences: map[string]contracts.SourceUsage{},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "ver-cli-001",
			GeneratedAt:         cliBaseTime(),
		},
	}
	b, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	return string(b)
}

// cliCheckResultJSON builds a valid FactualCheckResult (canned LLM response
// for doFinalCheck). The final checker recomputes the overall status from
// the parsed findings (high/critical -> fail, medium -> review, none ->
// pass), so the canned findings carry the severity the test wants.
func cliCheckResultJSON(t *testing.T, status contracts.FactualCheckStatus) string {
	t.Helper()
	result := contracts.FactualCheckResult{
		StableID:        "check-cli-001",
		InputArtifactID: "artifact-cli-001",
		CheckedAt:       cliBaseTime(),
		ClaimIDs:        []string{"claim-001", "claim-002"},
		ClaimVerificationStatus: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusSupported,
		},
		OverallStatus:   status,
		ConfidenceLevel: contracts.ConfidenceHigh,
		Summary:         "Check completed",
	}
	switch status {
	case contracts.FactualCheckStatusFail:
		result.UnsupportedStatements = []contracts.UnsupportedStatement{
			{
				Location:      contracts.StatementLocation{LocationType: contracts.LocationTypeBody, Index: 0},
				Statement:     "An unverified fact was introduced",
				StatementType: contracts.UnsupportedStatementNewFacts,
				Severity:      contracts.WarningSeverityHigh,
				ClaimIDs:      []string{"claim-001"},
			},
		}
		result.FactualFindings = []contracts.FactualFinding{
			{
				FindingID:       "finding-cli-001",
				FindingType:     contracts.FindingTypeError,
				Statement:       "The artifact introduces facts not present in the verification result",
				Severity:        contracts.WarningSeverityHigh,
				Category:        contracts.FindingCategoryClaim,
				RelatedClaimIDs: []string{"claim-001"},
			},
		}
	case contracts.FactualCheckStatusReview:
		result.FactualFindings = []contracts.FactualFinding{
			{
				FindingID:       "finding-cli-002",
				FindingType:     contracts.FindingTypeConcern,
				Statement:       "A detail is expressed with less precision than the verified claim",
				Severity:        contracts.WarningSeverityMedium,
				Category:        contracts.FindingCategoryConsistency,
				RelatedClaimIDs: []string{"claim-002"},
			},
		}
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal check result: %v", err)
	}
	return string(b)
}

// cliPlannerJSON is the canned search plan for discovery-mode research.
func cliPlannerJSON(topic string) string {
	plan := struct {
		OriginalTopic string `json:"original_topic"`
		Queries       []struct {
			Query   string `json:"query"`
			Purpose string `json:"purpose"`
		} `json:"queries"`
	}{OriginalTopic: topic}
	plan.Queries = append(plan.Queries,
		struct {
			Query   string `json:"query"`
			Purpose string `json:"purpose"`
		}{"cli discovery fixtures", "Find fixture articles"},
		struct {
			Query   string `json:"query"`
			Purpose string `json:"purpose"`
		}{"cli discovery fixtures second", "Find more fixture articles"},
	)
	b, _ := json.Marshal(plan)
	return string(b)
}

// cliSourceSpec pairs a source ID with its URL for dossier fixtures.
type cliSourceSpec struct {
	id  string
	url string
}

// cliDossierResponse is the canned researcher response. The source URLs must
// match the article server paths the workflow actually fetched.
func cliDossierResponse(topic string, sources ...cliSourceSpec) string {
	refs := make([]researcher.SourceReference, 0, len(sources))
	for _, s := range sources {
		refs = append(refs, researcher.SourceReference{
			StableID:    s.id,
			OriginalURL: s.url,
			SourceType:  types.SourceTypeWeb,
			RetrievedAt: cliBaseTime(),
		})
	}
	dossier := researcher.ResearchDossier{
		StableID:    "dossier-cli-900",
		Topic:       topic,
		GeneratedAt: cliBaseTime(),
		Sources:     refs,
		Claims: []researcher.Claim{{
			ID:               "claim-cli-001",
			Statement:        "The fixture article reports a fact.",
			Confidence:       researcher.ConfidenceHigh,
			IsUnsupported:    true,
			UncertaintyNotes: "fixture",
		}},
		Contradictions: []researcher.Contradiction{},
		ResearchNotes:  []researcher.ResearchNote{},
	}
	b, _ := json.Marshal(dossier)
	return string(b)
}

// sampleCLIHTML is an article large enough for the chunker (300-char target).
const sampleCLIHTML = `<!DOCTYPE html>
<html>
<head><title>CLI Fixture Article</title></head>
<body>
<article>
<h1>Understanding the CLI Fixture</h1>
<p>This fixture article exercises the newsroom CLI end to end. It covers the
research pipeline stages: fetching, extraction, chunking, embedding, and
retrieval. The content is deliberately long enough for the chunker to split
it into multiple chunks, which the archive stores with embeddings.</p>
<p>Key points covered include: configuration resolution through the full
precedence chain, flag handling, and deterministic fake services standing in
for the LLM, embedding, and article endpoints.</p>
</article>
</body>
</html>`

// llmMarkers match the embedded stage prompts (see prompts/*.prompt).
const (
	markerPlanner      = "search query planner"
	markerResearcher   = "research analyst"
	markerVerifier     = "verification analyst"
	markerWriter       = "Writer stage"
	markerFinalChecker = "Final Factual Checker"
)

// =============================================================================
// config
// =============================================================================

func TestDoConfigShowAndValidate(t *testing.T) {
	setHermeticEnv(t)

	// No config file anywhere: both commands must succeed on the
	// environment/defaults path.
	if err := runCommand(t, "config", "show"); err != nil {
		t.Errorf("config show (text): %v", err)
	}
	if err := runCommand(t, "config", "show", "--format", "json"); err != nil {
		t.Errorf("config show (json): %v", err)
	}
	if err := runCommand(t, "config", "show", "--format", "bogus"); err == nil {
		t.Error("config show (bogus format): expected error")
	}
	if err := runCommand(t, "config", "validate"); err != nil {
		t.Errorf("config validate (no file): %v", err)
	}

	// A real (valid) config file via --config: covers the file-loaded path in
	// loadAppConfig and the "file is valid" branch in doConfigValidate.
	cfgPath := filepath.Join(t.TempDir(), "newsroom.yaml")
	mustWriteFile(t, cfgPath, "llm:\n  base_url: http://127.0.0.1:1/v1\n  model: fixture-model\n")
	if err := runCommand(t, "config", "show", "--config", cfgPath); err != nil {
		t.Errorf("config show with file: %v", err)
	}
	if err := runCommand(t, "config", "validate", "--config", cfgPath); err != nil {
		t.Errorf("config validate with file: %v", err)
	}
	if appConfig.FilePath != cfgPath {
		t.Errorf("appConfig.FilePath = %q, want %q", appConfig.FilePath, cfgPath)
	}

	// --config pointing at a missing file must fail in PersistentPreRunE.
	if err := runCommand(t, "config", "show", "--config", filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("config show with missing file: expected error")
	}
}

// =============================================================================
// archive
// =============================================================================

func TestDoArchiveAdd(t *testing.T) {
	setHermeticEnv(t)

	articleServer := newFakeArticleServer(t, map[string]string{
		"article1": sampleCLIHTML,
		"article2": sampleCLIHTML,
	})
	embedServer := newFakeEmbeddingServer(t, 8)
	dbPath := filepath.Join(t.TempDir(), "archive.db")

	// Happy path: two URLs, both succeed.
	if err := runCommand(t,
		"archive", "add",
		"--url", articleServer.URL+"/article1",
		"--url", articleServer.URL+"/article2",
		"--db-path", dbPath,
		"--embedding-base-url", embedServer.URL+"/v1",
		"--embedding-model", "fake-embed",
		"--progress", "false",
		"--parallel-fetch", "1",
	); err != nil {
		t.Fatalf("archive add (happy path): %v", err)
	}

	store, err := archive.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	docs, err := store.ListDocuments(context.Background(), 0)
	_ = store.Close()
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 archived documents, got %d", len(docs))
	}
	for _, d := range docs {
		if d.ChunkCount == 0 {
			t.Errorf("document %s has no chunks", d.StableID)
		}
	}

	// One 404 URL: the command must fail, but the good URL stays archived.
	resetAllCLIFlags(t)
	before := len(docs)
	err = runCommand(t,
		"archive", "add",
		"--url", articleServer.URL+"/article1",
		"--url", articleServer.URL+"/missing",
		"--db-path", dbPath,
		"--embedding-base-url", embedServer.URL+"/v1",
		"--embedding-model", "fake-embed",
		"--progress", "false",
		"--parallel-fetch", "1",
	)
	if err == nil {
		t.Fatal("archive add (404 URL): expected error")
	} else if !strings.Contains(err.Error(), "failed to archive") {
		t.Errorf("unexpected error: %v", err)
	}
	store, err = archive.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	docs, err = store.ListDocuments(context.Background(), 0)
	_ = store.Close()
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if len(docs) != before {
		t.Errorf("expected %d documents after duplicate+failed add, got %d", before, len(docs))
	}

	// URL file with comments and blank lines.
	resetAllCLIFlags(t)
	urlsFile := filepath.Join(t.TempDir(), "urls.txt")
	mustWriteFile(t, urlsFile, "# comment line\n\n"+articleServer.URL+"/article1\n")
	if err := runCommand(t,
		"archive", "add",
		"--urls-file", urlsFile,
		"--db-path", dbPath,
		"--embedding-base-url", embedServer.URL+"/v1",
		"--embedding-model", "fake-embed",
		"--progress", "false",
	); err != nil {
		t.Fatalf("archive add (urls file): %v", err)
	}

	// No URLs at all.
	resetAllCLIFlags(t)
	if err := runCommand(t, "archive", "add", "--db-path", dbPath, "--embedding-base-url", embedServer.URL+"/v1"); err == nil {
		t.Fatal("archive add (no URLs): expected error")
	} else if !strings.Contains(err.Error(), "no URLs provided") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDoArchiveSearch(t *testing.T) {
	setHermeticEnv(t)

	embedServer := newFakeEmbeddingServer(t, 8)
	dbPath := filepath.Join(t.TempDir(), "archive.db")
	seedArchiveStore(t, dbPath, "The Dolphins drafted a new defensive coordinator this spring training camp.")

	if err := runCommand(t,
		"archive", "search",
		"--query", "defensive coordinator",
		"--db-path", dbPath,
		"--embedding-base-url", embedServer.URL+"/v1",
		"--embedding-model", "fake-embed",
		"--top-k", "5",
		"--format", "text",
	); err != nil {
		t.Fatalf("archive search (text): %v", err)
	}

	resetAllCLIFlags(t)
	if err := runCommand(t,
		"archive", "search",
		"--query", "defensive coordinator",
		"--db-path", dbPath,
		"--embedding-base-url", embedServer.URL+"/v1",
		"--embedding-model", "fake-embed",
		"--top-k", "5",
		"--format", "json",
	); err != nil {
		t.Fatalf("archive search (json): %v", err)
	}

	// Empty query is rejected.
	resetAllCLIFlags(t)
	if err := runCommand(t, "archive", "search", "--db-path", dbPath, "--embedding-base-url", embedServer.URL+"/v1"); err == nil {
		t.Fatal("archive search (empty query): expected error")
	} else if !strings.Contains(err.Error(), "no query provided") {
		t.Errorf("unexpected error: %v", err)
	}

	// Empty database: zero hits, "No results found." (no error).
	emptyDB := filepath.Join(t.TempDir(), "empty.db")
	seedArchiveStore(t, emptyDB, "")
	resetAllCLIFlags(t)
	if err := runCommand(t,
		"archive", "search",
		"--query", "anything",
		"--db-path", emptyDB,
		"--embedding-base-url", embedServer.URL+"/v1",
		"--embedding-model", "fake-embed",
		"--format", "text",
	); err != nil {
		t.Fatalf("archive search (empty db): %v", err)
	}
}

func TestDoArchiveStats(t *testing.T) {
	setHermeticEnv(t)

	dbPath := filepath.Join(t.TempDir(), "archive.db")
	seedArchiveStore(t, dbPath, "The Dolphins drafted a new defensive coordinator this spring training camp.")

	for _, format := range []string{"text", "json"} {
		resetAllCLIFlags(t)
		if err := runCommand(t,
			"archive", "stats",
			"--db-path", dbPath,
			"--embedding-base-url", newFakeEmbeddingServer(t, 8).URL+"/v1",
			"--format", format,
		); err != nil {
			t.Fatalf("archive stats (%s): %v", format, err)
		}
	}
}

// =============================================================================
// verify / write / final-check
// =============================================================================

func TestDoVerify(t *testing.T) {
	setHermeticEnv(t)

	tmp := t.TempDir()
	dossierPath := filepath.Join(tmp, "dossier.json")
	mustWriteFile(t, dossierPath, cliDossierJSON(t))
	outPath := filepath.Join(tmp, "verification.json")

	verification := cliVerificationJSON(t, map[string]contracts.VerificationStatus{
		"claim-001": contracts.VerificationStatusSupported,
		"claim-002": contracts.VerificationStatusSupported,
	})
	llmServer := newFakeLLMServer(t, []llmResponseRule{
		{marker: markerVerifier, response: verification},
	}, "")

	if err := runCommand(t,
		"verify",
		"--dossier", dossierPath,
		"--output", outPath,
		"--llm-base-url", llmServer.URL,
		"--llm-model", "fixture-model",
		"--llm-api-key", "fixture-key",
		"--quality-threshold", "60",
		"--verification-date", "2024-01-15T12:00:00Z",
	); err != nil {
		t.Fatalf("verify: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("verification output not written: %v", err)
	}
	var got contracts.VerificationResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("verification output invalid JSON: %v", err)
	}
	if got.StableID == "" || got.QualityScore <= 0 {
		t.Errorf("unexpected verification result: %+v", got)
	}

	// Error paths.
	if err := runCommand(t,
		"verify",
		"--dossier", filepath.Join(tmp, "nope.json"),
		"--llm-base-url", llmServer.URL, "--llm-model", "m",
	); err == nil {
		t.Fatal("verify (missing dossier): expected error")
	}
	mustWriteFile(t, dossierPath, "{not json")
	if err := runCommand(t,
		"verify",
		"--dossier", dossierPath,
		"--llm-base-url", llmServer.URL, "--llm-model", "m",
	); err == nil {
		t.Fatal("verify (bad JSON): expected error")
	}
	mustWriteFile(t, dossierPath, cliDossierJSON(t))
	if err := runCommand(t,
		"verify",
		"--dossier", dossierPath,
		"--llm-base-url", llmServer.URL, "--llm-model", "m",
		"--verification-date", "not-a-date",
	); err == nil {
		t.Fatal("verify (bad date): expected error")
	}
	// Missing LLM configuration (env pinned, no flags): buildLLMClient error.
	if err := runCommand(t, "verify", "--dossier", dossierPath); err == nil {
		t.Fatal("verify (no LLM config): expected error")
	} else if !strings.Contains(err.Error(), "LLM base URL required") {
		t.Errorf("unexpected error: %v", err)
	}
}

// setLLMEnv points the OMLX_* environment variables at a fake LLM endpoint.
// The write and final-check commands have no --llm-* flags; their LLM client
// is resolved through the full precedence chain, where environment variables
// are the natural knob for tests.
func setLLMEnv(t *testing.T, baseURL, model string) {
	t.Helper()
	t.Setenv("OMLX_BASE_URL", baseURL)
	t.Setenv("OMLX_MODEL", model)
	t.Setenv("OMLX_API_KEY", "fixture-key")
}

func TestDoWrite(t *testing.T) {
	setHermeticEnv(t)

	tmp := t.TempDir()
	verificationPath := filepath.Join(tmp, "verification.json")
	mustWriteFile(t, verificationPath, cliVerificationJSON(t, map[string]contracts.VerificationStatus{
		"claim-001": contracts.VerificationStatusSupported,
		"claim-002": contracts.VerificationStatusSupported,
	}))
	outPath := filepath.Join(tmp, "artifact.json")

	artifact := cliArtifactJSON(t, "The council approved the budget, and renewable capacity grew 15% by 2025.")
	llmServer := newFakeLLMServer(t, []llmResponseRule{
		{marker: markerWriter, response: artifact},
	}, "")
	setLLMEnv(t, llmServer.URL, "fixture-model")

	// Happy path (article).
	if err := runCommand(t,
		"write",
		"--dossier", verificationPath,
		"--output", outPath,
		"--format", "article",
	); err != nil {
		t.Fatalf("write (article): %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("artifact not written: %v", err)
	}
	var got contracts.EditorialArtifact
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("artifact invalid JSON: %v", err)
	}
	if got.ArtifactType != contracts.ArtifactTypeArticle {
		t.Errorf("artifact type = %q, want article", got.ArtifactType)
	}

	// Dry run: no output written.
	dryPath := filepath.Join(tmp, "dry.json")
	if err := runCommand(t,
		"write",
		"--dossier", verificationPath,
		"--output", dryPath,
		"--dry-run",
	); err != nil {
		t.Fatalf("write (dry run): %v", err)
	}
	if _, err := os.Stat(dryPath); !os.IsNotExist(err) {
		t.Error("dry run must not write output")
	}

	// Invalid format is rejected in PreRunE.
	if err := runCommand(t,
		"write",
		"--dossier", verificationPath,
		"--format", "bogus",
	); err == nil {
		t.Fatal("write (bogus format): expected error")
	} else if !strings.Contains(err.Error(), "output_format") {
		t.Errorf("unexpected error: %v", err)
	}

	// Error paths: unreadable dossier, prompt file, profile dir.
	if err := runCommand(t,
		"write",
		"--dossier", filepath.Join(tmp, "nope.json"),
	); err == nil {
		t.Fatal("write (missing dossier): expected error")
	}
	if err := runCommand(t,
		"write",
		"--dossier", verificationPath,
		"--prompt-file", filepath.Join(tmp, "missing-prompt.txt"),
	); err == nil {
		t.Fatal("write (missing prompt file): expected error")
	} else if !strings.Contains(err.Error(), "prompt file") {
		t.Errorf("unexpected error: %v", err)
	}
	if err := runCommand(t,
		"write",
		"--dossier", verificationPath,
		"--profile-dir", filepath.Join(tmp, "no-profiles"),
	); err == nil {
		t.Fatal("write (missing profile dir): expected error")
	} else if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDoFinalCheck(t *testing.T) {
	setHermeticEnv(t)

	tmp := t.TempDir()
	artifactPath := filepath.Join(tmp, "artifact.json")
	verificationPath := filepath.Join(tmp, "verification.json")
	mustWriteFile(t, artifactPath, cliArtifactJSON(t, "The council approved the budget, and renewable capacity grew 15% by 2025."))
	mustWriteFile(t, verificationPath, cliVerificationJSON(t, map[string]contracts.VerificationStatus{
		"claim-001": contracts.VerificationStatusSupported,
		"claim-002": contracts.VerificationStatusSupported,
	}))

	mkServer := func(status contracts.FactualCheckStatus) string {
		s := newFakeLLMServer(t, []llmResponseRule{
			{marker: markerFinalChecker, response: cliCheckResultJSON(t, status)},
		}, "")
		setLLMEnv(t, s.URL, "fixture-model")
		return s.URL
	}

	// PASS: exit 0, output written.
	passOut := filepath.Join(tmp, "check-pass.json")
	mkServer(contracts.FactualCheckStatusPass)
	if err := runCommand(t,
		"final-check",
		"--artifact", artifactPath,
		"--verification", verificationPath,
		"--output", passOut,
	); err != nil {
		t.Fatalf("final-check (pass): %v", err)
	}
	if _, err := os.Stat(passOut); err != nil {
		t.Errorf("check output not written on pass: %v", err)
	}

	// FAIL without --allow-failed-output: error, no output, but the
	// debug draft and findings files are written when requested.
	resetAllCLIFlags(t)
	failOut := filepath.Join(tmp, "check-fail.json")
	findingsPath := filepath.Join(tmp, "findings.json")
	mkServer(contracts.FactualCheckStatusFail)
	err := runCommand(t,
		"final-check",
		"--artifact", artifactPath,
		"--verification", verificationPath,
		"--output", failOut,
		"--write-draft",
		"--write-findings", findingsPath,
	)
	if err == nil || !strings.Contains(err.Error(), "factual check failed") {
		t.Fatalf("final-check (fail): expected factual-check-failed error, got %v", err)
	}
	if _, statErr := os.Stat(failOut); !os.IsNotExist(statErr) {
		t.Error("failed check must not write output without --allow-failed-output")
	}
	if _, statErr := os.Stat(failOut + ".draft"); statErr != nil {
		t.Errorf("draft artifact not written: %v", statErr)
	}
	if _, statErr := os.Stat(findingsPath); statErr != nil {
		t.Errorf("findings file not written: %v", statErr)
	}

	// FAIL with --allow-failed-output: output written, still an error.
	resetAllCLIFlags(t)
	failOut2 := filepath.Join(tmp, "check-fail2.json")
	mkServer(contracts.FactualCheckStatusFail)
	if err := runCommand(t,
		"final-check",
		"--artifact", artifactPath,
		"--verification", verificationPath,
		"--output", failOut2,
		"--allow-failed-output",
	); err == nil {
		t.Fatal("final-check (fail, allow output): expected error")
	}
	if _, statErr := os.Stat(failOut2); statErr != nil {
		t.Errorf("allowed failed output not written: %v", statErr)
	}

	// REVIEW: non-zero exit, no output.
	resetAllCLIFlags(t)
	reviewOut := filepath.Join(tmp, "check-review.json")
	mkServer(contracts.FactualCheckStatusReview)
	if err := runCommand(t,
		"final-check",
		"--artifact", artifactPath,
		"--verification", verificationPath,
		"--output", reviewOut,
	); err == nil {
		t.Fatal("final-check (review): expected error")
	} else if !strings.Contains(err.Error(), "requires review") {
		t.Errorf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(reviewOut); !os.IsNotExist(statErr) {
		t.Error("review check must not write output")
	}

	// Missing inputs.
	setLLMEnv(t, "http://127.0.0.1:1", "m")
	if err := runCommand(t,
		"final-check",
		"--verification", verificationPath,
	); err == nil {
		t.Fatal("final-check (missing artifact): expected error")
	}
	if err := runCommand(t,
		"final-check",
		"--artifact", artifactPath,
	); err == nil {
		t.Fatal("final-check (missing verification): expected error")
	}
}

// =============================================================================
// research
// =============================================================================

func TestDoResearchURLMode(t *testing.T) {
	setHermeticEnv(t)

	articleServer := newFakeArticleServer(t, map[string]string{
		"article1": sampleCLIHTML,
		"article2": sampleCLIHTML,
	})
	outPath := filepath.Join(t.TempDir(), "dossier.json")

	topic := "CLI Integration Topic"
	url1 := articleServer.URL + "/article1"
	url2 := articleServer.URL + "/article2"

	llm := newFakeLLMServer(t, []llmResponseRule{
		{marker: markerResearcher, response: cliDossierResponse(topic, cliSourceSpec{"source-cli-1", url1}, cliSourceSpec{"source-cli-2", url2})},
	}, "")

	if err := runCommand(t,
		"research",
		"--topic", topic,
		"--url", url1,
		"--url", url2,
		"--llm-base-url", llm.URL,
		"--llm-model", "fixture-model",
		"--llm-api-key", "fixture-key",
		"--output", outPath,
		"--max-words", "50000",
	); err != nil {
		t.Fatalf("research (URL mode): %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("dossier output not written: %v", err)
	}
	var dossier researcher.ResearchDossier
	if err := json.Unmarshal(data, &dossier); err != nil {
		t.Fatalf("dossier output invalid: %v", err)
	}
	if dossier.Topic != topic {
		t.Errorf("dossier topic = %q, want %q", dossier.Topic, topic)
	}
}

func TestDoResearchDiscoveryMode(t *testing.T) {
	setHermeticEnv(t)

	articleServer := newFakeArticleServer(t, map[string]string{
		"found1": sampleCLIHTML,
		"found2": sampleCLIHTML,
	})
	topic := "CLI Discovery Topic"
	searxngServer := newFakeSearxngServer(t, map[string][]fakeSearxngResult{
		"cli discovery": {
			{url: articleServer.URL + "/found1", title: "Found Article 1", snippet: "snippet one", rank: 1},
			{url: articleServer.URL + "/found2", title: "Found Article 2", snippet: "snippet two", rank: 2},
		},
	})
	llm := newFakeLLMServer(t, []llmResponseRule{
		{marker: markerPlanner, response: cliPlannerJSON(topic)},
		{marker: markerResearcher, response: cliDossierResponse(topic, cliSourceSpec{"source-cli-1", articleServer.URL + "/found1"}, cliSourceSpec{"source-cli-2", articleServer.URL + "/found2"})},
	}, "")

	outPath := filepath.Join(t.TempDir(), "dossier.json")
	if err := runCommand(t,
		"research",
		"--topic", topic,
		"--search-base-url", searxngServer.URL,
		"--llm-base-url", llm.URL,
		"--llm-model", "fixture-model",
		"--output", outPath,
	); err != nil {
		t.Fatalf("research (discovery mode): %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("dossier output not written: %v", err)
	}
}

func TestDoResearchSupplementMode(t *testing.T) {
	setHermeticEnv(t)

	articleServer := newFakeArticleServer(t, map[string]string{
		"explicit1": sampleCLIHTML,
		"found1":    sampleCLIHTML,
	})
	topic := "CLI Supplement Topic"
	searxngServer := newFakeSearxngServer(t, map[string][]fakeSearxngResult{
		"cli supplement fixtures": {
			{url: articleServer.URL + "/found1", title: "Found Article", snippet: "s", rank: 1},
		},
	})
	llm := newFakeLLMServer(t, []llmResponseRule{
		{marker: markerPlanner, response: cliPlannerJSON(topic)},
		{marker: markerResearcher, response: cliDossierResponse(topic, cliSourceSpec{"source-cli-1", articleServer.URL + "/explicit1"}, cliSourceSpec{"source-cli-2", articleServer.URL + "/found1"})},
	}, "")

	outPath := filepath.Join(t.TempDir(), "dossier.json")
	if err := runCommand(t,
		"research",
		"--topic", topic,
		"--url", articleServer.URL+"/explicit1",
		"--search-base-url", searxngServer.URL,
		"--llm-base-url", llm.URL,
		"--llm-model", "fixture-model",
		"--output", outPath,
	); err != nil {
		t.Fatalf("research (supplement mode): %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("dossier output not written: %v", err)
	}
}

func TestDoResearchErrorPaths(t *testing.T) {
	setHermeticEnv(t)

	// Topic but no source mode.
	err := runCommand(t,
		"research",
		"--topic", "T",
		"--llm-base-url", "http://127.0.0.1:1", "--llm-model", "m",
	)
	if err == nil || !strings.Contains(err.Error(), "source mode") {
		t.Fatalf("research (no source mode): expected source-mode error, got %v", err)
	}

	// URL provided but no LLM endpoint configured (env pinned): the
	// researcher call must fail.
	articleServer := newFakeArticleServer(t, map[string]string{"a1": sampleCLIHTML})
	if err := runCommand(t,
		"research",
		"--topic", "T",
		"--url", articleServer.URL+"/a1",
		"--output", filepath.Join(t.TempDir(), "dossier.json"),
	); err == nil {
		t.Fatal("research (no LLM config): expected error")
	}

	// Unreadable prompt file.
	if err := runCommand(t,
		"research",
		"--topic", "T",
		"--url", articleServer.URL+"/a1",
		"--llm-base-url", "http://127.0.0.1:1", "--llm-model", "m",
		"--prompt-file", filepath.Join(t.TempDir(), "missing-prompt.txt"),
		"--output", filepath.Join(t.TempDir(), "dossier.json"),
	); err == nil {
		t.Fatal("research (missing prompt file): expected error")
	} else if !strings.Contains(err.Error(), "prompt file") {
		t.Errorf("unexpected error: %v", err)
	}
}
