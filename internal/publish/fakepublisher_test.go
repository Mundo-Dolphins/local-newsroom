package publish_test

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
)

// fakePublisher is a scriptable Publisher for engine tests.
//
// It is bound to the plan and target it serves. Each call gets the approved
// content bytes and the prior receipt; the script decides the outcome. Every
// call is recorded so tests can assert exactly how many times — and with
// what — a target was executed. This is where the "never blindly repeated"
// invariant gets its teeth: a succeeded target's publisher must see zero
// further calls.
type fakePublisher struct {
	planID   publication.PublicationID
	targetID publication.TargetID

	mu     sync.Mutex
	script []outcome
	calls  []publishedCall
}

// outcome is one scripted call result. A nil receipt with a nil error means
// "success without proof" (a publisher defect the engine must refuse).
// raw keeps the receipt's own content digest instead of re-binding it to the
// call's bytes (the corrupted-receipt path: a receipt that proves different
// bytes than what was sent).
type outcome struct {
	receipt *publication.PublicationReceipt
	err     error
	raw     bool
}

// publishedCall records one execution of the fake publisher.
type publishedCall struct {
	contentDigest publication.Digest
	content       []byte
	prior         *publication.PublicationReceipt
}

// newFakePublisher builds a publisher for one target. With no script, every
// call is a full success with a fresh receipt.
func newFakePublisher(planID publication.PublicationID, targetID publication.TargetID, script ...outcome) *fakePublisher {
	return &fakePublisher{planID: planID, targetID: targetID, script: script}
}

// Publish implements publish.Publisher.
func (f *fakePublisher) Publish(_ context.Context, content []byte, prior *publication.PublicationReceipt) (*publication.PublicationReceipt, error) {
	f.mu.Lock()
	n := len(f.calls)
	f.calls = append(f.calls, publishedCall{
		contentDigest: publication.DigestOf(content),
		content:       append([]byte(nil), content...),
		prior:         prior,
	})
	f.mu.Unlock()

	if len(f.script) == 0 {
		return f.fullSuccess(content)
	}
	idx := n
	if idx >= len(f.script) {
		idx = len(f.script) - 1
	}
	o := f.script[idx]
	if o.receipt != nil && !o.raw {
		// Re-bind the scripted receipt template to the exact bytes of this
		// call (a receipt must digest what was actually sent).
		o.receipt = f.receiptFor(o.receipt, content)
	}
	return o.receipt, o.err
}

// fullSuccess builds a receipt for a complete publication of the content.
func (f *fakePublisher) fullSuccess(content []byte) (*publication.PublicationReceipt, error) {
	return publication.NewReceipt(
		f.planID,
		f.targetID,
		"test",
		"https://example.com/live",
		fmt.Sprintf("rec-%s-%d", f.targetID, time.Now().UnixNano()),
		publication.DigestOf(content),
		"",
		"test-publisher/0.6",
		nil,
		time.Now().UTC(),
	), nil
}

// receiptFor re-binds a scripted receipt template to the exact content of
// the call, keeping the template's provenance fields.
func (f *fakePublisher) receiptFor(template *publication.PublicationReceipt, content []byte) *publication.PublicationReceipt {
	return publication.NewReceipt(
		f.planID,
		f.targetID,
		template.Provider,
		template.ExternalURI,
		template.ExternalRecordID,
		publication.DigestOf(content),
		"",
		"test-publisher/0.6",
		template.Metadata,
		time.Now().UTC(),
	)
}

// callCount is the number of times the publisher was executed.
func (f *fakePublisher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// callPrior returns the prior receipt of call n (0-based).
func (f *fakePublisher) callPrior(n int) *publication.PublicationReceipt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[n].prior
}

// callContent returns the content bytes of call n (0-based).
func (f *fakePublisher) callContent(n int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[n].content
}

// ensure the type is a Publisher.
var _ publish.Publisher = (*fakePublisher)(nil)
