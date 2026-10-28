package writer

import (
	"context"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planPromptVerification builds a minimal verification result for prompt tests.
func planPromptVerification() *contracts.VerificationResult {
	now := time.Date(2025, 10, 1, 12, 0, 0, 0, time.UTC)
	return &contracts.VerificationResult{
		StableID:       "verify-p",
		InputDossierID: "dossier-p",
		VerifiedAt:     now,
		VerificationStatuses: map[string]contracts.VerificationStatus{
			"claim-1": contracts.VerificationStatusSupported,
		},
		ClaimDetails: map[string]contracts.ClaimVerificationDetails{
			"claim-1": {ClaimID: "claim-1", Statement: "Bills are 4-2", VerificationStatus: contracts.VerificationStatusSupported, ConfidenceLevel: contracts.ConfidenceHigh},
		},
	}
}

// TestWriterPromptStandardUnchanged: with no recipe guidance and no plan,
// the user prompt carries neither recipe nor plan section — the standard
// flow is untouched.
func TestWriterPromptStandardUnchanged(t *testing.T) {
	prompt := buildUserPrompt(planPromptVerification(), map[string]string{}, WriterConfig{}, nil)
	assert.NotContains(t, prompt, "RECIPE")
	assert.NotContains(t, prompt, "CONTENT PLAN")
}

func TestWriterPromptRecipeInstructions(t *testing.T) {
	cfg := WriterConfig{RecipeInstructions: "Lead with the standings."}
	prompt := buildUserPrompt(planPromptVerification(), map[string]string{}, cfg, nil)
	assert.Contains(t, prompt, "=== RECIPE WRITING INSTRUCTIONS ===")
	assert.Contains(t, prompt, "untrusted content")
	assert.Contains(t, prompt, "Lead with the standings.")
	assert.NotContains(t, prompt, "CONTENT PLAN")
}

func TestWriterPromptContentPlan(t *testing.T) {
	plan := &contracts.ContentPlan{
		StableID: "plan-p",
		RecipeID: "team-weekly",
		Format:   "thread",
		Units: []contracts.PlanUnit{
			{ID: "unit-1", Role: "intro", Title: "Week 7", Required: true, ClaimIDs: []string{"claim-1"}},
			{ID: "unit-2", Role: "entity", Title: "Bills", Entity: &contracts.PlanEntity{Kind: "team", Name: "Bills", Fields: map[string]string{"record": "4-2"}}, ClaimIDs: []string{"claim-1"}},
		},
	}
	prompt := buildUserPrompt(planPromptVerification(), map[string]string{}, WriterConfig{}, plan)
	assert.Contains(t, prompt, "=== CONTENT PLAN (implement these units, in order) ===")
	assert.Contains(t, prompt, "role: intro")
	assert.Contains(t, prompt, "entity: Bills (team)")
	assert.Contains(t, prompt, "record: 4-2")
	assert.Contains(t, prompt, "grounded claims: claim-1")
	assert.NotContains(t, prompt, "RECIPE")
}

// TestWriterPromptPlanConfigLevel: a plan bound at the writer config level
// (orchestrator path) is honored by plain Generate as well.
func TestWriterPromptConfigLevelPlan(t *testing.T) {
	plan := &contracts.ContentPlan{
		StableID: "plan-c",
		RecipeID: "team-weekly",
		Format:   "article",
		Units:    []contracts.PlanUnit{{ID: "unit-1", Role: "intro", Required: true, ClaimIDs: []string{"claim-1"}}},
	}
	cfg := WriterConfig{ContentPlan: plan}
	prompt := buildUserPrompt(planPromptVerification(), map[string]string{}, cfg, nil)
	assert.Contains(t, prompt, "CONTENT PLAN")
}

func TestGenerateWithPlanRequiresSupportedClaims(t *testing.T) {
	// The plan must pass claim validation against the verification; a plan
	// citing a claim that is absent from the verification cannot be written.
	plan := &contracts.ContentPlan{
		StableID: "plan-bad",
		RecipeID: "team-weekly",
		Format:   "article",
		Units:    []contracts.PlanUnit{{ID: "unit-1", Role: "intro", ClaimIDs: []string{"claim-999"}}},
	}
	w, err := New(&fakeLLMClient{}, "test-model", WriterConfig{})
	require.NoError(t, err)
	_, err = w.GenerateWithPlan(context.Background(), planPromptVerification(), plan, nil)
	require.Error(t, err)
}
