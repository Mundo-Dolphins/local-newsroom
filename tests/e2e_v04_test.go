// Package tests provides end-to-end tests for the v0.4 editorial pipeline.
//
// This package tests the complete offline pipeline from ResearchDossier through
// VerificationResult, EditorialArtifact, and final render without requiring
// external LLM services, oMLX, SearXNG, or web access.
package tests

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/chunker"
	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/embedding"
	"github.com/Mundo-Dolphins/local-newsroom/internal/finalchecker"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/Mundo-Dolphins/local-newsroom/internal/profiles"
	"github.com/Mundo-Dolphins/local-newsroom/internal/renderers"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
	"github.com/Mundo-Dolphins/local-newsroom/internal/verifier"
	"github.com/Mundo-Dolphins/local-newsroom/internal/writer"
)

// =============================================================================
// Fake LLM Client for v0.4 Pipeline
// =============================================================================

// v04fakeLLMClient is a deterministic fake LLM client for the v0.4 pipeline.
// It returns responses from a FIFO queue, making each test fully deterministic.
type v04fakeLLMClient struct {
	responses []llm.Response
	err       error
	calls     int
}

// Complete implements llm.Client.
func (f *v04fakeLLMClient) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if f.err != nil {
		return llm.Response{}, f.err
	}
	if f.calls >= len(f.responses) {
		return llm.Response{}, llm.Internal("no more responses in queue")
	}
	resp := f.responses[f.calls]
	f.calls++
	return resp, nil
}

// NewV04FakeLLM creates a fake LLM client with the given JSON responses in order.
func NewV04FakeLLM(jsonResponses ...string) *v04fakeLLMClient {
	resps := make([]llm.Response, len(jsonResponses))
	for i, s := range jsonResponses {
		resps[i] = llm.Response{Content: s}
	}
	return &v04fakeLLMClient{responses: resps}
}

// =============================================================================
// Test Fixtures
// =============================================================================

// v04baseTime returns a fixed timestamp for deterministic tests.
func v04baseTime() time.Time {
	return time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
}

// v04createDossier creates a valid ResearchDossier with the given claims.
func v04createDossier(claims ...researcher.Claim) *researcher.ResearchDossier {
	return &researcher.ResearchDossier{
		StableID:    "dossier-v04-001",
		Topic:       "Test Research Topic",
		GeneratedAt: v04baseTime(),
		Sources: []researcher.SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com/article1",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: v04baseTime(),
				Title:       "Source Article 1",
			},
			{
				StableID:    "source-002",
				OriginalURL: "https://example.com/article2",
				SourceType:  types.SourceTypeWeb,
				RetrievedAt: v04baseTime(),
				Title:       "Source Article 2",
			},
		},
		Claims:         claims,
		Contradictions: []researcher.Contradiction{},
		ResearchNotes:  []researcher.ResearchNote{},
	}
}

// v04claim creates a researcher.Claim with the given parameters.
func v04claim(id, statement string, sourceID string, confidence researcher.ConfidenceLevel) researcher.Claim {
	return researcher.Claim{
		ID:         id,
		Statement:  statement,
		Confidence: confidence,
		Evidence: []researcher.Evidence{
			{
				SourceID: sourceID,
				Excerpts: []researcher.Excerpt{
					{Text: statement, StartOffset: 0, EndOffset: len(statement)},
				},
			},
		},
		IsUnsupported: false,
	}
}

// v04verifierInput builds a valid verifier.Input with required parameters.
func v04verifierInput(dossier *researcher.ResearchDossier) verifier.Input {
	return verifier.Input{
		Dossier: dossier,
		Parameters: verifier.VerificationParameters{
			VerifierID:       "v04-test-verifier",
			VerificationDate: v04baseTime(),
		},
	}
}

// =============================================================================
// Test 1: All Claims Supported → Article Succeeds
// =============================================================================

// TestE2E_v04_PositiveAllClaimsSupported verifies the full pipeline when all
// claims are supported: dossier → verifier → writer → checker → render.
func TestE2E_v04_PositiveAllClaimsSupported(t *testing.T) {
	ctx := context.Background()

	dossier := v04createDossier(
		v04claim("claim-001", "City council approved budget on 2024-01-10.", "source-001", researcher.ConfidenceHigh),
		v04claim("claim-002", "Projected 15% growth in renewable energy by 2025.", "source-002", researcher.ConfidenceHigh),
	)

	verificationJSON := v04verificationJSON(dossier.StableID, map[string]contracts.VerificationStatus{
		"claim-001": contracts.VerificationStatusSupported,
		"claim-002": contracts.VerificationStatusSupported,
	})

	artifactJSON := v04artifactJSON("article-v04-001", contracts.ArtifactTypeArticle, "All Supported Article",
		"Both claims are well-supported by evidence. The council approved the budget, and renewable energy is projected to grow 15% by 2025.",
		map[string]contracts.ClaimUsage{
			"claim-001": {ClaimID: "claim-001", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
			"claim-002": {ClaimID: "claim-002", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
		})

	checkResultJSON := v04checkResultJSON("check-v04-001", "article-v04-001", contracts.FactualCheckStatusPass, contracts.ConfidenceHigh)

	ver := v04buildVerifier(t, verificationJSON)
	verification, err := ver.Verify(ctx, v04verifierInput(dossier))
	if err != nil {
		t.Fatalf("Verifier failed: %v", err)
	}
	if err := verification.Validate(); err != nil {
		t.Fatalf("Verification result invalid: %v", err)
	}

	wr := v04buildWriter(t, artifactJSON)
	artifact, err := wr.Generate(ctx, verification, nil)
	if err != nil {
		t.Fatalf("Writer failed: %v", err)
	}
	if err := artifact.Validate(); err != nil {
		t.Fatalf("Artifact invalid: %v", err)
	}

	checker := v04buildChecker(t, checkResultJSON)
	result, err := checker.Check(ctx, artifact, verification)
	if err != nil {
		t.Fatalf("Final checker failed: %v", err)
	}
	if result.OverallStatus != contracts.FactualCheckStatusPass {
		t.Errorf("Expected pass, got %s", result.OverallStatus)
	}

	markdown, err := renderers.NewMarkdownRenderer().Render(artifact)
	if err != nil {
		t.Fatalf("Markdown render failed: %v", err)
	}
	if !strings.Contains(markdown, "All Supported Article") {
		t.Error("Markdown missing title")
	}

	posts, err := renderers.NewThreadRenderer().Render(artifact)
	if err != nil {
		t.Fatalf("Thread render failed: %v", err)
	}
	for i, p := range posts {
		if len(p) > renderers.BlueskyMaxChars {
			t.Errorf("Post %d exceeds 300 chars: %d", i, len(p))
		}
	}

	t.Logf("PASS: full pipeline with all-supported claims produced valid article and thread")
}

// =============================================================================
// Test 2: Contradiction Prevents Writer from Asserting Claim as Fact
// =============================================================================

func TestE2E_v04_ContradictionPreventsFactAssertion(t *testing.T) {
	ctx := context.Background()

	dossier := v04createDossier(
		v04claim("claim-001", "The population declined by 5% in 2023.", "source-001", researcher.ConfidenceHigh),
		v04claim("claim-002", "Experts disagree on the cause of the decline.", "source-002", researcher.ConfidenceMedium),
	)

	verificationJSON := v04verificationJSON(dossier.StableID, map[string]contracts.VerificationStatus{
		"claim-001": contracts.VerificationStatusContradicted,
		"claim-002": contracts.VerificationStatusSupported,
	})

	artifactJSON := v04artifactJSON("article-v04-002", contracts.ArtifactTypeArticle, "Contradiction Test",
		"Experts disagree on the cause of the decline. The 5% figure is disputed.",
		map[string]contracts.ClaimUsage{
			"claim-002": {ClaimID: "claim-002", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
		})

	checkResultJSON := v04checkResultJSON("check-v04-002", "article-v04-002", contracts.FactualCheckStatusPass, contracts.ConfidenceHigh)

	ver := v04buildVerifier(t, verificationJSON)
	verification, err := ver.Verify(ctx, v04verifierInput(dossier))
	if err != nil {
		t.Fatalf("Verifier failed: %v", err)
	}
	if verification.VerificationStatuses["claim-001"] != contracts.VerificationStatusContradicted {
		t.Error("Expected claim-001 to be contradicted")
	}

	wr := v04buildWriter(t, artifactJSON)
	artifact, err := wr.Generate(ctx, verification, nil)
	if err != nil {
		t.Fatalf("Writer failed: %v", err)
	}

	if usage, ok := artifact.ClaimReferences["claim-001"]; ok {
		if usage.UsageType == contracts.ClaimUsageCore {
			t.Error("Contradicted claim-001 used as core fact")
		}
	}

	checker := v04buildChecker(t, checkResultJSON)
	result, err := checker.Check(ctx, artifact, verification)
	if err != nil {
		t.Fatalf("Final checker failed: %v", err)
	}
	if result.OverallStatus != contracts.FactualCheckStatusPass {
		t.Errorf("Expected pass, got %s", result.OverallStatus)
	}

	t.Logf("PASS: contradicted claim not asserted as fact, pipeline passes")
}

// =============================================================================
// Test 3: Uncertain Claim Remains Qualified
// =============================================================================

func TestE2E_v04_UncertainClaimRemainsQualified(t *testing.T) {
	ctx := context.Background()

	dossier := v04createDossier(
		v04claim("claim-001", "Some sources suggest a 10% increase.", "source-001", researcher.ConfidenceLow),
		v04claim("claim-002", "The official report confirms 5% growth.", "source-002", researcher.ConfidenceHigh),
	)

	verificationJSON := v04verificationJSON(dossier.StableID, map[string]contracts.VerificationStatus{
		"claim-001": contracts.VerificationStatusUncertain,
		"claim-002": contracts.VerificationStatusSupported,
	})

	artifactJSON := v04artifactJSON("article-v04-003", contracts.ArtifactTypeArticle, "Uncertainty Test",
		"The official report confirms 5% growth. Some sources suggest a 10% increase, though this remains unconfirmed.",
		map[string]contracts.ClaimUsage{
			"claim-001": {ClaimID: "claim-001", UsageType: contracts.ClaimUsageBackground, Paraphrased: true},
			"claim-002": {ClaimID: "claim-002", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
		})

	checkResultJSON := v04checkResultJSON("check-v04-003", "article-v04-003", contracts.FactualCheckStatusPass, contracts.ConfidenceMedium)

	ver := v04buildVerifier(t, verificationJSON)
	verification, err := ver.Verify(ctx, v04verifierInput(dossier))
	if err != nil {
		t.Fatalf("Verifier failed: %v", err)
	}

	wr := v04buildWriter(t, artifactJSON)
	artifact, err := wr.Generate(ctx, verification, nil)
	if err != nil {
		t.Fatalf("Writer failed: %v", err)
	}

	usage, ok := artifact.ClaimReferences["claim-001"]
	if !ok {
		t.Log("Uncertain claim not referenced (acceptable)")
	} else if usage.UsageType == contracts.ClaimUsageCore {
		t.Error("Uncertain claim-001 used as core fact")
	}

	checker := v04buildChecker(t, checkResultJSON)
	result, err := checker.Check(ctx, artifact, verification)
	if err != nil {
		t.Fatalf("Final checker failed: %v", err)
	}
	if result.OverallStatus != contracts.FactualCheckStatusPass {
		t.Errorf("Expected pass, got %s", result.OverallStatus)
	}

	t.Logf("PASS: uncertain claim remains qualified, pipeline passes")
}

// =============================================================================
// Test 4: Writer Introduces Extra Fact → Final Checker Fails
// =============================================================================

func TestE2E_v04_WriterIntroducesExtraFact(t *testing.T) {
	ctx := context.Background()

	dossier := v04createDossier(
		v04claim("claim-001", "The statistic is 50%.", "source-001", researcher.ConfidenceHigh),
	)

	verificationJSON := v04verificationJSON(dossier.StableID, map[string]contracts.VerificationStatus{
		"claim-001": contracts.VerificationStatusSupported,
	})

	artifactJSON := v04artifactJSON("article-v04-004", contracts.ArtifactTypeArticle, "Extra Fact",
		"The statistic is 50%, and additionally the economy grew by 12% this quarter. This new fact was not verified.",
		map[string]contracts.ClaimUsage{
			"claim-001": {ClaimID: "claim-001", UsageType: contracts.ClaimUsageCore, Paraphrased: true},
		})

	checkResultJSON := v04checkResultJSON("check-v04-004", "article-v04-004", contracts.FactualCheckStatusFail, contracts.ConfidenceHigh)

	ver := v04buildVerifier(t, verificationJSON)
	verification, err := ver.Verify(ctx, v04verifierInput(dossier))
	if err != nil {
		t.Fatalf("Verifier failed: %v", err)
	}

	wr := v04buildWriter(t, artifactJSON)
	artifact, err := wr.Generate(ctx, verification, nil)
	if err != nil {
		t.Fatalf("Writer failed: %v", err)
	}

	checker := v04buildChecker(t, checkResultJSON)
	result, err := checker.Check(ctx, artifact, verification)
	if err != nil {
		t.Fatalf("Final checker failed: %v", err)
	}

	if result.OverallStatus != contracts.FactualCheckStatusFail {
		t.Errorf("Expected FAIL for extra fact, got %s", result.OverallStatus)
	}
	if len(result.UnsupportedStatements) == 0 {
		t.Error("Expected unsupported statements in check result")
	}

	t.Logf("PASS: writer extra fact correctly detected by final checker (status=%s)", result.OverallStatus)
}

// =============================================================================
// Test 5: Writer Changes Statistic → Final Checker Fails
// =============================================================================

func TestE2E_v04_WriterChangesStatistic(t *testing.T) {
	ctx := context.Background()

	dossier := v04createDossier(
		v04claim("claim-001", "Growth rate was 15% annually.", "source-001", researcher.ConfidenceHigh),
	)

	verificationJSON := v04verificationJSON(dossier.StableID, map[string]contracts.VerificationStatus{
		"claim-001": contracts.VerificationStatusSupported,
	})

	artifactJSON := v04artifactJSON("article-v04-005", contracts.ArtifactTypeArticle, "Statistic Changed",
		"Growth rate was actually 25% annually, a significant revision from earlier reports.",
		map[string]contracts.ClaimUsage{
			"claim-001": {ClaimID: "claim-001", UsageType: contracts.ClaimUsageCore, Paraphrased: true},
		})

	checkResultJSON := v04checkResultJSON("check-v04-005", "article-v04-005", contracts.FactualCheckStatusFail, contracts.ConfidenceHigh)

	ver := v04buildVerifier(t, verificationJSON)
	verification, err := ver.Verify(ctx, v04verifierInput(dossier))
	if err != nil {
		t.Fatalf("Verifier failed: %v", err)
	}

	wr := v04buildWriter(t, artifactJSON)
	artifact, err := wr.Generate(ctx, verification, nil)
	if err != nil {
		t.Fatalf("Writer failed: %v", err)
	}

	checker := v04buildChecker(t, checkResultJSON)
	result, err := checker.Check(ctx, artifact, verification)
	if err != nil {
		t.Fatalf("Final checker failed: %v", err)
	}

	if result.OverallStatus != contracts.FactualCheckStatusFail {
		t.Errorf("Expected FAIL for altered statistic, got %s", result.OverallStatus)
	}

	t.Logf("PASS: writer statistic change correctly detected by final checker (status=%s)", result.OverallStatus)
}

// =============================================================================
// Test 6: Article Rendering
// =============================================================================

func TestE2E_v04_ArticleRendering(t *testing.T) {
	artifact := &contracts.EditorialArtifact{
		StableID:     "render-article-001",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Test Article Title",
		Subtitle:     "A subtitle line",
		Body:         "This is the main body text.\n\nIt has multiple paragraphs.",
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-001": {ClaimID: "claim-001", UsageType: contracts.ClaimUsageCore, Paraphrased: false},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "ver-001",
			GeneratedAt:         v04baseTime(),
		},
	}

	renderer := renderers.NewMarkdownRenderer()
	markdown, err := renderer.Render(artifact)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if !strings.Contains(markdown, "# Test Article Title") {
		t.Error("Title missing from markdown")
	}
	if !strings.Contains(markdown, "A subtitle line") {
		t.Error("Subtitle missing from markdown")
	}
	if !strings.Contains(markdown, "This is the main body text") {
		t.Error("Body missing from markdown")
	}

	t.Logf("PASS: article renders to markdown with %d bytes", len(markdown))
}

// =============================================================================
// Test 7: Bluesky ≤300 Character Invariant
// =============================================================================

func TestE2E_v04_Bluesky300CharInvariant(t *testing.T) {
	longBody := strings.Repeat("This is a long sentence with multiple words to ensure the content exceeds the character limit. ", 10)

	artifact := &contracts.EditorialArtifact{
		StableID:     "render-thread-001",
		ArtifactType: contracts.ArtifactTypeThread,
		Title:        "Test Thread",
		Posts: []contracts.Post{
			{Order: 0, Body: longBody, ClaimIDs: []string{"claim-001"}},
			{Order: 1, Body: "Short follow-up post.", ClaimIDs: []string{"claim-002"}},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-001": {ClaimID: "claim-001", UsageType: contracts.ClaimUsageCore},
			"claim-002": {ClaimID: "claim-002", UsageType: contracts.ClaimUsageSupporting},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "ver-001",
			GeneratedAt:         v04baseTime(),
		},
	}

	renderer := renderers.NewThreadRenderer()
	posts, err := renderer.Render(artifact)
	if err != nil {
		t.Fatalf("Thread render failed: %v", err)
	}
	if len(posts) == 0 {
		t.Fatal("No posts generated")
	}
	for i, post := range posts {
		if len(post) > renderers.BlueskyMaxChars {
			t.Errorf("Post %d exceeds 300 chars: %d chars", i, len(post))
		}
	}

	t.Logf("PASS: all %d Bluesky posts within 300-char limit", len(posts))
}

// =============================================================================
// Test 8: Profile Inheritance
// =============================================================================

func TestE2E_v04_ProfileInheritance(t *testing.T) {
	dir := t.TempDir()
	profileDir := filepath.Join(dir, "profiles")
	commonDir := filepath.Join(profileDir, "common")
	authorDir := filepath.Join(profileDir, "authors", "test-author")

	if err := os.MkdirAll(commonDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(authorDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(commonDir, "style.md"), []byte("## Style\nUse concise, factual language.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "facts-policy.md"), []byte("## Facts\nOnly assert verified claims.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authorDir, "style.md"), []byte("## Author Override\nPrefer analytical tone.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	loader, err := profiles.NewLoader(profiles.Config{BasePath: profileDir, AuthorOverride: "test-author"})
	if err != nil {
		t.Fatalf("Failed to create loader: %v", err)
	}
	profile, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Failed to load profile: %v", err)
	}

	if profile.Style == "" {
		t.Error("Style should not be empty")
	}

	injector := profiles.NewInjector(profile)
	writerInstructions := injector.InjectWriterInstructions()
	if writerInstructions == "" {
		t.Error("Writer instructions should not be empty")
	}

	t.Logf("PASS: profile loaded with inherited style, writer instructions = %d chars", len(writerInstructions))
}

// =============================================================================
// Test 9: Intermediate Artifact Round-Trip
// =============================================================================

func TestE2E_v04_IntermediateArtifactRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// 1. VerificationResult round-trip.
	verification := &contracts.VerificationResult{
		StableID:       "ver-roundtrip-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     v04baseTime(),
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusContradicted,
			"claim-003": contracts.VerificationStatusUncertain,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Verified claim one",
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh,
				SupportingEvidence: []contracts.EvidenceReference{{SourceID: "source-001", Excerpt: "Evidence 1"}},
			},
			"claim-002": {
				ClaimID:             "claim-002",
				Statement:           "Contradicted claim two",
				VerificationStatus:  contracts.VerificationStatusContradicted,
				ConflictingEvidence: []contracts.EvidenceReference{{SourceID: "source-002", Excerpt: "Contradiction"}},
			},
			"claim-003": {
				ClaimID:            "claim-003",
				Statement:          "Uncertain claim three",
				VerificationStatus: contracts.VerificationStatusUncertain,
			},
		},
		Contradictions: []contracts.VerifiedContradiction{
			{ID: "contrad-001", ClaimIDs: []string{"claim-002"}, Description: "Evidence conflicts"},
		},
		QualityScore: 55.0,
	}

	verJSON, err := json.Marshal(verification)
	if err != nil {
		t.Fatalf("VerificationResult marshal failed: %v", err)
	}
	verPath := filepath.Join(dir, "verification.json")
	if err := os.WriteFile(verPath, verJSON, 0644); err != nil {
		t.Fatalf("Write verification failed: %v", err)
	}
	data, err := os.ReadFile(verPath)
	if err != nil {
		t.Fatalf("Read verification failed: %v", err)
	}
	var restoredVer contracts.VerificationResult
	if err := json.Unmarshal(data, &restoredVer); err != nil {
		t.Fatalf("VerificationResult unmarshal failed: %v", err)
	}
	if restoredVer.StableID != "ver-roundtrip-001" {
		t.Error("VerificationResult stable_id mismatch")
	}
	if len(restoredVer.ClaimDetails) != 3 {
		t.Errorf("Expected 3 claim details, got %d", len(restoredVer.ClaimDetails))
	}
	if len(restoredVer.Contradictions) != 1 {
		t.Errorf("Expected 1 contradiction, got %d", len(restoredVer.Contradictions))
	}

	// 2. EditorialArtifact round-trip.
	artifact := &contracts.EditorialArtifact{
		StableID:     "artifact-roundtrip-001",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Round Trip Test",
		Body:         "Original content.",
		Sections: []contracts.Section{
			{Order: 0, Title: "Section 1", Body: "Content 1", ClaimIDs: []string{"claim-001"}},
			{Order: 1, Title: "Section 2", Body: "Content 2", ClaimIDs: []string{"claim-002"}},
		},
		Posts: []contracts.Post{
			{Order: 0, Body: "Post 1", ClaimIDs: []string{"claim-003"}},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-001": {ClaimID: "claim-001", UsageType: contracts.ClaimUsageCore},
			"claim-002": {ClaimID: "claim-002", UsageType: contracts.ClaimUsageSupporting},
			"claim-003": {ClaimID: "claim-003", UsageType: contracts.ClaimUsageBackground},
		},
		SourceReferences: map[string]contracts.SourceUsage{
			"source-001": {SourceID: "source-001", UsageCount: 1},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "ver-roundtrip-001",
			GeneratedAt:         v04baseTime(),
		},
		Warnings: []contracts.Warning{
			{WarningType: contracts.WarningTypeLowConfidence, Severity: contracts.WarningSeverityLow, Message: "Low confidence"},
		},
	}

	artJSON, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("EditorialArtifact marshal failed: %v", err)
	}
	artPath := filepath.Join(dir, "artifact.json")
	if err := os.WriteFile(artPath, artJSON, 0644); err != nil {
		t.Fatalf("Write artifact failed: %v", err)
	}
	data, err = os.ReadFile(artPath)
	if err != nil {
		t.Fatalf("Read artifact failed: %v", err)
	}
	var restoredArt contracts.EditorialArtifact
	if err := json.Unmarshal(data, &restoredArt); err != nil {
		t.Fatalf("EditorialArtifact unmarshal failed: %v", err)
	}
	if restoredArt.StableID != "artifact-roundtrip-001" {
		t.Error("EditorialArtifact stable_id mismatch")
	}
	if len(restoredArt.Sections) != 2 {
		t.Errorf("Expected 2 sections, got %d", len(restoredArt.Sections))
	}
	if len(restoredArt.Posts) != 1 {
		t.Errorf("Expected 1 post, got %d", len(restoredArt.Posts))
	}
	if len(restoredArt.ClaimReferences) != 3 {
		t.Errorf("Expected 3 claim references, got %d", len(restoredArt.ClaimReferences))
	}
	if len(restoredArt.Warnings) != 1 {
		t.Errorf("Expected 1 warning, got %d", len(restoredArt.Warnings))
	}

	// 3. FactualCheckResult round-trip.
	checkResult := &contracts.FactualCheckResult{
		StableID:        "check-roundtrip-001",
		InputArtifactID: "artifact-roundtrip-001",
		CheckedAt:       v04baseTime(),
		ClaimIDs:        []string{"claim-001", "claim-002"},
		ClaimVerificationStatus: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusContradicted,
		},
		OverallStatus:   contracts.FactualCheckStatusPass,
		ConfidenceLevel: contracts.ConfidenceHigh,
		Summary:         "All checks passed",
	}

	checkJSON, err := json.Marshal(checkResult)
	if err != nil {
		t.Fatalf("FactualCheckResult marshal failed: %v", err)
	}
	checkPath := filepath.Join(dir, "check-result.json")
	if err := os.WriteFile(checkPath, checkJSON, 0644); err != nil {
		t.Fatalf("Write check result failed: %v", err)
	}
	data, err = os.ReadFile(checkPath)
	if err != nil {
		t.Fatalf("Read check result failed: %v", err)
	}
	var restoredCheck contracts.FactualCheckResult
	if err := json.Unmarshal(data, &restoredCheck); err != nil {
		t.Fatalf("FactualCheckResult unmarshal failed: %v", err)
	}
	if restoredCheck.StableID != "check-roundtrip-001" {
		t.Error("FactualCheckResult stable_id mismatch")
	}
	if restoredCheck.OverallStatus != contracts.FactualCheckStatusPass {
		t.Error("FactualCheckResult status mismatch")
	}

	t.Logf("PASS: all 3 v0.4 artifacts round-trip through JSON successfully")
}

// =============================================================================
// Test 10: v0.3 Research/RAG Remains Unaffected
// =============================================================================

func TestE2E_v04_v03Unaffected(t *testing.T) {
	ctx := context.Background()

	store := createInMemoryArchive(t)

	cfg := chunker.Config{TargetChunkSize: 200, MaxChunkSize: 300, OverlapRatio: 0.1}
	_, err := ingestDoc(ctx, store, "v03-rag-test", "v0.3 RAG still works in v0.4", cfg, embedding.NewFakeEmbedder(), t)
	if err != nil {
		t.Fatalf("v0.3 ingestDoc failed: %v", err)
	}

	t.Logf("PASS: v0.3 research/RAG remains unaffected by v0.4 changes")
}

// =============================================================================
// Core Pipeline Full Wiring
// =============================================================================

func TestE2E_v04_CorePipelineFullWiring(t *testing.T) {
	ctx := context.Background()

	dossier := v04createDossier(
		v04claim("claim-001", "The dataset contains 150 records.", "source-001", researcher.ConfidenceHigh),
		v04claim("claim-002", "Average latency was 23ms.", "source-002", researcher.ConfidenceHigh),
	)

	verificationJSON := v04verificationJSON(dossier.StableID, map[string]contracts.VerificationStatus{
		"claim-001": contracts.VerificationStatusSupported,
		"claim-002": contracts.VerificationStatusSupported,
	})

	artifactJSON := v04threadArtifactJSON("thread-v04-001",
		"The dataset contains 150 records with 23ms average latency.",
		"Average latency was 23ms. The dataset contains 150 records.",
		map[string]contracts.ClaimUsage{
			"claim-001": {ClaimID: "claim-001", UsageType: contracts.ClaimUsageCore},
			"claim-002": {ClaimID: "claim-002", UsageType: contracts.ClaimUsageCore},
		})

	checkResultJSON := v04checkResultJSON("check-v04-full", "thread-v04-001", contracts.FactualCheckStatusPass, contracts.ConfidenceHigh)

	ver := v04buildVerifier(t, verificationJSON)
	verification, err := ver.Verify(ctx, v04verifierInput(dossier))
	if err != nil {
		t.Fatalf("Verifier failed: %v", err)
	}

	wr := v04buildWriter(t, artifactJSON)
	artifact, err := wr.Generate(ctx, verification, nil)
	if err != nil {
		t.Fatalf("Writer failed: %v", err)
	}

	checker := v04buildChecker(t, checkResultJSON)
	result, err := checker.Check(ctx, artifact, verification)
	if err != nil {
		t.Fatalf("Final checker failed: %v", err)
	}
	if result.OverallStatus != contracts.FactualCheckStatusPass {
		t.Fatalf("Expected pass, got %s", result.OverallStatus)
	}

	posts, err := renderers.NewThreadRenderer().Render(artifact)
	if err != nil {
		t.Fatalf("Thread render failed: %v", err)
	}
	if len(posts) == 0 {
		t.Fatal("No posts generated")
	}
	for i, p := range posts {
		if len(p) > renderers.BlueskyMaxChars {
			t.Errorf("Post %d exceeds 300 chars: %d", i, len(p))
		}
	}

	markdown, err := renderers.NewMarkdownRenderer().Render(artifact)
	if err != nil {
		t.Fatalf("Markdown render failed: %v", err)
	}
	if !strings.Contains(markdown, "#") {
		t.Error("Markdown missing heading")
	}

	t.Logf("PASS: full pipeline produced %d thread posts and %d-byte markdown", len(posts), len(markdown))
}

// =============================================================================
// Helper: Build Pipeline Stages
// =============================================================================

func v04buildVerifier(t *testing.T, fakeResponseJSON string) *verifier.Verifier {
	t.Helper()
	client := NewV04FakeLLM(fakeResponseJSON)
	v, err := verifier.New(client, "test-model", verifier.VerifierConfig{
		PromptOverride: "# Verification Task\nYou are a verification analyst.",
		TimeNow:        func() time.Time { return v04baseTime() },
	})
	if err != nil {
		t.Fatalf("Failed to create verifier: %v", err)
	}
	return v
}

func v04buildWriter(t *testing.T, fakeResponseJSON string) *writer.Writer {
	t.Helper()
	client := NewV04FakeLLM(fakeResponseJSON)
	w, err := writer.New(client, "test-model", writer.WriterConfig{
		PromptOverride: "# Writer Stage\nYou are the Writer stage.",
		Language:       "en",
		ArtifactType:   "article",
		TimeNow:        func() time.Time { return v04baseTime() },
	})
	if err != nil {
		t.Fatalf("Failed to create writer: %v", err)
	}
	return w
}

func v04buildChecker(t *testing.T, fakeResponseJSON string) *finalchecker.FinalFactualChecker {
	t.Helper()
	client := NewV04FakeLLM(fakeResponseJSON)
	c, err := finalchecker.New(client, "test-model", finalchecker.CheckerConfig{
		PromptOverride: "# Final Factual Checker\nYou are the Final Factual Checker.",
		TimeNow:        func() time.Time { return v04baseTime() },
	})
	if err != nil {
		t.Fatalf("Failed to create final checker: %v", err)
	}
	return c
}

// =============================================================================
// JSON Fixture Builders
// =============================================================================

func v04verificationJSON(dossierID string, statuses map[string]contracts.VerificationStatus) string {
	details := make(map[string]contracts.ClaimVerificationDetails, len(statuses))
	for claimID, status := range statuses {
		details[claimID] = contracts.ClaimVerificationDetails{
			ClaimID:            claimID,
			Statement:          "Statement for " + claimID,
			VerificationStatus: status,
			ConfidenceLevel:    contracts.ConfidenceHigh,
		}
	}
	result := contracts.VerificationResult{
		StableID:             "ver-" + dossierID,
		InputDossierID:       dossierID,
		VerifiedAt:           v04baseTime(),
		VerificationStatuses: statuses,
		ClaimDetails:         details,
		Contradictions:       []contracts.VerifiedContradiction{},
		QualityScore:         85.0,
	}
	b, _ := json.Marshal(result)
	return string(b)
}

func v04artifactJSON(stableID string, artifactType contracts.ArtifactType, title, body string, claimRefs map[string]contracts.ClaimUsage) string {
	artifact := contracts.EditorialArtifact{
		StableID:         stableID,
		ArtifactType:     artifactType,
		Title:            title,
		Body:             body,
		ClaimReferences:  claimRefs,
		SourceReferences: map[string]contracts.SourceUsage{},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "ver-dossier-v04-001",
			GeneratedAt:         v04baseTime(),
		},
	}
	b, _ := json.Marshal(artifact)
	return string(b)
}

func v04threadArtifactJSON(stableID, post1, post2 string, claimRefs map[string]contracts.ClaimUsage) string {
	artifact := contracts.EditorialArtifact{
		StableID:     stableID,
		ArtifactType: contracts.ArtifactTypeThread,
		Title:        "Thread Title",
		Posts: []contracts.Post{
			{Order: 0, Body: post1, ClaimIDs: []string{"claim-001"}},
			{Order: 1, Body: post2, ClaimIDs: []string{"claim-002"}},
		},
		ClaimReferences:  claimRefs,
		SourceReferences: map[string]contracts.SourceUsage{},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "ver-dossier-v04-001",
			GeneratedAt:         v04baseTime(),
		},
	}
	b, _ := json.Marshal(artifact)
	return string(b)
}

func v04checkResultJSON(stableID, artifactID string, status contracts.FactualCheckStatus, confidence contracts.ConfidenceLevel) string {
	result := contracts.FactualCheckResult{
		StableID:        stableID,
		InputArtifactID: artifactID,
		CheckedAt:       v04baseTime(),
		ClaimIDs:        []string{"claim-001", "claim-002"},
		ClaimVerificationStatus: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusSupported,
		},
		OverallStatus:   status,
		ConfidenceLevel: confidence,
		Summary:         "Check completed",
	}
	if status == contracts.FactualCheckStatusFail {
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
				FindingID:       "finding-001",
				FindingType:     contracts.FindingTypeError,
				Statement:       "The artifact introduces facts not present in the verification result",
				Severity:        contracts.WarningSeverityHigh,
				Category:        contracts.FindingCategoryClaim,
				RelatedClaimIDs: []string{"claim-001"},
			},
		}
	}
	b, _ := json.Marshal(result)
	return string(b)
}
