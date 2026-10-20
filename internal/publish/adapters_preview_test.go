package publish_test

import (
	"context"

	"os"
	"path/filepath"

	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/gitpub"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
)

// The adapter Previewers supply the approval screen's adapter-specific
// facts (the exact branch/path for a site target, the exact post count for
// a social target) without the orchestrator knowing anything about the
// platform — and, most importantly, a preview is a rehearsal: it must
// never mutate the checkout or write a single external record.

// TestBlueskyBridgePreview drives the real Bluesky adapter's rehearsal:
// local validation and the post report, zero network calls, and the exact
// post count the approval screen must show.
func TestBlueskyBridgePreview(t *testing.T) {
	fx := newAdapterFake(t)
	pub := fx.newPublisher(t)
	bridge := publish.NewBlueskyPublisher(pub, testPlanID(), "thread")

	previewer, ok := bridge.(publish.Previewer)
	if !ok {
		t.Fatal("the bluesky bridge must implement Previewer")
	}

	posts, err := publish.ThreadContent(
		"EU AI Act enters into force. What it means for builders: https://example.com/ai-act",
		"High-risk systems get conformity assessments; general-purpose models get transparency duties.",
		"More: https://example.com/ai-act/guide",
	)
	if err != nil {
		t.Fatalf("ThreadContent: %v", err)
	}

	prev, err := previewer.Preview(context.Background(), posts, nil)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !prev.OK {
		t.Fatalf("a valid thread must preview OK: %+v", prev)
	}
	if got := prev.Metadata["post_count"]; got != "3" {
		t.Fatalf("preview post_count = %q, want 3 (the exact post count the approval screen shows)", got)
	}
	if prev.Details == "" {
		t.Fatal("the preview must carry the adapter's rehearsal report")
	}
	if fx.recordCalls() != 0 {
		t.Fatalf("the dry run made %d createRecord call(s) — a preview must never write", fx.recordCalls())
	}

	// A post that cannot be published previews as a failed rehearsal with
	// the issue, not as an error: the human sees it on the approval
	// screen.
	oversized := strings.Repeat("x", 400_000)
	content, err := publish.ThreadContent(oversized)
	if err != nil {
		t.Fatalf("ThreadContent: %v", err)
	}
	prev2, err := previewer.Preview(context.Background(), content, nil)
	if err != nil {
		t.Fatalf("Preview of an oversized post: %v (a content defect is a reported rehearsal, not an error)", err)
	}
	if prev2.OK {
		t.Fatal("an oversized post must preview as failed")
	}
	if len(prev2.Issues) == 0 {
		t.Fatal("a failed rehearsal must list its issues")
	}
	if fx.recordCalls() != 0 {
		t.Fatalf("the failing dry run made %d createRecord call(s)", fx.recordCalls())
	}
}

// TestBlueskyBridgePreviewSurfacesSessionIndependentValidation ensures the
// preview reports a content defect (a NUL inside a post) as a failed
// rehearsal even before any platform interaction.
func TestBlueskyBridgePreviewReportsMalformedThread(t *testing.T) {
	fx := newAdapterFake(t)
	pub := fx.newPublisher(t)
	bridge := publish.NewBlueskyPublisher(pub, testPlanID(), "thread")
	previewer, ok := bridge.(publish.Previewer)
	if !ok {
		t.Fatal("the bluesky bridge must implement Previewer")
	}

	// A double NUL leaves an empty segment: the canonical thread form is
	// ambiguous, and the bridge reports a failed rehearsal (not an error).
	content := []byte("first post\x00\x00second post")
	prev, err := previewer.Preview(context.Background(), content, nil)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if prev.OK || len(prev.Issues) == 0 {
		t.Fatalf("a malformed thread must preview failed with issues: %+v", prev)
	}
	if fx.recordCalls() != 0 {
		t.Fatalf("the dry run made %d createRecord call(s)", fx.recordCalls())
	}
}

// TestGitBridgePreview drives the real git adapter's rehearsal against a
// real repository: the exact branch and path the approval screen must
// show, and zero mutation (no commit, no staged file, no branch).
func TestGitBridgePreview(t *testing.T) {
	root := newTestRepo(t)

	pub, err := gitpub.New(context.Background(), gitpub.Config{
		RepoRoot:       root,
		TargetDir:      ".",
		Branch:         "newsroom/publish",
		CommittedName:  "Newsroom Bot",
		CommittedEmail: "newsroom-bot@example.com",
	})
	if err != nil {
		t.Fatalf("gitpub.New: %v", err)
	}
	defer pub.Close() //nolint:errcheck

	bridge := publish.NewGitPublisher(pub, testPlanID(), "site",
		"content/news/20260714-ai-act.md", "EU AI Act enters into force", "run-20260714T120000Z-000000000000")
	previewer, ok := bridge.(publish.Previewer)
	if !ok {
		t.Fatal("the git bridge must implement Previewer")
	}

	content := []byte("# EU AI Act\n\nThe Act enters into force.\n")
	prev, err := previewer.Preview(context.Background(), content, nil)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !prev.OK {
		t.Fatalf("a clean checkout with new content must preview OK: %+v", prev)
	}
	if got := prev.Metadata["branch"]; got != "newsroom/publish" {
		t.Fatalf("preview branch = %q, want newsroom/publish (the exact branch the approval screen shows)", got)
	}
	if got := prev.Metadata["path"]; got != "content/news/20260714-ai-act.md" {
		t.Fatalf("preview path = %q, want the exact checkout-relative path", got)
	}
	if prev.Metadata["base_branch"] == "" {
		t.Fatal("the preview must name the base branch it would commit from")
	}
	if prev.Details == "" {
		t.Fatal("the preview must carry the adapter's rehearsal report")
	}

	// The rehearsal mutated nothing: still exactly one commit (the initial
	// one), no new branch, a clean tree.
	if got := gitRun(t, root, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("commits after preview = %s, want 1 (a preview commits nothing)", got)
	}
	if out := gitRun(t, root, "status", "--porcelain"); out != "" {
		t.Fatalf("the preview left the tree dirty: %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "content/news/20260714-ai-act.md")); !os.IsNotExist(err) {
		t.Fatal("the preview wrote the file into the checkout")
	}
}

// TestGitBridgePreviewDirtyTreeReportsTheBlocker previews against a dirty
// checkout: the rehearsal fails with the blocker, still mutating nothing.
func TestGitBridgePreviewDirtyTree(t *testing.T) {
	root := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(root, "stray.md"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	pub, err := gitpub.New(context.Background(), gitpub.Config{
		RepoRoot:       root,
		TargetDir:      ".",
		Branch:         "newsroom/publish",
		CommittedName:  "Newsroom Bot",
		CommittedEmail: "newsroom-bot@example.com",
	})
	if err != nil {
		t.Fatalf("gitpub.New: %v", err)
	}
	defer pub.Close() //nolint:errcheck

	bridge := publish.NewGitPublisher(pub, testPlanID(), "site",
		"content/news/20260714-ai-act.md", "EU AI Act enters into force", "run-20260714T120000Z-000000000000")
	previewer, _ := bridge.(publish.Previewer)

	prev, err := previewer.Preview(context.Background(), []byte("# EU AI Act\n"), nil)
	if err != nil {
		t.Fatalf("Preview on a dirty tree: %v (a blocker is a reported rehearsal, not an error)", err)
	}
	if prev.OK {
		t.Fatal("a dirty checkout must preview as failed")
	}
	if len(prev.Issues) == 0 {
		t.Fatal("a failed rehearsal must list its issues")
	}
}

// TestGitBridgePreviewPredictsIdempotentSkip previews content that is
// already committed on the publication branch: the rehearsal reports the
// would-skip prediction (a human approving this would see it).
func TestGitBridgePreviewPredictsIdempotentSkip(t *testing.T) {
	root := newTestRepo(t)

	pub, err := gitpub.New(context.Background(), gitpub.Config{
		RepoRoot:       root,
		TargetDir:      ".",
		Branch:         "newsroom/publish",
		CommittedName:  "Newsroom Bot",
		CommittedEmail: "newsroom-bot@example.com",
	})
	if err != nil {
		t.Fatalf("gitpub.New: %v", err)
	}
	defer pub.Close() //nolint:errcheck

	bridge := publish.NewGitPublisher(pub, testPlanID(), "site",
		"content/news/20260714-ai-act.md", "EU AI Act enters into force", "run-20260714T120000Z-000000000000")
	previewer, _ := bridge.(publish.Previewer)

	content := []byte("# EU AI Act\n\nThe Act enters into force.\n")
	ctx := context.Background()
	if _, err := bridge.Publish(ctx, content, nil); err != nil {
		t.Fatalf("real publish: %v", err)
	}

	prev, err := previewer.Preview(ctx, content, nil)
	if err != nil {
		t.Fatalf("Preview after a real commit: %v", err)
	}
	if got := prev.Metadata["would_skip"]; got != "true" {
		t.Fatalf("preview would_skip = %q, want true (the content is already committed)", got)
	}
	if !prev.OK {
		t.Fatalf("a would-skip is a valid rehearsal, not a failure: %+v", prev)
	}
	if got := gitRun(t, root, "rev-list", "--count", "newsroom/publish"); got != "2" {
		t.Fatalf("commits after the preview = %s, want still 2 (the preview added nothing)", got)
	}
}

// TestAdaptersImplementPreviewer is the interface-level pin: both
// production bridges are Previewers, so an orchestrator composed of them
// gets approval-screen facts without any adapter-specific code.
func TestAdaptersImplementPreviewer(t *testing.T) {
	fx := newAdapterFake(t)
	blueskyBridge := publish.NewBlueskyPublisher(fx.newPublisher(t), testPlanID(), "thread")
	if _, ok := blueskyBridge.(publish.Previewer); !ok {
		t.Fatal("the bluesky bridge is not a Previewer")
	}

	root := newTestRepo(t)
	pub, err := gitpub.New(context.Background(), gitpub.Config{
		RepoRoot:       root,
		TargetDir:      ".",
		Branch:         "newsroom/publish",
		CommittedName:  "Newsroom Bot",
		CommittedEmail: "newsroom-bot@example.com",
	})
	if err != nil {
		t.Fatalf("gitpub.New: %v", err)
	}
	defer pub.Close() //nolint:errcheck
	gitBridge := publish.NewGitPublisher(pub, testPlanID(), "site", "content/news/a.md", "t", "run-x")
	if _, ok := gitBridge.(publish.Previewer); !ok {
		t.Fatal("the git bridge is not a Previewer")
	}
}
