// Package writer tests the Writer stage.
//
// Tests cover:
// - Valid long-form artifact generation
// - Artifact using only supported claims
// - Exclusion of contradicted claims
// - Uncertainty wording and references
// - Unknown claim references detection
// - Malformed JSON handling
// - Writer LLM failure cases
// - Validation of claim references
// - Warnings for uncertain/contradicted claims
package writer

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
// Helper Functions
// ============================================================================

// createBasicVerificationResult creates a verification result with various claim statuses.
func createBasicVerificationResult() *contracts.VerificationResult {
	baseTime := parseTime("2024-01-15T12:00:00Z")

	return &contracts.VerificationResult{
		StableID:       "verify-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     baseTime,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-001": contracts.VerificationStatusSupported,
			"claim-002": contracts.VerificationStatusSupported,
			"claim-003": contracts.VerificationStatusUncertain,
			"claim-004": contracts.VerificationStatusContradicted,
			"claim-005": contracts.VerificationStatusInsufficientEvidence,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "The program processed 150 records",
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh,
			},
			"claim-002": {
				ClaimID:            "claim-002",
				Statement:          "Latency averaged 23ms",
				VerificationStatus: contracts.VerificationStatusSupported,
				ConfidenceLevel:    contracts.ConfidenceHigh,
			},
			"claim-003": {
				ClaimID:            "claim-003",
				Statement:          "Users reported high satisfaction",
				VerificationStatus: contracts.VerificationStatusUncertain,
				ConfidenceLevel:    contracts.ConfidenceMedium,
			},
			"claim-004": {
				ClaimID:            "claim-004",
				Statement:          "The system achieved 99.9% uptime",
				VerificationStatus: contracts.VerificationStatusContradicted,
				ConfidenceLevel:    contracts.ConfidenceMedium,
			},
			"claim-005": {
				ClaimID:            "claim-005",
				Statement:          "Cost reduction was 25%",
				VerificationStatus: contracts.VerificationStatusInsufficientEvidence,
				ConfidenceLevel:    contracts.ConfidenceLow,
			},
		},
		QualityScore: 60.0,
	}
}

// createValidArtifact creates a valid EditorialArtifact for testing.
func createValidArtifact() *contracts.EditorialArtifact {
	baseTime := parseTime("2024-01-15T12:00:00Z")

	return &contracts.EditorialArtifact{
		StableID:     "writer-dossier-001-article",
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
			GeneratedAt:         baseTime,
			WriterParameters: map[string]string{
				"language":      "en",
				"artifact_type": "article",
			},
		},
	}
}

// ============================================================================
// Writer Tests
// ============================================================================

func TestNewWriter_WithNilClient(t *testing.T) {
	_, err := New(nil, "test-model", WriterConfig{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "llm.Client must not be nil")
}

func TestNewWriter_Success(t *testing.T) {
	client := &fakeLLMClient{}
	config := WriterConfig{
		Temperature:     0.3,
		MaxOutputTokens: 10000,
		Language:        "en",
		TimeNow:         func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator:     contracts.NewUUIDGenerator(),
	}

	writer, err := New(client, "test-model", config)
	assert.NoError(t, err)
	assert.NotNil(t, writer)
	assert.Equal(t, "test-model", writer.model)
	assert.Equal(t, 0.3, writer.config.Temperature)
}

func TestGenerate_WithNilVerificationResult(t *testing.T) {
	client := &fakeLLMClient{}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	_, err = writer.Generate(context.Background(), nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "verificationResult cannot be nil")
}

func TestGenerate_ValidArtifact(t *testing.T) {
	// Create a valid JSON response from LLM
	validResponse := `{
		"stable_id": "writer-dossier-001-article",
		"artifact_type": "article",
		"title": "Performance Report",
		"body": "The program processed 150 records with 23ms average latency.",
		"sections": [
			{
				"order": 0,
				"title": "Key Findings",
				"body": "Core metrics were measured successfully.",
				"claim_ids": ["claim-001", "claim-002"]
			}
		],
		"claim_references": {
			"claim-001": {
				"claim_id": "claim-001",
				"usage_type": "core",
				"paraphrased": true
			},
			"claim-002": {
				"claim_id": "claim-002",
				"usage_type": "supporting",
				"paraphrased": true
			}
		},
		"generation_metadata": {
			"input_verification_id": "verify-001",
			"generated_at": "2024-01-15T12:00:00Z",
			"writer_parameters": {
				"language": "en",
				"artifact_type": "article"
			}
		}
	}`

	client := &fakeLLMClient{response: validResponse}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{
		"language":      "en",
		"tone":          "neutral",
		"artifact_type": "article",
	}

	artifact, err := writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.NoError(t, err)
	assert.NotNil(t, artifact)
	assert.Equal(t, "writer-dossier-001-article", artifact.StableID)
	assert.Equal(t, contracts.ArtifactTypeArticle, artifact.ArtifactType)
	assert.Equal(t, "Performance Report", artifact.Title)
}

// ============================================================================
// Valid Long-Form Artifact Tests
// ============================================================================

func TestGenerate_ValidLongFormArticle(t *testing.T) {
	// Create a response with a full long-form article
	longFormResponse := `{
		"stable_id": "writer-dossier-001-article",
		"artifact_type": "article",
		"title": "System Performance Analysis",
		"subtitle": "A comprehensive review of metrics and user feedback",
		"body": "This report analyzes the performance of the system based on verified data.\n\nKey metrics show the system processed 150 records with an average latency of 23ms. User satisfaction was reported as high, though this requires further verification.\n\nThe analysis covers core performance indicators, user feedback, and system reliability metrics.",
		"sections": [
			{
				"order": 0,
				"title": "Introduction",
				"body": "Overview of the analysis scope and methodology.",
				"claim_ids": []
			},
			{
				"order": 1,
				"title": "Performance Metrics",
				"body": "Record processing reached 150 records. Latency averaged 23ms across all measurements.",
				"claim_ids": ["claim-001", "claim-002"]
			},
			{
				"order": 2,
				"title": "User Feedback",
				"body": "Sources suggest users reported high satisfaction, though verification is ongoing.",
				"claim_ids": ["claim-003"]
			},
			{
				"order": 3,
				"title": "Conclusion",
				"body": "Overall performance meets expectations.",
				"claim_ids": []
			}
		],
		"claim_references": {
			"claim-001": {
				"claim_id": "claim-001",
				"usage_type": "core",
				"usage_context": "Primary performance metric",
				"locations": [
					{"location_type": "section", "index": 1}
				],
				"paraphrased": true
			},
			"claim-002": {
				"claim_id": "claim-002",
				"usage_type": "core",
				"usage_context": "Primary performance metric",
				"locations": [
					{"location_type": "section", "index": 1}
				],
				"paraphrased": true
			},
			"claim-003": {
				"claim_id": "claim-003",
				"usage_type": "background",
				"usage_context": "User feedback requiring verification",
				"locations": [
					{"location_type": "section", "index": 2}
				],
				"paraphrased": true
			}
		},
		"generation_metadata": {
			"input_verification_id": "verify-001",
			"generated_at": "2024-01-15T12:00:00Z",
			"writer_parameters": {
				"language": "en",
				"artifact_type": "article"
			}
		},
		"warnings": [
			{
				"warning_type": "uncertain_facts",
				"severity": "medium",
				"message": "Core claim claim-003 has uncertain status - verify uncertainty language is used",
				"related_claim_ids": ["claim-003"]
			}
		]
	}`

	client := &fakeLLMClient{response: longFormResponse}
	writer, err := New(client, "test-model", WriterConfig{
		Temperature: 0.3,
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{
		"language":      "en",
		"tone":          "neutral",
		"artifact_type": "article",
	}

	artifact, err := writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.NoError(t, err)
	assert.NotNil(t, artifact)
	assert.Len(t, artifact.Sections, 4)
	assert.Len(t, artifact.ClaimReferences, 3)
	assert.NotEmpty(t, artifact.Warnings)
	assert.Len(t, artifact.Warnings, 1)

	// Verify warnings about uncertain claims
	uncertainWarningFound := false
	for _, w := range artifact.Warnings {
		if w.WarningType == contracts.WarningTypeUncertainFacts {
			uncertainWarningFound = true
			assert.Contains(t, w.Message, "claim-003")
			assert.Equal(t, contracts.WarningSeverityMedium, w.Severity)
		}
	}
	assert.True(t, uncertainWarningFound)
}

// ============================================================================
// Artifact Using Only Supported Claims Tests
// ============================================================================

func TestGenerate_UsingOnlySupportedClaims(t *testing.T) {
	response := `{
		"stable_id": "writer-dossier-001-article",
		"artifact_type": "article",
		"title": "Verified Facts Report",
		"body": "The system processed 150 records and achieved 23ms latency.",
		"sections": [
			{
				"order": 0,
				"title": "Findings",
				"body": "All metrics are verified.",
				"claim_ids": ["claim-001", "claim-002"]
			}
		],
		"claim_references": {
			"claim-001": {
				"claim_id": "claim-001",
				"usage_type": "core",
				"paraphrased": true
			},
			"claim-002": {
				"claim_id": "claim-002",
				"usage_type": "core",
				"paraphrased": true
			}
		},
		"generation_metadata": {
			"input_verification_id": "verify-001",
			"generated_at": "2024-01-15T12:00:00Z"
		}
	}`

	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{}

	artifact, err := writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.NoError(t, err)

	// Verify only supported claims are referenced
	artifactClaimIDs := artifact.ExtractClaimIDs()
	assert.ElementsMatch(t, []string{"claim-001", "claim-002"}, artifactClaimIDs)

	// Verify no contradicted or uncertain claims are used
	for claimID := range artifact.ClaimReferences {
		status := verificationResult.VerificationStatuses[claimID]
		assert.Equal(t, contracts.VerificationStatusSupported, status,
			"Claim %s should only use supported claims", claimID)
	}
}

// ============================================================================
// Exclusion of Contradicted Claims Tests
// ============================================================================

func TestGenerate_ContradictedClaimsNotUsedAsFacts(t *testing.T) {
	// The LLM might try to use contradicted claims, we need to verify warnings
	response := `{
		"stable_id": "writer-dossier-001-article",
		"artifact_type": "article",
		"title": "Report with Contradiction",
		"body": "While some sources claim 99.9% uptime, other evidence shows otherwise.",
		"sections": [],
		"claim_references": {
			"claim-001": {
				"claim_id": "claim-001",
				"usage_type": "core",
				"paraphrased": true
			},
			"claim-004": {
				"claim_id": "claim-004",
				"usage_type": "counterpoint",
				"paraphrased": false,
				"usage_context": "Presented as disputed claim"
			}
		},
		"generation_metadata": {
			"input_verification_id": "verify-001",
			"generated_at": "2024-01-15T12:00:00Z"
		}
	}`

	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{}

	artifact, err := writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.NoError(t, err)

	// Verify a warning was added about the contradicted claim
	assert.NotEmpty(t, artifact.Warnings)

	contradictedWarningFound := false
	for _, w := range artifact.Warnings {
		if w.WarningType == contracts.WarningTypeUncertainFacts &&
			strings.Contains(w.Message, "claim-004") {
			contradictedWarningFound = true
			assert.Equal(t, contracts.WarningSeverityHigh, w.Severity)
		}
	}
	assert.True(t, contradictedWarningFound, "Warning for contradicted claim should be added")
}

// ============================================================================
// Uncertainty Wording/Reference Tests
// ============================================================================

func TestGenerate_UncertainClaimsWithAppropriateLanguage(t *testing.T) {
	response := `{
		"stable_id": "writer-dossier-001-article",
		"artifact_type": "article",
		"title": "Uncertain Facts Report",
		"body": "Sources suggest users reported high satisfaction, though verification is pending.",
		"sections": [
			{
				"order": 0,
				"title": "Findings",
				"body": "User satisfaction - according to reports - is high.",
				"claim_ids": ["claim-003"]
			}
		],
		"claim_references": {
			"claim-003": {
				"claim_id": "claim-003",
				"usage_type": "core",
				"paraphrased": true,
				"usage_context": "Uncertain claim requiring verification"
			}
		},
		"generation_metadata": {
			"input_verification_id": "verify-001",
			"generated_at": "2024-01-15T12:00:00Z"
		},
		"warnings": [
			{
				"warning_type": "uncertain_facts",
				"severity": "medium",
				"message": "Core claim claim-003 has uncertain status",
				"related_claim_ids": ["claim-003"]
			}
		]
	}`

	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{}

	artifact, err := writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.NoError(t, err)

	// Verify warning about uncertain claim
	assert.NotEmpty(t, artifact.Warnings)
	uncertainWarning := artifact.Warnings[0]
	assert.Equal(t, contracts.WarningTypeUncertainFacts, uncertainWarning.WarningType)
	assert.Contains(t, uncertainWarning.Message, "claim-003")
}

// ============================================================================
// Unknown Claim References Tests
// ============================================================================

func TestGenerate_UnknownClaimReferences(t *testing.T) {
	response := `{
		"stable_id": "writer-dossier-001-article",
		"artifact_type": "article",
		"title": "Report with Unknown Claims",
		"body": "Some facts were processed.",
		"sections": [
			{
				"order": 0,
				"title": "Findings",
				"body": "Content.",
				"claim_ids": ["claim-999", "claim-001"]
			}
		],
		"claim_references": {
			"claim-999": {
				"claim_id": "claim-999",
				"usage_type": "core",
				"paraphrased": true
			}
		},
		"generation_metadata": {
			"input_verification_id": "verify-001",
			"generated_at": "2024-01-15T12:00:00Z"
		}
	}`

	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{}

	_, err = writer.Generate(context.Background(), verificationResult, profileInstructions)

	// Should fail validation because claim-999 doesn't exist in verification result
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "claim reference \"claim-999\" not found")
}

// ============================================================================
// Malformed JSON Tests
// ============================================================================

func TestGenerate_MalformedJSON(t *testing.T) {
	invalidJSON := `{this is not valid json`

	client := &fakeLLMClient{response: invalidJSON}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{}

	_, err = writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse JSON")
	assert.Contains(t, err.Error(), "invalid character")
}

func TestGenerate_JSONWithMissingRequiredFields(t *testing.T) {
	response := `{
		"artifact_type": "article",
		"body": "No stable_id provided"
	}`

	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{}

	_, err = writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing required field: stable_id")
}

// ============================================================================
// Writer LLM Failure Tests
// ============================================================================

func TestGenerate_LLMContextCancelled(t *testing.T) {
	client := &fakeLLMClient{
		err: llm.ContextCanceled("request cancelled"),
	}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err = writer.Generate(ctx, verificationResult, profileInstructions)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "llm completion failed")
}

func TestGenerate_LLMInternalError(t *testing.T) {
	client := &fakeLLMClient{
		err: llm.Internal("provider error"),
	}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{}

	_, err = writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "llm completion failed")
	assert.Contains(t, err.Error(), "provider error")
}

func TestGenerate_EmptyLLMResponse(t *testing.T) {
	client := &fakeLLMClient{response: ""}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{}

	_, err = writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty model response")
}

func TestGenerate_WhitespaceOnlyResponse(t *testing.T) {
	client := &fakeLLMClient{response: "   \n\n  "}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{}

	_, err = writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "model response contained only whitespace")
}

// ============================================================================
// Thread and Hybrid Artifact Tests
// ============================================================================

func TestGenerate_ThreadArtifact(t *testing.T) {
	response := `{
		"stable_id": "writer-dossier-001-thread",
		"artifact_type": "thread",
		"title": "Thread Summary",
		"posts": [
			{
				"order": 0,
				"body": "150 records processed.",
				"claim_ids": ["claim-001"]
			},
			{
				"order": 1,
				"body": "23ms average latency.",
				"claim_ids": ["claim-002"]
			}
		],
		"claim_references": {
			"claim-001": {
				"claim_id": "claim-001",
				"usage_type": "core",
				"paraphrased": true,
				"locations": [
					{"location_type": "post", "index": 0}
				]
			},
			"claim-002": {
				"claim_id": "claim-002",
				"usage_type": "core",
				"paraphrased": true,
				"locations": [
					{"location_type": "post", "index": 1}
				]
			}
		},
		"generation_metadata": {
			"input_verification_id": "verify-001",
			"generated_at": "2024-01-15T12:00:00Z",
			"writer_parameters": {
				"artifact_type": "thread"
			}
		}
	}`

	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		ArtifactType: "thread",
		TimeNow:      func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator:  contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{
		"artifact_type": "thread",
	}

	artifact, err := writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.NoError(t, err)
	assert.Equal(t, contracts.ArtifactTypeThread, artifact.ArtifactType)
	assert.Len(t, artifact.Posts, 2)
	assert.Len(t, artifact.ClaimReferences, 2)
}

func TestGenerate_HybridArtifact(t *testing.T) {
	response := `{
		"stable_id": "writer-dossier-001-hybrid",
		"artifact_type": "hybrid",
		"title": "Hybrid Report",
		"body": "Summary article content.",
		"sections": [
			{
				"order": 0,
				"title": "Findings",
				"body": "Core content.",
				"claim_ids": ["claim-001"]
			}
		],
		"posts": [
			{
				"order": 0,
				"body": "Thread post.",
				"claim_ids": ["claim-002"]
			}
		],
		"claim_references": {
			"claim-001": {
				"claim_id": "claim-001",
				"usage_type": "core",
				"paraphrased": true
			},
			"claim-002": {
				"claim_id": "claim-002",
				"usage_type": "core",
				"paraphrased": true
			}
		},
		"generation_metadata": {
			"input_verification_id": "verify-001",
			"generated_at": "2024-01-15T12:00:00Z"
		}
	}`

	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		ArtifactType: "hybrid",
		TimeNow:      func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator:  contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{
		"artifact_type": "hybrid",
	}

	artifact, err := writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.NoError(t, err)
	assert.Equal(t, contracts.ArtifactTypeHybrid, artifact.ArtifactType)
	assert.Len(t, artifact.Sections, 1)
	assert.Len(t, artifact.Posts, 1)
}

// ============================================================================
// Validation Tests
// ============================================================================

func TestValidateClaimReferences_NoValidationErrors(t *testing.T) {
	client := &fakeLLMClient{response: createValidArtifactJSON()}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{}

	artifact, err := writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.NoError(t, err)

	// Verify artifact is valid
	err = artifact.Validate()
	assert.NoError(t, err)
}

func TestGenerate_ProfileInstructionsOverride(t *testing.T) {
	response := createValidArtifactJSON()
	client := &fakeLLMClient{response: response}

	// Test with custom prompt override
	customPrompt := "You are a custom writer. Return JSON only."
	writer, err := New(client, "test-model", WriterConfig{
		PromptOverride: customPrompt,
		TimeNow:        func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator:    contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()
	profileInstructions := map[string]string{
		"language":      "es",
		"tone":          "conversational",
		"artifact_type": "article",
	}

	artifact, err := writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.NoError(t, err)
	assert.NotNil(t, artifact)
}

// ============================================================================
// Helper Functions for Tests
// ============================================================================

func createValidArtifactJSON() string {
	return `{
		"stable_id": "writer-dossier-001-article",
		"artifact_type": "article",
		"title": "Performance Report",
		"body": "The program processed 150 records with 23ms average latency.",
		"sections": [
			{
				"order": 0,
				"title": "Key Findings",
				"body": "Core metrics were measured successfully.",
				"claim_ids": ["claim-001", "claim-002"]
			}
		],
		"claim_references": {
			"claim-001": {
				"claim_id": "claim-001",
				"usage_type": "core",
				"paraphrased": true,
				"locations": [
					{"location_type": "section", "index": 0}
				]
			},
			"claim-002": {
				"claim_id": "claim-002",
				"usage_type": "supporting",
				"paraphrased": true,
				"locations": [
					{"location_type": "section", "index": 0}
				]
			}
		},
		"generation_metadata": {
			"input_verification_id": "verify-001",
			"generated_at": "2024-01-15T12:00:00Z"
		}
	}`
}

// ============================================================================
// Helper Functions Tests
// ============================================================================

func TestGetSupportedClaims(t *testing.T) {
	verificationResult := createBasicVerificationResult()

	supported := GetSupportedClaims(verificationResult)
	assert.Len(t, supported, 2)
	assert.Contains(t, supported, "claim-001")
	assert.Contains(t, supported, "claim-002")
}

func TestGetContradictedClaims(t *testing.T) {
	verificationResult := createBasicVerificationResult()

	contradicted := GetContradictedClaims(verificationResult)
	assert.Len(t, contradicted, 1)
	assert.Contains(t, contradicted, "claim-004")
}

func TestGetUncertainClaims(t *testing.T) {
	verificationResult := createBasicVerificationResult()

	uncertain := GetUncertainClaims(verificationResult)
	assert.Len(t, uncertain, 1)
	assert.Contains(t, uncertain, "claim-003")
}

func TestGetInsufficientEvidenceClaims(t *testing.T) {
	verificationResult := createBasicVerificationResult()

	insufficient := GetInsufficientEvidenceClaims(verificationResult)
	assert.Len(t, insufficient, 1)
	assert.Contains(t, insufficient, "claim-005")
}

func TestValidateClaimID(t *testing.T) {
	assert.True(t, ValidateClaimID("claim-001"))
	assert.True(t, ValidateClaimID("claim-123"))
	assert.False(t, ValidateClaimID("invalid"))
	assert.False(t, ValidateClaimID("123"))
	assert.False(t, ValidateClaimID("claim-"))
}

// ============================================================================
// Integration Tests
// ============================================================================

func TestWriter_ValidationChain(t *testing.T) {
	// Full chain: Create valid response, verify it can be parsed and validated
	response := `{
		"stable_id": "integration-test-001",
		"artifact_type": "article",
		"title": "Integration Test",
		"body": "Test content.",
		"sections": [],
		"claim_references": {},
		"generation_metadata": {
			"input_verification_id": "verify-001",
			"generated_at": "2024-01-15T12:00:00Z"
		}
	}`

	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	// Create a minimal verification result
	verificationResult := &contracts.VerificationResult{
		StableID:             "verify-001",
		InputDossierID:       "dossier-001",
		VerifiedAt:           parseTime("2024-01-15T12:00:00Z"),
		VerificationStatuses: map[string]contracts.VerificationStatus{},
		ClaimDetails:         map[string]contracts.ClaimVerificationDetails{},
		QualityScore:         0.0,
	}

	artifact, err := writer.Generate(context.Background(), verificationResult, map[string]string{})
	assert.NoError(t, err)
	assert.NotNil(t, artifact)

	// Validate the artifact structure
	err = artifact.Validate()
	assert.NoError(t, err)
}

// ============================================================================
// Edge Case Tests
// ============================================================================

func TestGenerate_InvalidArtifactType(t *testing.T) {
	response := `{
		"stable_id": "test",
		"artifact_type": "invalid",
		"body": "Test",
		"generation_metadata": {
			"input_verification_id": "verify-001",
			"generated_at": "2024-01-15T12:00:00Z"
		}
	}`

	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()

	_, err = writer.Generate(context.Background(), verificationResult, map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid artifact_type")
}

func TestGenerate_MissingGenerationMetadata(t *testing.T) {
	response := `{
		"stable_id": "test",
		"artifact_type": "article",
		"body": "Test"
	}`

	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()

	_, err = writer.Generate(context.Background(), verificationResult, map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "generated_at is zero value")
}

func TestGenerate_MalformedJSONWithMarkdown(t *testing.T) {
	response := "```json\n{invalid json}\n```"

	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()

	_, err = writer.Generate(context.Background(), verificationResult, map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse JSON")
}

func TestBuildUserPrompt(t *testing.T) {
	// Verify the buildUserPrompt function creates appropriate content
	verificationResult := createBasicVerificationResult()

	config := WriterConfig{
		Temperature:  0.3,
		Language:     "en",
		ArtifactType: "article",
	}

	prompt := buildUserPrompt(verificationResult, map[string]string{}, config)

	// Verify prompt contains expected sections
	assert.Contains(t, prompt, "PROFILE/INSTRUCTIONS")
	assert.Contains(t, prompt, "VERIFICATION RESULTS")
	assert.Contains(t, prompt, "SUPPORTED CLAIMS")
	assert.Contains(t, prompt, "UNCERTAIN CLAIMS")
	assert.Contains(t, prompt, "CONTRADICTED CLAIMS")
	assert.Contains(t, prompt, "INSUFFICIENT EVIDENCE CLAIMS")

	// Verify specific claim statements are included
	assert.Contains(t, prompt, "The program processed 150 records")
	assert.Contains(t, prompt, "Latency averaged 23ms")
	assert.Contains(t, prompt, "Users reported high satisfaction")
}

// ============================================================================
// Test Rate Limited Errors
// ============================================================================

func TestGenerate_LLMRateLimited(t *testing.T) {
	client := &fakeLLMClient{
		err: llm.RateLimited("rate limit exceeded"),
	}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()

	_, err = writer.Generate(context.Background(), verificationResult, map[string]string{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "llm completion failed")
	assert.Contains(t, err.Error(), "rate limit")
}

// ============================================================================
// Test Empty Verification Result
// ============================================================================

func TestGenerate_EmptyVerificationResult(t *testing.T) {
	response := `{
		"stable_id": "empty-verify-001",
		"artifact_type": "article",
		"title": "Empty Report",
		"body": "No claims verified.",
		"claim_references": {},
		"generation_metadata": {
			"input_verification_id": "verify-empty",
			"generated_at": "2024-01-15T12:00:00Z"
		}
	}`

	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	// Empty verification result (no claims)
	verificationResult := &contracts.VerificationResult{
		StableID:             "verify-empty",
		InputDossierID:       "dossier-empty",
		VerifiedAt:           parseTime("2024-01-15T12:00:00Z"),
		VerificationStatuses: map[string]contracts.VerificationStatus{},
		ClaimDetails:         map[string]contracts.ClaimVerificationDetails{},
		QualityScore:         0.0,
	}

	artifact, err := writer.Generate(context.Background(), verificationResult, map[string]string{})
	assert.NoError(t, err)
	assert.NotNil(t, artifact)
}

// ============================================================================
// Test Profile Instructions With Additional Fields
// ============================================================================

func TestGenerate_ProfileWithCustomFields(t *testing.T) {
	response := createValidArtifactJSON()
	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()

	// Profile with custom fields
	profileInstructions := map[string]string{
		"language":            "en",
		"tone":                "analytical",
		"artifact_type":       "article",
		"max_sentence_length": "30",
		"vocabulary_level":    "professional",
		"additional_notes":    "Include comparisons",
	}

	artifact, err := writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.NoError(t, err)
	assert.NotNil(t, artifact)
}

// ============================================================================
// Test JSON Serialization
// ============================================================================

func TestWriteAndReadJSON(t *testing.T) {
	artifact := createValidArtifact()

	// Serialize to JSON
	data, err := json.Marshal(artifact)
	require.NoError(t, err)

	// Deserialize back
	var decoded contracts.EditorialArtifact
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	// Verify fields match
	assert.Equal(t, artifact.StableID, decoded.StableID)
	assert.Equal(t, artifact.ArtifactType, decoded.ArtifactType)
	assert.Equal(t, artifact.Title, decoded.Title)
	assert.Equal(t, artifact.Body, decoded.Body)
	assert.Len(t, decoded.Sections, len(artifact.Sections))
}

// ============================================================================
// Test ValidateAgainstVerificationResult
// ============================================================================

func TestValidateClaimReferences_MatchVerification(t *testing.T) {
	// Create a case where all claim references match verification result
	verificationResult := createBasicVerificationResult()

	client := &fakeLLMClient{response: createValidArtifactJSON()}
	writer, err := New(client, "test-model", WriterConfig{
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	artifact, err := writer.Generate(context.Background(), verificationResult, map[string]string{})
	assert.NoError(t, err)

	// Verify no validation errors for matching claim references
	assert.NotNil(t, artifact)
}

// ============================================================================
// Test Format Constraints From Profile
// ============================================================================

func TestGenerate_FormatConstraintsFromProfile(t *testing.T) {
	response := createValidArtifactJSON()
	client := &fakeLLMClient{response: response}
	writer, err := New(client, "test-model", WriterConfig{
		Temperature: 0.3,
		TimeNow:     func() time.Time { return parseTime("2024-01-15T12:00:00Z") },
		IDGenerator: contracts.NewUUIDGenerator(),
	})
	require.NoError(t, err)

	verificationResult := createBasicVerificationResult()

	profileInstructions := map[string]string{
		"language":           "en",
		"artifact_type":      "article",
		"format_constraints": "single-column|no-indented-quotes|number-paragraphs",
	}

	artifact, err := writer.Generate(context.Background(), verificationResult, profileInstructions)
	assert.NoError(t, err)
	assert.NotNil(t, artifact)
}
