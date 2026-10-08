// Package finalchecker tests the FinalFactualChecker.
//
// Tests cover:
// - Clean artifact passes all checks
// - Unknown claim reference detection
// - Changed number detection
// - Contradicted claim stated as fact
// - Invented fact detection
// - Uncertain claim overstated
// - Malformed checker output handling
// - Deterministic validation errors
// - LLM response parsing
// - Edge cases (empty artifact, no claims, etc.)
package finalchecker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseTime parses an RFC3339 time string.
func parseTime(t string) time.Time {
	parsed, err := time.Parse(time.RFC3339, t)
	if err != nil {
		panic(err)
	}
	return parsed
}

// ============================================================================
// Fake LLM Client for Testing
// ============================================================================

type fakeLLMClient struct {
	response string
	err      error
}

func (f *fakeLLMClient) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if f.err != nil {
		return llm.Response{}, f.err
	}
	return llm.Response{Content: f.response}, nil
}

// ============================================================================
// Test Fixtures
// ============================================================================

// createTestVerificationResult creates a verification result for testing.
func createTestVerificationResult() *contracts.VerificationResult {
	return &contracts.VerificationResult{
		StableID:       "verify-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     parseTime("2024-01-15T12:00:00Z"),
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusSupported,
			"claim-003": contracts.VerificationStatusUncertain,
			"claim-004": contracts.VerificationStatusContradicted,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "The program processed 150 records",
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh,
				SupportingEvidence: []contracts.EvidenceReference{
					{SourceID: "source-001", Excerpt: "Log shows 150 records processed"},
				},
			},
			"claim-002": {
				ClaimID:            "claim-002",
				Statement:          "Latency averaged 23ms",
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh,
				SupportingEvidence: []contracts.EvidenceReference{
					{SourceID: "source-002", Excerpt: "Average latency: 23ms"},
				},
			},
			"claim-003": {
				ClaimID:            "claim-003",
				Statement:          "Users reported high satisfaction",
				VerificationStatus: contracts.VerificationStatusUncertain,
				ConfidenceLevel:    contracts.ConfidenceMedium,
			},
			"claim-004": {
				ClaimID:            "claim-004",
				Statement:          "System achieved 99.9% uptime",
				VerificationStatus: contracts.VerificationStatusContradicted,
				ConfidenceLevel:    contracts.ConfidenceMedium,
			},
		},
		QualityScore: 60.0,
	}
}

// createCleanArtifact creates a clean artifact that should pass all checks.
func createCleanArtifact() *contracts.EditorialArtifact {
	return &contracts.EditorialArtifact{
		StableID:     "artifact-001",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Performance Report",
		Body:         "The program processed 150 records with 23ms average latency.",
		Sections: []contracts.Section{
			{
				Order:    0,
				Title:    "Key Findings",
				Body:     "Core metrics were measured successfully.",
				ClaimIDs: []string{"claim-001", "claim-002"},
			},
		},
		ClaimReferences: map[string]contracts.ClaimUsage{
			"claim-001": {
				ClaimID:     "claim-001",
				UsageType:   contracts.ClaimUsageCore,
				Paraphrased: true,
				Locations: []contracts.ClaimLocation{
					{LocationType: contracts.LocationTypeSection, Index: 0},
				},
			},
			"claim-002": {
				ClaimID:     "claim-002",
				UsageType:   contracts.ClaimUsageSupporting,
				Paraphrased: true,
				Locations: []contracts.ClaimLocation{
					{LocationType: contracts.LocationTypeSection, Index: 0},
				},
			},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "verify-001",
			GeneratedAt:         parseTime("2024-01-15T12:00:00Z"),
			WriterParameters: map[string]string{
				"language":      "en",
				"artifact_type": "article",
			},
		},
	}
}

// createArtifactWithUnknownClaim creates an artifact with an unknown claim reference.
func createArtifactWithUnknownClaim() *contracts.EditorialArtifact {
	artifact := createCleanArtifact()
	artifact.ClaimReferences["claim-999"] = contracts.ClaimUsage{
		ClaimID:     "claim-999",
		UsageType:   contracts.ClaimUsageCore,
		Paraphrased: true,
	}
	return artifact
}

// createArtifactWithChangedNumber creates an artifact with a changed number.
func createArtifactWithChangedNumber() *contracts.EditorialArtifact {
	artifact := createCleanArtifact()
	artifact.Body = "The program processed 1500 records with 23ms average latency."
	return artifact
}

// createArtifactWithContradictedFact creates an artifact stating a contradicted claim as fact.
func createArtifactWithContradictedFact() *contracts.EditorialArtifact {
	artifact := createCleanArtifact()
	artifact.Body = "The system achieved 99.9% uptime as confirmed by logs."
	artifact.ClaimReferences["claim-004"] = contracts.ClaimUsage{
		ClaimID:     "claim-004",
		UsageType:   contracts.ClaimUsageCore,
		Paraphrased: true,
	}
	return artifact
}

// createArtifactWithOverstatedUncertain creates an artifact that overstates an uncertain claim.
func createArtifactWithOverstatedUncertain() *contracts.EditorialArtifact {
	artifact := createCleanArtifact()
	artifact.Body = "Users definitely reported high satisfaction with the system."
	artifact.ClaimReferences["claim-003"] = contracts.ClaimUsage{
		ClaimID:     "claim-003",
		UsageType:   contracts.ClaimUsageCore,
		Paraphrased: true,
	}
	return artifact
}

// createArtifactWithInventedFact creates an artifact with an invented fact.
func createArtifactWithInventedFact() *contracts.EditorialArtifact {
	artifact := createCleanArtifact()
	artifact.Body = "The program processed 150 records, generated 45 charts, and served 500 users."
	// Note: only claim-001 and claim-002 are referenced, but the text mentions charts and users
	return artifact
}

// ============================================================================
// Checker Constructor Tests
// ============================================================================

func TestNewChecker_WithNilClient(t *testing.T) {
	_, err := New(nil, TestModel, CheckerConfig{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "llm.Client must not be nil")
}

func TestNewChecker_Success(t *testing.T) {
	client := &fakeLLMClient{}
	config := CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	}

	checker, err := New(client, TestModel, config)
	assert.NoError(t, err)
	assert.NotNil(t, checker)
	assert.Equal(t, TestModel, checker.model)
	assert.Equal(t, TestTemp, checker.config.Temperature)
}

func TestNewChecker_WithPromptOverride(t *testing.T) {
	client := &fakeLLMClient{}
	customPrompt := "You are a custom checker."
	config := CheckerConfig{
		PromptOverride: customPrompt,
		TimeNow:        func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:    contracts.NewUUIDGenerator(),
	}

	checker, err := New(client, TestModel, config)
	assert.NoError(t, err)
	assert.NotNil(t, checker)
	assert.Equal(t, customPrompt, checker.prompt)
}

// ============================================================================
// Check Method Tests
// ============================================================================

func TestCheck_WithNilArtifact(t *testing.T) {
	checker, err := New(&fakeLLMClient{}, TestModel, CheckerConfig{
		TimeNow:     func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	_, err = checker.Check(context.Background(), nil, createTestVerificationResult())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "artifact cannot be nil")
}

func TestCheck_WithNilVerificationResult(t *testing.T) {
	checker, err := New(&fakeLLMClient{}, TestModel, CheckerConfig{
		TimeNow:     func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	_, err = checker.Check(context.Background(), createCleanArtifact(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "verificationResult cannot be nil")
}

func TestCheck_CleanArtifactPasses(t *testing.T) {
	// LLM returns a passing result
	acceptingResult := `{
		"stable_id": "checker-artifact-001-run-1",
		"input_artifact_id": "artifact-001",
		"checked_at": "2024-01-15T12:00:00Z",
		"claim_ids": ["claim-001", "claim-002", "claim-003", "claim-004"],
		"claim_verification_status": {
			"claim-001": "supported",
			"claim-002": "supported",
			"claim-003": "uncertain",
			"claim-004": "contradicted"
		},
		"unsupported_statements": [],
		"altered_details": [],
		"factual_findings": [],
		"overall_status": "pass",
		"confidence_level": "high",
		"summary": "All factual claims are verified and supported.",
		"recommendations": []
	}`

	checker, err := New(&fakeLLMClient{response: acceptingResult}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createCleanArtifact()

	result, err := checker.Check(context.Background(), artifact, verificationResult)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, contracts.FactualCheckStatusPass, result.OverallStatus)
	assert.Empty(t, result.FactualFindings)
	assert.Empty(t, result.UnsupportedStatements)
}

// ============================================================================
// Unknown Claim Reference Detection
// ============================================================================

func TestCheck_UnknownClaimReferenceDetected(t *testing.T) {
	// LLM returns an empty result - deterministic check should catch the unknown claim
	llmEmptyResult := `{
		"stable_id": "checker-artifact-001-run-2",
		"input_artifact_id": "artifact-001",
		"checked_at": "2024-01-15T12:00:00Z",
		"claim_ids": ["claim-001", "claim-002", "claim-003", "claim-004"],
		"claim_verification_status": {
			"claim-001": "supported",
			"claim-002": "supported",
			"claim-003": "uncertain",
			"claim-004": "contradicted"
		},
		"unsupported_statements": [],
		"altered_details": [],
		"factual_findings": [],
		"overall_status": "pass",
		"confidence_level": "high",
		"summary": "LLM check passed - deterministic checks run separately.",
		"recommendations": []
	}`

	checker, err := New(&fakeLLMClient{response: llmEmptyResult}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createArtifactWithUnknownClaim()

	result, err := checker.Check(context.Background(), artifact, verificationResult)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, contracts.FactualCheckStatusFail, result.OverallStatus)
	// Should have at least one deterministic finding for unknown claim
	assert.NotEmpty(t, result.FactualFindings)
	foundUnknownClaim := false
	for _, f := range result.FactualFindings {
		if strings.Contains(f.Statement, "claim-999") {
			foundUnknownClaim = true
			break
		}
	}
	assert.True(t, foundUnknownClaim, "Should detect unknown claim claim-999")
}

// ============================================================================
// Changed Number Detection
// ============================================================================

func TestCheck_ChangedNumberDetected(t *testing.T) {
	// LLM returns a result with altered number
	negativeResult := `{
		"stable_id": "checker-artifact-001-run-3",
		"input_artifact_id": "artifact-001",
		"checked_at": "2024-01-15T12:00:00Z",
		"claim_ids": ["claim-001", "claim-002", "claim-003", "claim-004"],
		"claim_verification_status": {
			"claim-001": "supported",
			"claim-002": "supported",
			"claim-003": "uncertain",
			"claim-004": "contradicted"
		},
		"unsupported_statements": [],
		"altered_details": [
			{
				"location": {
					"location_type": "body",
					"index": -1
				},
				"detail_type": "number",
				"claim_ids": ["claim-001"],
				"original_value": "150",
				"altered_value": "1500",
				"significance": "critical",
				"is_error": true,
				"suggested_revision": "Change 1500 to 150"
			}
		],
		"factual_findings": [
			{
				"finding_id": "finding-001",
				"finding_type": "error",
				"statement": "Number altered: 150 → 1500",
				"evidence": "Claim statement says 150 records, artifact says 1500 records",
				"severity": "critical",
				"category": "accuracy",
				"related_claim_ids": ["claim-001"]
			}
		],
		"overall_status": "fail",
		"confidence_level": "high",
		"summary": "Critical factual alteration detected.",
		"recommendations": [
			{
				"recommendation_id": "rec-001",
				"recommendation_type": "fix",
				"description": "Review and address: Number altered: 150 → 1500",
				"priority": "critical",
				"related_finding_ids": ["finding-001"],
				"effort_estimate": "substantial"
			}
		]
	}`

	checker, err := New(&fakeLLMClient{response: negativeResult}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createArtifactWithChangedNumber()

	result, err := checker.Check(context.Background(), artifact, verificationResult)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, contracts.FactualCheckStatusFail, result.OverallStatus)
	assert.Len(t, result.AlteredDetails, 1)
	assert.Equal(t, contracts.AlteredDetailTypeNumber, result.AlteredDetails[0].DetailType)
	assert.Equal(t, "150", result.AlteredDetails[0].OriginalValue)
	assert.Equal(t, "1500", result.AlteredDetails[0].AlteredImageValue)
}

// ============================================================================
// Contradicted Claim as Fact Detection
// ============================================================================

func TestCheck_ContradictedClaimStatedAsFact(t *testing.T) {
	// LLM returns an empty result - deterministic check should catch contradicted as core
	llmEmptyResult := `{
		"stable_id": "checker-artifact-001-run-4",
		"input_artifact_id": "artifact-001",
		"checked_at": "2024-01-15T12:00:00Z",
		"claim_ids": ["claim-001", "claim-002", "claim-003", "claim-004"],
		"claim_verification_status": {
			"claim-001": "supported",
			"claim-002": "supported",
			"claim-003": "uncertain",
			"claim-004": "contradicted"
		},
		"unsupported_statements": [],
		"altered_details": [],
		"factual_findings": [],
		"overall_status": "pass",
		"confidence_level": "high",
		"summary": "LLM check passed - deterministic checks run separately.",
		"recommendations": []
	}`

	checker, err := New(&fakeLLMClient{response: llmEmptyResult}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createArtifactWithContradictedFact()

	result, err := checker.Check(context.Background(), artifact, verificationResult)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, contracts.FactualCheckStatusFail, result.OverallStatus)
	// Should have at least one deterministic finding for contradicted claim used as core
	assert.NotEmpty(t, result.FactualFindings)
	foundContradicted := false
	for _, f := range result.FactualFindings {
		if strings.Contains(f.Statement, "claim-004") && strings.Contains(f.Statement, "used as core") {
			foundContradicted = true
			break
		}
	}
	assert.True(t, foundContradicted, "Should detect contradicted claim claim-004 used as core")
}

// ============================================================================
// Invented Fact Detection
// ============================================================================

func TestCheck_InventedFactDetected(t *testing.T) {
	// LLM returns a result with invented fact
	negativeResult := `{
		"stable_id": "checker-artifact-001-run-5",
		"input_artifact_id": "artifact-001",
		"checked_at": "2024-01-15T12:00:00Z",
		"claim_ids": ["claim-001", "claim-002", "claim-003", "claim-004"],
		"claim_verification_status": {
			"claim-001": "supported",
			"claim-002": "supported",
			"claim-003": "uncertain",
			"claim-004": "contradicted"
		},
		"unsupported_statements": [
			{
				"location": {
					"location_type": "body",
					"index": -1
				},
				"statement": "generated 45 charts",
				"statement_type": "new_facts",
				"severity": "high",
				"evidence_missing": "No claim references this fact",
				"claim_ids": []
			},
			{
				"location": {
					"location_type": "body",
					"index": -1
				},
				"statement": "served 500 users",
				"statement_type": "new_facts",
				"severity": "high",
				"evidence_missing": "No claim references this fact",
				"claim_ids": []
			}
		],
		"altered_details": [],
		"factual_findings": [
			{
				"finding_id": "finding-001",
				"finding_type": "error",
				"statement": "Factual statements not traceable to verified claims",
				"evidence": "Artifact contains facts not supported by any claim in verification result",
				"severity": "high",
				"category": "evidence",
				"related_claim_ids": []
			}
		],
		"overall_status": "fail",
		"confidence_level": "high",
		"summary": "Invented facts detected in artifact.",
		"recommendations": [
			{
				"recommendation_id": "rec-001",
				"recommendation_type": "remove",
				"description": "Remove invented facts: generated 45 charts, served 500 users",
				"priority": "high",
				"related_finding_ids": ["finding-001"],
				"effort_estimate": "moderate"
			}
		]
	}`

	checker, err := New(&fakeLLMClient{response: negativeResult}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createArtifactWithInventedFact()

	result, err := checker.Check(context.Background(), artifact, verificationResult)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, contracts.FactualCheckStatusFail, result.OverallStatus)
	assert.Len(t, result.UnsupportedStatements, 2)
}

// ============================================================================
// Uncertain Claim Overstated Detection
// ============================================================================

func TestCheck_UncertainClaimOverstated(t *testing.T) {
	// This is an LLM-only check (prose-level analysis)
	// Deterministic checks don't detect this - only LLM analysis does
	// Set up LLM to return an error result
	llmNegativeResult := `{
		"stable_id": "checker-artifact-001-run-6",
		"input_artifact_id": "artifact-001",
		"checked_at": "2024-01-15T12:00:00Z",
		"claim_ids": ["claim-001", "claim-002", "claim-003", "claim-004"],
		"claim_verification_status": {
			"claim-001": "supported",
			"claim-002": "supported",
			"claim-003": "uncertain",
			"claim-004": "contradicted"
		},
		"unsupported_statements": [],
		"altered_details": [],
		"factual_findings": [
			{
				"finding_id": "finding-001",
				"finding_type": "error",
				"statement": "Uncertain claim presented with false certainty",
				"evidence": "Claim 'Users reported high satisfaction' uses certainty language 'definitely' instead of uncertainty framing",
				"severity": "high",
				"category": "accuracy",
				"related_claim_ids": ["claim-003"]
			}
		],
		"overall_status": "fail",
		"confidence_level": "high",
		"summary": "Uncertain claim overstated.",
		"recommendations": [
			{
				"recommendation_id": "rec-001",
				"recommendation_type": "clarify",
				"description": "Add uncertainty framing to claim claim-003",
				"priority": "high",
				"related_finding_ids": ["finding-001"],
				"effort_estimate": "quick"
			}
		]
	}`

	checker, err := New(&fakeLLMClient{response: llmNegativeResult}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createArtifactWithOverstatedUncertain()

	result, err := checker.Check(context.Background(), artifact, verificationResult)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	// Should have both deterministic (pass) AND LLM findings (fail)
	assert.NotEmpty(t, result.FactualFindings)
	// Find the uncertain claim finding
	var foundOverstatement bool
	for _, f := range result.FactualFindings {
		if strings.Contains(f.Statement, "certainty") || strings.Contains(f.Statement, "uncertain claim") {
			foundOverstatement = true
			// Finding type can be error or concern depending on severity
			assert.Equal(t, contracts.WarningSeverityHigh, f.Severity)
			break
		}
	}
	assert.True(t, foundOverstatement, "Should detect overstated uncertain claim")
	// Overall status depends on how merge combines deterministic and LLM findings
	// Since deterministic finds nothing, and LLM finds high severity issue, expect review or fail
	assert.NotEqual(t, contracts.FactualCheckStatusPass, result.OverallStatus, "Should not pass when LLM detects overstated uncertain claim")
}

// ============================================================================
// Malformed Checker Output Tests
// ============================================================================

func TestCheck_MalformedLLMOutput(t *testing.T) {
	// LLM returns malformed JSON
	invalidJSON := `{this is not valid json}`

	checker, err := New(&fakeLLMClient{response: invalidJSON}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createCleanArtifact()

	_, err = checker.Check(context.Background(), artifact, verificationResult)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse JSON")
}

func TestCheck_MissingRequiredFieldsInLLMOutput(t *testing.T) {
	// LLM returns JSON missing required fields
	incompleteJSON := `{
		"stable_id": "checker-artifact-001"
	}`

	checker, err := New(&fakeLLMClient{response: incompleteJSON}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createCleanArtifact()

	_, err = checker.Check(context.Background(), artifact, verificationResult)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing required field")
}

func TestCheck_EmptyLLMResponse(t *testing.T) {
	checker, err := New(&fakeLLMClient{response: ""}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createCleanArtifact()

	_, err = checker.Check(context.Background(), artifact, verificationResult)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty model response")
}

// ============================================================================
// Deterministic Validation Tests
// ============================================================================

func TestCheck_DeterministicUnknownClaimDetected(t *testing.T) {
	// When LLM fails, deterministic checks should still run
	checker, err := New(&fakeLLMClient{err: llm.Internal("LLM error")}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createArtifactWithUnknownClaim()

	result, err := checker.Check(context.Background(), artifact, verificationResult)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, contracts.FactualCheckStatusFail, result.OverallStatus)
	assert.Len(t, result.FactualFindings, 1)
}

func TestCheck_ContradictedClaimUsedAsCore(t *testing.T) {
	// Deterministic check should detect contradicted claim used as core
	checker, err := New(&fakeLLMClient{}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createArtifactWithContradictedFact()

	// The deterministic check should catch this before LLM is even called
	// But since we're using fake LLM, we need to also return appropriate findings
	negativeResult := `{
		"stable_id": "checker-test",
		"input_artifact_id": "artifact-001",
		"checked_at": "2024-01-15T12:00:00Z",
		"claim_ids": ["claim-001", "claim-002", "claim-003", "claim-004"],
		"claim_verification_status": {
			"claim-001": "supported",
			"claim-002": "supported",
			"claim-003": "uncertain",
			"claim-004": "contradicted"
		},
		"unsupported_statements": [],
		"altered_details": [],
		"factual_findings": [
			{
				"finding_id": "finding-det-001",
				"statement": "Contradicted claim claim-004 used as core fact",
				"evidence": "Contradicted claim used with usage_type=core",
				"severity": "critical",
				"category": "claim",
				"related_claim_ids": ["claim-004"],
				"finding_type": "error"
			}
		],
		"overall_status": "fail",
		"confidence_level": "high",
		"summary": "Deterministic validation failed.",
		"recommendations": []
	}`

	// Override the checker's client to return our negative result
	checker.client = &fakeLLMClient{response: negativeResult}

	result, err := checker.Check(context.Background(), artifact, verificationResult)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, contracts.FactualCheckStatusFail, result.OverallStatus)
	assert.NotEmpty(t, result.FactualFindings)
}

// ============================================================================
// Edge Case Tests
// ============================================================================

func TestCheck_EmptyArtifact(t *testing.T) {
	emptyResult := `{
		"stable_id": "checker-empty-001",
		"input_artifact_id": "artifact-empty",
		"checked_at": "2024-01-15T12:00:00Z",
		"claim_ids": [],
		"claim_verification_status": {},
		"unsupported_statements": [],
		"altered_details": [],
		"factual_findings": [],
		"overall_status": "pass",
		"confidence_level": "high",
		"summary": "No claims to verify.",
		"recommendations": []
	}`

	checker, err := New(&fakeLLMClient{response: emptyResult}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	emptyVerificationResult := &contracts.VerificationResult{
		StableID:             "verify-empty",
		InputDossierID:       "dossier-empty",
		VerifiedAt:           parseTime("2024-01-15T12:00:00Z"),
		VerificationStatuses: map[string]contracts.VerificationStatus{},
		ClaimDetails:         map[string]contracts.ClaimVerificationDetails{},
		QualityScore:         0.0,
	}

	// Create minimal valid artifact (needs body or sections for Article type)
	emptyArtifact := &contracts.EditorialArtifact{
		StableID:        "artifact-empty",
		ArtifactType:    contracts.ArtifactTypeArticle,
		Title:           "Empty Report",
		Body:            "No content available.",
		Sections:        []contracts.Section{},
		ClaimReferences: map[string]contracts.ClaimUsage{},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "verify-empty",
			GeneratedAt:         parseTime("2024-01-15T12:00:00Z"),
		},
	}

	result, err := checker.Check(context.Background(), emptyArtifact, emptyVerificationResult)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, contracts.FactualCheckStatusPass, result.OverallStatus)
}

func TestCheck_ClaimIDValidation(t *testing.T) {
	resultJSON := `{
		"stable_id": "checker-validation-001",
		"input_artifact_id": "artifact-001",
		"checked_at": "2024-01-15T12:00:00Z",
		"claim_ids": ["claim-001", "claim-002"],
		"claim_verification_status": {
			"claim-001": "supported",
			"claim-002": "supported"
		},
		"unsupported_statements": [],
		"altered_details": [],
		"factual_findings": [],
		"overall_status": "pass",
		"confidence_level": "high",
		"summary": "Claims validated.",
		"recommendations": []
	}`

	checker, err := New(&fakeLLMClient{response: resultJSON}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createCleanArtifact()

	checkResult, err := checker.Check(context.Background(), artifact, verificationResult)
	assert.NoError(t, err)

	// Verify claim IDs are properly extracted
	assert.Contains(t, checkResult.ClaimIDs, "claim-001")
	assert.Contains(t, checkResult.ClaimIDs, "claim-002")
	assert.Contains(t, checkResult.ClaimIDs, "claim-003")
	assert.Contains(t, checkResult.ClaimIDs, "claim-004")
}

// ============================================================================
// LLM Failure Handling
// ============================================================================

func TestCheck_LLMContextCancelled(t *testing.T) {
	checker, err := New(&fakeLLMClient{err: llm.ContextCanceled("request cancelled")}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createCleanArtifact()

	_, err = checker.Check(context.Background(), artifact, verificationResult)
	assert.Error(t, err)
	// Error message should contain context cancellation details
	assert.Contains(t, err.Error(), "ContextCanceled")
	assert.Contains(t, err.Error(), "cancelled")
}

func TestCheck_LLMRateLimited(t *testing.T) {
	checker, err := New(&fakeLLMClient{err: llm.RateLimited("rate limit exceeded")}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createCleanArtifact()

	_, err = checker.Check(context.Background(), artifact, verificationResult)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "rate limit")
}

// ============================================================================
// Result Validation Tests
// ============================================================================

func TestFactualCheckResultValidation(t *testing.T) {
	result := &contracts.FactualCheckResult{
		StableID:        "checker-001",
		InputArtifactID: "artifact-001",
		CheckedAt:       parseTime("2024-01-15T12:00:00Z"),
		ClaimIDs:        []string{"claim-001", "claim-002"},
		ClaimVerificationStatus: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusSupported,
		},
		OverallStatus:   contracts.FactualCheckStatusPass,
		ConfidenceLevel: contracts.ConfidenceHigh,
	}

	err := result.Validate()
	assert.NoError(t, err)
}

func TestFactualCheckResultValidation_MismatchedClaimCounts(t *testing.T) {
	result := &contracts.FactualCheckResult{
		StableID:        "checker-001",
		InputArtifactID: "artifact-001",
		CheckedAt:       parseTime("2024-01-15T12:00:00Z"),
		ClaimIDs:        []string{"claim-001", "claim-002", "claim-003"},
		ClaimVerificationStatus: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusSupported,
		},
		OverallStatus:   contracts.FactualCheckStatusPass,
		ConfidenceLevel: contracts.ConfidenceHigh,
	}

	err := result.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "claim_ids")
	assert.Contains(t, err.Error(), "claim_verification_status")
	assert.Contains(t, err.Error(), "different lengths")
}

// ============================================================================
// Helper Function Tests
// ============================================================================

func TestExtractSupportedClaims(t *testing.T) {
	verificationResult := createTestVerificationResult()

	supported := ExtractSupportedClaims(verificationResult)
	assert.Len(t, supported, 2)
	assert.Contains(t, supported, "claim-001")
	assert.Contains(t, supported, "claim-002")
}

func TestExtractClaimIDsFromArtifact(t *testing.T) {
	artifact := createCleanArtifact()

	claimIDs := ExtractClaimIDsFromArtifact(artifact)
	assert.Len(t, claimIDs, 2)
	assert.Contains(t, claimIDs, "claim-001")
	assert.Contains(t, claimIDs, "claim-002")
}

func TestAreClaimIDsConsistent(t *testing.T) {
	verificationResult := createTestVerificationResult()
	artifact := createCleanArtifact()

	assert.True(t, AreClaimIDsConsistent(artifact, verificationResult))

	unknownArtifact := createArtifactWithUnknownClaim()
	assert.False(t, AreClaimIDsConsistent(unknownArtifact, verificationResult))
}

func TestGetVerifiedClaimStatement(t *testing.T) {
	verificationResult := createTestVerificationResult()

	statement, exists := GetVerifiedClaimStatement(verificationResult, "claim-001")
	assert.True(t, exists)
	assert.Equal(t, "The program processed 150 records", statement)

	_, exists = GetVerifiedClaimStatement(verificationResult, "claim-999")
	assert.False(t, exists)
}

func TestGetVerifiedClaimStatus(t *testing.T) {
	verificationResult := createTestVerificationResult()

	status, exists := GetVerifiedClaimStatus(verificationResult, "claim-001")
	assert.True(t, exists)
	assert.Equal(t, contracts.VerificationStatusSupported, status)

	status, exists = GetVerifiedClaimStatus(verificationResult, "claim-003")
	assert.True(t, exists)
	assert.Equal(t, contracts.VerificationStatusUncertain, status)
}

// ============================================================================
// Utility Function Tests
// ============================================================================

func TestNumberComparison(t *testing.T) {
	// Test exact match
	result := CompareNumbers("150", "150")
	assert.True(t, result.Match)
	assert.Equal(t, "150", result.Original)
	assert.Equal(t, "150", result.Altered)

	// Test mismatch
	result = CompareNumbers("150", "1500")
	assert.False(t, result.Match)

	// Test format normalization
	result = CompareNumbers("150%", "150%")
	assert.True(t, result.Match)

	result = CompareNumbers("23ms", "23ms")
	assert.True(t, result.Match)
}

func TestExtractNumbersFromText(t *testing.T) {
	text := "Processed 150 records with 23ms latency"
	numbers := ExtractNumbersFromText(text)
	assert.Contains(t, numbers, "150")
	assert.Contains(t, numbers, "23ms")
}

func TestExtractDatesFromText(t *testing.T) {
	text := "Report from 2024-01-15 and January 10, 2024"
	dates := ExtractDatesFromText(text)
	assert.Contains(t, dates, "2024-01-15")
	assert.Contains(t, dates, "January 10, 2024")
}

func TestExtractNamesFromText(t *testing.T) {
	text := "John Smith worked at Acme Corp with Jane Doe"
	names := ExtractNamesFromText(text)
	assert.Contains(t, names, "John Smith")
	assert.Contains(t, names, "Acme Corp")
	assert.Contains(t, names, "Jane Doe")
}

func TestUniqueClaimIDs(t *testing.T) {
	ids := []string{"claim-001", "claim-002", "claim-001", "claim-003", "claim-002"}
	unique := UniqueClaimIDs(ids)
	assert.Len(t, unique, 3)
	assert.Contains(t, unique, "claim-001")
	assert.Contains(t, unique, "claim-002")
	assert.Contains(t, unique, "claim-003")
}

func TestSortClaimIDs(t *testing.T) {
	ids := []string{"claim-003", "claim-001", "claim-002"}
	SortClaimIDs(ids)
	assert.Equal(t, []string{"claim-001", "claim-002", "claim-003"}, ids)
}

func TestClaimsPresentInBoth(t *testing.T) {
	a := []string{"claim-001", "claim-002", "claim-003"}
	b := []string{"claim-002", "claim-003", "claim-004"}

	both := ClaimsPresentInBoth(a, b)
	assert.Len(t, both, 2)
	assert.Contains(t, both, "claim-002")
	assert.Contains(t, both, "claim-003")
}

func TestCalculateVerificationCoverage(t *testing.T) {
	verificationResult := createTestVerificationResult()

	// All claims used
	fullArtifact := createCleanArtifact()
	fullArtifact.ClaimReferences["claim-003"] = contracts.ClaimUsage{
		ClaimID:     "claim-003",
		UsageType:   contracts.ClaimUsageSupporting,
		Paraphrased: true,
	}
	coverage := CalculateVerificationCoverage(fullArtifact, verificationResult)
	assert.Equal(t, 75.0, coverage) // 3 out of 4 claims

	// No claims used
	emptyArtifact := &contracts.EditorialArtifact{
		StableID:        "empty",
		ArtifactType:    contracts.ArtifactTypeArticle,
		ClaimReferences: map[string]contracts.ClaimUsage{},
	}
	coverage = CalculateVerificationCoverage(emptyArtifact, verificationResult)
	assert.Equal(t, 0.0, coverage)
}

func TestContentMerger(t *testing.T) {
	sections := []contracts.Section{
		{Order: 0, Title: "Intro", Body: "Introduction text"},
		{Order: 1, Title: "Body", Body: "Main body text"},
	}
	posts := []contracts.Post{
		{Order: 0, Body: "First post"},
		{Order: 1, Body: "Second post"},
	}

	content := ContentMerger(sections, posts)
	assert.Contains(t, content, "Introduction text")
	assert.Contains(t, content, "Main body text")
	assert.Contains(t, content, "First post")
	assert.Contains(t, content, "Second post")
}

// ============================================================================
// BuildUserPrompt Tests
// ============================================================================

func TestBuildUserPrompt_CreatesExpectedStructure(t *testing.T) {
	verificationResult := createTestVerificationResult()
	artifact := createCleanArtifact()

	prompt := buildUserPrompt(artifact, verificationResult, CheckerConfig{
		Temperature: TestTemp,
		Language:    "en",
	})

	// Verify prompt contains expected sections
	assert.Contains(t, prompt, "VERIFICATION RESULTS SUMMARY")
	assert.Contains(t, prompt, "VERIFIED CLAIMS")
	assert.Contains(t, prompt, "EDITORIAL ARTIFACT CONTENT")
	assert.Contains(t, prompt, "COMPARISON TASK")

	// Verify specific claim statements are included
	assert.Contains(t, prompt, "The program processed 150 records")
	assert.Contains(t, prompt, "Latency averaged 23ms")

	// Verify artifact content is included
	assert.Contains(t, prompt, "artifact-001")
	assert.Contains(t, prompt, "Performance Report")
}

// ============================================================================
// CheckResult Helper Function Tests
// ============================================================================

func TestHasUnsupportedStatements(t *testing.T) {
	result := &contracts.FactualCheckResult{
		UnsupportedStatements: []contracts.UnsupportedStatement{
			{Statement: "test statement"},
		},
	}
	assert.True(t, HasUnsupportedStatements(result))

	result.UnsupportedStatements = []contracts.UnsupportedStatement{}
	assert.False(t, HasUnsupportedStatements(result))
}

func TestHasAlteredDetails(t *testing.T) {
	result := &contracts.FactualCheckResult{
		AlteredDetails: []contracts.AlteredDetail{
			{OriginalValue: "100", AlteredImageValue: "200"},
		},
	}
	assert.True(t, HasAlteredDetails(result))

	result.AlteredDetails = []contracts.AlteredDetail{}
	assert.False(t, HasAlteredDetails(result))
}

func TestGetCriticalFindings(t *testing.T) {
	result := &contracts.FactualCheckResult{
		FactualFindings: []contracts.FactualFinding{
			{FindingID: "1", Statement: "low", Severity: contracts.WarningSeverityLow},
			{FindingID: "2", Statement: "medium", Severity: contracts.WarningSeverityMedium},
			{FindingID: "3", Statement: "high", Severity: contracts.WarningSeverityHigh},
			{FindingID: "4", Statement: "critical", Severity: contracts.WarningSeverityCritical},
		},
	}

	critical := GetCriticalFindings(result)
	assert.Len(t, critical, 1)
	assert.Equal(t, "4", critical[0].FindingID)
}

func TestGetCriticalAlteredDetails(t *testing.T) {
	result := &contracts.FactualCheckResult{
		AlteredDetails: []contracts.AlteredDetail{
			{OriginalValue: "100", AlteredImageValue: "200", Significance: contracts.AlterationSignificanceTrivial},
			{OriginalValue: "100", AlteredImageValue: "999", Significance: contracts.AlterationSignificanceCritical},
		},
	}

	critical := GetCriticalAlteredDetails(result)
	assert.Len(t, critical, 1)
	assert.Equal(t, "999", critical[0].AlteredImageValue)
}

// ============================================================================
// CheckRequiresLLM Tests
// ============================================================================

func TestCheckRequiresLLM_WithBody(t *testing.T) {
	artifact := &contracts.EditorialArtifact{
		Body: "Some content",
	}
	assert.True(t, CheckRequiresLLM(artifact))
}

func TestCheckRequiresLLM_WithSections(t *testing.T) {
	artifact := &contracts.EditorialArtifact{
		Body: "",
		Sections: []contracts.Section{
			{Order: 0, Title: "Test", Body: "Section content"},
		},
	}
	assert.True(t, CheckRequiresLLM(artifact))
}

func TestCheckRequiresLLM_EmptyArtifact(t *testing.T) {
	artifact := &contracts.EditorialArtifact{
		Body:            "",
		Sections:        []contracts.Section{},
		Posts:           []contracts.Post{},
		ClaimReferences: map[string]contracts.ClaimUsage{},
	}
	assert.False(t, CheckRequiresLLM(artifact))
}

// ============================================================================
// Integration Test
// ============================================================================

func TestEndToEnd_FactualCheck(t *testing.T) {
	// Simulate a complete check scenario
	passResult := `{
		"stable_id": "checker-test-end-to-end",
		"input_artifact_id": "artifact-001",
		"checked_at": "2024-01-15T12:00:00Z",
		"claim_ids": ["claim-001", "claim-002", "claim-003", "claim-004"],
		"claim_verification_status": {
			"claim-001": "supported",
			"claim-002": "supported",
			"claim-003": "uncertain",
			"claim-004": "contradicted"
		},
		"unsupported_statements": [],
		"altered_details": [],
		"factual_findings": [],
		"overall_status": "pass",
		"confidence_level": "high",
		"summary": "Artifact passes all factual checks.",
		"recommendations": []
	}`

	checker, err := New(&fakeLLMClient{response: passResult}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createTestVerificationResult()
	artifact := createCleanArtifact()

	result, err := checker.Check(context.Background(), artifact, verificationResult)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, contracts.FactualCheckStatusPass, result.OverallStatus)

	// Verify JSON serialization works
	jsonData, err := json.Marshal(result)
	assert.NoError(t, err)

	// Verify JSON can be parsed back
	var parsed contracts.FactualCheckResult
	err = json.Unmarshal(jsonData, &parsed)
	assert.NoError(t, err)
	assert.Equal(t, result.StableID, parsed.StableID)
	assert.Equal(t, result.OverallStatus, parsed.OverallStatus)
}

// ============================================================================
// Test Deterministic vs LLM Responsibilities
// ============================================================================

func TestDeterministicChecksRunFirst(t *testing.T) {
	// Verify that deterministic checks run even if LLM would fail
	checker, err := New(&fakeLLMClient{err: llm.Internal("LLM failed")}, TestModel, CheckerConfig{
		Temperature:     TestTemp,
		MaxOutputTokens: TestMaxTokens,
		TimeNow:         func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator:     contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	// Create artifact with deterministic issue (unknown claim)
	artifact := createArtifactWithUnknownClaim()
	verificationResult := createTestVerificationResult()

	result, err := checker.Check(context.Background(), artifact, verificationResult)

	// Should return result with deterministic findings even though LLM failed
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, contracts.FactualCheckStatusFail, result.OverallStatus)
	assert.NotEmpty(t, result.FactualFindings)
}

// ============================================================================
// Test Deterministic Evidence Building
// ============================================================================

func TestBuildDeterministicEvidence(t *testing.T) {
	checker := &FinalFactualChecker{
		config: CheckerConfig{
			TimeNow:     func() time.Time { return parseTime(TestBaseTime) },
			IDGenerator: contracts.NewUUIDGenerator(),
		},
	}

	issue := deterministicIssue{
		claimID:   "claim-999",
		issueType: deterministicIssueUnknownClaim,
		severity:  contracts.WarningSeverityCritical,
		message:   "Claim reference not found",
	}

	evidence := checker.buildDeterministicEvidence(issue)
	assert.Contains(t, evidence, "claim-999")
	assert.Contains(t, evidence, "verification result")
}

// ============================================================================
// Test JSON Parsing with Markdown
// ============================================================================

func TestParseFactualCheckResponse_WithMarkdown(t *testing.T) {
	response := "```json\n{\n  \"stable_id\": \"test\",\n  \"input_artifact_id\": \"artifact-001\",\n  \"checked_at\": \"2024-01-15T12:00:00Z\",\n  \"claim_ids\": [],\n  \"claim_verification_status\": {},\n  \"unsupported_statements\": [],\n  \"altered_details\": [],\n  \"factual_findings\": [],\n  \"overall_status\": \"pass\",\n  \"confidence_level\": \"high\",\n  \"summary\": \"Test\",\n  \"recommendations\": []\n}\n```"

	result, err := parseFactualCheckResponse(response)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "test", result.StableID)
	assert.Equal(t, contracts.FactualCheckStatusPass, result.OverallStatus)
}

// ============================================================================
// Test Prompt Loading
// ============================================================================

func TestNewChecker_LoadsPromptFromFile(t *testing.T) {
	client := &fakeLLMClient{}
	config := CheckerConfig{
		TimeNow:     func() time.Time { return parseTime(TestBaseTime) },
		IDGenerator: contracts.NewUUIDGenerator(),
	}

	checker, err := New(client, TestModel, config)
	assert.NoError(t, err)
	assert.NotNil(t, checker)
	assert.NotEmpty(t, checker.prompt)
	assert.Contains(t, checker.prompt, "Final Factual Checker")
}
