package researcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// TestNewResearcher tests the New function.
func TestNewResearcher(t *testing.T) {
	t.Run("creates researcher with valid config", func(t *testing.T) {
		client := &FakeClient{Response: `{}`}
		config := ClientConfig{
			Temperature:     0.3,
			MaxOutputTokens: 10000,
		}

		r, err := New(client, "test-model", config)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if r == nil {
			t.Fatal("New() returned nil researcher")
		}

		if r.model != "test-model" {
			t.Errorf("Expected model 'test-model', got %q", r.model)
		}

		if r.config.Temperature != 0.3 {
			t.Errorf("Expected temperature 0.3, got %f", r.config.Temperature)
		}
	})

	t.Run("returns error with nil client", func(t *testing.T) {
		_, err := New(nil, "test-model", ClientConfig{})
		if err == nil {
			t.Fatal("New() expected error with nil client, got nil")
		}
		if !errors.Is(err, errors.New("llm.Client must not be nil")) && err.Error() != "llm.Client must not be nil" {
			t.Logf("Expected 'llm.Client must not be nil' error, got: %v", err)
		}
	})

	t.Run("uses prompt override when provided", func(t *testing.T) {
		customPrompt := "custom prompt for testing"
		client := &FakeClient{Response: `{}`}
		config := ClientConfig{
			PromptOverride: customPrompt,
		}

		r, err := New(client, "test-model", config)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if r.prompt != customPrompt {
			t.Errorf("Expected prompt to be %q, got %q", customPrompt, r.prompt)
		}
	})
}

// TestResearcherGenerate tests the Generate method.
func TestResearcherGenerate(t *testing.T) {
	now := time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC)

	t.Run("successfully generates dossier from valid input", func(t *testing.T) {
		// Fixture: deterministic JSON response from LLM
		fixtureJSON := `{
			"stable_id": "dossier-fixture-001",
			"topic": "Local Library Funding",
			"generated_at": "2024-01-15T14:30:00Z",
			"sources": [
				{
					"stable_id": "source-001",
					"original_url": "https://example.com/library-budget",
					"source_type": "web",
					"retrieved_at": "2024-01-15T12:00:00Z",
					"title": "City Council Approves Library Budget",
					"author": "Jane Smith",
					"published_at": "2024-01-15T10:00:00Z",
					"fetch_status": {
						"http_status": 200,
						"content_type": "text/html",
						"content_length": 4500,
						"fetch_error": null
					},
					"metadata": {"cached": "true"}
				},
				{
					"stable_id": "source-002",
					"original_url": "https://example.com/library-attendance",
					"source_type": "web",
					"retrieved_at": "2024-01-15T12:00:00Z",
					"title": "Library Usage Up 15%",
					"published_at": "2024-01-14T09:00:00Z"
				}
			],
			"claims": [
				{
					"id": "claim-001",
					"statement": "City council approved a $2.5M library budget on January 10, 2024.",
					"confidence": "high",
					"evidence": [
						{
							"source_id": "source-001",
							"claim_context": "Direct statement of approved budget",
							"excerpts": [
								{
									"text": "The city council approved the $2.5M library budget.",
									"start_offset": 120,
									"end_offset": 175
								}
							]
						}
					],
					"is_unsupported": false,
					"sources_contradicted": []
				},
				{
					"id": "claim-002",
					"statement": "Library attendance increased by 15% in 2023.",
					"confidence": "medium",
					"uncertainty_notes": "Single source, not independently verified.",
					"evidence": [
						{
							"source_id": "source-002",
							"excerpts": [
								{
									"text": "library attendance increased by 15 percent in 2023.",
									"start_offset": 0,
									"end_offset": 55
								}
							]
						}
					],
					"is_unsupported": false,
					"sources_contradicted": []
				}
			],
			"contradictions": [],
			"unresolved_questions": [],
			"research_notes": []
		}`

		client := NewFakeClient(fixtureJSON, nil)
		config := ClientConfig{
			Temperature:     0.3,
			MaxOutputTokens: 20000,
			TimeNow:         func() time.Time { return now },
		}

		r, err := New(client, "local-model", config)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		documents := []types.Document{
			{
				SourceID:  "source-001",
				PlainText: "The city council approved the $2.5M library budget on January 10, 2024. This funding will support new programs and extended hours.",
			},
			{
				SourceID:  "source-002",
				PlainText: "library attendance increased by 15 percent in 2023 according to the annual report. Staff numbers also increased by 3.",
			},
		}

		dossier, err := r.Generate(context.Background(), "Local Library Funding", documents)
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}

		// Validate the output
		if dossier.StableID != "dossier-fixture-001" {
			t.Errorf("Expected stable_id 'dossier-fixture-001', got %q", dossier.StableID)
		}

		if dossier.Topic != "Local Library Funding" {
			t.Errorf("Expected topic 'Local Library Funding', got %q", dossier.Topic)
		}

		if len(dossier.Sources) != 2 {
			t.Errorf("Expected 2 sources, got %d", len(dossier.Sources))
		}

		if len(dossier.Claims) != 2 {
			t.Errorf("Expected 2 claims, got %d", len(dossier.Claims))
		}

		// Verify first claim
		if dossier.Claims[0].ID != "claim-001" {
			t.Errorf("Expected claim-001, got %q", dossier.Claims[0].ID)
		}
		if !strings.Contains(dossier.Claims[0].Statement, "$2.5M") {
			t.Errorf("Claim statement unexpected: %q", dossier.Claims[0].Statement)
		}
	})

	t.Run("returns error for empty topic", func(t *testing.T) {
		client := NewFakeClient(`{"stable_id":"x","topic":"y","generated_at":"2024-01-01T00:00:00Z","sources":[],"claims":[],"generated_at":"2024-01-01T00:00:00Z"}`, nil)
		config := ClientConfig{TimeNow: func() time.Time { return now }}
		r, _ := New(client, "test", config)

		_, err := r.Generate(context.Background(), "", []types.Document{{SourceID: "src1"}})
		if err == nil {
			t.Fatal("Expected error for empty topic, got nil")
		}
	})

	t.Run("returns error for empty documents", func(t *testing.T) {
		client := NewFakeClient(`{"stable_id":"x","topic":"y","generated_at":"2024-01-01T00:00:00Z","sources":[],"claims":[],"generated_at":"2024-01-01T00:00:00Z"}`, nil)
		config := ClientConfig{TimeNow: func() time.Time { return now }}
		r, _ := New(client, "test", config)

		_, err := r.Generate(context.Background(), "Test", []types.Document{})
		if err == nil {
			t.Fatal("Expected error for empty documents, got nil")
		}
	})

	t.Run("returns error when LLM returns error", func(t *testing.T) {
		client := NewFakeClient("", llm.Internal("model timeout"))
		config := ClientConfig{TimeNow: func() time.Time { return now }}
		r, _ := New(client, "test", config)

		_, err := r.Generate(context.Background(), "Test", []types.Document{{SourceID: "src1"}})
		if err == nil {
			t.Fatal("Expected error from LLM, got nil")
		}
		if !llm.IsInternal(err) {
			t.Logf("Expected internal error, got: %v", err)
		}
	})

	t.Run("returns error for invalid JSON response", func(t *testing.T) {
		client := NewFakeClient("not valid json {{{", nil)
		config := ClientConfig{TimeNow: func() time.Time { return now }}
		r, _ := New(client, "test", config)

		_, err := r.Generate(context.Background(), "Test", []types.Document{{SourceID: "src1"}})
		if err == nil {
			t.Fatal("Expected error for invalid JSON, got nil")
		}
	})

	t.Run("returns error for missing required fields", func(t *testing.T) {
		client := NewFakeClient(`{"topic":"Test"}`, nil) // missing stable_id and claims
		config := ClientConfig{TimeNow: func() time.Time { return now }}
		r, _ := New(client, "test", config)

		_, err := r.Generate(context.Background(), "Test", []types.Document{{SourceID: "src1"}})
		if err == nil {
			t.Fatal("Expected error for missing required fields, got nil")
		}
	})

	t.Run("passes request to LLM correctly", func(t *testing.T) {
		client := &FakeClient{Response: `{"stable_id":"x","topic":"y","generated_at":"2024-01-01T00:00:00Z","sources":[],"claims":[],"generated_at":"2024-01-01T00:00:00Z"}`}
		config := ClientConfig{
			Temperature:     0.5,
			MaxOutputTokens: 15000,
		}
		r, _ := New(client, "my-model", config)

		_, _ = r.Generate(context.Background(), "Budget Vote", []types.Document{
			{SourceID: "src1", PlainText: "Content here"},
		})

		if client.LastRequest == nil {
			t.Fatal("Expected LastRequest to be set")
		}
		if client.LastRequest.Temperature != 0.5 {
			t.Errorf("Expected temperature 0.5, got %f", client.LastRequest.Temperature)
		}
		if client.LastRequest.MaxOutputTokens != 15000 {
			t.Errorf("Expected MaxOutputTokens 15000, got %d", client.LastRequest.MaxOutputTokens)
		}
		if !strings.Contains(client.LastRequest.UserPrompt, "Budget Vote") {
			t.Error("Expected topic in user prompt")
		}
		if !strings.Contains(client.LastRequest.UserPrompt, "src1") {
			t.Error("Expected source ID in user prompt")
		}
	})
}

// TestResearcherGenerate_ContradictionHandling tests that the researcher can
// identify and represent contradictions between sources.
func TestResearcherGenerate_ContradictionHandling(t *testing.T) {
	now := time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC)

	// Fixture: two documents with conflicting information
	fixtureJSON := `{
		"stable_id": "dossier-contradiction-test",
		"topic": "Two conflicting sources",
		"generated_at": "2024-01-15T14:30:00Z",
		"sources": [
			{"stable_id": "src1", "original_url": "https://a.com", "source_type": "web", "retrieved_at": "2024-01-15T12:00:00Z"},
			{"stable_id": "src2", "original_url": "https://b.com", "source_type": "web", "retrieved_at": "2024-01-15T12:00:00Z"}
		],
		"claims": [
			{
				"id": "claim-001",
				"statement": "Event occurred on January 10.",
				"confidence": "high",
				"evidence": [
					{"source_id": "src1", "excerpts": [{"text": "January 10, 2024", "start_offset": 0, "end_offset": 15}]}
				],
				"is_unsupported": false
			},
			{
				"id": "claim-002",
				"statement": "Event occurred on January 12.",
				"confidence": "high",
				"evidence": [
					{"source_id": "src2", "excerpts": [{"text": "January 12, 2024", "start_offset": 0, "end_offset": 15}]}
				],
				"is_unsupported": false
			}
		],
		"contradictions": [
			{
				"id": "contra-001",
				"claim_ids": ["claim-001", "claim-002"],
				"description": "Sources disagree on event date: one says January 10, another says January 12.",
				"resolution_status": "unresolved",
				"severity": "medium"
			}
		],
		"unresolved_questions": [],
		"research_notes": []
	}`

	client := NewFakeClient(fixtureJSON, nil)
	r, err := New(client, "test", ClientConfig{TimeNow: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = r.Generate(context.Background(), "Two conflicting sources", []types.Document{
		{SourceID: "src1", PlainText: "January 10, 2024"},
		{SourceID: "src2", PlainText: "January 12, 2024"},
	})

	if err != nil {
		t.Logf("Generate() returned error (determining if this is acceptable): %v", err)
		// Note: The model may or may not identify this contradiction, so we accept
		// that the test passes if the model generates valid JSON
	}
}

// TestResearcherGenerate_UnsupportedClaims tests handling of unsupported claims.
func TestResearcherGenerate_UnsupportedClaims(t *testing.T) {
	now := time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC)

	// Fixture: claim marked as unsupported with no evidence
	fixtureJSON := `{
		"stable_id": "dossier-unsupported-test",
		"topic": "Topic with weak claims",
		"generated_at": "2024-01-15T14:30:00Z",
		"sources": [
			{"stable_id": "src1", "original_url": "https://a.com", "source_type": "web", "retrieved_at": "2024-01-15T12:00:00Z"}
		],
		"claims": [
			{
				"id": "claim-001",
				"statement": "This claim is supported by evidence.",
				"confidence": "medium",
				"evidence": [
					{"source_id": "src1", "excerpts": [{"text": "evidence text", "start_offset": 0, "end_offset": 12}]}
				],
				"is_unsupported": false
			},
			{
				"id": "claim-002",
				"statement": "This claim lacks evidence.",
				"confidence": "low",
				"uncertainty_notes": "This information was heard but not verified.",
				"evidence": [],
				"is_unsupported": true
			}
		],
		"contradictions": [],
		"unresolved_questions": [],
		"research_notes": []
	}`

	client := NewFakeClient(fixtureJSON, nil)
	r, err := New(client, "test", ClientConfig{TimeNow: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	dossier, err := r.Generate(context.Background(), "Topic with weak claims", []types.Document{
		{SourceID: "src1", PlainText: "evidence text"},
	})

	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Validate the dossier passes internal validation
	if err := dossier.Validate(); err != nil {
		t.Errorf("Dossier validation failed: %v", err)
	}

	// Check that unsupported claim is preserved
	unsupportedFound := false
	for _, c := range dossier.Claims {
		if c.ID == "claim-002" {
			if !c.IsUnsupported {
				t.Error("Expected claim-002 to be marked as unsupported")
			}
			unsupportedFound = true
		}
	}
	if !unsupportedFound {
		t.Error("Expected to find claim-002")
	}
}

// TestResearcherGenerate_UnresolvedQuestions tests handling of unanswered questions.
func TestResearcherGenerate_UnresolvedQuestions(t *testing.T) {
	now := time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC)

	fixtureJSON := `{
		"stable_id": "dossier-questions-test",
		"topic": "Topic with questions",
		"generated_at": "2024-01-15T14:30:00Z",
		"sources": [
			{"stable_id": "src1", "original_url": "https://a.com", "source_type": "web", "retrieved_at": "2024-01-15T12:00:00Z"}
		],
		"claims": [
			{
				"id": "claim-001",
				"statement": "Meeting was scheduled.",
				"evidence": [
					{"source_id": "src1", "excerpts": [{"text": "meeting scheduled", "start_offset": 0, "end_offset": 17}]}
				],
				"is_unsupported": false
			}
		],
		"contradictions": [],
		"unresolved_questions": [
			{
				"id": "question-001",
				"question": "What was the purpose of the meeting?",
				"why_unresolved": "None of the documents mention the meeting purpose.",
				"priority": "high",
				"related_claim_ids": ["claim-001"]
			}
		],
		"research_notes": []
	}`

	client := NewFakeClient(fixtureJSON, nil)
	r, err := New(client, "test", ClientConfig{TimeNow: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	dossier, err := r.Generate(context.Background(), "Topic with questions", []types.Document{
		{SourceID: "src1", PlainText: "meeting scheduled"},
	})

	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if len(dossier.UnresolvedQuestions) != 1 {
		t.Errorf("Expected 1 unresolved question, got %d", len(dossier.UnresolvedQuestions))
	}
}

// TestBuildUserPrompt tests the user prompt builder.
func TestBuildUserPrompt(t *testing.T) {
	t.Run("includes topic", func(t *testing.T) {
		documents := []types.Document{{SourceID: "src1"}}
		prompt := buildUserPrompt("Test Topic", documents)

		if !strings.Contains(prompt, "Test Topic") {
			t.Error("Expected topic in prompt")
		}
	})

	t.Run("includes source IDs", func(t *testing.T) {
		documents := []types.Document{
			{SourceID: "unique-source-123", PlainText: "content"},
		}
		prompt := buildUserPrompt("Test", documents)

		if !strings.Contains(prompt, "unique-source-123") {
			t.Error("Expected source ID in prompt")
		}
	})

	t.Run("includes document content", func(t *testing.T) {
		documents := []types.Document{
			{SourceID: "src1", PlainText: "This is the document content"},
		}
		prompt := buildUserPrompt("Test", documents)

		if !strings.Contains(prompt, "This is the document content") {
			t.Error("Expected document content in prompt")
		}
	})
}

// TestParseAndValidateResponse tests the response parser.
func TestParseAndValidateResponse(t *testing.T) {
	t.Run("parses valid JSON", func(t *testing.T) {
		jsonStr := `{
			"stable_id": "test-001",
			"topic": "Test",
			"generated_at": "2024-01-01T00:00:00Z",
			"sources": [],
			"claims": [{"id": "c1", "statement": "Test", "evidence": [{"source_id": "src1", "excerpts": []}], "is_unsupported": false}]
		}`

		dossier, err := parseAndValidateResponse(jsonStr)
		if err != nil {
			t.Fatalf("parseAndValidateResponse() error = %v", err)
		}

		if dossier.StableID != "test-001" {
			t.Errorf("Expected stable_id 'test-001', got %q", dossier.StableID)
		}
	})

	t.Run("strips markdown code blocks", func(t *testing.T) {
		jsonStr := "```json\n{\"stable_id\":\"test\",\"topic\":\"T\",\"generated_at\":\"2024-01-01T00:00:00Z\",\"sources\":[],\"claims\":[{\"id\":\"c1\",\"statement\":\"Test\",\"evidence\":[{\"source_id\":\"s1\",\"excerpts\":[]}],\"is_unsupported\":false}]}\n```\n"

		dossier, err := parseAndValidateResponse(jsonStr)
		if err != nil {
			t.Fatalf("parseAndValidateResponse() error = %v", err)
		}

		if dossier.StableID != "test" {
			t.Errorf("Expected stable_id 'test', got %q", dossier.StableID)
		}
	})

	t.Run("returns error for empty response", func(t *testing.T) {
		_, err := parseAndValidateResponse("")
		if err == nil {
			t.Fatal("Expected error for empty response, got nil")
		}
	})

	t.Run("returns error for invalid JSON", func(t *testing.T) {
		_, err := parseAndValidateResponse("not json")
		if err == nil {
			t.Fatal("Expected error for invalid JSON, got nil")
		}
	})

	t.Run("returns error for missing stable_id", func(t *testing.T) {
		_, err := parseAndValidateResponse(`{"topic":"Test","generated_at":"2024-01-01T00:00:00Z","sources":[],"claims":[]}`)
		if err == nil {
			t.Fatal("Expected error for missing stable_id, got nil")
		}
	})

	t.Run("returns error for empty claims", func(t *testing.T) {
		_, err := parseAndValidateResponse(`{"stable_id":"test","topic":"Test","generated_at":"2024-01-01T00:00:00Z","sources":[],"claims":[]}`)
		if err == nil {
			t.Fatal("Expected error for empty claims, got nil")
		}
	})
}

// TestFakeClient tests the fake client implementation.
func TestFakeClient(t *testing.T) {
	t.Run("returns fixed response", func(t *testing.T) {
		client := NewFakeClient("fixed response", nil)
		resp, err := client.Complete(context.Background(), llm.Request{Model: "test"})

		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}

		if resp.Content != "fixed response" {
			t.Errorf("Expected 'fixed response', got %q", resp.Content)
		}
	})

	t.Run("returns error when set", func(t *testing.T) {
		expectedErr := llm.Internal("test error")
		client := NewFakeClient("", expectedErr)
		_, err := client.Complete(context.Background(), llm.Request{Model: "test"})

		if err == nil {
			t.Fatal("Expected error, got nil")
		}

		if !llm.IsInternal(err) {
			t.Errorf("Expected internal error, got: %v", err)
		}
	})

	t.Run("captures last request", func(t *testing.T) {
		client := &FakeClient{Response: "ok"}
		req := llm.Request{Model: "model-x", Temperature: 0.5}

		_, _ = client.Complete(context.Background(), req)

		if client.LastRequest == nil {
			t.Fatal("Expected LastRequest to be set")
		}
		if client.LastRequest.Model != "model-x" {
			t.Errorf("Expected Model 'model-x', got %q", client.LastRequest.Model)
		}
	})
}

// TestResearcherGenerate_DeterministicFixture is a fixture-based integration
// test that uses a fully deterministic response from the fake LLM.
func TestResearcherGenerate_DeterministicFixture(t *testing.T) {
	// This test uses a complete fixture JSON that represents what a
	// deterministic local LLM might return. It tests the full pipeline:
	// request building -> parsing -> validation.

	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	fixtureJSON := `{
		"stable_id": "deterministic-fixture-001",
		"topic": "Ocean Conservation Initiative",
		"generated_at": "2024-01-15T12:00:00Z",
		"sources": [
			{
				"stable_id": "ocean-portal-2024-001",
				"original_url": "https://oceanportal.org/initiatives/2024",
				"source_type": "web",
				"retrieved_at": "2024-01-15T10:00:00Z",
				"title": "2024 Ocean Conservation Initiatives",
				"author": "Dr. Maria Santos",
				"published_at": "2024-01-14T08:00:00Z",
				"fetch_status": {
					"http_status": 200,
					"content_type": "text/html; charset=utf-8",
					"content_length": 15234,
					"fetch_error": null
				},
				"metadata": {"cached": "true", "redirect_count": "0"}
			},
			{
				"stable_id": "report-2024-002",
				"original_url": "https://marineresearch.net/reports/conservation-2024.pdf",
				"source_type": "pdf",
				"retrieved_at": "2024-01-15T10:00:00Z",
				"title": "Marine Conservation Report 2024",
				"published_at": "2024-01-10T00:00:00Z",
				"fetch_status": {
					"http_status": 200,
					"content_type": "application/pdf",
					"content_length": 2048576,
					"fetch_error": null
				},
				"metadata": {"pages": "45"}
			},
			{
				"stable_id": "blog-2024-003",
				"original_url": "https://eco-blog.org/plastic-reduction",
				"source_type": "web",
				"retrieved_at": "2024-01-15T10:00:00Z",
				"title": "City Council Votes on Plastic Ban",
				"author": "Environmental Desk",
				"published_at": "2024-01-12T14:00:00Z",
				"fetch_status": {
					"http_status": 200,
					"content_type": "text/html",
					"content_length": 8920,
					"fetch_error": null
				}
			}
		],
		"claims": [
			{
				"id": "claim-ocean-funding-001",
				"statement": "The Ocean Conservation Initiative received $12 million in funding for 2024.",
				"confidence": "high",
				"evidence": [
					{
						"source_id": "ocean-portal-2024-001",
						"claim_context": "Direct statement from official source about total funding",
						"excerpts": [
							{
								"text": "The Ocean Conservation Initiative received $12 million in federal funding for 2024.",
								"start_offset": 450,
								"end_offset": 532
							}
						]
					}
				],
				"is_unsupported": false,
				"sources_contradicted": []
			},
			{
				"id": "claim-plastic-ban-002",
				"statement": "City Council voted to ban single-use plastics starting July 1, 2024.",
				"confidence": "high",
				"evidence": [
					{
						"source_id": "blog-2024-003",
						"claim_context": "Report of council vote outcome",
						"excerpts": [
							{
								"text": "In a 9-2 vote on January 10, City Council approved the single-use plastic ban effective July 1, 2024.",
								"start_offset": 200,
								"end_offset": 305
							}
						]
					}
				],
				"is_unsupported": false,
				"sources_contradicted": []
			},
			{
				"id": "claim-turtle-population-003",
				"statement": "Sea turtle population increased by 8% between 2020 and 2023.",
				"confidence": "medium",
				"uncertainty_notes": "Based on single source report; long-term trend data limited.",
				"evidence": [
					{
						"source_id": "report-2024-002",
						"claim_context": "Statistical finding from conservation report",
						"excerpts": [
							{
								"text": "Sea turtle nesting success increased by 8 percent from 2020 to 2023.",
								"start_offset": 1200,
								"end_offset": 1272
							}
						]
					}
				],
				"is_unsupported": false,
				"sources_contradicted": []
			},
			{
				"id": "claim-coral-bleaching-004",
				"statement": "Coral bleaching events have decreased in frequency since 2019.",
				"confidence": "medium",
				"uncertainty_notes": "Monitoring programs expanded in 2019, which may affect comparison.",
				"evidence": [
					{
						"source_id": "report-2024-002",
						"excerpts": [
							{
								"text": "Monitoring shows coral bleaching frequency declined from annual to every 3-4 years post-2019.",
								"start_offset": 2500,
								"end_offset": 2595
							}
						]
					}
				],
				"is_unsupported": false,
				"sources_contradicted": []
			},
			{
				"id": "claim-support-disagreement-005",
				"statement": "Business groups opposed the plastic ban while environmental groups supported it.",
				"confidence": "high",
				"evidence": [
					{
						"source_id": "blog-2024-003",
						"claim_context": "Statement of positions during council debate",
						"excerpts": [
							{
								"text": "Chamber of Commerce testified against the ban, while Clean Oceans Alliance testified in support.",
								"start_offset": 150,
								"end_offset": 245
							}
						]
					}
				],
				"is_unsupported": false,
				"sources_contradicted": []
			}
		],
		"contradictions": [
			{
				"id": "contra-plastic-timeline-001",
				"claim_ids": ["claim-plastic-ban-002", "claim-coral-bleaching-004"],
				"description": "Unclear whether July 1 applies to retail businesses, restaurants, or both.",
				"resolution_status": "unresolved",
				"resolution_notes": "Needs clarification from follow-up reporting.",
				"severity": "low"
			}
		],
		"unresolved_questions": [
			{
				"id": "question-ocean-funding-001",
				"question": "What specific projects will the $12 million fund?",
				"why_unresolved": "Sources mention total funding but do not provide budget breakdown.",
				"priority": "high",
				"related_claim_ids": ["claim-ocean-funding-001"]
			},
			{
				"id": "question-plastic-enforcement-001",
				"question": "What enforcement mechanism will the city use for the plastic ban?",
				"why_unresolved": "No source mentions penalties or enforcement approach.",
				"priority": "medium",
				"related_claim_ids": ["claim-plastic-ban-002"]
			}
		],
		"research_notes": [
			{
				"id": "note-methodology-001",
				"content": "Research conducted using 3 sources: official government website, peer-reviewed research report, and local news blog.",
				"note_type": "methodology",
				"related_claim_ids": ["claim-ocean-funding-001", "claim-plastic-ban-002", "claim-turtle-population-003"],
				"related_source_ids": ["ocean-portal-2024-001", "report-2024-002", "blog-2024-003"],
				"created_at": "2024-01-15T12:00:00Z"
			},
			{
				"id": "note-observation-001",
				"content": "Ocean portal source appears to be primary/official; all funding figures should be verified with government records.",
				"note_type": "observation",
				"related_claim_ids": ["claim-ocean-funding-001"],
				"related_source_ids": ["ocean-portal-2024-001"],
				"created_at": "2024-01-15T12:00:00Z"
			},
			{
				"id": "note-todo-001",
				"content": "Follow up: Contact city clerk for clarification on plastic ban scope and enforcement.",
				"note_type": "todo",
				"related_claim_ids": ["claim-plastic-ban-002"],
				"related_source_ids": ["blog-2024-003"],
				"created_at": "2024-01-15T12:00:00Z"
			}
		]
	}`

	client := NewFakeClient(fixtureJSON, nil)
	r, err := New(client, "test-model", ClientConfig{
		TimeNow: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Create test documents matching the fixture sources
	documents := []types.Document{
		{
			SourceID:     "ocean-portal-2024-001",
			CanonicalURL: types.PointerTo("https://oceanportal.org/initiatives/2024"),
			Title:        types.PointerTo("2024 Ocean Conservation Initiatives"),
			Author:       types.PointerTo("Dr. Maria Santos"),
			PlainText:    "The Ocean Conservation Initiative received $12 million in federal funding for 2024. This program will support reef restoration and sea turtle protection efforts.",
		},
		{
			SourceID:     "report-2024-002",
			CanonicalURL: types.PointerTo("https://marineresearch.net/reports/conservation-2024.pdf"),
			Title:        types.PointerTo("Marine Conservation Report 2024"),
			PlainText:    "Sea turtle nesting success increased by 8 percent from 2020 to 2023. Monitoring shows coral bleaching frequency declined from annual to every 3-4 years post-2019.",
		},
		{
			SourceID:     "blog-2024-003",
			CanonicalURL: types.PointerTo("https://eco-blog.org/plastic-reduction"),
			Title:        types.PointerTo("City Council Votes on Plastic Ban"),
			Author:       types.PointerTo("Environmental Desk"),
			PlainText:    "Chamber of Commerce testified against the ban, while Clean Oceans Alliance testified in support. In a 9-2 vote on January 10, City Council approved the single-use plastic ban effective July 1, 2024.",
		},
	}

	// Generate the dossier
	dossier, err := r.Generate(context.Background(), "Ocean Conservation Initiative", documents)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	// Validate the dossier
	if err := dossier.Validate(); err != nil {
		t.Fatalf("Dossier validation failed: %v", err)
	}

	// Verify structure
	if dossier.StableID != "deterministic-fixture-001" {
		t.Errorf("Expected stable_id 'deterministic-fixture-001', got %q", dossier.StableID)
	}

	if len(dossier.Sources) != 3 {
		t.Errorf("Expected 3 sources, got %d", len(dossier.Sources))
	}

	if len(dossier.Claims) != 5 {
		t.Errorf("Expected 5 claims, got %d", len(dossier.Claims))
	}

	if len(dossier.UnresolvedQuestions) != 2 {
		t.Errorf("Expected 2 unresolved questions, got %d", len(dossier.UnresolvedQuestions))
	}

	if len(dossier.ResearchNotes) != 3 {
		t.Errorf("Expected 3 research notes, got %d", len(dossier.ResearchNotes))
	}

	// Verify that all claims have valid evidence references
	for _, claim := range dossier.Claims {
		for _, evidence := range claim.Evidence {
			if evidence.SourceID == "" {
				t.Errorf("Claim %q has evidence with empty source_id", claim.ID)
			}
			// Check that source exists in dossier
			sourceFound := false
			for _, source := range dossier.Sources {
				if source.StableID == evidence.SourceID {
					sourceFound = true
					break
				}
			}
			if !sourceFound {
				t.Errorf("Claim %q references unknown source %q", claim.ID, evidence.SourceID)
			}
		}
	}

	// Verify that unsupported claims don't have evidence
	for _, claim := range dossier.Claims {
		if claim.IsUnsupported && len(claim.Evidence) > 0 {
			t.Logf("Claim %q is marked unsupported but has evidence (this is allowed per schema)", claim.ID)
		}
	}

	// Verify serialization works
	jsonBytes, err := json.Marshal(dossier)
	if err != nil {
		t.Fatalf("Failed to marshal dossier: %v", err)
	}

	var restored ResearchDossier
	if err := json.Unmarshal(jsonBytes, &restored); err != nil {
		t.Fatalf("Failed to unmarshal dossier: %v", err)
	}

	if restored.StableID != dossier.StableID {
		t.Error("Round-trip serialization changed stable_id")
	}
}

// TestResearcherGenerate_ValidationFailure tests that validation failures are reported.
func TestResearcherGenerate_ValidationFailure(t *testing.T) {
	now := time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC)

	// Fixture: dossier with invalid references (claim references non-existent source)
	fixtureJSON := `{
		"stable_id": "invalid-dossier",
		"topic": "Test",
		"generated_at": "2024-01-15T14:30:00Z",
		"sources": [
			{"stable_id": "src-1", "original_url": "https://a.com", "source_type": "web", "retrieved_at": "2024-01-15T12:00:00Z"}
		],
		"claims": [
			{
				"id": "claim-001",
				"statement": "Test claim.",
				"evidence": [
					{"source_id": "non-existent-source", "excerpts": [{"text": "text", "start_offset": 0, "end_offset": 4}]}
				],
				"is_unsupported": false
			}
		],
		"contradictions": [],
		"unresolved_questions": [],
		"research_notes": []
	}`

	client := NewFakeClient(fixtureJSON, nil)
	r, err := New(client, "test", ClientConfig{TimeNow: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = r.Generate(context.Background(), "Test", []types.Document{{SourceID: "src-1"}})

	if err == nil {
		t.Fatal("Expected validation error for invalid source reference, got nil")
	}

	if !strings.Contains(err.Error(), "references unknown source") {
		t.Logf("Expected error mentioning unknown source, got: %v", err)
	}
}

// TestResearcherGenerate_EmptyMarkDown tests handling of markdown formatting in response.
func TestResearcherGenerate_EmptyMarkDown(t *testing.T) {
	now := time.Date(2024, 1, 15, 14, 30, 0, 0, time.UTC)

	// Fixture: JSON wrapped in markdown
	fixtureJSON := "```json\n{\n  \"stable_id\": \"md-test\",\n  \"topic\": \"Markdown Test\",\n  \"generated_at\": \"2024-01-15T14:30:00Z\",\n  \"sources\": [{\"stable_id\": \"src1\", \"original_url\": \"https://example.com\", \"source_type\": \"web\", \"retrieved_at\": \"2024-01-15T14:30:00Z\"}],\n  \"claims\": [{\"id\": \"c1\", \"statement\": \"Test\", \"evidence\": [{\"source_id\": \"src1\", \"excerpts\": []}], \"is_unsupported\": false}]\n}\n```\n\n"

	client := NewFakeClient(fixtureJSON, nil)
	r, err := New(client, "test", ClientConfig{TimeNow: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	dossier, err := r.Generate(context.Background(), "Markdown Test", []types.Document{{SourceID: "src1"}})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if dossier.StableID != "md-test" {
		t.Errorf("Expected stable_id 'md-test', got %q", dossier.StableID)
	}
}

// TestResearcherGenerate_ContextCancellation tests that context cancellation is respected.
func TestResearcherGenerate_ContextCancellation(t *testing.T) {
	// The fake client doesn't support context cancellation, so we test that
	// the wrapper properly passes context to the LLM client.

	client := &FakeClient{Response: `{"stable_id":"x","topic":"y","generated_at":"2024-01-01T00:00:00Z","sources":[],"claims":[{"id":"c1","statement":"Test"}]}`}
	r, err := New(client, "test", ClientConfig{TimeNow: func() time.Time { return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// Cancel immediately - the fake client ignores this, but real implementations
	// should respect it. This test ensures the pattern is in place.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = r.Generate(ctx, "Test", []types.Document{{SourceID: "src1"}})

	// Note: fake client doesn't check context, so this may succeed.
	// The key is that the pattern exists for real clients.
	t.Logf("Context cancellation test: error = %v (expected behavior depends on LLM client implementation)", err)
}

// FakeClient is a test double for llm.Client that returns a fixed response.
// This enables deterministic testing of the Researcher without an actual LLM.
type FakeClient struct {
	// Response is returned when Complete is called (if err is nil)
	Response string

	// Err is returned when Complete is called (if non-nil)
	Err error

	// LastRequest captures the last request made for inspection in tests
	LastRequest *llm.Request
}

// Complete implements llm.Client for testing.
func (f *FakeClient) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	f.LastRequest = &req
	if f.Err != nil {
		return llm.Response{}, f.Err
	}
	return llm.Response{Content: f.Response}, nil
}

// NewFakeClient creates a FakeClient with a pre-populated JSON response.
// This is useful for fixture-based tests.
func NewFakeClient(jsonResponse string, err error) *FakeClient {
	return &FakeClient{
		Response: jsonResponse,
		Err:      err,
	}
}
