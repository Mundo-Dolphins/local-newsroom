package editorialedit

import (
	"context"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
)

// The tests of this package use a deterministic fake everywhere a model
// would be needed: the FakeClassifier implements Classifier with canned
// labels, and a scripted llm.Client fakes the ClassifyLLM path. No LLM, no
// network, no timing: every assertion here is on deterministic behavior.

// FakeClassifier is a scripted, deterministic Classifier for tests.
//
// A FakeClassifier returns one canned Classification per unit, in order,
// when Err is nil; it returns Err (and nothing) otherwise. It implements
// NamedClassifier so results record its name.
type FakeClassifier struct {
	// name is the classifier name recorded on results (default "fake").
	name string

	// Answer is the classification returned for every unit.
	Answer Classification

	// Err, when non-nil, is returned in place of classifications.
	Err error
}

// NewFakeClassifier builds a FakeClassifier that returns the given canned
// classification for every unit.
func NewFakeClassifier(ans Classification) *FakeClassifier {
	return &FakeClassifier{name: "fake", Answer: ans}
}

// Classify implements Classifier.
func (f *FakeClassifier) Classify(_ context.Context, units []ClassificationUnit) ([]Classification, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	out := make([]Classification, len(units))
	for i := range out {
		out[i] = f.Answer
	}
	return out, nil
}

// Name implements NamedClassifier.
func (f *FakeClassifier) Name() string {
	if f.name == "" {
		return "fake"
	}
	return f.name
}

var _ Classifier = (*FakeClassifier)(nil)

// scriptedLLMClient is a deterministic llm.Client for tests: it returns the
// canned content (or error) for every request and records the requests it
// received.
type scriptedLLMClient struct {
	// Content is the canned response content.
	Content string
	// Err, when non-nil, is returned instead of a response.
	Err error
	// Requests records every request for assertions.
	Requests []llm.Request
}

// Complete implements llm.Client.
func (c *scriptedLLMClient) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	c.Requests = append(c.Requests, req)
	if c.Err != nil {
		return llm.Response{}, c.Err
	}
	return llm.Response{Content: c.Content}, nil
}

var _ llm.Client = (*scriptedLLMClient)(nil)

var _ = time.Now
