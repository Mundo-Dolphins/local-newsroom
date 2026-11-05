package editorialedit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
)

// ClassifyLLM is the optional LLM classifier of issue #147: it sends the
// rendered diff hunks to a local LLM client and reads back a structured
// JSON answer (one classification per hunk).
//
// Contract (mirrors the final-checker's approach in this repository):
//
//   - structured output, enforced: the prompt demands a strict JSON object;
//     a response that cannot be parsed, or that names a hunk count that
//     does not match, is an error — the analyzer then degrades to the bare
//     deterministic diff (labels are advisory, the diff is truth);
//   - the model's categories are re-validated against the canonical list
//     (unknowns map to CategoryNone) and its confidence is clamped to
//     [0,1], so a hallucinated label can never enter a record as an
//     invented taxonomy entry;
//   - the model is told the category vocabulary explicitly (the canonical
//     list plus "none"), so its answers stay within it.
//
// A ClassifyLLM is safe for concurrent use: it holds only its immutable
// configuration.
type ClassifyLLM struct {
	// client is the LLM endpoint (injected; a fake in tests).
	client llm.Client

	// model is the model name passed on every request.
	model string

	// systemPrompt is the system instruction; when empty, the embedded
	// prompts/editorial-diff-classifier.prompt is used.
	systemPrompt string

	// maxOutputTokens bounds the model's answer (0 = provider default).
	maxOutputTokens int

	// timeout bounds one classification call (0 = the client's default).
	timeout time.Duration

	// name records itself on results (NamedClassifier).
	name string
}

// NewClassifyLLM builds an LLM classifier. client must be non-nil.
// systemPrompt may be empty (the embedded prompt is used then); it exists
// for tests and for operators who want to narrow the vocabulary.
func NewClassifyLLM(client llm.Client, model, systemPrompt string) *ClassifyLLM {
	if client == nil {
		panic("editorialedit: NewClassifyLLM with a nil llm.Client")
	}
	prompt := systemPrompt
	if prompt == "" {
		prompt = defaultClassifierPrompt
	}
	return &ClassifyLLM{
		client:       client,
		model:        model,
		systemPrompt: prompt,
		name:         "llm",
	}
}

// Name implements NamedClassifier.
func (c *ClassifyLLM) Name() string { return c.name }

// WithMaxOutputTokens bounds the model answer size and returns c.
func (c *ClassifyLLM) WithMaxOutputTokens(n int) *ClassifyLLM {
	c.maxOutputTokens = n
	return c
}

// WithTimeout bounds one classification call and returns c.
func (c *ClassifyLLM) WithTimeout(d time.Duration) *ClassifyLLM {
	c.timeout = d
	return c
}

// Classify implements Classifier.
//
// It makes one request carrying every unit, and parses the structured
// answer. Any failure (transport, unparseable JSON, count mismatch) returns
// an error; the analyzer degrades to the deterministic diff.
func (c *ClassifyLLM) Classify(ctx context.Context, units []ClassificationUnit) ([]Classification, error) {
	if len(units) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.effectiveTimeout())
	defer cancel()

	resp, err := c.client.Complete(ctx, llm.Request{
		SystemPrompt:    c.systemPrompt,
		UserPrompt:      userPromptForUnits(units),
		Model:           c.model,
		Temperature:     0.0, // labels should be as stable as the model allows
		MaxOutputTokens: c.maxOutputTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("editorial diff classification LLM call: %w", err)
	}
	return parseClassifierResponse(resp.Content, len(units))
}

// effectiveTimeout returns the configured timeout, or a 120s default for
// the whole multi-hunk classification call.
func (c *ClassifyLLM) effectiveTimeout() time.Duration {
	if c.timeout > 0 {
		return c.timeout
	}
	return 120 * time.Second
}

// userPromptForUnits renders the classification request: the units as a
// numbered diff block, one classification demanded per unit.
func userPromptForUnits(units []ClassificationUnit) string {
	var b strings.Builder
	b.WriteString("Classify each numbered diff hunk below. The documents are a generated article/thread (before) and the human-edited final (after).\n")
	b.WriteString("Respond with ONLY the JSON object described in your instructions; no prose, no markdown fences.\n\n")
	b.WriteString("Diff hunks:\n")
	for _, u := range units {
		fmt.Fprintf(&b, "### Hunk %d\n%s\n\n", u.Index+1, u.Text)
	}
	return b.String()
}

// classifierAnswer is the structured shape the model must return.
type classifierAnswer struct {
	Classifications []struct {
		Index      int      `json:"index"`
		Categories []string `json:"categories"`
		Note       string   `json:"note"`
		Confidence float64  `json:"confidence"`
	} `json:"classifications"`
}

// jsonUnmarshal unmarshals data into v.
func jsonUnmarshal(data string, v any) error {
	return json.Unmarshal([]byte(data), v)
}

// defaultClassifierPrompt is the prompt used when none is provided.
var defaultClassifierPrompt = embeddedClassifierPrompt

// parseClassifierResponse parses the model's structured answer and maps it
// onto the classifications.
//
// Failures are strict: empty or unparseable content, and a count mismatch
// (the model must label exactly the units it was given) are errors. The
// analyzer treats both as "advisory layer failed" and returns the
// deterministic diff unclassified. Individual entries may still be
// normalized afterwards (unknown categories -> CategoryNone, confidence
// clamped) by the caller — normalization is lenient, parsing is not.
func parseClassifierResponse(content string, units int) ([]Classification, error) {
	var ans classifierAnswer
	if err := jsonUnmarshal(strings.TrimSpace(content), &ans); err != nil {
		return nil, fmt.Errorf("classifier returned non-JSON content: %w", err)
	}
	if len(ans.Classifications) != units {
		return nil, fmt.Errorf("classifier labeled %d of %d hunks", len(ans.Classifications), units)
	}
	out := make([]Classification, units)
	for _, cl := range ans.Classifications {
		if cl.Index < 0 || cl.Index >= units {
			return nil, fmt.Errorf("classifier referenced unknown hunk %d (have %d)", cl.Index, units)
		}
		var cats []Category
		for _, raw := range cl.Categories {
			c, err := ParseCategory(raw)
			if err != nil {
				c = CategoryNone
			}
			cats = append(cats, c)
		}
		out[cl.Index] = Classification{
			Categories: cats,
			Note:       cl.Note,
			Confidence: cl.Confidence,
		}
	}
	return out, nil
}
