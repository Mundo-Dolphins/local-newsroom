package contracts

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBaseTime = "2024-01-15T12:00:00Z"

func parseTime(t string) time.Time {
	parsed, err := time.Parse(time.RFC3339, t)
	if err != nil {
		panic(err)
	}
	return parsed
}

// ============================================================================
// VerificationResult Tests
// ============================================================================

func TestVerificationResult_ValidStruct(t *testing.T) {
	result := &VerificationResult{
		StableID:       "verify-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     parseTime(testBaseTime),
		VerificationStatuses: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
			"claim-002": VerificationStatusContradicted,
		},
		ClaimDetails: map[string]ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Fact A",
				VerificationStatus: VerificationStatusSupported,
				ConfidenceLevel:    researcher.ConfidenceHigh,
				SupportingEvidence: []EvidenceReference{
					{
						SourceID:        "source-001",
						Excerpt:         "Direct quote supporting Fact A",
						EvidenceContext: "Supporting evidence",
						OriginalClaimID: "claim-001",
					},
				},
			},
			"claim-002": {
				ClaimID:            "claim-002",
				Statement:          "Fact B",
				VerificationStatus: VerificationStatusContradicted,
				ConfidenceLevel:    researcher.ConfidenceMedium,
				ConflictingEvidence: []EvidenceReference{
					{
						SourceID:        "source-002",
						Excerpt:         "Contradictory evidence",
						EvidenceContext: "Conflicting evidence",
					},
				},
			},
		},
		VerificationNotes: []VerificationNote{
			{
				ID:              "note-001",
				NoteType:        VerificationNoteFinding,
				Content:         "Verification completed",
				CreatedAt:       parseTime(testBaseTime),
				RelatedClaimIDs: []string{"claim-001", "claim-002"},
			},
		},
		VerificationSummary: "Verification complete",
		QualityScore:        75.0,
	}

	err := result.Validate()
	assert.NoError(t, err)
}

func TestVerificationResult_InvalidMissingDetails(t *testing.T) {
	result := &VerificationResult{
		StableID:       "verify-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     parseTime(testBaseTime),
		VerificationStatuses: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
		},
		ClaimDetails: map[string]ClaimVerificationDetails{},
		QualityScore: 50.0,
	}

	err := result.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "verification_statuses (1) and claim_details (0)")
}

func TestVerificationResult_InvalidQualityScore(t *testing.T) {
	result := &VerificationResult{
		StableID:       "verify-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     parseTime(testBaseTime),
		VerificationStatuses: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
		},
		ClaimDetails: map[string]ClaimVerificationDetails{
			"claim-001": {ClaimID: "claim-001"},
		},
		QualityScore: 150.0, // Out of range
	}

	err := result.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "quality_score")
}

func TestVerificationResult_InvalidStatusMissingDetail(t *testing.T) {
	result := &VerificationResult{
		StableID:       "verify-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     parseTime(testBaseTime),
		VerificationStatuses: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
			"claim-002": VerificationStatusUncertain,
		},
		ClaimDetails: map[string]ClaimVerificationDetails{
			"claim-001": {ClaimID: "claim-001"},
			// claim-002 missing
		},
		QualityScore: 50.0,
	}

	err := result.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "verification_statuses has no corresponding claim_details")
}

func TestVerificationResult_ExtractClaimIDs(t *testing.T) {
	result := &VerificationResult{
		VerificationStatuses: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
			"claim-002": VerificationStatusContradicted,
			"claim-003": VerificationStatusUncertain,
		},
	}

	ids := result.ExtractClaimIDs()
	assert.Len(t, ids, 3)
	assert.Contains(t, ids, "claim-001")
	assert.Contains(t, ids, "claim-002")
	assert.Contains(t, ids, "claim-003")
}

func TestVerificationResult_GetSupportedClaims(t *testing.T) {
	result := &VerificationResult{
		VerificationStatuses: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
			"claim-002": VerificationStatusContradicted,
			"claim-003": VerificationStatusSupported,
		},
	}

	supported := result.GetSupportedClaims()
	assert.Len(t, supported, 2)
	assert.Contains(t, supported, "claim-001")
	assert.Contains(t, supported, "claim-003")
	assert.NotContains(t, supported, "claim-002")
}

func TestVerificationResult_GetContradictedClaims(t *testing.T) {
	result := &VerificationResult{
		VerificationStatuses: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
			"claim-002": VerificationStatusContradicted,
			"claim-003": VerificationStatusContradicted,
		},
	}

	contradicted := result.GetContradictedClaims()
	assert.Len(t, contradicted, 2)
	assert.Contains(t, contradicted, "claim-002")
	assert.Contains(t, contradicted, "claim-003")
}

func TestVerificationResult_GetUncertainClaims(t *testing.T) {
	result := &VerificationResult{
		VerificationStatuses: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
			"claim-002": VerificationStatusUncertain,
		},
	}

	uncertain := result.GetUncertainClaims()
	assert.Len(t, uncertain, 1)
	assert.Contains(t, uncertain, "claim-002")
}

func TestVerificationResult_CalculateQualityScore(t *testing.T) {
	tests := []struct {
		name     string
		statuses map[string]VerificationStatus
		expected float64
	}{
		{
			name:     "all supported",
			statuses: map[string]VerificationStatus{"claim-001": VerificationStatusSupported},
			expected: 60.0, // 100% supported = 60 points
		},
		{
			name: "mixed",
			statuses: map[string]VerificationStatus{
				"claim-001": VerificationStatusSupported,
				"claim-002": VerificationStatusUncertain,
			},
			expected: 40.0, // 50% supported + 50% uncertain = 30 + 10
		},
		{
			name:     "no claims",
			statuses: map[string]VerificationStatus{},
			expected: 0.0,
		},
		{
			name:     "insufficient evidence only",
			statuses: map[string]VerificationStatus{"claim-001": VerificationStatusInsufficientEvidence},
			expected: 0.0,
		},
		{
			name: "all uncertain",
			statuses: map[string]VerificationStatus{
				"claim-001": VerificationStatusUncertain,
				"claim-002": VerificationStatusUncertain,
			},
			expected: 20.0, // 100% uncertain = 20 points
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := &VerificationResult{
				VerificationStatuses: tt.statuses,
			}
			score := result.CalculateQualityScore()
			assert.InDelta(t, tt.expected, score, 0.01)
		})
	}
}

func TestVerificationResult_JSONSerialization(t *testing.T) {
	result := &VerificationResult{
		StableID:       "verify-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     parseTime(testBaseTime),
		VerificationStatuses: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
		},
		ClaimDetails: map[string]ClaimVerificationDetails{
			"claim-001": {
				ClaimID:            "claim-001",
				Statement:          "Fact A",
				VerificationStatus: VerificationStatusSupported,
				ConfidenceLevel:    researcher.ConfidenceHigh,
			},
		},
		QualityScore: 75.0,
	}

	jsonBytes, err := json.Marshal(result)
	require.NoError(t, err)

	var decoded VerificationResult
	err = json.Unmarshal(jsonBytes, &decoded)
	require.NoError(t, err)

	assert.Equal(t, result.StableID, decoded.StableID)
	assert.Equal(t, result.InputDossierID, decoded.InputDossierID)
	assert.Equal(t, result.QualityScore, decoded.QualityScore)
	assert.Equal(t, result.VerificationStatuses["claim-001"], decoded.VerificationStatuses["claim-001"])
}

// ============================================================================
// EditorialArtifact Tests
// ============================================================================

func TestEditorialArtifact_ValidArticle(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:     "artifact-001",
		ArtifactType: ArtifactTypeArticle,
		Title:        "Test Article",
		Body:         "Article body content.",
		ClaimReferences: map[string]ClaimUsage{
			"claim-001": {
				ClaimID:     "claim-001",
				UsageType:   ClaimUsageCore,
				Paraphrased: true,
			},
		},
		GenerationMetadata: GenerationMetadata{
			InputVerificationID: "verify-001",
			GeneratedAt:         parseTime(testBaseTime),
		},
	}

	err := artifact.Validate()
	assert.NoError(t, err)
}

func TestEditorialArtifact_ValidThread(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:     "artifact-001",
		ArtifactType: ArtifactTypeThread,
		Title:        "Test Thread",
		Posts: []Post{
			{
				Order:    0,
				Body:     "First post",
				ClaimIDs: []string{"claim-001"},
			},
			{
				Order:    1,
				Body:     "Second post",
				ClaimIDs: []string{"claim-002"},
			},
		},
		ClaimReferences: map[string]ClaimUsage{
			"claim-001": {
				ClaimID:     "claim-001",
				UsageType:   ClaimUsageSupporting,
				Paraphrased: false,
			},
			"claim-002": {
				ClaimID:     "claim-002",
				UsageType:   ClaimUsageCore,
				Paraphrased: true,
			},
		},
		GenerationMetadata: GenerationMetadata{
			InputVerificationID: "verify-001",
			GeneratedAt:         parseTime(testBaseTime),
		},
	}

	err := artifact.Validate()
	assert.NoError(t, err)
}

func TestEditorialArtifact_ValidHybrid(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:     "artifact-001",
		ArtifactType: ArtifactTypeHybrid,
		Title:        "Test Hybrid",
		Body:         "Summary article.",
		Sections: []Section{
			{
				Order:    0,
				Title:    "Introduction",
				Body:     "Intro content.",
				ClaimIDs: []string{"claim-001"},
			},
		},
		Posts: []Post{
			{
				Order:    0,
				Body:     "Thread post",
				ClaimIDs: []string{"claim-002"},
			},
		},
		ClaimReferences: map[string]ClaimUsage{
			"claim-001": {ClaimID: "claim-001"},
			"claim-002": {ClaimID: "claim-002"},
		},
		GenerationMetadata: GenerationMetadata{
			InputVerificationID: "verify-001",
			GeneratedAt:         parseTime(testBaseTime),
		},
	}

	err := artifact.Validate()
	assert.NoError(t, err)
}

func TestEditorialArtifact_InvalidArticleNoContent(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:        "artifact-001",
		ArtifactType:    ArtifactTypeArticle,
		Body:            "",
		Sections:        []Section{},
		ClaimReferences: map[string]ClaimUsage{},
		GenerationMetadata: GenerationMetadata{
			GeneratedAt: parseTime(testBaseTime),
		},
	}

	err := artifact.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "article artifact must have body or sections")
}

func TestEditorialArtifact_InvalidThreadNoPosts(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:        "artifact-001",
		ArtifactType:    ArtifactTypeThread,
		Posts:           []Post{},
		ClaimReferences: map[string]ClaimUsage{},
		GenerationMetadata: GenerationMetadata{
			GeneratedAt: parseTime(testBaseTime),
		},
	}

	err := artifact.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "thread artifact must have at least one post")
}

func TestEditorialArtifact_InvalidInvalidType(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:     "artifact-001",
		ArtifactType: "invalid-type",
		GenerationMetadata: GenerationMetadata{
			GeneratedAt: parseTime(testBaseTime),
		},
	}

	err := artifact.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown artifact type")
}

func TestEditorialArtifact_SectionsMissingClaims(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:     "artifact-001",
		ArtifactType: ArtifactTypeArticle,
		Body:         "Body.",
		Sections: []Section{
			{
				Order:    0,
				Title:    "Section",
				Body:     "Content.",
				ClaimIDs: []string{"claim-001"},
			},
		},
		ClaimReferences: map[string]ClaimUsage{
			"claim-002": {ClaimID: "claim-002"},
		},
		GenerationMetadata: GenerationMetadata{
			GeneratedAt: parseTime(testBaseTime),
		},
	}

	err := artifact.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "section 0 references unknown claim")
}

func TestEditorialArtifact_PostsMissingClaims(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:     "artifact-001",
		ArtifactType: ArtifactTypeThread,
		Posts: []Post{
			{
				Order:    0,
				Body:     "Post",
				ClaimIDs: []string{"claim-001"},
			},
		},
		ClaimReferences: map[string]ClaimUsage{
			"claim-002": {ClaimID: "claim-002"},
		},
		GenerationMetadata: GenerationMetadata{
			GeneratedAt: parseTime(testBaseTime),
		},
	}

	err := artifact.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "post 0 references unknown claim")
}

func TestEditorialArtifact_InvalidWarnings(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:     "artifact-001",
		ArtifactType: ArtifactTypeThread,
		Posts: []Post{
			{
				Order: 0,
				Body:  "Post",
			},
		},
		ClaimReferences: map[string]ClaimUsage{},
		Warnings: []Warning{
			{
				WarningType:     WarningTypeUnverifiedClaim,
				Severity:        WarningSeverityMedium,
				Message:         "Warning 1",
				RelatedClaimIDs: []string{"claim-001"},
			},
			{
				WarningType:     WarningTypeUnverifiedClaim, // Duplicate type
				Severity:        WarningSeverityLow,
				Message:         "Warning 2",
				RelatedClaimIDs: []string{"claim-002"},
			},
		},
		GenerationMetadata: GenerationMetadata{
			GeneratedAt: parseTime(testBaseTime),
		},
	}

	err := artifact.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate warning type")
}

func TestEditorialArtifact_InvalidTokenUsage(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:     "artifact-001",
		ArtifactType: ArtifactTypeThread,
		Posts: []Post{
			{
				Order: 0,
				Body:  "Post",
			},
		},
		ClaimReferences: map[string]ClaimUsage{},
		GenerationMetadata: GenerationMetadata{
			GeneratedAt: parseTime(testBaseTime),
			TokenUsage: &TokenUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      40, // Less than both prompt and completion
			},
		},
	}

	err := artifact.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "total_tokens must be >=")
}

func TestEditorialArtifact_ExtractClaimIDs(t *testing.T) {
	artifact := &EditorialArtifact{
		ClaimReferences: map[string]ClaimUsage{
			"claim-001": {ClaimID: "claim-001"},
			"claim-002": {ClaimID: "claim-002"},
			"claim-003": {ClaimID: "claim-003"},
		},
	}

	ids := artifact.ExtractClaimIDs()
	assert.Len(t, ids, 3)
	assert.Contains(t, ids, "claim-001")
	assert.Contains(t, ids, "claim-002")
	assert.Contains(t, ids, "claim-003")
}

func TestEditorialArtifact_GetCoreClaims(t *testing.T) {
	artifact := &EditorialArtifact{
		ClaimReferences: map[string]ClaimUsage{
			"claim-001": {ClaimID: "claim-001", UsageType: ClaimUsageCore},
			"claim-002": {ClaimID: "claim-002", UsageType: ClaimUsageSupporting},
			"claim-003": {ClaimID: "claim-003", UsageType: ClaimUsageCore},
		},
	}

	coreClaims := artifact.GetCoreClaims()
	assert.Len(t, coreClaims, 2)
	assert.Contains(t, coreClaims, "claim-001")
	assert.Contains(t, coreClaims, "claim-003")
	assert.NotContains(t, coreClaims, "claim-002")
}

func TestEditorialArtifact_HasCriticalWarnings(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:     "artifact-001",
		ArtifactType: ArtifactTypeThread,
		Posts: []Post{
			{
				Order: 0,
				Body:  "Post",
			},
		},
		ClaimReferences: map[string]ClaimUsage{},
		Warnings: []Warning{
			{
				WarningType: WarningTypeUnverifiedClaim,
				Severity:    WarningSeverityLow,
				Message:     "Low warning",
			},
			{
				WarningType: WarningTypeLowConfidence,
				Severity:    WarningSeverityCritical,
				Message:     "Critical warning",
			},
		},
		GenerationMetadata: GenerationMetadata{
			GeneratedAt: parseTime(testBaseTime),
		},
	}

	assert.True(t, artifact.HasCriticalWarnings())
}

func TestEditorialArtifact_GetWarningsBySeverity(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:        "artifact-001",
		ArtifactType:    ArtifactTypeThread,
		Posts:           []Post{{Order: 0, Body: "Post"}},
		ClaimReferences: map[string]ClaimUsage{},
		Warnings: []Warning{
			{WarningType: WarningTypeUnverifiedClaim, Severity: WarningSeverityLow, Message: "Low"},
			{WarningType: WarningTypeLowConfidence, Severity: WarningSeverityMedium, Message: "Medium"},
			{WarningType: WarningTypeInsufficientEvidence, Severity: WarningSeverityHigh, Message: "High"},
		},
		GenerationMetadata: GenerationMetadata{GeneratedAt: parseTime(testBaseTime)},
	}

	mediumWarnings := artifact.GetWarningsBySeverity(WarningSeverityMedium)
	assert.Len(t, mediumWarnings, 1)
	assert.Equal(t, "Medium", mediumWarnings[0].Message)
}

func TestEditorialArtifact_SectionNames(t *testing.T) {
	artifact := &EditorialArtifact{
		ArtifactType: ArtifactTypeArticle,
		Sections: []Section{
			{Order: 0, Title: "Introduction"},
			{Order: 1, Title: "Main"},
			{Order: 2, Title: "Conclusion"},
		},
	}

	names := artifact.SectionNames()
	assert.Len(t, names, 3)
	assert.Equal(t, "Introduction", names[0])
	assert.Equal(t, "Main", names[1])
	assert.Equal(t, "Conclusion", names[2])
}

func TestEditorialArtifact_PostContents(t *testing.T) {
	artifact := &EditorialArtifact{
		ArtifactType: ArtifactTypeThread,
		Posts: []Post{
			{Order: 0, Body: "Post 1"},
			{Order: 1, Body: "Post 2"},
		},
	}

	contents := artifact.PostContents()
	assert.Len(t, contents, 2)
	assert.Equal(t, "Post 1", contents[0])
	assert.Equal(t, "Post 2", contents[1])
}

func TestEditorialArtifact_HasSections(t *testing.T) {
	artifact := &EditorialArtifact{
		ArtifactType: ArtifactTypeArticle,
	}

	assert.False(t, artifact.HasSections())

	artifact.Sections = []Section{{Order: 0, Title: "Section"}}
	assert.True(t, artifact.HasSections())
}

func TestEditorialArtifact_HasPosts(t *testing.T) {
	artifact := &EditorialArtifact{
		ArtifactType: ArtifactTypeThread,
	}

	assert.False(t, artifact.HasPosts())

	artifact.Posts = []Post{{Order: 0, Body: "Post"}}
	assert.True(t, artifact.HasPosts())
}

func TestEditorialArtifact_GetClaimReference(t *testing.T) {
	artifact := &EditorialArtifact{
		ClaimReferences: map[string]ClaimUsage{
			"claim-001": {ClaimID: "claim-001", UsageType: ClaimUsageCore},
			"claim-002": {ClaimID: "claim-002", UsageType: ClaimUsageSupporting},
		},
	}

	usage, exists := artifact.GetClaimReference("claim-001")
	assert.True(t, exists)
	assert.Equal(t, ClaimUsageCore, usage.UsageType)

	_, exists = artifact.GetClaimReference("claim-003")
	assert.False(t, exists)
}

func TestEditorialArtifact_MergeClaimIDs(t *testing.T) {
	sections := []Section{
		{Order: 0, ClaimIDs: []string{"claim-001", "claim-002"}},
		{Order: 1, ClaimIDs: []string{"claim-002", "claim-003"}},
	}
	posts := []Post{
		{Order: 0, ClaimIDs: []string{"claim-003", "claim-004"}},
	}

	ids := MergeClaimIDs(sections, posts)
	assert.Len(t, ids, 4)
	assert.Contains(t, ids, "claim-001")
	assert.Contains(t, ids, "claim-002")
	assert.Contains(t, ids, "claim-003")
	assert.Contains(t, ids, "claim-004")
}

func TestEditorialArtifact_JSONSerialization(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:     "artifact-001",
		ArtifactType: ArtifactTypeArticle,
		Title:        "Test Article",
		Body:         "Body content",
		ClaimReferences: map[string]ClaimUsage{
			"claim-001": {
				ClaimID:     "claim-001",
				UsageType:   ClaimUsageCore,
				Paraphrased: true,
			},
		},
		SourceReferences: map[string]SourceUsage{
			"source-001": {
				SourceID:      "source-001",
				UsageCount:    3,
				CitationStyle: CitationStyleLink,
				CitationText:  "[1]",
			},
		},
		GenerationMetadata: GenerationMetadata{
			InputVerificationID: "verify-001",
			GeneratedAt:         parseTime(testBaseTime),
		},
	}

	jsonBytes, err := json.Marshal(artifact)
	require.NoError(t, err)

	var decoded EditorialArtifact
	err = json.Unmarshal(jsonBytes, &decoded)
	require.NoError(t, err)

	assert.Equal(t, artifact.StableID, decoded.StableID)
	assert.Equal(t, artifact.ArtifactType, decoded.ArtifactType)
	assert.Equal(t, artifact.Title, decoded.Title)
	assert.Equal(t, artifact.Body, decoded.Body)
	assert.Equal(t, artifact.ClaimReferences["claim-001"], decoded.ClaimReferences["claim-001"])
}

// ============================================================================
// FactualCheckResult Tests
// ============================================================================

func TestFactualCheckResult_ValidPass(t *testing.T) {
	result := &FactualCheckResult{
		StableID:        "check-001",
		InputArtifactID: "artifact-001",
		CheckedAt:       parseTime(testBaseTime),
		ClaimIDs: []string{
			"claim-001",
			"claim-002",
		},
		ClaimVerificationStatus: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
			"claim-002": VerificationStatusSupported,
		},
		OverallStatus:   FactualCheckStatusPass,
		ConfidenceLevel: ConfidenceHigh,
		Summary:         "All claims verified",
		Recommendations: []Recommendation{
			{
				RecommendationID:   "rec-001",
				RecommendationType: RecommendationTypeAdd,
				Description:        "Add more sources",
				Priority:           RecommendationPriorityLow,
			},
		},
	}

	err := result.Validate()
	assert.NoError(t, err)
}

func TestFactualCheckResult_ValidFail(t *testing.T) {
	result := &FactualCheckResult{
		StableID:        "check-001",
		InputArtifactID: "artifact-001",
		CheckedAt:       parseTime(testBaseTime),
		ClaimIDs:        []string{"claim-001"},
		ClaimVerificationStatus: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
		},
		OverallStatus:   FactualCheckStatusFail,
		ConfidenceLevel: ConfidenceHigh,
		UnsupportedStatements: []UnsupportedStatement{
			{
				Location: StatementLocation{
					LocationType: LocationTypeBody,
				},
				Statement:         "Unverified fact",
				StatementType:     UnsupportedStatementNewFacts,
				Severity:          WarningSeverityCritical,
				EvidenceMissing:   "No supporting evidence",
				SuggestedRevision: "Remove or verify",
			},
		},
		Summary: "Failed due to unverified content",
	}

	err := result.Validate()
	assert.NoError(t, err)
}

func TestFactualCheckResult_ValidReview(t *testing.T) {
	result := &FactualCheckResult{
		StableID:        "check-001",
		InputArtifactID: "artifact-001",
		CheckedAt:       parseTime(testBaseTime),
		ClaimIDs:        []string{"claim-001"},
		ClaimVerificationStatus: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
		},
		OverallStatus:   FactualCheckStatusReview,
		ConfidenceLevel: ConfidenceMedium,
		FactualFindings: []FactualFinding{
			{
				FindingID:       "find-001",
				FindingType:     FindingTypeConcern,
				Statement:       "Minor concern",
				Severity:        WarningSeverityMedium,
				Category:        FindingCategoryConsistency,
				RelatedClaimIDs: []string{"claim-001"},
			},
		},
		Summary: "Review recommended",
	}

	err := result.Validate()
	assert.NoError(t, err)
}

func TestFactualCheckResult_InvalidClaimIDsLength(t *testing.T) {
	result := &FactualCheckResult{
		StableID:        "check-001",
		InputArtifactID: "artifact-001",
		CheckedAt:       parseTime(testBaseTime),
		ClaimIDs:        []string{"claim-001", "claim-002"},
		ClaimVerificationStatus: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
			// claim-002 missing
		},
		OverallStatus:   FactualCheckStatusPass,
		ConfidenceLevel: ConfidenceHigh,
	}

	err := result.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "claim_ids")
}

func TestFactualCheckResult_InvalidClaimIDNotInStatus(t *testing.T) {
	result := &FactualCheckResult{
		StableID:        "check-001",
		InputArtifactID: "artifact-001",
		CheckedAt:       parseTime(testBaseTime),
		ClaimIDs:        []string{"claim-001", "claim-002"},
		ClaimVerificationStatus: map[string]VerificationStatus{
			"claim-002": VerificationStatusSupported,
		},
		OverallStatus:   FactualCheckStatusPass,
		ConfidenceLevel: ConfidenceHigh,
	}

	err := result.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "has no verification status")
}

func TestFactualCheckResult_InvalidOverallStatus(t *testing.T) {
	result := &FactualCheckResult{
		StableID:        "check-001",
		InputArtifactID: "artifact-001",
		CheckedAt:       parseTime(testBaseTime),
		ClaimIDs:        []string{"claim-001"},
		ClaimVerificationStatus: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
		},
		OverallStatus:   "invalid-status",
		ConfidenceLevel: ConfidenceHigh,
	}

	err := result.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid overall_status")
}

func TestFactualCheckResult_InvalidConfidenceLevel(t *testing.T) {
	result := &FactualCheckResult{
		StableID:        "check-001",
		InputArtifactID: "artifact-001",
		CheckedAt:       parseTime(testBaseTime),
		ClaimIDs:        []string{"claim-001"},
		ClaimVerificationStatus: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
		},
		OverallStatus:   FactualCheckStatusPass,
		ConfidenceLevel: "invalid-confidence",
	}

	err := result.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid confidence_level")
}

func TestFactualCheckResult_PassRequirements(t *testing.T) {
	tests := []struct {
		name     string
		result   *FactualCheckResult
		expected bool
	}{
		{
			name: "no issues",
			result: &FactualCheckResult{
				UnsupportedStatements: []UnsupportedStatement{},
				AlteredDetails:        []AlteredDetail{},
				FactualFindings:       []FactualFinding{},
			},
			expected: true,
		},
		{
			name: "critical unsupported",
			result: &FactualCheckResult{
				UnsupportedStatements: []UnsupportedStatement{
					{Severity: WarningSeverityCritical},
				},
			},
			expected: false,
		},
		{
			name: "high unsupported",
			result: &FactualCheckResult{
				UnsupportedStatements: []UnsupportedStatement{
					{Severity: WarningSeverityHigh},
				},
			},
			expected: false,
		},
		{
			name: "critical altered",
			result: &FactualCheckResult{
				AlteredDetails: []AlteredDetail{
					{Significance: AlterationSignificanceCritical},
				},
			},
			expected: false,
		},
		{
			name: "critical error finding",
			result: &FactualCheckResult{
				FactualFindings: []FactualFinding{
					{FindingType: FindingTypeError, Severity: WarningSeverityCritical},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.result
			result.ClaimIDs = []string{"claim-001"}
			result.ClaimVerificationStatus = map[string]VerificationStatus{"claim-001": VerificationStatusSupported}
			result.OverallStatus = FactualCheckStatusPass
			result.ConfidenceLevel = ConfidenceHigh

			assert.Equal(t, tt.expected, result.PassRequirements())
		})
	}
}

func TestFactualCheckResult_DetermineStatus(t *testing.T) {
	tests := []struct {
		name     string
		result   *FactualCheckResult
		expected FactualCheckStatus
	}{
		{
			name: "pass",
			result: &FactualCheckResult{
				UnsupportedStatements: []UnsupportedStatement{{Severity: WarningSeverityLow}},
				AlteredDetails:        []AlteredDetail{{Significance: AlterationSignificanceTrivial}},
				FactualFindings:       []FactualFinding{{FindingType: FindingTypeSupport, Severity: WarningSeverityLow}},
			},
			expected: FactualCheckStatusPass,
		},
		{
			name: "review due to medium warnings",
			result: &FactualCheckResult{
				UnsupportedStatements: []UnsupportedStatement{{Severity: WarningSeverityMedium}},
			},
			expected: FactualCheckStatusReview,
		},
		{
			name: "fail due to critical unsupported",
			result: &FactualCheckResult{
				UnsupportedStatements: []UnsupportedStatement{{Severity: WarningSeverityCritical}},
			},
			expected: FactualCheckStatusFail,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.result
			result.ClaimIDs = []string{"claim-001"}
			result.ClaimVerificationStatus = map[string]VerificationStatus{"claim-001": VerificationStatusSupported}

			assert.Equal(t, tt.expected, result.DetermineStatus())
		})
	}
}

func TestFactualCheckResult_GetCriticalIssues(t *testing.T) {
	result := &FactualCheckResult{
		UnsupportedStatements: []UnsupportedStatement{
			{Statement: "Critical unsupported", Severity: WarningSeverityCritical},
			{Statement: "Low unsupported", Severity: WarningSeverityLow},
		},
		AlteredDetails: []AlteredDetail{
			{OriginalValue: "100", AlteredImageValue: "101", Significance: AlterationSignificanceCritical},
		},
		FactualFindings: []FactualFinding{
			{Statement: "Critical error", FindingType: FindingTypeError, Severity: WarningSeverityCritical},
		},
	}

	issues := result.GetCriticalIssues()
	assert.Len(t, issues, 3)
	assert.Contains(t, issues[0], "Critical unsupported")
	assert.Contains(t, issues[1], "100")
	assert.Contains(t, issues[2], "Critical error")
}

func TestFactualCheckResult_GetRecommendationsByPriority(t *testing.T) {
	result := &FactualCheckResult{
		Recommendations: []Recommendation{
			{Priority: RecommendationPriorityLow},
			{Priority: RecommendationPriorityMedium},
			{Priority: RecommendationPriorityHigh},
			{Priority: RecommendationPriorityCritical},
		},
	}

	medium := result.GetRecommendationsByPriority(RecommendationPriorityMedium)
	assert.Len(t, medium, 1)
	assert.Equal(t, RecommendationPriorityMedium, medium[0].Priority)

	critical := result.GetRecommendationsByPriority(RecommendationPriorityCritical)
	assert.Len(t, critical, 1)
	assert.Equal(t, RecommendationPriorityCritical, critical[0].Priority)
}

func TestFactualCheckResult_GetCriticalRecommendations(t *testing.T) {
	result := &FactualCheckResult{
		Recommendations: []Recommendation{
			{Priority: RecommendationPriorityLow},
			{Priority: RecommendationPriorityHigh},
			{Priority: RecommendationPriorityCritical},
		},
	}

	criticalRecs := result.GetCriticalRecommendations()
	assert.Len(t, criticalRecs, 2)
	for _, rec := range criticalRecs {
		assert.Contains(t, []RecommendationPriority{RecommendationPriorityHigh, RecommendationPriorityCritical}, rec.Priority)
	}
}

func TestFactualCheckResult_SummaryText(t *testing.T) {
	result := &FactualCheckResult{
		StableID:                "check-001",
		InputArtifactID:         "artifact-001",
		CheckedAt:               parseTime(testBaseTime),
		ClaimIDs:                []string{"claim-001"},
		ClaimVerificationStatus: map[string]VerificationStatus{"claim-001": VerificationStatusSupported},
		OverallStatus:           FactualCheckStatusPass,
		ConfidenceLevel:         ConfidenceHigh,
		Summary:                 "All claims verified",
		UnsupportedStatements:   []UnsupportedStatement{{Statement: "Test", StatementType: UnsupportedStatementNewFacts}},
		AlteredDetails:          []AlteredDetail{{OriginalValue: "100", AlteredImageValue: "101"}},
		Recommendations: []Recommendation{
			{RecommendationID: "rec-001", Description: "Add more sources"},
		},
	}

	summary := result.SummaryText()
	assert.Contains(t, summary, "artifact-001")
	assert.Contains(t, summary, "pass") // SummaryText uses lowercase
	assert.Contains(t, summary, "Claims checked: 1")
	assert.Contains(t, summary, "Unsupported statements: 1")
	assert.Contains(t, summary, "Altered details: 1")
	assert.Contains(t, summary, "Recommendations: 1")
}

func TestFactualCheckResult_JSONSerialization(t *testing.T) {
	result := &FactualCheckResult{
		StableID:        "check-001",
		InputArtifactID: "artifact-001",
		CheckedAt:       parseTime(testBaseTime),
		ClaimIDs: []string{
			"claim-001",
		},
		ClaimVerificationStatus: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
		},
		OverallStatus:   FactualCheckStatusPass,
		ConfidenceLevel: ConfidenceHigh,
		UnsupportedStatements: []UnsupportedStatement{
			{
				Location: StatementLocation{
					LocationType: LocationTypeBody,
					BodyOffset:   100,
				},
				Statement:         "Unverified fact",
				StatementType:     UnsupportedStatementNewFacts,
				Severity:          WarningSeverityHigh,
				EvidenceMissing:   "Need sources",
				SuggestedRevision: "Add verification",
			},
		},
		AlteredDetails: []AlteredDetail{
			{
				DetailType:        AlteredDetailTypeNumber,
				OriginalValue:     "100",
				AlteredImageValue: "101",
				Significance:      AlterationSignificanceModerate,
				IsError:           true,
			},
		},
		FactualFindings: []FactualFinding{
			{
				FindingID:       "find-001",
				FindingType:     FindingTypeSupport,
				Statement:       "Well supported",
				Severity:        WarningSeverityLow,
				Category:        FindingCategoryAccuracy,
				RelatedClaimIDs: []string{"claim-001"},
			},
		},
		Recommendations: []Recommendation{
			{
				RecommendationID:   "rec-001",
				RecommendationType: RecommendationTypeFix,
				Description:        "Correct the number",
				Priority:           RecommendationPriorityMedium,
				EffortEstimate:     RecommendationEffortQuick,
			},
		},
	}

	jsonBytes, err := json.Marshal(result)
	require.NoError(t, err)

	var decoded FactualCheckResult
	err = json.Unmarshal(jsonBytes, &decoded)
	require.NoError(t, err)

	assert.Equal(t, result.StableID, decoded.StableID)
	assert.Equal(t, result.InputArtifactID, decoded.InputArtifactID)
	assert.Equal(t, result.OverallStatus, decoded.OverallStatus)
	assert.Equal(t, result.ConfidenceLevel, decoded.ConfidenceLevel)
	assert.Len(t, decoded.UnsupportedStatements, 1)
	assert.Len(t, decoded.AlteredDetails, 1)
	assert.Len(t, decoded.FactualFindings, 1)
	assert.Len(t, decoded.Recommendations, 1)
}

// ============================================================================
// Shared Types Tests
// ============================================================================

func TestTimeToUTC(t *testing.T) {
	utc := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	local := time.Date(2024, 1, 15, 12, 0, 0, 0, time.FixedZone("EST", -5*3600))

	assert.Equal(t, time.UTC, TimeToUTC(utc).Location())
	result := TimeToUTC(local)
	assert.Equal(t, time.UTC, result.Location())
}

func TestTimeSlice(t *testing.T) {
	utc := parseTime(testBaseTime)
	local := utc.AddDate(0, 0, 0) // Same time, potentially different location

	times := []time.Time{utc, local}
	result := TimeSlice(times)

	for _, tt := range result {
		assert.Equal(t, time.UTC, tt.Location())
	}
}

func TestIsEmpty(t *testing.T) {
	assert.True(t, IsEmpty(nil))
	assert.True(t, IsEmpty([]string{}))
	assert.False(t, IsEmpty([]string{"test"}))
}

func TestContains(t *testing.T) {
	slice := []string{"a", "b", "c"}
	assert.True(t, Contains(slice, "b"))
	assert.False(t, Contains(slice, "d"))
	assert.False(t, Contains(slice, ""))
}

func TestUniqueStrings(t *testing.T) {
	input := []string{"a", "b", "a", "c", "b", "d"}
	result := UniqueStrings(input)

	assert.Len(t, result, 4)
	assert.Contains(t, result, "a")
	assert.Contains(t, result, "b")
	assert.Contains(t, result, "c")
	assert.Contains(t, result, "d")

	// Preserve order
	assert.Equal(t, "a", result[0])
	assert.Equal(t, "b", result[1])
	assert.Equal(t, "c", result[2])
	assert.Equal(t, "d", result[3])
}

func TestMergeStringSlices(t *testing.T) {
	slice1 := []string{"a", "b"}
	slice2 := []string{"b", "c"}
	slice3 := []string{"c", "d"}

	result := MergeStringSlices(slice1, slice2, slice3)

	assert.Len(t, result, 4)
	assert.Contains(t, result, "a")
	assert.Contains(t, result, "b")
	assert.Contains(t, result, "c")
	assert.Contains(t, result, "d")
}

func TestEqualStringSlices(t *testing.T) {
	slice1 := []string{"a", "b", "c"}
	slice2 := []string{"c", "b", "a"}
	slice3 := []string{"a", "b", "d"}

	assert.True(t, EqualStringSlices(slice1, slice2))
	assert.False(t, EqualStringSlices(slice1, slice3))
	assert.True(t, EqualStringSlices(nil, nil))
	assert.True(t, EqualStringSlices([]string{}, []string{}))
}

// ============================================================================
// Integration Test: Full Pipeline Contracts
// ============================================================================

func TestContractProvenanceChain(t *testing.T) {
	// Simulate a full provenance chain: ResearchDossier -> VerificationResult -> EditorialArtifact -> FactualCheckResult

	// Step 1: ResearchDossier claims (from researcher package)
	claims := []struct {
		ID         string
		Statement  string
		Confidence researcher.ConfidenceLevel
	}{
		{ID: "claim-001", Statement: "The program processed 150 records", Confidence: researcher.ConfidenceHigh},
		{ID: "claim-002", Statement: "The latency averaged 23ms", Confidence: researcher.ConfidenceMedium},
		{ID: "claim-003", Statement: "Users reported satisfaction", Confidence: researcher.ConfidenceLow},
	}

	// Step 2: VerificationResult
	verifyResult := &VerificationResult{
		StableID:       "verify-001",
		InputDossierID: "dossier-001",
		VerifiedAt:     parseTime(testBaseTime),
		VerificationStatuses: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
			"claim-002": VerificationStatusSupported,
			"claim-003": VerificationStatusUncertain,
		},
		ClaimDetails: map[string]ClaimVerificationDetails{},
		QualityScore: 70.0,
	}

	for _, c := range claims {
		verifyResult.ClaimDetails[c.ID] = ClaimVerificationDetails{
			ClaimID:            c.ID,
			Statement:          c.Statement,
			VerificationStatus: verifyResult.VerificationStatuses[c.ID],
			ConfidenceLevel:    c.Confidence,
		}
	}

	// Step 3: EditorialArtifact using verified claims
	artifact := &EditorialArtifact{
		StableID:     "artifact-001",
		ArtifactType: ArtifactTypeHybrid,
		Title:        "Performance Report",
		Body:         "The program processed 150 records with 23ms average latency.",
		Sections: []Section{
			{
				Order:    0,
				Title:    "Findings",
				Body:     "Core metrics were measured.",
				ClaimIDs: []string{"claim-001", "claim-002"},
			},
		},
		Posts: []Post{
			{
				Order:    0,
				Body:     "150 records processed.",
				ClaimIDs: []string{"claim-001"},
			},
		},
		ClaimReferences: map[string]ClaimUsage{
			"claim-001": {
				ClaimID:     "claim-001",
				UsageType:   ClaimUsageCore,
				Paraphrased: true,
			},
			"claim-002": {
				ClaimID:     "claim-002",
				UsageType:   ClaimUsageCore,
				Paraphrased: true,
			},
			"claim-003": {
				ClaimID:     "claim-003",
				UsageType:   ClaimUsageBackground,
				Paraphrased: false,
			},
		},
		GenerationMetadata: GenerationMetadata{
			InputVerificationID: "verify-001",
			GeneratedAt:         parseTime(testBaseTime),
		},
	}

	// Step 4: FactualCheckResult checking the artifact
	factResult := &FactualCheckResult{
		StableID:                "check-001",
		InputArtifactID:         "artifact-001",
		CheckedAt:               parseTime(testBaseTime),
		ClaimIDs:                []string{"claim-001", "claim-002", "claim-003"},
		ClaimVerificationStatus: verifyResult.VerificationStatuses,
		OverallStatus:           FactualCheckStatusReview,
		ConfidenceLevel:         ConfidenceMedium,
		Summary:                 "Article passes factual check with one uncertain claim noted.",
		Recommendations: []Recommendation{
			{
				RecommendationID:   "rec-001",
				RecommendationType: RecommendationTypeClarify,
				Description:        "Clarify that user satisfaction claims are uncertain",
				Priority:           RecommendationPriorityMedium,
				RelatedFindingIDs:  []string{"find-001"},
			},
		},
	}

	// Validate all contracts
	require.NoError(t, verifyResult.Validate())
	require.NoError(t, artifact.Validate())
	require.NoError(t, factResult.Validate())

	// Verify provenance chain
	assert.Equal(t, "dossier-001", verifyResult.InputDossierID)
	assert.Equal(t, "verify-001", artifact.GenerationMetadata.InputVerificationID)
	assert.NotNil(t, factResult.ClaimVerificationStatus)

	// Verify claim consistency
	assert.Equal(t, verifyResult.VerificationStatuses["claim-001"], factResult.ClaimVerificationStatus["claim-001"])
	assert.Equal(t, verifyResult.VerificationStatuses["claim-002"], factResult.ClaimVerificationStatus["claim-002"])
	assert.Equal(t, verifyResult.VerificationStatuses["claim-003"], factResult.ClaimVerificationStatus["claim-003"])
}

// ============================================================================
// Edge Case Tests
// ============================================================================

func TestVerificationResult_EmptyMaps(t *testing.T) {
	result := &VerificationResult{
		StableID:             "verify-001",
		InputDossierID:       "dossier-001",
		VerifiedAt:           parseTime(testBaseTime),
		VerificationStatuses: map[string]VerificationStatus{},
		ClaimDetails:         map[string]ClaimVerificationDetails{},
		QualityScore:         0.0,
	}

	err := result.Validate()
	assert.NoError(t, err) // Empty is valid
}

func TestEditorialArtifact_EmptyClaims(t *testing.T) {
	artifact := &EditorialArtifact{
		StableID:           "artifact-001",
		ArtifactType:       ArtifactTypeThread,
		Posts:              []Post{{Order: 0, Body: "Post with no claims"}},
		ClaimReferences:    map[string]ClaimUsage{},
		GenerationMetadata: GenerationMetadata{GeneratedAt: parseTime(testBaseTime)},
	}

	err := artifact.Validate()
	assert.NoError(t, err)
}

func TestFactualCheckResult_EmptyFindings(t *testing.T) {
	result := &FactualCheckResult{
		StableID:        "check-001",
		InputArtifactID: "artifact-001",
		CheckedAt:       parseTime(testBaseTime),
		ClaimIDs:        []string{"claim-001"},
		ClaimVerificationStatus: map[string]VerificationStatus{
			"claim-001": VerificationStatusSupported,
		},
		OverallStatus:   FactualCheckStatusPass,
		ConfidenceLevel: ConfidenceHigh,
	}

	err := result.Validate()
	assert.NoError(t, err)
}

func TestUUIDGenerator(t *testing.T) {
	gen := NewUUIDGenerator()

	id1 := gen.GenerateID()
	id2 := gen.GenerateID()
	id3 := gen.GenerateIDWithPrefix("test")

	assert.Equal(t, "id-1", id1)
	assert.Equal(t, "id-2", id2)
	assert.Equal(t, "test-3", id3)

	// IDs should be unique
	assert.NotEqual(t, id1, id2)
	assert.NotEqual(t, id1, id3)
	assert.NotEqual(t, id2, id3)
}
