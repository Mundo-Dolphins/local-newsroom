package publish

import (
	"context"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
)

// Publisher performs the external side effect for one publication target.
//
// A publisher is bound to one target: its adapter-specific configuration
// (account, repository, branch, file path) is part of the binding, never
// part of the plan. The engine calls Publish with:
//
//   - content: the exact bytes of the approved artifact (the plan's
//     Artifact.Path, loaded from the run directory after the digest
//     check). A publisher MUST send exactly these bytes: the plan state
//     machine verifies the returned receipt's content digest against the
//     plan's artifact digest and refuses a receipt for different bytes.
//
//   - prior: the target's existing receipt (nil on the first execution).
//     Publishers whose external action builds up record by record (a thread
//     posted post by post) use it to skip the records already proven
//     published, so a retried cycle never duplicates a previously
//     successful external action. Publishers whose action is atomic (a git
//     commit) may ignore it and deduplicate by content.
//
// The publisher reports the outcome as (receipt, error):
//
//   - (receipt, nil): the full side effect is confirmed; receipt proves it.
//   - (partial, err): part of the effect was confirmed; partial is the
//     resumable receipt covering exactly what was proven, and err explains
//     the interruption. The engine records the target as partially
//     succeeded with that receipt, so the next cycle resumes from it.
//   - (nil, err): nothing was confirmed; the target fails and is retried
//     from scratch (with prior = nil) next cycle.
//
// After every return (and across process crashes, because the engine
// persists the plan around each call), the persisted state reflects the
// external world to the degree the publisher was able to prove it.
type Publisher interface {
	Publish(ctx context.Context, content []byte, prior *publication.PublicationReceipt) (*publication.PublicationReceipt, error)
}

// PublisherFunc adapts an ordinary function to Publisher.
type PublisherFunc func(ctx context.Context, content []byte, prior *publication.PublicationReceipt) (*publication.PublicationReceipt, error)

// Publish implements Publisher.
func (f PublisherFunc) Publish(ctx context.Context, content []byte, prior *publication.PublicationReceipt) (*publication.PublicationReceipt, error) {
	return f(ctx, content, prior)
}
