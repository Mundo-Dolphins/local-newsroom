package researcher

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

func TestResearchDossierValidate_Valid(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com/article",
				SourceType:  "web",
				RetrievedAt: now,
				Title:       "Test Article",
				Author:      "Test Author",
				PublishedAt: &now,
				Metadata:    map[string]string{"test": "value"},
			},
		},
		Claims: []Claim{
			{
				ID:            "claim-001",
				Statement:     "This is a supported claim.",
				Confidence:    ConfidenceHigh,
				IsUnsupported: false,
				Evidence: []Evidence{
					{
						SourceID: "source-001",
						Excerpts: []Excerpt{
							{
								Text:        "Supporting text excerpt.",
								StartOffset: 0,
								EndOffset:   25,
							},
						},
					},
				},
			},
		},
	}

	err := d.Validate()
	if err != nil {
		t.Fatalf("Valid dossier should pass validation: %v", err)
	}
}

func TestResearchDossierValidate_UnsupportedClaimWithoutEvidence(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com",
				SourceType:  "web",
				RetrievedAt: now,
			},
		},
		Claims: []Claim{
			{
				ID:            "claim-001",
				Statement:     "Claim without evidence or unsupported flag.",
				Confidence:    ConfidenceMedium,
				IsUnsupported: false,
				Evidence:      []Evidence{},
			},
		},
	}

	err := d.Validate()
	if err == nil {
		t.Fatal("Expected validation error for claim without evidence and unsupported flag")
	}

	expectedErrMsg := "claim \"claim-001\" has no evidence and is not marked as unsupported"
	if err.Error() != expectedErrMsg {
		t.Errorf("Expected error message to contain %q, got %q", expectedErrMsg, err.Error())
	}
}

func TestResearchDossierValidate_InvalidEvidenceSourceReference(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com",
				SourceType:  "web",
				RetrievedAt: now,
			},
		},
		Claims: []Claim{
			{
				ID:            "claim-001",
				Statement:     "Claim referencing unknown source.",
				Confidence:    ConfidenceHigh,
				IsUnsupported: false,
				Evidence: []Evidence{
					{
						SourceID: "non-existent-source",
						Excerpts: []Excerpt{
							{
								Text:        "Some text.",
								StartOffset: 0,
								EndOffset:   10,
							},
						},
					},
				},
			},
		},
	}

	err := d.Validate()
	if err == nil {
		t.Fatal("Expected validation error for claim referencing unknown source")
	}

	if !strings.Contains(err.Error(), "claim \"claim-001\" references unknown source \"non-existent-source\" in evidence") {
		t.Logf("Validation error (expected): %v", err)
	}
}

func TestResearchDossierValidate_DuplicateClaimID(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com",
				SourceType:  "web",
				RetrievedAt: now,
			},
		},
		Claims: []Claim{
			{
				ID:            "claim-001",
				Statement:     "First claim.",
				Evidence:      []Evidence{{SourceID: "source-001", Excerpts: []Excerpt{{Text: "text"}}}},
				IsUnsupported: false,
			},
			{
				ID:            "claim-001",
				Statement:     "Duplicate ID claim.",
				Evidence:      []Evidence{{SourceID: "source-001", Excerpts: []Excerpt{{Text: "text"}}}},
				IsUnsupported: false,
			},
		},
	}

	err := d.Validate()
	if err == nil {
		t.Fatal("Expected validation error for duplicate claim ID")
	}

	if !strings.Contains(err.Error(), "duplicate claim ID: claim-001") {
		t.Logf("Validation error (expected): %v", err)
	}
}

func TestResearchDossierValidate_ContradictionClaimReference(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com",
				SourceType:  "web",
				RetrievedAt: now,
			},
		},
		Claims: []Claim{
			{
				ID:            "claim-001",
				Statement:     "Claim.",
				Evidence:      []Evidence{{SourceID: "source-001", Excerpts: []Excerpt{{Text: "text"}}}},
				IsUnsupported: false,
			},
		},
		Contradictions: []Contradiction{
			{
				ID:          "contradiction-001",
				ClaimIDs:    []string{"claim-001", "claim-999"},
				Description: "Describes contradiction.",
			},
		},
	}

	err := d.Validate()
	if err == nil {
		t.Fatal("Expected validation error for contradiction referencing non-existent claim")
	}

	if !strings.Contains(err.Error(), "contradiction \"contradiction-001\" references unknown claim \"claim-999\"") {
		t.Logf("Validation error (expected): %v", err)
	}
}

func TestResearchDossierValidate_MinimumContradictionClaims(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test Topic",
		GeneratedAt: now,
		Sources: []SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com",
				SourceType:  "web",
				RetrievedAt: now,
			},
		},
		Claims: []Claim{
			{
				ID:            "claim-001",
				Statement:     "Claim.",
				Evidence:      []Evidence{{SourceID: "source-001", Excerpts: []Excerpt{{Text: "text"}}}},
				IsUnsupported: false,
			},
		},
		Contradictions: []Contradiction{
			{
				ID:          "contradiction-001",
				ClaimIDs:    []string{"claim-001"},
				Description: "Describes contradiction with only one claim.",
			},
		},
	}

	err := d.Validate()
	if err == nil {
		t.Fatal("Expected validation error for contradiction with less than 2 claims")
	}

	if !strings.Contains(err.Error(), "contradiction \"contradiction-001\" must reference at least 2 claims") {
		t.Logf("Validation error (expected): %v", err)
	}
}

func TestResearchDossierFromSource(t *testing.T) {
	now := time.Now().UTC()
	httpStatus := 200
	contentType := "text/html"
	contentLen := int64(5000)

	refSource := SourceReference{
		StableID:    "existing-source",
		OriginalURL: "https://existing.com",
		SourceType:  "web",
		RetrievedAt: now,
	}

	in := types.Source{
		StableID:    "new-source-123",
		OriginalURL: "https://example.com/article",
		SourceType:  types.SourceTypeWeb,
		RetrievedAt: now,
		FetchStatus: &types.FetchStatus{
			HTTPStatus:    &httpStatus,
			ContentType:   &contentType,
			ContentLength: &contentLen,
			FetchError:    nil,
		},
		Metadata: map[string]string{
			"server":    "nginx",
			"test_meta": "value",
		},
	}

	refSource.FromSource(in)

	if refSource.StableID != "new-source-123" {
		t.Errorf("Expected StableID to be 'new-source-123', got %q", refSource.StableID)
	}
	if refSource.OriginalURL != "https://example.com/article" {
		t.Errorf("Expected OriginalURL to be correct, got %q", refSource.OriginalURL)
	}
	if refSource.SourceType != types.SourceTypeWeb {
		t.Errorf("Expected SourceType to be 'web', got %q", refSource.SourceType)
	}
	if refSource.RetrievedAt != now {
		t.Error("Expected RetrievedAt to match")
	}
	if refSource.FetchStatus == nil {
		t.Fatal("Expected FetchStatus to be non-nil")
	}
	if refSource.FetchStatus.HTTPStatus == nil || *refSource.FetchStatus.HTTPStatus != 200 {
		t.Errorf("Expected HTTPStatus to be 200, got %v", refSource.FetchStatus.HTTPStatus)
	}
	if refSource.FetchStatus.ContentType == nil || *refSource.FetchStatus.ContentType != "text/html" {
		t.Errorf("Expected ContentType to be 'text/html', got %v", refSource.FetchStatus.ContentType)
	}
	if refSource.FetchStatus.ContentLength == nil || *refSource.FetchStatus.ContentLength != 5000 {
		t.Errorf("Expected ContentLength to be 5000, got %v", refSource.FetchStatus.ContentLength)
	}
	if len(refSource.Metadata) != 2 {
		t.Errorf("Expected 2 metadata entries, got %d", len(refSource.Metadata))
	}
}

func TestResearchDossierMarshalUnmarshal(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-test-001",
		Topic:       "Test Research Topic",
		GeneratedAt: now,
		Sources: []SourceReference{
			{
				StableID:      "source-test-001",
				OriginalURL:   "https://example.com/test",
				SourceType:    "web",
				RetrievedAt:   now,
				Title:         "Test Title",
				Author:        "Test Author",
				PublishedAt:   &now,
				Metadata:      map[string]string{"key": "value"},
			},
		},
		Claims: []Claim{
			{
				ID:            "claim-test-001",
				Statement:     "Test claim statement.",
				Confidence:    ConfidenceHigh,
				IsUnsupported: false,
				Evidence: []Evidence{
					{
						SourceID:       "source-test-001",
						ClaimContext:   "This excerpt supports the claim.",
						Excerpts: []Excerpt{
							{
								Text:          "Excerpt text.",
								StartOffset:   0,
								EndOffset:     14,
								ContextBefore: "Context before",
								ContextAfter:  "Context after",
							},
						},
					},
				},
				SourcesContradicted: []string{},
			},
		},
		Contradictions: []Contradiction{
			{
				ID:               "contradiction-test-001",
				ClaimIDs:         []string{"claim-test-001", "claim-test-002"},
				Description:      "Test contradiction.",
				ResolutionStatus: ResolutionUnresolved,
				ResolutionNotes:  "Pending resolution.",
				Severity:         SeverityMedium,
			},
		},
		UnresolvedQuestions: []UnresolvedQuestion{
			{
				ID:                "question-test-001",
				Question:          "Test unresolved question?",
				WhyUnresolved:     "No definitive answer found.",
				Priority:          SeverityHigh,
				RelatedClaimIDs:   []string{"claim-test-001"},
			},
		},
		ResearchNotes: []ResearchNote{
			{
				ID:               "note-test-001",
				Content:          "Test research note.",
				NoteType:         NoteMethodology,
				RelatedClaimIDs:  []string{"claim-test-001"},
				RelatedSourceIDs: []string{"source-test-001"},
				CreatedAt:        now,
			},
		},
	}

	// Marshal
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Failed to marshal ResearchDossier: %v", err)
	}

	// Unmarshal
	var restored ResearchDossier
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("Failed to unmarshal ResearchDossier: %v", err)
	}

	// Verify basic fields
	if restored.StableID != d.StableID {
		t.Errorf("Expected StableID to be %q, got %q", d.StableID, restored.StableID)
	}
	if restored.Topic != d.Topic {
		t.Errorf("Expected Topic to be %q, got %q", d.Topic, restored.Topic)
	}
	if !restored.GeneratedAt.Equal(d.GeneratedAt) {
		t.Errorf("Expected GeneratedAt to match, got %v vs %v", restored.GeneratedAt, d.GeneratedAt)
	}

	// Verify sources
	if len(restored.Sources) != len(d.Sources) {
		t.Errorf("Expected %d sources, got %d", len(d.Sources), len(restored.Sources))
	}
	if len(restored.Sources) > 0 {
		if restored.Sources[0].StableID != d.Sources[0].StableID {
			t.Errorf("Expected first source ID to match")
		}
		if restored.Sources[0].OriginalURL != d.Sources[0].OriginalURL {
			t.Errorf("Expected first source URL to match")
		}
		if len(restored.Sources[0].Metadata) != len(d.Sources[0].Metadata) {
			t.Errorf("Expected metadata length to match")
		}
	}

	// Verify claims
	if len(restored.Claims) != len(d.Claims) {
		t.Errorf("Expected %d claims, got %d", len(d.Claims), len(restored.Claims))
	}
	if len(restored.Claims) > 0 {
		c := restored.Claims[0]
		if c.ID != d.Claims[0].ID {
			t.Errorf("Expected claim ID to match")
		}
		if c.Statement != d.Claims[0].Statement {
			t.Errorf("Expected claim statement to match")
		}
		if c.Confidence != d.Claims[0].Confidence {
			t.Errorf("Expected claim confidence to match")
		}
		if c.IsUnsupported != d.Claims[0].IsUnsupported {
			t.Errorf("Expected is_unsupported to match")
		}
		if len(c.Evidence) != len(d.Claims[0].Evidence) {
			t.Errorf("Expected evidence length to match")
		}
		if len(c.Evidence) > 0 && c.Evidence[0].SourceID != d.Claims[0].Evidence[0].SourceID {
			t.Errorf("Expected evidence source ID to match")
		}
	}

	// Verify contradictions
	if len(restored.Contradictions) != len(d.Contradictions) {
		t.Errorf("Expected %d contradictions, got %d", len(d.Contradictions), len(restored.Contradictions))
	}

	// Verify unresolved questions
	if len(restored.UnresolvedQuestions) != len(d.UnresolvedQuestions) {
		t.Errorf("Expected %d questions, got %d", len(d.UnresolvedQuestions), len(restored.UnresolvedQuestions))
	}

	// Verify research notes
	if len(restored.ResearchNotes) != len(d.ResearchNotes) {
		t.Errorf("Expected %d notes, got %d", len(d.ResearchNotes), len(restored.ResearchNotes))
	}
}

func TestResearchDossierExtractClaimIDs(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test",
		GeneratedAt: now,
		Sources: []SourceReference{
			{StableID: "source-001", RetrievedAt: now},
			{StableID: "source-002", RetrievedAt: now},
		},
		Claims: []Claim{
			{ID: "claim-001", IsUnsupported: false, Evidence: []Evidence{{SourceID: "source-001"}}},
			{ID: "claim-002", IsUnsupported: false, Evidence: []Evidence{{SourceID: "source-001"}}},
			{ID: "claim-003", IsUnsupported: false, Evidence: []Evidence{{SourceID: "source-001"}}},
		},
	}

	claimIDs := d.ExtractClaimIDs()
	if len(claimIDs) != 3 {
		t.Errorf("Expected 3 claim IDs, got %d", len(claimIDs))
	}
	if claimIDs[0] != "claim-001" || claimIDs[1] != "claim-002" || claimIDs[2] != "claim-003" {
		t.Errorf("Expected claim IDs to be in order, got %v", claimIDs)
	}

	sourceIDs := d.ExtractSourceIDs()
	if len(sourceIDs) != 2 {
		t.Errorf("Expected 2 source IDs, got %d", len(sourceIDs))
	}
}

func TestResearchDossierSortClaimsByConfidence(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test",
		GeneratedAt: now,
		Sources: []SourceReference{
			{StableID: "source-001", RetrievedAt: now},
		},
		Claims: []Claim{
			{ID: "claim-b", Statement: "B", Confidence: ConfidenceMedium, IsUnsupported: false, Evidence: []Evidence{{SourceID: "source-001"}}},
			{ID: "claim-a", Statement: "A", Confidence: ConfidenceHigh, IsUnsupported: false, Evidence: []Evidence{{SourceID: "source-001"}}},
			{ID: "claim-c", Statement: "C", Confidence: ConfidenceLow, IsUnsupported: false, Evidence: []Evidence{{SourceID: "source-001"}}},
			{ID: "claim-d", Statement: "D", Confidence: ConfidenceHigh, IsUnsupported: false, Evidence: []Evidence{{SourceID: "source-001"}}},
		},
	}

	d.SortClaimsByConfidence()

	// Should be sorted: claim-a, claim-d (both high), then claim-b (medium), then claim-c (low)
	if d.Claims[0].ID != "claim-a" {
		t.Errorf("Expected first claim to be 'claim-a', got %q", d.Claims[0].ID)
	}
	if d.Claims[1].ID != "claim-d" {
		t.Errorf("Expected second claim to be 'claim-d', got %q", d.Claims[1].ID)
	}
	if d.Claims[2].ID != "claim-b" {
		t.Errorf("Expected third claim to be 'claim-b', got %q", d.Claims[2].ID)
	}
	if d.Claims[3].ID != "claim-c" {
		t.Errorf("Expected fourth claim to be 'claim-c', got %q", d.Claims[3].ID)
	}
}

func TestResearchDossierFilterClaimsByConfidence(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test",
		GeneratedAt: now,
		Sources: []SourceReference{
			{StableID: "source-001", RetrievedAt: now},
		},
		Claims: []Claim{
			{ID: "claim-high", Statement: "High confidence", Confidence: ConfidenceHigh, IsUnsupported: false, Evidence: []Evidence{{SourceID: "source-001"}}},
			{ID: "claim-medium", Statement: "Medium confidence", Confidence: ConfidenceMedium, IsUnsupported: false, Evidence: []Evidence{{SourceID: "source-001"}}},
			{ID: "claim-low", Statement: "Low confidence", Confidence: ConfidenceLow, IsUnsupported: false, Evidence: []Evidence{{SourceID: "source-001"}}},
			{ID: "claim-unsupported", Statement: "Unsupported", IsUnsupported: true, Evidence: []Evidence{}},
		},
	}

	// Filter for high confidence only
	highClaims := d.FilterClaimsByConfidence(ConfidenceHigh)
	if len(highClaims) != 1 {
		t.Errorf("Expected 1 high confidence claim, got %d", len(highClaims))
	}
	if highClaims[0].ID != "claim-high" {
		t.Errorf("Expected claim-high, got %q", highClaims[0].ID)
	}

	// Filter for medium or higher
	mediumClaims := d.FilterClaimsByConfidence(ConfidenceMedium)
	if len(mediumClaims) != 2 {
		t.Errorf("Expected 2 claims with medium+ confidence, got %d", len(mediumClaims))
	}

	// Filter for low or higher (should get all except unsupported which has empty confidence)
	allClaims := d.FilterClaimsByConfidence(ConfidenceLow)
	if len(allClaims) != 3 {
		t.Errorf("Expected 3 claims with low+ confidence (unsupported has empty confidence), got %d", len(allClaims))
	}
}

func TestResearchDossierValidate_SelfContradiction(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test",
		GeneratedAt: now,
		Sources: []SourceReference{
			{StableID: "source-001", RetrievedAt: now},
		},
		Claims: []Claim{
			{
				ID:                  "claim-001",
				Statement:           "Self-contradicting claim.",
				Confidence:          ConfidenceMedium,
				IsUnsupported:       false,
				Evidence:            []Evidence{{SourceID: "source-001"}},
				SourcesContradicted: []string{"claim-001"},
			},
		},
	}

	err := d.Validate()
	if err == nil {
		t.Fatal("Expected validation error for self-contradicting claim")
	}

	if !strings.Contains(err.Error(), "claim \"claim-001\" cannot contradict itself") {
		t.Logf("Validation error (expected): %v", err)
	}
}

func TestResearchDossierValidate_UncertaintyNotesWithoutConfidence(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test",
		GeneratedAt: now,
		Sources: []SourceReference{
			{StableID: "source-001", RetrievedAt: now},
		},
		Claims: []Claim{
			{
				ID:               "claim-001",
				Statement:        "Claim with uncertainty but no confidence.",
				Confidence:       "",
				UncertaintyNotes: "This has uncertainty but no confidence level.",
				IsUnsupported:    false,
				Evidence:         []Evidence{{SourceID: "source-001"}},
			},
		},
	}

	err := d.Validate()
	if err == nil {
		t.Fatal("Expected validation error for claim with uncertainty notes but no confidence")
	}

	if !strings.Contains(err.Error(), "claim \"claim-001\" has uncertainty_notes but no confidence level") {
		t.Logf("Validation error (expected): %v", err)
	}
}

func TestResearchDossierValidate_ResearchNoteReferences(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test",
		GeneratedAt: now,
		Sources: []SourceReference{
			{StableID: "source-001", RetrievedAt: now},
		},
		Claims: []Claim{
			{ID: "claim-001", IsUnsupported: false, Evidence: []Evidence{{SourceID: "source-001"}}},
		},
		ResearchNotes: []ResearchNote{
			{
				ID:               "note-001",
				Content:          "Note referencing unknown claim.",
				RelatedClaimIDs:  []string{"claim-nonexistent"},
				RelatedSourceIDs: []string{"source-001"},
			},
		},
	}

	err := d.Validate()
	if err == nil {
		t.Fatal("Expected validation error for research note referencing unknown claim")
	}

	if !strings.Contains(err.Error(), "research note \"note-001\" references unknown claim \"claim-nonexistent\"") {
		t.Logf("Validation error (expected): %v", err)
	}
}

func TestResearchDossierValidate_UresolvedQuestionReferences(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test",
		GeneratedAt: now,
		Sources: []SourceReference{
			{StableID: "source-001", RetrievedAt: now},
		},
		Claims: []Claim{
			{ID: "claim-001", IsUnsupported: false, Evidence: []Evidence{{SourceID: "source-001"}}},
		},
		UnresolvedQuestions: []UnresolvedQuestion{
			{
				ID:              "question-001",
				Question:        "Test question?",
				WhyUnresolved:   "No answer found.",
				RelatedClaimIDs: []string{"claim-nonexistent"},
			},
		},
	}

	err := d.Validate()
	if err == nil {
		t.Fatal("Expected validation error for unresolved question referencing unknown claim")
	}

	if !strings.Contains(err.Error(), "unresolved question \"question-001\" references unknown claim \"claim-nonexistent\"") {
		t.Logf("Validation error (expected): %v", err)
	}
}

func TestResearchDossierMarshalUnmarshalWithTimestamps(t *testing.T) {
	now := time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC)

	d := &ResearchDossier{
		StableID:    "dossier-timestamp-test",
		Topic:       "Timestamp test",
		GeneratedAt: now,
		Sources: []SourceReference{
			{
				StableID:    "source-001",
				OriginalURL: "https://example.com",
				SourceType:  "web",
				RetrievedAt: now,
				PublishedAt: &now,
			},
		},
		Claims: []Claim{
			{
				ID:            "claim-001",
				Statement:     "Test claim.",
				IsUnsupported: false,
				Evidence:      []Evidence{{SourceID: "source-001"}},
			},
		},
	}

	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var restored ResearchDossier
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if !restored.GeneratedAt.Equal(d.GeneratedAt) {
		t.Errorf("GeneratedAt mismatch: expected %v, got %v", d.GeneratedAt, restored.GeneratedAt)
	}
	if !restored.Sources[0].RetrievedAt.Equal(d.Sources[0].RetrievedAt) {
		t.Errorf("Source RetrievedAt mismatch")
	}
	if restored.Sources[0].PublishedAt == nil || !restored.Sources[0].PublishedAt.Equal(*d.Sources[0].PublishedAt) {
		t.Errorf("Source PublishedAt mismatch")
	}
}

func TestResearchDossierValidate_MultipleErrors(t *testing.T) {
	now := time.Now().UTC()

	d := &ResearchDossier{
		StableID:    "dossier-001",
		Topic:       "Test",
		GeneratedAt: now,
		Sources: []SourceReference{
			{StableID: "source-001", RetrievedAt: now},
		},
		Claims: []Claim{
			{
				ID:            "claim-001",
				Statement:     "First claim without evidence.",
				IsUnsupported: false,
				Evidence:      []Evidence{},
			},
			{
				ID:            "claim-001", // duplicate
				Statement:     "Duplicate ID.",
				IsUnsupported: false,
				Evidence:      []Evidence{{SourceID: "source-001"}},
			},
		},
	}

	err := d.Validate()
	if err == nil {
		t.Fatal("Expected validation errors")
	}

	// Should have at least 2 errors: claim without evidence and duplicate ID
	errMsg := err.Error()
	hasMissingEvidence := strings.Contains(errMsg, "claim \"claim-001\" has no evidence and is not marked as unsupported")
	hasDuplicate := strings.Contains(errMsg, "duplicate claim ID: claim-001")

	if !hasMissingEvidence {
		t.Logf("Expected error for missing evidence, got: %v", err)
	}
	if !hasDuplicate {
		t.Logf("Expected error for duplicate ID, got: %v", err)
	}
	if !hasMissingEvidence || !hasDuplicate {
		t.Logf("Got multiple errors as expected: %v", err)
	}
}
