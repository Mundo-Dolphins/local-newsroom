package publish

import (
	"context"
	"fmt"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/bluesky"
	"github.com/Mundo-Dolphins/local-newsroom/internal/gitpub"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/social"
)

// Adapter bridges.
//
// These adapt the concrete v0.6 publishers (Bluesky, git) to the
// engine's per-target Publisher interface. A bridge is bound to one plan
// and one target at construction: the plan ID and target ID are what the
// adapter records on the receipt it builds, and the adapter's own
// configuration (account, checkout, branch, identity) stays with the
// adapter — the plan document and the bridge carry no credentials.

// NewBlueskyPublisher adapts a Bluesky publisher to one thread target.
//
// The content bytes must be the canonical thread form (ThreadContent): the
// posts NUL-joined in publish order. The bridge splits the approved bytes
// back into posts (unambiguous by construction) and passes the target's
// prior receipt to the adapter as its resume state, so:
//
//   - a retried partially posted thread publishes only the posts the
//     receipt does not yet prove — a successful post is never re-posted;
//   - a fully proven thread is an exact no-op inside the adapter (it
//     publishes nothing and confirms the thread's records);
//   - the receipt's content digest is the digest of the posts as sent,
//     which equals the digest of the NUL-joined content — the plan state
//     machine's digest binding accepts it.
//
// The engine never calls a bridge for a target that already succeeded, so
// the adapter's internal no-op path is a second, independent layer of
// deduplication (defense in depth: if the plan record were ever corrupted
// in a way that re-exposed a succeeded target, the adapter still would not
// post a proven thread again).
func NewBlueskyPublisher(pub *bluesky.Publisher, planID publication.PublicationID, targetID publication.TargetID) Publisher {
	return PublisherFunc(func(ctx context.Context, content []byte, prior *publication.PublicationReceipt) (*publication.PublicationReceipt, error) {
		posts, err := ThreadPosts(content)
		if err != nil {
			return nil, fmt.Errorf("bluesky target %s: %w", targetID, err)
		}
		req := social.Request{
			PlanID:       planID,
			TargetID:     targetID,
			Posts:        social.PostsFromTexts(posts, nil),
			PriorReceipt: prior,
		}
		res, perr := pub.Publish(ctx, req)
		if res == nil {
			return nil, perr
		}
		receipt, rerr := pub.Receipt(req, res, time.Now().UTC())
		if rerr != nil {
			// The adapter could not build a receipt (e.g. a did:key
			// account, or a partial with zero proven posts): the engine
			// must not persist an unproven "success" — report the error
			// without a receipt so the target fails cleanly.
			if perr != nil {
				return nil, perr
			}
			return nil, fmt.Errorf("bluesky target %s: %w", targetID, rerr)
		}
		return receipt, perr
	})
}

// NewGitPublisher adapts a git site publisher to one file target.
//
// The content bytes are the exact file to commit; the bridge closes over
// the checkout-relative path (and the commit's provenance: title, run ID)
// because those are the target's binding, not the plan's content. A git
// commit is atomic: the bridge maps a successful run (including an
// idempotent skip, where the content is already committed on the branch) to
// a receipt bound to the commit, and any failure to a nil receipt — there
// is no partial git commit to resume from. Re-pushing the same content
// after a failure is therefore safe: the adapter's own idempotency (index
// identical to HEAD) refuses to duplicate the commit.
func NewGitPublisher(pub *gitpub.Publisher, planID publication.PublicationID, targetID publication.TargetID, relPath, title, runID string) Publisher {
	return PublisherFunc(func(ctx context.Context, content []byte, _ *publication.PublicationReceipt) (*publication.PublicationReceipt, error) {
		req := gitpub.Request{
			PlanID:   planID,
			TargetID: targetID,
			Title:    title,
			RunID:    runID,
			Files:    []gitpub.File{{Path: relPath, Content: content}},
		}
		res, err := pub.Publish(ctx, req)
		if err != nil || res == nil || res.CommitSHA == "" {
			return nil, err
		}
		receipt, rerr := res.Receipt(planID, targetID)
		if rerr != nil {
			return nil, fmt.Errorf("git target %s: %w", targetID, rerr)
		}
		return receipt, nil
	})
}
