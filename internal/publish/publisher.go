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

// Preview is the output of one dry-run rehearsal: what the publisher would
// do to the approved content, without doing any of it.
type Preview struct {
	// OK is the rehearsal verdict: true when the publisher would accept the
	// content and perform the action; false when it would refuse or only
	// partially succeed (see Issues).
	OK bool

	// Details is a human-readable preview summary (e.g. the post
	// order/length report, the planned branch and file paths). Always
	// redacted by the adapter.
	Details string

	// Issues are the rehearsal blockers (required when OK is false):
	// validation failures, dirty trees, missing identities, ... They are
	// the dry-run problems the human must see before approving.
	Issues []publication.DryRunIssue

	// Metadata carries the adapter's own facts about the action it would
	// perform, in provider-specific keys the approval screen renders:
	// a git publisher reports "branch", "base_branch", "path";
	// a social publisher reports "post_count". Values must be
	// credential-free (the adapters redact).
	Metadata map[string]string
}

// Previewer is the optional dry-run capability of a Publisher.
//
// A Publisher that also implements Previewer can rehearse the publication
// with zero external side effects (no network call, no file write, no
// commit): the orchestrator uses the rehearsal to build the approval
// preview, and to record a plan-contract DryRunResult before a human
// approves. The arguments mirror Publish: the exact approved bytes and the
// target's prior receipt (so a rehearsal of a partially published target
// can report the remainder rather than the whole).
//
// A Publisher that does not implement Previewer simply has no preview:
// the orchestrator reports the target as preview-unavailable and the human
// decides on the remaining evidence. Nothing in the execution path
// requires a preview — only the approval screen benefits from one.
type Previewer interface {
	Publisher

	// Preview rehearses the publication for one target.
	//
	// It must perform no external action of any kind. It returns a Preview
	// for a completed rehearsal (OK true or false, with issues), or an
	// error when the rehearsal itself could not run (e.g. the adapter's
	// own configuration or checkout is broken); a caller that cannot
	// distinguish the two treats the error as "no preview available".
	Preview(ctx context.Context, content []byte, prior *publication.PublicationReceipt) (*Preview, error)
}
