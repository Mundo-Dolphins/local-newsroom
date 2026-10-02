package planner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
)

// TestNewPlanner tests the New function.
func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config.MinQueries != 1 {
		t.Errorf("Expected MinQueries 1, got %d", config.MinQueries)
	}
	if config.MaxQueries != 5 {
		t.Errorf("Expected MaxQueries 5, got %d", config.MaxQueries)
	}
	if config.Temperature != 0.5 {
		t.Errorf("Expected Temperature 0.5, got %f", config.Temperature)
	}
	if config.MaxOutputTokens != 4000 {
		t.Errorf("Expected MaxOutputTokens 4000, got %d", config.MaxOutputTokens)
	}
}

func TestNewPlanner(t *testing.T) {
	t.Run("creates planner with valid config", func(t *testing.T) {
		client := &FakeClient{Response: `{
			"original_topic": "test",
			"queries": [{"query": "test query", "purpose": "test purpose"}]
		}`}
		config := Config{
			MinQueries:      1,
			MaxQueries:      5,
			Temperature:     0.5,
			MaxOutputTokens: 4000,
		}

		p, err := New(client, "test-model", config)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if p == nil {
			t.Fatal("New() returned nil planner")
		}

		if p.model != "test-model" {
			t.Errorf("Expected model 'test-model', got %q", p.model)
		}
	})

	t.Run("returns error with nil client", func(t *testing.T) {
		_, err := New(nil, "test-model", Config{})
		if err == nil {
			t.Fatal("New() expected error with nil client, got nil")
		}
	})

	t.Run("uses prompt override when provided", func(t *testing.T) {
		customPrompt := "custom prompt for testing"
		client := &FakeClient{Response: `{
			"original_topic": "test",
			"queries": [{"query": "test query", "purpose": "test purpose"}]
		}`}
		config := Config{
			PromptOverride: customPrompt,
		}

		p, err := New(client, "test-model", config)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if p.prompt != customPrompt {
			t.Errorf("Expected prompt to be %q, got %q", customPrompt, p.prompt)
		}
	})

	t.Run("uses default config values when not specified", func(t *testing.T) {
		client := &FakeClient{Response: `{
			"original_topic": "test",
			"queries": [{"query": "test query", "purpose": "test purpose"}]
		}`}
		config := Config{}

		p, err := New(client, "test-model", config)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		if p.config.MinQueries != 1 {
			t.Errorf("Expected MinQueries 1, got %d", p.config.MinQueries)
		}
		if p.config.MaxQueries != 5 {
			t.Errorf("Expected MaxQueries 5, got %d", p.config.MaxQueries)
		}
		if p.config.Temperature != 0.5 {
			t.Errorf("Expected Temperature 0.5, got %f", p.config.Temperature)
		}
	})

	t.Run("uses default MinQueries when 0", func(t *testing.T) {
		client := &FakeClient{Response: `{}`}
		p, err := New(client, "test", Config{MinQueries: 0})
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if p.config.MinQueries != 1 {
			t.Errorf("Expected MinQueries 1 (default), got %d", p.config.MinQueries)
		}
	})

	t.Run("returns error for invalid MaxQueries", func(t *testing.T) {
		client := &FakeClient{Response: `{}`}
		_, err := New(client, "test", Config{MaxQueries: 15})
		if err == nil {
			t.Fatal("Expected error for MaxQueries=15, got nil")
		}
	})

	t.Run("returns error when MinQueries > MaxQueries", func(t *testing.T) {
		client := &FakeClient{Response: `{}`}
		_, err := New(client, "test", Config{MinQueries: 5, MaxQueries: 3})
		if err == nil {
			t.Fatal("Expected error for MinQueries > MaxQueries, got nil")
		}
	})
}

// TestPlannerGenerate tests the Generate method.
func TestPlannerGenerate(t *testing.T) {
	t.Run("successfully generates plan from valid input", func(t *testing.T) {
		fixtureJSON := `{
			"original_topic": "Solar panel efficiency research 2024",
			"language": "en",
			"time_range_hint": {
				"start_date": "2024-01-01",
				"end_date": "2024-12-31"
			},
			"queries": [
				{
					"query": "solar photovoltaic efficiency NREL report 2024",
					"purpose": "Obtain authoritative efficiency data from NREL",
					"language": "en",
					"time_range": {
						"start_date": "2024-01-01",
						"end_date": "2024-12-31"
					}
				},
				{
					"query": "solar cell efficiency records IEA 2024",
					"purpose": "Find official record-keeping for solar cell performance",
					"time_range": {
						"start_date": "2024-01-01",
						"end_date": "2024-12-31"
					}
				},
				{
					"query": "perovskite solar cell efficiency 2024 review site:.edu",
					"purpose": "Find academic research on emerging technologies"
				}
			]
		}`

		client := NewFakeClient(fixtureJSON, nil)
		config := Config{
			MinQueries:      1,
			MaxQueries:      10,
			Temperature:     0.5,
			MaxOutputTokens: 5000,
		}

		p, err := New(client, "local-model", config)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}

		plan, err := p.Generate(context.Background(), "Solar panel efficiency research 2024", "en", &TimeRangeHint{
			StartDate: "2024-01-01",
			EndDate:   "2024-12-31",
		})
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}

		if plan.OriginalTopic != "Solar panel efficiency research 2024" {
			t.Errorf("Expected topic 'Solar panel efficiency research 2024', got %q", plan.OriginalTopic)
		}

		if len(plan.Queries) != 3 {
			t.Errorf("Expected 3 queries, got %d", len(plan.Queries))
		}

		if plan.Language != "en" {
			t.Errorf("Expected language 'en', got %q", plan.Language)
		}
	})

	t.Run("returns error for empty topic", func(t *testing.T) {
		client := NewFakeClient(`{"original_topic":"x","queries":[]}`, nil)
		config := Config{}
		p, _ := New(client, "test", config)

		_, err := p.Generate(context.Background(), "", "", nil)
		if err == nil {
			t.Fatal("Expected error for empty topic, got nil")
		}
		if !errors.Is(err, ErrEmptyTopic) {
			t.Logf("Expected ErrEmptyTopic, got: %v", err)
		}
	})

	t.Run("returns error when LLM returns error", func(t *testing.T) {
		client := NewFakeClient("", llm.Internal("model timeout"))
		config := Config{}
		p, _ := New(client, "test", config)

		_, err := p.Generate(context.Background(), "Test", "en", nil)
		if err == nil {
			t.Fatal("Expected error from LLM, got nil")
		}
	})

	t.Run("returns error for invalid JSON response", func(t *testing.T) {
		client := NewFakeClient("not valid json {{{", nil)
		config := Config{}
		p, _ := New(client, "test", config)

		_, err := p.Generate(context.Background(), "Test", "en", nil)
		if err == nil {
			t.Fatal("Expected error for invalid JSON, got nil")
		}
		if !strings.Contains(err.Error(), "invalid_json") {
			t.Logf("Expected invalid_json error, got: %v", err)
		}
	})

	t.Run("returns error for missing required fields", func(t *testing.T) {
		client := NewFakeClient(`{"queries":[]}`, nil) // missing original_topic
		config := Config{}
		p, _ := New(client, "test", config)

		_, err := p.Generate(context.Background(), "Test", "en", nil)
		if err == nil {
			t.Fatal("Expected error for missing required fields, got nil")
		}
	})

	t.Run("returns error for missing queries", func(t *testing.T) {
		client := NewFakeClient(`{"original_topic":"Test"}`, nil)
		config := Config{}
		p, _ := New(client, "test", config)

		_, err := p.Generate(context.Background(), "Test", "en", nil)
		if err == nil {
			t.Fatal("Expected error for missing queries, got nil")
		}
		if !strings.Contains(err.Error(), "empty_queries") {
			t.Logf("Expected empty_queries error, got: %v", err)
		}
	})

	t.Run("passes request to LLM correctly", func(t *testing.T) {
		client := &FakeClient{Response: `{
			"original_topic": "Test",
			"queries": [{"query": "test", "purpose": "test"}]
		}`}
		config := Config{
			Temperature:     0.5,
			MaxOutputTokens: 4000,
		}
		p, _ := New(client, "my-model", config)

		timeRange := &TimeRangeHint{
			StartDate: "2024-01-01",
			EndDate:   "2024-12-31",
		}

		_, _ = p.Generate(context.Background(), "Budget Vote", "en", timeRange)

		if client.LastRequest == nil {
			t.Fatal("Expected LastRequest to be set")
		}
		if client.LastRequest.Temperature != 0.5 {
			t.Errorf("Expected temperature 0.5, got %f", client.LastRequest.Temperature)
		}
		if client.LastRequest.Model != "my-model" {
			t.Errorf("Expected model 'my-model', got %q", client.LastRequest.Model)
		}
		if !strings.Contains(client.LastRequest.UserPrompt, "Budget Vote") {
			t.Error("Expected topic in user prompt")
		}
	})

	t.Run("handles optional language parameter", func(t *testing.T) {
		client := &FakeClient{Response: `{
			"original_topic": "Test",
			"queries": [{"query": "test", "purpose": "test"}]
		}`}
		p, _ := New(client, "test", Config{})

		_, _ = p.Generate(context.Background(), "Test", "fr", nil)

		if !strings.Contains(client.LastRequest.UserPrompt, "language: fr") {
			t.Error("Expected language in user prompt")
		}
	})

	t.Run("handles nil time range", func(t *testing.T) {
		client := &FakeClient{Response: `{
			"original_topic": "Test",
			"queries": [{"query": "test", "purpose": "test"}]
		}`}
		p, _ := New(client, "test", Config{})

		_, _ = p.Generate(context.Background(), "Test", "en", nil)

		// Should not contain time_range if nil
		if strings.Contains(client.LastRequest.UserPrompt, "time_range") {
			t.Error("Unexpected time_range in user prompt for nil timeRangeHint")
		}
	})

	t.Run("handles empty time range hint", func(t *testing.T) {
		client := &FakeClient{Response: `{
			"original_topic": "Test",
			"queries": [{"query": "test", "purpose": "test"}]
		}`}
		p, _ := New(client, "test", Config{})

		_, _ = p.Generate(context.Background(), "Test", "en", &TimeRangeHint{})

		// Should not contain time_range if both dates empty
		if strings.Contains(client.LastRequest.UserPrompt, "time_range") {
			t.Error("Unexpected time_range in user prompt for empty timeRangeHint")
		}
	})

	t.Run("handles partial time range hint", func(t *testing.T) {
		client := &FakeClient{Response: `{
			"original_topic": "Test",
			"queries": [{"query": "test", "purpose": "test"}]
		}`}
		p, _ := New(client, "test", Config{})

		timeRange := &TimeRangeHint{StartDate: "2024-01-01"}
		_, _ = p.Generate(context.Background(), "Test", "en", timeRange)

		if !strings.Contains(client.LastRequest.UserPrompt, "time_range") {
			t.Error("Expected time_range in user prompt for non-empty timeRangeHint")
		}
	})
}

// TestParseAndValidateResponse tests the response parser.
func TestParseAndValidateResponse(t *testing.T) {
	t.Run("parses valid JSON", func(t *testing.T) {
		jsonStr := `{
			"original_topic": "Test",
			"queries": [
				{"query": "test query", "purpose": "test purpose"},
				{"query": "another query", "purpose": "another purpose"}
			]
		}`

		plan, err := parseAndValidateResponse(jsonStr, Config{MinQueries: 1, MaxQueries: 10})
		if err != nil {
			t.Fatalf("parseAndValidateResponse() error = %v", err)
		}

		if plan.OriginalTopic != "Test" {
			t.Errorf("Expected topic 'Test', got %q", plan.OriginalTopic)
		}
		if len(plan.Queries) != 2 {
			t.Errorf("Expected 2 queries, got %d", len(plan.Queries))
		}
	})

	t.Run("strips markdown code blocks", func(t *testing.T) {
		jsonStr := "```json\n{\"original_topic\":\"Test\",\"queries\":[{\"query\":\"test\",\"purpose\":\"test\"}]}\n```\n"

		plan, err := parseAndValidateResponse(jsonStr, Config{MinQueries: 1, MaxQueries: 10})
		if err != nil {
			t.Fatalf("parseAndValidateResponse() error = %v", err)
		}

		if plan.OriginalTopic != "Test" {
			t.Errorf("Expected topic 'Test', got %q", plan.OriginalTopic)
		}
		if len(plan.Queries) != 1 {
			t.Errorf("Expected 1 query, got %d", len(plan.Queries))
		}
	})

	t.Run("returns error for empty response", func(t *testing.T) {
		_, err := parseAndValidateResponse("", Config{})
		if err == nil {
			t.Fatal("Expected error for empty response, got nil")
		}
	})

	t.Run("returns error for invalid JSON", func(t *testing.T) {
		_, err := parseAndValidateResponse("not json", Config{})
		if err == nil {
			t.Fatal("Expected error for invalid JSON, got nil")
		}
	})

	t.Run("returns error for empty topic", func(t *testing.T) {
		_, err := parseAndValidateResponse(`{"queries":[]}`, Config{})
		if err == nil {
			t.Fatal("Expected error for empty topic, got nil")
		}
	})

	t.Run("returns error for empty queries array", func(t *testing.T) {
		_, err := parseAndValidateResponse(`{"original_topic":"Test","queries":[]}`, Config{})
		if err == nil {
			t.Fatal("Expected error for empty queries, got nil")
		}
	})

	t.Run("returns error for empty query string", func(t *testing.T) {
		_, err := parseAndValidateResponse(`{"original_topic":"Test","queries":[{"query":"","purpose":"test"}]}`, Config{})
		if err == nil {
			t.Fatal("Expected error for empty query, got nil")
		}
	})

	t.Run("returns error for empty purpose", func(t *testing.T) {
		_, err := parseAndValidateResponse(`{"original_topic":"Test","queries":[{"query":"test","purpose":""}]}`, Config{})
		if err == nil {
			t.Fatal("Expected error for empty purpose, got nil")
		}
	})

	t.Run("returns error for too many queries", func(t *testing.T) {
		jsonStr := `{"original_topic":"Test","queries":[{"query":"q1","purpose":"p1"},{"query":"q2","purpose":"p2"},{"query":"q3","purpose":"p2"},{"query":"q4","purpose":"p4"},{"query":"q5","purpose":"p5"},{"query":"q6","purpose":"p6"}]}`
		_, err := parseAndValidateResponse(jsonStr, Config{MinQueries: 1, MaxQueries: 5})
		if err == nil {
			t.Fatal("Expected error for too many queries, got nil")
		}
	})

	t.Run("returns error for too few queries", func(t *testing.T) {
		jsonStr := `{"original_topic":"Test","queries":[{"query":"q1","purpose":"p1"}]}`
		_, err := parseAndValidateResponse(jsonStr, Config{MinQueries: 3, MaxQueries: 5})
		if err == nil {
			t.Fatal("Expected error for too few queries, got nil")
		}
	})

	t.Run("handles optional language field", func(t *testing.T) {
		jsonStr := `{"original_topic":"Test","language":"es","queries":[{"query":"test","purpose":"test"}]}`
		plan, err := parseAndValidateResponse(jsonStr, Config{MinQueries: 1, MaxQueries: 10})
		if err != nil {
			t.Fatalf("parseAndValidateResponse() error = %v", err)
		}
		if plan.Language != "es" {
			t.Errorf("Expected language 'es', got %q", plan.Language)
		}
	})

	t.Run("handles optional time_range_hint", func(t *testing.T) {
		jsonStr := `{"original_topic":"Test","queries":[{"query":"test","purpose":"test"}],"time_range_hint":{"start_date":"2024-01-01","end_date":"2024-12-31"}}`
		plan, err := parseAndValidateResponse(jsonStr, Config{MinQueries: 1, MaxQueries: 10})
		if err != nil {
			t.Fatalf("parseAndValidateResponse() error = %v", err)
		}
		if plan.TimeRangeHint == nil {
			t.Fatal("Expected time_range_hint, got nil")
		}
		if plan.TimeRangeHint.StartDate != "2024-01-01" {
			t.Errorf("Expected start_date '2024-01-01', got %q", plan.TimeRangeHint.StartDate)
		}
		if plan.TimeRangeHint.EndDate != "2024-12-31" {
			t.Errorf("Expected end_date '2024-12-31', got %q", plan.TimeRangeHint.EndDate)
		}
	})

	t.Run("handles query-level time_range", func(t *testing.T) {
		jsonStr := `{"original_topic":"Test","queries":[{"query":"test","purpose":"test","time_range":{"start_date":"2024-06-01"}}]}`
		plan, err := parseAndValidateResponse(jsonStr, Config{MinQueries: 1, MaxQueries: 10})
		if err != nil {
			t.Fatalf("parseAndValidateResponse() error = %v", err)
		}
		if plan.Queries[0].TimeRange == nil {
			t.Fatal("Expected query-level time_range, got nil")
		}
	})

	t.Run("handles optional query language", func(t *testing.T) {
		jsonStr := `{"original_topic":"Test","queries":[{"query":"test","purpose":"test","language":"fr"}]}`
		plan, err := parseAndValidateResponse(jsonStr, Config{MinQueries: 1, MaxQueries: 10})
		if err != nil {
			t.Fatalf("parseAndValidateResponse() error = %v", err)
		}
		if plan.Queries[0].Language != "fr" {
			t.Errorf("Expected query language 'fr', got %q", plan.Queries[0].Language)
		}
	})
}

// TestDeduplicateQueries tests the deduplication logic.
func TestDeduplicateQueries(t *testing.T) {
	t.Run("removes exact duplicates", func(t *testing.T) {
		queries := []SearchQuery{
			{Query: "test query", Purpose: "first"},
			{Query: "test query", Purpose: "second"},
			{Query: "test query", Purpose: "third"},
		}

		result := deduplicateQueries(queries)
		if len(result) != 1 {
			t.Errorf("Expected 1 query after dedup, got %d", len(result))
		}
	})

	t.Run("removes case-insensitive duplicates", func(t *testing.T) {
		queries := []SearchQuery{
			{Query: "Test Query", Purpose: "first"},
			{Query: "test query", Purpose: "second"},
			{Query: "TEST QUERY", Purpose: "third"},
		}

		result := deduplicateQueries(queries)
		if len(result) != 1 {
			t.Errorf("Expected 1 query after case-insensitive dedup, got %d", len(result))
		}
	})

	t.Run("keeps queries with different normalized forms", func(t *testing.T) {
		queries := []SearchQuery{
			{Query: "solar efficiency", Purpose: "first"},
			{Query: "solar cell efficiency", Purpose: "second"},
			{Query: "photovoltaic efficiency", Purpose: "third"},
		}

		result := deduplicateQueries(queries)
		if len(result) != 3 {
			t.Errorf("Expected 3 queries, got %d", len(result))
		}
	})

	t.Run("strips site: operator for dedup", func(t *testing.T) {
		queries := []SearchQuery{
			{Query: "site:.gov climate change", Purpose: "gov only"},
			{Query: "site:gov climate change", Purpose: "generic gov"},
		}

		result := deduplicateQueries(queries)
		// Both should normalize to "climate change"
		if len(result) != 1 {
			t.Errorf("Expected 1 query after stripping site:, got %d", len(result))
		}
	})

	t.Run("keeps original query after dedup", func(t *testing.T) {
		queries := []SearchQuery{
			{Query: "original text", Purpose: "keep this"},
			{Query: "original text", Purpose: "discard"},
		}

		result := deduplicateQueries(queries)
		if len(result) != 1 {
			t.Fatal("Expected 1 query")
		}
		if result[0].Purpose != "keep this" {
			t.Errorf("Expected to keep first query's purpose, got %q", result[0].Purpose)
		}
	})

	t.Run("handles empty queries slice", func(t *testing.T) {
		result := deduplicateQueries([]SearchQuery{})
		if len(result) != 0 {
			t.Errorf("Expected 0 queries, got %d", len(result))
		}
	})

	t.Run("handles whitespace variations", func(t *testing.T) {
		queries := []SearchQuery{
			{Query: "  test   query  ", Purpose: "first"},
			{Query: "test query", Purpose: "second"},
		}

		result := deduplicateQueries(queries)
		if len(result) != 1 {
			t.Errorf("Expected 1 query after whitespace normalization, got %d", len(result))
		}
	})
}

// TestNormalizeQuery tests query normalization.
func TestNormalizeQuery(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "lowercase conversion",
			input:    "TEST QUERY",
			expected: "test query",
		},
		{
			name:     "whitespace normalization",
			input:    "test   query   here",
			expected: "test query here",
		},
		{
			name:     "strips site:",
			input:    "site:.gov climate change",
			expected: "climate change",
		},
		{
			name:     "strips intitle:",
			input:    "intitle:definition solar energy",
			expected: "solar energy",
		},
		{
			name:     "strips filetype:",
			input:    "filetype:pdf annual report 2024",
			expected: "annual report 2024",
		},
		{
			name:     "strips trailing punctuation",
			input:    "test query.",
			expected: "test query",
		},
		{
			name:     "strips trailing question marks",
			input:    "what is climate change?",
			expected: "what is climate change",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeQuery(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeQuery(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestSearchPlanValidate tests SearchPlan validation.
func TestSearchPlanValidate(t *testing.T) {
	t.Run("returns error for empty topic", func(t *testing.T) {
		plan := &SearchPlan{
			OriginalTopic: "",
			Queries:       []SearchQuery{{Query: "test", Purpose: "test"}},
		}
		err := plan.Validate()
		if err == nil {
			t.Fatal("Expected error for empty topic, got nil")
		}
		if !errors.Is(err, ErrEmptyTopic) {
			t.Logf("Expected ErrEmptyTopic, got: %v", err)
		}
	})

	t.Run("returns error for empty queries", func(t *testing.T) {
		plan := &SearchPlan{
			OriginalTopic: "Test",
			Queries:       []SearchQuery{},
		}
		err := plan.Validate()
		if err == nil {
			t.Fatal("Expected error for empty queries, got nil")
		}
		if !errors.Is(err, ErrEmptyQueries) {
			t.Logf("Expected ErrEmptyQueries, got: %v", err)
		}
	})

	t.Run("validates empty query string", func(t *testing.T) {
		plan := &SearchPlan{
			OriginalTopic: "Test",
			Queries:       []SearchQuery{{Query: "", Purpose: "test"}},
		}
		err := plan.Validate()
		if err == nil {
			t.Fatal("Expected error for empty query, got nil")
		}
	})

	t.Run("validates empty purpose", func(t *testing.T) {
		plan := &SearchPlan{
			OriginalTopic: "Test",
			Queries:       []SearchQuery{{Query: "test", Purpose: ""}},
		}
		err := plan.Validate()
		if err == nil {
			t.Fatal("Expected error for empty purpose, got nil")
		}
	})

	t.Run("validates multi-query plan", func(t *testing.T) {
		plan := &SearchPlan{
			OriginalTopic: "Test",
			Queries: []SearchQuery{
				{Query: "q1", Purpose: "p1"},
				{Query: "q2", Purpose: "p2"},
			},
		}
		err := plan.Validate()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
	})

	t.Run("accepts whitespace-only topic as empty", func(t *testing.T) {
		plan := &SearchPlan{
			OriginalTopic: "   \n\t  ",
			Queries:       []SearchQuery{{Query: "test", Purpose: "test"}},
		}
		err := plan.Validate()
		if err == nil {
			t.Fatal("Expected error for whitespace-only topic, got nil")
		}
	})
}

// TestSearchPlanValidateAgainstConfig tests config-based validation.
func TestSearchPlanValidateAgainstConfig(t *testing.T) {
	config := Config{MinQueries: 2, MaxQueries: 4}

	t.Run("passes for queries within bounds", func(t *testing.T) {
		plan := &SearchPlan{
			OriginalTopic: "Test",
			Queries: []SearchQuery{
				{Query: "q1", Purpose: "p1"},
				{Query: "q2", Purpose: "p2"},
				{Query: "q3", Purpose: "p3"},
			},
		}
		err := plan.ValidateAgainstConfig(config)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
	})

	t.Run("returns error for too few queries", func(t *testing.T) {
		plan := &SearchPlan{
			OriginalTopic: "Test",
			Queries: []SearchQuery{
				{Query: "q1", Purpose: "p1"},
			},
		}
		err := plan.ValidateAgainstConfig(config)
		if err == nil {
			t.Fatal("Expected error for too few queries, got nil")
		}
	})

	t.Run("returns error for too many queries", func(t *testing.T) {
		plan := &SearchPlan{
			OriginalTopic: "Test",
			Queries: []SearchQuery{
				{Query: "q1", Purpose: "p1"},
				{Query: "q2", Purpose: "p2"},
				{Query: "q3", Purpose: "p3"},
				{Query: "q4", Purpose: "p4"},
				{Query: "q5", Purpose: "p5"},
			},
		}
		err := plan.ValidateAgainstConfig(config)
		if err == nil {
			t.Fatal("Expected error for too many queries, got nil")
		}
	})
}

// TestTimeRangeHintValidation tests TimeRangeHint validation.
func TestTimeRangeHintValidation(t *testing.T) {
	t.Run("validates valid date format", func(t *testing.T) {
		tr := &TimeRangeHint{StartDate: "2024-01-15", EndDate: "2024-12-31"}
		if err := tr.ValidateStartTime(); err != nil {
			t.Errorf("ValidateStartTime() error = %v", err)
		}
		if err := tr.ValidateEndTime(); err != nil {
			t.Errorf("ValidateEndTime() error = %v", err)
		}
	})

	t.Run("validates valid datetime format", func(t *testing.T) {
		tr := &TimeRangeHint{StartDate: "2024-01-15T00:00:00Z", EndDate: "2024-12-31T23:59:59Z"}
		if err := tr.ValidateStartTime(); err != nil {
			t.Errorf("ValidateStartTime() error = %v", err)
		}
	})

	t.Run("accepts nil time range", func(t *testing.T) {
		if err := (*TimeRangeHint)(nil).ValidateStartTime(); err != nil {
			t.Errorf("ValidateStartTime() on nil = %v", err)
		}
	})

	t.Run("accepts empty time range", func(t *testing.T) {
		tr := &TimeRangeHint{}
		if err := tr.ValidateStartTime(); err != nil {
			t.Errorf("ValidateStartTime() on empty = %v", err)
		}
	})

	t.Run("rejects invalid date format", func(t *testing.T) {
		tr := &TimeRangeHint{StartDate: "2024/01/15"}
		if err := tr.ValidateStartTime(); err == nil {
			t.Error("Expected error for invalid date format, got nil")
		}
	})

	t.Run("rejects invalid month", func(t *testing.T) {
		tr := &TimeRangeHint{StartDate: "2024-13-15"}
		if err := tr.ValidateStartTime(); err == nil {
			t.Error("Expected error for invalid month, got nil")
		}
	})

	t.Run("rejects invalid day", func(t *testing.T) {
		tr := &TimeRangeHint{StartDate: "2024-01-32"}
		if err := tr.ValidateStartTime(); err == nil {
			t.Error("Expected error for invalid day, got nil")
		}
	})

	t.Run("rejects non-numeric characters", func(t *testing.T) {
		tr := &TimeRangeHint{StartDate: "2024-0a-15"}
		if err := tr.ValidateStartTime(); err == nil {
			t.Error("Expected error for non-numeric characters, got nil")
		}
	})
}

// TestJSONSerialization tests JSON marshaling/unmarshaling.
func TestJSONSerialization(t *testing.T) {
	t.Run("marshals SearchPlan", func(t *testing.T) {
		plan := &SearchPlan{
			OriginalTopic: "Test",
			Queries: []SearchQuery{
				{Query: "test query", Purpose: "test purpose"},
			},
			Language: "en",
			TimeRangeHint: &TimeRangeHint{
				StartDate: "2024-01-01",
			},
		}

		jsonBytes, err := json.Marshal(plan)
		if err != nil {
			t.Fatalf("Marshal error: %v", err)
		}

		if !strings.Contains(string(jsonBytes), "original_topic") {
			t.Error("Expected original_topic in JSON")
		}
	})

	t.Run("unmarshals SearchPlan", func(t *testing.T) {
		jsonStr := `{
			"original_topic": "Test",
			"queries": [{"query": "test", "purpose": "test"}],
			"language": "en",
			"time_range_hint": {"start_date": "2024-01-01"}
		}`

		var plan SearchPlan
		if err := json.Unmarshal([]byte(jsonStr), &plan); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}

		if plan.OriginalTopic != "Test" {
			t.Errorf("Expected topic 'Test', got %q", plan.OriginalTopic)
		}
		if plan.Language != "en" {
			t.Errorf("Expected language 'en', got %q", plan.Language)
		}
	})

	t.Run("handles null time_range_hint", func(t *testing.T) {
		jsonStr := `{"original_topic":"Test","queries":[{"query":"test","purpose":"test"}],"time_range_hint":null}`
		var plan SearchPlan
		if err := json.Unmarshal([]byte(jsonStr), &plan); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}
		if plan.TimeRangeHint != nil {
			t.Errorf("Expected nil TimeRangeHint, got %v", plan.TimeRangeHint)
		}
	})

	t.Run("handles optional query fields", func(t *testing.T) {
		jsonStr := `{"original_topic":"Test","queries":[{
			"query":"test",
			"purpose":"test",
			"language":"fr",
			"time_range":{"start_date":"2024-06-01"}
		}]}`

		var plan SearchPlan
		if err := json.Unmarshal([]byte(jsonStr), &plan); err != nil {
			t.Fatalf("Unmarshal error: %v", err)
		}

		if plan.Queries[0].Language != "fr" {
			t.Errorf("Expected query language 'fr', got %q", plan.Queries[0].Language)
		}
	})
}

// TestEdgeCases tests various edge cases.
func TestEdgeCases(t *testing.T) {
	t.Run("handles markdown with different casing", func(t *testing.T) {
		tests := []struct {
			name  string
			input string
		}{
			{"JSON uppercase", "```JSON\n{\"original_topic\":\"Test\",\"queries\":[{\"query\":\"q\",\"purpose\":\"p\"}]}\n```\n"},
			{"json lowercase", "```json\n{\"original_topic\":\"Test\",\"queries\":[{\"query\":\"q\",\"purpose\":\"p\"}]}\n```\n"},
			{"backticks only", "```\n{\"original_topic\":\"Test\",\"queries\":[{\"query\":\"q\",\"purpose\":\"p\"}]}\n```\n"},
		}

		for _, tt := range tests {
			plan, err := parseAndValidateResponse(tt.input, Config{MinQueries: 1, MaxQueries: 10})
			if err != nil {
				t.Errorf("%s: parseAndValidateResponse() error = %v", tt.name, err)
			}
			if plan == nil || plan.OriginalTopic != "Test" {
				t.Errorf("%s: unexpected result", tt.name)
			}
		}
	})

	t.Run("handles whitespace-only content", func(t *testing.T) {
		_, err := parseAndValidateResponse("   \n\n   \n", Config{})
		if err == nil {
			t.Fatal("Expected error for whitespace-only content, got nil")
		}
	})

	t.Run("handles query with only operators", func(t *testing.T) {
		// After stripping operators, the normalized query becomes empty
		// But the original query is preserved for actual search use
		// The normalized form is only used for deduplication
		jsonStr := `{"original_topic":"Test","queries":[{"query":"site:.gov","purpose":"gov only"}]}`
		plan, err := parseAndValidateResponse(jsonStr, Config{MinQueries: 1, MaxQueries: 10})
		if err != nil {
			t.Errorf("parseAndValidateResponse() error = %v", err)
			return
		}
		if len(plan.Queries) != 1 {
			t.Fatalf("Expected 1 query, got %d", len(plan.Queries))
		}
		// Original query is preserved
		if plan.Queries[0].Query != "site:.gov" {
			t.Errorf("Expected original query preserved, got %q", plan.Queries[0].Query)
		}
	})

	t.Run("handles duplicate queries with different purposes", func(t *testing.T) {
		fixtureJSON := `{
			"original_topic": "Test",
			"queries": [
				{"query": "duplicate", "purpose": "first reason"},
				{"query": "duplicate", "purpose": "second reason"},
				{"query": "duplicate", "purpose": "third reason"}
			]
		}`

		plan, err := parseAndValidateResponse(fixtureJSON, Config{MinQueries: 1, MaxQueries: 10})
		if err != nil {
			t.Fatalf("parseAndValidateResponse() error = %v", err)
		}

		if len(plan.Queries) != 1 {
			t.Errorf("Expected 1 query after dedup, got %d", len(plan.Queries))
		}
		if plan.Queries[0].Purpose != "first reason" {
			t.Errorf("Expected first purpose to be kept, got %q", plan.Queries[0].Purpose)
		}
	})
}

// TestFakeClient tests the fake client implementation.
func TestFakeClient(t *testing.T) {
	t.Run("returns fixed response", func(t *testing.T) {
		client := NewFakeClient("fixed response", nil)
		resp, err := client.Complete(context.Background(), Request{Model: "test"})

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
		_, err := client.Complete(context.Background(), Request{Model: "test"})

		if err == nil {
			t.Fatal("Expected error, got nil")
		}

		if !llm.IsInternal(err) {
			t.Errorf("Expected internal error, got: %v", err)
		}
	})

	t.Run("captures last request", func(t *testing.T) {
		client := &FakeClient{Response: "ok"}
		req := Request{Model: "model-x", Temperature: 0.5}

		_, _ = client.Complete(context.Background(), req)

		if client.LastRequest == nil {
			t.Fatal("Expected LastRequest to be set")
		}
		if client.LastRequest.Model != "model-x" {
			t.Errorf("Expected Model 'model-x', got %q", client.LastRequest.Model)
		}
	})
}

// FakeClient is a test double for Planner.Client that returns a fixed response.
type FakeClient struct {
	// Response is returned when Complete is called (if err is nil)
	Response string

	// Err is returned when Complete is called (if non-nil)
	Err error

	// LastRequest captures the last request made for inspection in tests
	LastRequest *Request
}

// Complete implements Planner.Client for testing.
func (f *FakeClient) Complete(ctx context.Context, req Request) (Response, error) {
	f.LastRequest = &req
	if f.Err != nil {
		return Response{}, f.Err
	}
	return Response{Content: f.Response}, nil
}

// NewFakeClient creates a FakeClient with a pre-populated JSON response.
func NewFakeClient(jsonResponse string, err error) *FakeClient {
	return &FakeClient{
		Response: jsonResponse,
		Err:      err,
	}
}
