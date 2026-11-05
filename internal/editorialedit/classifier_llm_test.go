package editorialedit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The LLM classifier path is tested against a scripted llm.Client (no model,
// no network): the acceptance criterion "test with a deterministic fake
// classifier."

func TestClassifyLLMParsesStructuredOutput(t *testing.T) {
	content := `{
  "classifications": [
    {"index": 0, "categories": ["factual_correction", "vocabulary_translation"], "note": "corrected the total", "confidence": 0.85},
    {"index": 1, "categories": ["tone_style"], "note": "", "confidence": 0.5}
  ]
}`
	client := &scriptedLLMClient{Content: content}
	c := NewClassifyLLM(client, "test-model", "")
	units := []ClassificationUnit{
		{Kind: UnitHunk, Index: 0, Text: " a\n-b\n+B"},
		{Kind: UnitHunk, Index: 1, Text: " c\n-d\n+D"},
	}
	got, err := c.Classify(context.Background(), units)
	if err != nil {
		t.Fatalf("a valid structured answer must parse: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 classifications, got %d", len(got))
	}
	if got[0].Confidence != 0.85 {
		t.Fatalf("confidence = %v, want 0.85", got[0].Confidence)
	}
	if !HasFactualChange(got[0].Categories) {
		t.Fatal("factual_correction must classify as factual")
	}
	if HasFactualChange(got[1].Categories) {
		t.Fatal("tone_style must not classify as factual")
	}
	if len(client.Requests) != 1 {
		t.Fatalf("one LLM call for all hunks, got %d", len(client.Requests))
	}
	req := client.Requests[0]
	if req.Model != "test-model" {
		t.Fatalf("model = %q", req.Model)
	}
	if req.Temperature != 0.0 {
		t.Fatalf("labels must request temperature 0, got %v", req.Temperature)
	}
	if !strings.Contains(req.SystemPrompt, "factual_correction") {
		t.Fatal("the system prompt must state the category vocabulary")
	}
	if !strings.Contains(req.UserPrompt, "### Hunk 1") || !strings.Contains(req.UserPrompt, "### Hunk 2") {
		t.Fatal("the user prompt must carry every hunk")
	}
}

func TestClassifyLLMUnknownCategoriesMapToNone(t *testing.T) {
	content := `{"classifications":[{"index":0,"categories":["purple_prose","tone_style"],"note":"","confidence":1.5}]}`
	client := &scriptedLLMClient{Content: content}
	c := NewClassifyLLM(client, "m", "")
	got, err := c.Classify(context.Background(), []ClassificationUnit{{Index: 0, Text: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Categories) != 2 {
		t.Fatalf("hallucinated labels map to none, not dropped: %v", got[0].Categories)
	}
	if !Contains(got[0].Categories, CategoryNone) || !Contains(got[0].Categories, CategoryToneStyle) {
		t.Fatalf("got %v", got[0].Categories)
	}
}

func TestClassifyLLMUnparseableIsError(t *testing.T) {
	client := &scriptedLLMClient{Content: "Sure! Here is my analysis: ..."}
	c := NewClassifyLLM(client, "m", "")
	if _, err := c.Classify(context.Background(), []ClassificationUnit{{Index: 0, Text: "x"}}); err == nil {
		t.Fatal("prose instead of JSON must be an error (advisory layer degrades)")
	}
}

func TestClassifyLLMCountMismatchIsError(t *testing.T) {
	client := &scriptedLLMClient{Content: `{"classifications":[]}`}
	c := NewClassifyLLM(client, "m", "")
	if _, err := c.Classify(context.Background(), []ClassificationUnit{{Index: 0, Text: "x"}}); err == nil {
		t.Fatal("a missing label must be an error")
	}
}

func TestClassifyLLMBadIndexIsError(t *testing.T) {
	client := &scriptedLLMClient{Content: `{"classifications":[{"index":5,"categories":["tone_style"],"confidence":0.5}]}`}
	c := NewClassifyLLM(client, "m", "")
	if _, err := c.Classify(context.Background(), []ClassificationUnit{{Index: 0, Text: "x"}}); err == nil {
		t.Fatal("an out-of-range hunk reference must be an error")
	}
}

func TestClassifyLLMTransportError(t *testing.T) {
	client := &scriptedLLMClient{Err: errors.New("connection refused")}
	c := NewClassifyLLM(client, "m", "")
	if _, err := c.Classify(context.Background(), []ClassificationUnit{{Index: 0, Text: "x"}}); err == nil {
		t.Fatal("a transport error must surface")
	}
}

func TestClassifyLLMEmptyUnits(t *testing.T) {
	client := &scriptedLLMClient{Content: "{}"}
	c := NewClassifyLLM(client, "m", "")
	got, err := c.Classify(context.Background(), nil)
	if err != nil || got != nil {
		t.Fatalf("no units => no call, got %v / %v", got, err)
	}
	if len(client.Requests) != 0 {
		t.Fatal("no units must not make an LLM call")
	}
}

func TestClassifyLLMCanceledContext(t *testing.T) {
	client := &scriptedLLMClient{}
	c := NewClassifyLLM(client, "m", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Classify(ctx, []ClassificationUnit{{Index: 0, Text: "x"}}); err == nil {
		t.Fatal("a canceled context must fail the call")
	}
}

func TestClassifyLLMNilClientPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil client must panic at construction")
		}
	}()
	NewClassifyLLM(nil, "m", "")
}

func TestClassifyLLMBuildsValidClassifier(t *testing.T) {
	c := NewClassifyLLM(&scriptedLLMClient{}, "m", "").WithMaxOutputTokens(4096).WithTimeout(5 * time.Second)
	var _ Classifier = c
	if c.Name() == "" {
		t.Fatal("the LLM classifier must name itself")
	}
}

// TestClassifyLLMCustomPrompt: an operator-narrowed prompt is used verbatim.
func TestClassifyLLMCustomPrompt(t *testing.T) {
	client := &scriptedLLMClient{Content: `{"classifications":[{"index":0,"categories":["none"],"confidence":0.2}]}`}
	c := NewClassifyLLM(client, "m", "CUSTOM PROMPT TEXT")
	_, _ = c.Classify(context.Background(), []ClassificationUnit{{Index: 0, Text: "x"}})
	if client.Requests[0].SystemPrompt != "CUSTOM PROMPT TEXT" {
		t.Fatal("a provided system prompt must be used verbatim")
	}
}
