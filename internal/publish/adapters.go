package publish

import (
	"context"
	"fmt"
	"strconv"
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
//
// Each bridge also implements Previewer: the same rehearsal the adapter
// performs with a dry run, projected into the orchestrator's Preview so the
// approval screen shows the exact action (branch, paths, post count)
// without the orchestrator knowing anything about the platform.

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
	return &blueskyBridge{pub: pub, planID: planID, targetID: targetID}
}

// blueskyBridge binds a Bluesky publisher to one plan and thread target.
type blueskyBridge struct {
	pub      *bluesky.Publisher
	planID   publication.PublicationID
	targetID publication.TargetID
}

// The bridge is both a Publisher (real side effect) and a Previewer (zero
// side effect).
var (
	_ Publisher = (*blueskyBridge)(nil)
	_ Previewer = (*blueskyBridge)(nil)
)

// Publish implements Publisher.
func (b *blueskyBridge) Publish(ctx context.Context, content []byte, prior *publication.PublicationReceipt) (*publication.PublicationReceipt, error) {
	posts, err := ThreadPosts(content)
	if err != nil {
		return nil, fmt.Errorf("bluesky target %s: %w", b.targetID, err)
	}
	req := social.Request{
		PlanID:       b.planID,
		TargetID:     b.targetID,
		Posts:        social.PostsFromTexts(posts, nil),
		PriorReceipt: prior,
	}
	res, perr := b.pub.Publish(ctx, req)
	if res == nil {
		return nil, perr
	}
	receipt, rerr := b.pub.Receipt(req, res, time.Now().UTC())
	if rerr != nil {
		// The adapter could not build a receipt (e.g. a did:key
		// account, or a partial with zero proven posts): the engine
		// must not persist an unproven "success" — report the error
		// without a receipt so the target fails cleanly.
		if perr != nil {
			return nil, perr
		}
		return nil, fmt.Errorf("bluesky target %s: %w", b.targetID, rerr)
	}
	return receipt, perr
}

// Preview implements Previewer: the adapter's full local rehearsal (post
// validation, order/length report) with zero network calls — no login, no
// write. The preview metadata reports the exact post count the approval
// screen must show, and a malformed thread (a content defect, not an
// infrastructure failure) is a reported rehearsal failure, not an error.
func (b *blueskyBridge) Preview(ctx context.Context, content []byte, prior *publication.PublicationReceipt) (*Preview, error) {
	posts, err := ThreadPosts(content)
	if err != nil {
		return &Preview{
			OK:       false,
			Details:  "the thread content is malformed; no post would be written",
			Issues:   []publication.DryRunIssue{publication.NewDryRunIssue("malformed-thread", err.Error())},
			Metadata: map[string]string{"post_count": "0"},
		}, nil
	}
	req := social.Request{
		PlanID:       b.planID,
		TargetID:     b.targetID,
		Posts:        social.PostsFromTexts(posts, nil),
		PriorReceipt: prior,
		DryRun:       true,
	}
	res, perr := b.pub.Publish(ctx, req)
	if res == nil {
		if perr != nil {
			return nil, fmt.Errorf("bluesky target %s: dry run could not run: %w", b.targetID, perr)
		}
		return nil, fmt.Errorf("bluesky target %s: the adapter returned no dry-run result", b.targetID)
	}
	return &Preview{
		OK:       len(res.Issues) == 0,
		Details:  res.Details,
		Issues:   res.Issues,
		Metadata: map[string]string{"post_count": strconv.Itoa(len(posts))},
	}, nil
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
	return &gitBridge{pub: pub, planID: planID, targetID: targetID, relPath: relPath, title: title, runID: runID}
}

// gitBridge binds a git publisher to one plan, target, and file path.
type gitBridge struct {
	pub      *gitpub.Publisher
	planID   publication.PublicationID
	targetID publication.TargetID
	relPath  string
	title    string
	runID    string
}

// The bridge is both a Publisher (a real commit) and a Previewer (a dry
// run that writes, stages, switches, and commits nothing).
var (
	_ Publisher = (*gitBridge)(nil)
	_ Previewer = (*gitBridge)(nil)
)

// Publish implements Publisher.
func (b *gitBridge) Publish(ctx context.Context, content []byte, _ *publication.PublicationReceipt) (*publication.PublicationReceipt, error) {
	req := gitpub.Request{
		PlanID:   b.planID,
		TargetID: b.targetID,
		Title:    b.title,
		RunID:    b.runID,
		Files:    []gitpub.File{{Path: b.relPath, Content: content}},
	}
	res, err := b.pub.Publish(ctx, req)
	if err != nil || res == nil || res.CommitSHA == "" {
		return nil, err
	}
	receipt, rerr := res.Receipt(b.planID, b.targetID)
	if rerr != nil {
		return nil, fmt.Errorf("git target %s: %w", b.targetID, rerr)
	}
	return receipt, nil
}

// Preview implements Previewer: the adapter's full dry run — validation,
// branch planning, dirty-tree and identity checks, file planning, commit
// message — with zero mutation. The metadata carries the exact facts the
// approval screen must show for a site target: the planned branch, the
// base branch it commits from, and the file path as written to the
// checkout.
func (b *gitBridge) Preview(ctx context.Context, content []byte, _ *publication.PublicationReceipt) (*Preview, error) {
	req := gitpub.Request{
		PlanID:   b.planID,
		TargetID: b.targetID,
		Title:    b.title,
		RunID:    b.runID,
		Files:    []gitpub.File{{Path: b.relPath, Content: content}},
		DryRun:   true,
	}
	res, err := b.pub.Publish(ctx, req)
	if res == nil {
		if err != nil {
			return nil, fmt.Errorf("git target %s: dry run could not run: %w", b.targetID, err)
		}
		return nil, fmt.Errorf("git target %s: the adapter returned no dry-run result", b.targetID)
	}
	meta := map[string]string{}
	if res.Branch != "" {
		meta["branch"] = res.Branch
	}
	if res.BaseBranch != "" {
		meta["base_branch"] = res.BaseBranch
	}
	if len(res.Files) > 0 {
		meta["path"] = res.Files[0].Relative
	}
	if res.WouldSkip {
		meta["would_skip"] = "true"
	}
	details := res.ToDryRunResult(time.Now().UTC()).Details
	return &Preview{
		OK:       len(res.Issues) == 0,
		Details:  details,
		Issues:   res.Issues,
		Metadata: meta,
	}, nil
}
