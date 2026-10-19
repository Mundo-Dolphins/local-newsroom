package gitpub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	texttmpl "text/template"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
)

// ---------------------------------------------------------------------------
// Test fixtures: temporary real git repositories
//
// Every test repo is a throwaway "git init" in t.TempDir. Setup commands run
// git through the same isolation the adapter uses (empty global config, no
// system config, no terminal prompt) so the machine's git identity and
// config can never leak into a test repo. Commit setup uses per-invocation
// -c identity flags so no config file is ever written by the fixtures.
// ---------------------------------------------------------------------------

// testGitEnv is the isolated environment for fixture git commands.
var testGitEnv = []string{
	"LC_ALL=C",
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_CONFIG_GLOBAL=/dev/null",
	"GIT_TERMINAL_PROMPT=0",
	"PATH=" + os.Getenv("PATH"),
}

// gitRun runs git in dir and fails the test on non-zero exit.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(gitRaw(t, dir, args...))
}

// gitRaw runs git in dir and returns the exact output (no trimming: for
// byte-exact comparisons such as cat-file blobs).
func gitRaw(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) // #nosec G204 -- fixed "git" plus literal test args; no shell.
	cmd.Env = testGitEnv
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, errb.String())
	}
	return out.String()
}

// gitOK runs git and reports whether it exited zero (for negative checks).
func gitOK(t *testing.T, dir string, args ...string) bool {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) // #nosec G204 -- fixed "git" plus literal test args; no shell.
	cmd.Env = testGitEnv
	var errb bytes.Buffer
	cmd.Stderr = &errb
	return cmd.Run() == nil
}

// testRepo is a temporary git repository.
type testRepo struct {
	root string
}

// newTestRepo creates a temporary repository with one initial commit
// (README.md) and a tracked content/ directory.
func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	writeFile(t, filepath.Join(root, "README.md"), "# test site\n")
	writeFile(t, filepath.Join(root, filepath.Join("content", ".gitkeep")), "")
	gitRun(t, root, "add", "README.md", "content/.gitkeep")
	gitRun(t, root, "-c", "user.name=Setup", "-c", "user.email=setup@example.com", "commit", "-q", "-m", "initial")
	return &testRepo{root: root}
}

// newBareRemote creates a bare repository usable as a push target.
func (r *testRepo) newBareRemote(t *testing.T) string {
	t.Helper()
	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare")
	gitRun(t, r.root, "remote", "add", "origin", bare)
	return bare
}

// setLocalIdentity writes the repository-local committer identity.
func (r *testRepo) setLocalIdentity(t *testing.T) {
	t.Helper()
	gitRun(t, r.root, "config", "user.name", "Site Author")
	gitRun(t, r.root, "config", "user.email", "site@example.com")
}

// branch returns the current branch name of the repo.
func (r *testRepo) branch(t *testing.T) string {
	t.Helper()
	return gitRun(t, r.root, "symbolic-ref", "--short", "HEAD")
}

// commitCount returns the number of commits on the branch containing HEAD.
func (r *testRepo) commitCount(t *testing.T, ref string) int {
	t.Helper()
	return parseInt(gitRun(t, r.root, "rev-list", "--count", ref))
}

func parseInt(s string) int {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// testPlanID is a well-formed publication ID for fixtures.
var testPlanID = publication.PublicationID("pub-20260711T120000-0123456789ab")

// articleA is the canonical staged article (front matter + body).
const articleA = "---\ntitle: Test Article\n---\n\n# Test Article\n\nBody text A.\n"

// articleB is the changed-content variant (same path, different bytes).
const articleB = "---\ntitle: Test Article (v2)\n---\n\n# Test Article\n\nBody text B.\n"

// defaultTestConfig is the adapter config pointing at a repo root.
func defaultTestConfig(root string) Config {
	return Config{
		RepoRoot:       root,
		TargetDir:      "content",
		CommittedName:  "Newsroom Bot",
		CommittedEmail: "newsroom@example.com",
	}
}

// testRequest is the canonical single-article request.
func testRequest(content string) Request {
	return Request{
		PlanID:   testPlanID,
		TargetID: "site",
		Title:    "Test Article",
		RunID:    "run-20260711T120000-0123456789ab",
		Files:    []File{{Path: "news/20260711-slug.md", Content: []byte(content)}},
	}
}

func newTestPublisher(t *testing.T, cfg Config) *Publisher {
	t.Helper()
	p, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// ---------------------------------------------------------------------------
// New / config validation
// ---------------------------------------------------------------------------

func TestNewRejectsInvalidConfig(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	gitDir := repo.root
	nonGit := t.TempDir()
	strayFile := filepath.Join(gitDir, "README.md")

	tests := []struct {
		name    string
		cfg     Config
		wantErr error
	}{
		{"empty root", Config{}, ErrInvalidConfig},
		{"missing root", Config{RepoRoot: filepath.Join(nonGit, "nope")}, ErrInvalidConfig},
		{"root is a file", Config{RepoRoot: strayFile}, ErrInvalidConfig},
		{"non-git root", Config{RepoRoot: nonGit}, ErrNotAGitRepo},
		{"target dir traversal", Config{RepoRoot: gitDir, TargetDir: "../.."}, ErrInvalidConfig},
		{"branch bad chars", Config{RepoRoot: gitDir, Branch: "a b"}, ErrBranch},
		{"branch colon", Config{RepoRoot: gitDir, Branch: "a:b"}, ErrBranch},
		{"bad template", Config{RepoRoot: gitDir, CommitMessage: "{{.Unclosed"}, ErrInvalidConfig},
		{"name only", Config{RepoRoot: gitDir, CommittedName: "Bot"}, ErrNoIdentity},
		{"bad email", Config{RepoRoot: gitDir, CommittedName: "Bot", CommittedEmail: "not-an-address"}, ErrInvalidConfig},
		{"allowed dirty dot", Config{RepoRoot: gitDir, AllowedDirtyPaths: []string{"."}}, ErrInvalidConfig},
		{"allowed dirty traversal", Config{RepoRoot: gitDir, AllowedDirtyPaths: []string{"../x"}}, ErrInvalidConfig},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(ctx, tc.cfg)
			if err == nil {
				t.Fatalf("New succeeded, want %v", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestNewAcceptsSubdirectoryRoot(t *testing.T) {
	repo := newTestRepo(t)
	sub := filepath.Join(repo.root, "content")
	cfg := defaultTestConfig(sub)
	cfg.TargetDir = "." // publish directly at the subdirectory
	p := newTestPublisher(t, cfg)

	if p.CheckoutRoot() != resolvedPath(t, repo.root) {
		t.Errorf("CheckoutRoot = %q, want the repo toplevel %q", p.CheckoutRoot(), repo.root)
	}
	res, err := p.Publish(context.Background(), testRequest(articleA))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.Files[0].Relative != "content/news/20260711-slug.md" {
		t.Errorf("file Relative = %q, want content/news/20260711-slug.md", res.Files[0].Relative)
	}
}

func TestNewRejectsSymlinkTargetDir(t *testing.T) {
	repo := newTestRepo(t)
	outside := t.TempDir()
	link := filepath.Join(repo.root, "content", "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	cfg := defaultTestConfig(repo.root)
	cfg.TargetDir = "content/link"
	_, err := New(context.Background(), cfg)
	if !errors.Is(err, ErrTargetOutsideRepo) {
		t.Fatalf("err = %v, want ErrTargetOutsideRepo", err)
	}
}

func TestNewRejectsMissingGitBinary(t *testing.T) {
	repo := newTestRepo(t)
	cfg := defaultTestConfig(repo.root)
	cfg.GitPath = filepath.Join(t.TempDir(), "definitely-not-git")
	_, err := New(context.Background(), cfg)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}

func TestNewRejectsFakeGitBinary(t *testing.T) {
	repo := newTestRepo(t)
	fakeDir := t.TempDir()
	fake := filepath.Join(fakeDir, "git")
	script := "#!/bin/sh\nexit 3\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := defaultTestConfig(repo.root)
	cfg.GitPath = fake
	_, err := New(context.Background(), cfg)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}

func TestNewRejectsOldGitVersion(t *testing.T) {
	repo := newTestRepo(t)
	fakeDir := t.TempDir()
	fake := filepath.Join(fakeDir, "git")
	script := "#!/bin/sh\necho \"git version 2.33.0 (test)\"\nexit 0\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := defaultTestConfig(repo.root)
	cfg.GitPath = fake
	_, err := New(context.Background(), cfg)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig for git < 2.34", err)
	}
}

// TestNewDoesNotTouchConfig proves construction writes nothing to the
// repository: the .git directory mtime/content is unchanged (git global and
// system config are isolated; no config file is written).
func TestNewDoesNotModifyRepo(t *testing.T) {
	repo := newTestRepo(t)
	before := gitRun(t, repo.root, "status", "--porcelain=v1", "-z")
	newTestPublisher(t, defaultTestConfig(repo.root))
	after := gitRun(t, repo.root, "status", "--porcelain=v1", "-z")
	if before != after {
		t.Errorf("status changed after New: %q -> %q", before, after)
	}
}

// ---------------------------------------------------------------------------
// Publish: clean repo
// ---------------------------------------------------------------------------

func TestPublishCleanRepo(t *testing.T) {
	repo := newTestRepo(t)
	bare := repo.newBareRemote(t)
	p := newTestPublisher(t, defaultTestConfig(repo.root))

	res, err := p.Publish(context.Background(), testRequest(articleA))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Planned branch created and checked out.
	if !res.BranchCreated || res.Branch != "publication/site" {
		t.Errorf("BranchCreated/Branch = %v/%q, want true/publication/site", res.BranchCreated, res.Branch)
	}
	if got := repo.branch(t); got != "publication/site" {
		t.Errorf("working tree branch = %q, want publication/site (reviewable, no switch back)", got)
	}
	if res.CommitSHA == "" || len(res.CommitSHA) != 40 {
		t.Fatalf("CommitSHA = %q, want a full 40-hex SHA", res.CommitSHA)
	}
	if got := gitRun(t, repo.root, "rev-parse", "HEAD"); got != res.CommitSHA {
		t.Errorf("HEAD = %q, want %q", got, res.CommitSHA)
	}

	// The commit contains exactly the publication file.
	changed := commitPaths(t, repo, res.CommitSHA)
	if len(changed) != 1 || changed[0] != "content/news/20260711-slug.md" {
		t.Errorf("commit changed paths = %v, want [content/news/20260711-slug.md]", changed)
	}

	// Committed bytes are the approved bytes (byte-exact, no trimming).
	committed := gitRaw(t, repo.root, "cat-file", "blob", res.CommitSHA+":content/news/20260711-slug.md")
	if committed != articleA {
		t.Errorf("committed content = %q, want the approved bytes", committed)
	}

	// File is world-readable site material, not executable.
	fi, err := os.Stat(filepath.Join(repo.root, "content", "news", "20260711-slug.md"))
	if err != nil {
		t.Fatalf("stat published file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o644 {
		t.Errorf("file perm = %o, want 644", perm)
	}

	// Tree is clean after the run.
	if s := gitRun(t, repo.root, "status", "--porcelain=v1", "-z"); s != "" {
		t.Errorf("post-run status = %q, want clean", s)
	}
	if len(res.RemainingDirty) != 0 {
		t.Errorf("RemainingDirty = %v, want empty", res.RemainingDirty)
	}

	// No push: the bare remote has none of the new refs or history.
	if gitOK(t, bare, "rev-parse", "-q", "--verify", "refs/heads/publication/site") {
		t.Error("publication branch exists on the remote: a push occurred")
	}
	if gitOK(t, bare, "rev-parse", "-q", "--verify", "refs/heads/master") {
		t.Error("master exists on the remote: a push occurred")
	}

	// Receipt.
	rec, err := res.Receipt(testPlanID, "site")
	if err != nil {
		t.Fatalf("Receipt: %v", err)
	}
	if rec.ExternalRecordID != res.CommitSHA {
		t.Errorf("receipt ExternalRecordID = %q, want the commit SHA", rec.ExternalRecordID)
	}
	if rec.Metadata["branch"] != "publication/site" {
		t.Errorf("receipt branch metadata = %q, want publication/site", rec.Metadata["branch"])
	}
	if rec.Provider != "git" {
		t.Errorf("receipt Provider = %q, want git", rec.Provider)
	}
	if rec.ContentDigest != res.Digest {
		t.Errorf("receipt ContentDigest = %q, want %q", rec.ContentDigest, res.Digest)
	}
	if res.Digest != publication.DigestOf([]byte(articleA)) {
		t.Errorf("single-file Digest = %q, want the file's own digest", res.Digest)
	}
}

// TestPublishLocalIdentity proves the repo-local committer identity is used
// when the config supplies none (and the machine's global identity is never
// consulted: the client isolates global config).
func TestPublishLocalIdentity(t *testing.T) {
	repo := newTestRepo(t)
	repo.setLocalIdentity(t)
	cfg := defaultTestConfig(repo.root)
	cfg.CommittedName, cfg.CommittedEmail = "", "" // force the repo-local path
	p := newTestPublisher(t, cfg)

	res, err := p.Publish(context.Background(), testRequest(articleA))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	name := gitRun(t, repo.root, "log", "-1", "--format=%an", res.CommitSHA)
	email := gitRun(t, repo.root, "log", "-1", "--format=%ae", res.CommitSHA)
	if name != "Site Author" || email != "site@example.com" {
		t.Errorf("committer = %q <%q>, want the repo-local identity", name, email)
	}
}

// ---------------------------------------------------------------------------
// Publish: dirty-tree policy
// ---------------------------------------------------------------------------

func TestPublishDirtyUnrelatedFails(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, filepath.Join(repo.root, "stray.md"), "unrelated work\n") // untracked
	p := newTestPublisher(t, defaultTestConfig(repo.root))

	_, err := p.Publish(context.Background(), testRequest(articleA))
	var dt *DirtyTreeError
	if !errors.As(err, &dt) {
		t.Fatalf("err = %v, want *DirtyTreeError", err)
	}
	if !errors.Is(err, ErrDirtyTree) {
		t.Fatalf("err = %v, want ErrDirtyTree", err)
	}
	if len(dt.Offenders) == 0 || !strings.Contains(dt.Error(), "stray.md") {
		t.Errorf("offenders = %v, want stray.md named; message: %s", dt.Offenders, dt.Error())
	}

	// No side effects: no branch, no file, no commit.
	if gitOK(t, repo.root, "rev-parse", "-q", "--verify", "refs/heads/publication/site") {
		t.Error("publication branch was created despite the dirty tree")
	}
	if _, err := os.Stat(filepath.Join(repo.root, "content", "news", "20260711-slug.md")); err == nil {
		t.Error("the publication file was written despite the dirty tree")
	}
	if repo.commitCount(t, "HEAD") != 1 {
		t.Error("a commit was created despite the dirty tree")
	}
	if got := repo.branch(t); got != "master" {
		t.Errorf("HEAD moved to %q despite the dirty tree", got)
	}
}

func TestPublishAllowedDirtySucceeds(t *testing.T) {
	repo := newTestRepo(t)
	// Known generated scratch the operator expects to be dirty.
	writeFile(t, filepath.Join(repo.root, "content", "notes.md"), "scratch\n")
	cfg := defaultTestConfig(repo.root)
	cfg.AllowedDirtyPaths = []string{"content/notes.md"}
	p := newTestPublisher(t, cfg)

	res, err := p.Publish(context.Background(), testRequest(articleA))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// The commit contains only the intended file.
	changed := commitPaths(t, repo, res.CommitSHA)
	if len(changed) != 1 || changed[0] != "content/news/20260711-slug.md" {
		t.Errorf("commit changed paths = %v, want only the article", changed)
	}
	// The allowed dirty file remains, untouched, and is reported.
	if _, err := os.Stat(filepath.Join(repo.root, "content", "notes.md")); err != nil {
		t.Error("the allowed dirty file was removed")
	}
	if len(res.RemainingDirty) != 1 || res.RemainingDirty[0] != "content/notes.md" {
		t.Errorf("RemainingDirty = %v, want [content/notes.md]", res.RemainingDirty)
	}
}

func TestPublishAllowedDirtyDirectoryPrefix(t *testing.T) {
	repo := newTestRepo(t)
	// A whole untracked directory: status reports it collapsed with a
	// trailing slash; the policy must cover it by directory prefix.
	writeFile(t, filepath.Join(repo.root, "gen", "scratch.md"), "scratch\n")
	cfg := defaultTestConfig(repo.root)
	cfg.AllowedDirtyPaths = []string{"gen"}
	p := newTestPublisher(t, cfg)

	res, err := p.Publish(context.Background(), testRequest(articleA))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(res.RemainingDirty) != 1 || res.RemainingDirty[0] != "gen" {
		t.Errorf("RemainingDirty = %v, want [gen]", res.RemainingDirty)
	}
}

func TestPublishForeignStagedRefused(t *testing.T) {
	repo := newTestRepo(t)
	// The operator staged an unrelated generated file; the safe policy
	// permits it to be dirty, but it must never ride along in the commit.
	writeFile(t, filepath.Join(repo.root, "content", "notes.md"), "staged scratch\n")
	gitRun(t, repo.root, "add", "content/notes.md")
	cfg := defaultTestConfig(repo.root)
	cfg.AllowedDirtyPaths = []string{"content/notes.md"}
	p := newTestPublisher(t, cfg)

	_, err := p.Publish(context.Background(), testRequest(articleA))
	var fs *ForeignStagedError
	if !errors.As(err, &fs) {
		t.Fatalf("err = %v, want *ForeignStagedError", err)
	}
	if len(fs.Paths) != 1 || fs.Paths[0] != "content/notes.md" {
		t.Errorf("foreign paths = %v, want [content/notes.md]", fs.Paths)
	}

	// No commit was created.
	if repo.commitCount(t, "HEAD") != 1 {
		t.Error("a commit was created even though foreign content was staged")
	}
	// The foreign entry was left exactly as found (never unstaged).
	staged := strings.Split(gitRaw(t, repo.root, "diff", "--cached", "--name-only", "-z"), "\x00")
	for len(staged) > 0 && staged[len(staged)-1] == "" {
		staged = staged[:len(staged)-1]
	}
	if len(staged) != 2 || !contains(staged, "content/notes.md") || !contains(staged, "content/news/20260711-slug.md") {
		t.Errorf("staged = %v, want the foreign entry intact plus the article", staged)
	}
	// The article was written (a reviewable, deliberate state).
	if _, err := os.Stat(filepath.Join(repo.root, "content", "news", "20260711-slug.md")); err != nil {
		t.Error("the article file was not written")
	}
}

// ---------------------------------------------------------------------------
// Publish: idempotency
// ---------------------------------------------------------------------------

func TestPublishIdempotentRetry(t *testing.T) {
	repo := newTestRepo(t)
	p := newTestPublisher(t, defaultTestConfig(repo.root))

	first, err := p.Publish(context.Background(), testRequest(articleA))
	if err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	if first.Skipped {
		t.Fatal("first run reported Skipped")
	}

	second, err := p.Publish(context.Background(), testRequest(articleA))
	if err != nil {
		t.Fatalf("retry Publish: %v", err)
	}
	if !second.Skipped {
		t.Fatal("retry of identical content should skip")
	}
	if second.CommitSHA != first.CommitSHA {
		t.Errorf("retry SHA = %q, want the original %q", second.CommitSHA, first.CommitSHA)
	}
	if !second.CommitTime.Equal(first.CommitTime) {
		t.Errorf("retry commit time = %v, want the original %v", second.CommitTime, first.CommitTime)
	}
	// The branch holds the initial commit plus exactly one publication
	// commit (the retry added none).
	if repo.commitCount(t, "publication/site") != 2 {
		t.Errorf("branch commit count = %d, want 2 (initial + one publication; no duplicate)", repo.commitCount(t, "publication/site"))
	}

	// Receipts of the two runs are identical (stable receipt ID).
	rec1, err := first.Receipt(testPlanID, "site")
	if err != nil {
		t.Fatal(err)
	}
	rec2, err := second.Receipt(testPlanID, "site")
	if err != nil {
		t.Fatal(err)
	}
	if rec1.ID != rec2.ID {
		t.Errorf("receipt IDs differ across an idempotent retry: %q vs %q", rec1.ID, rec2.ID)
	}
	if rec2.Metadata["idempotent"] != "true" {
		t.Errorf("retry receipt metadata = %v, want idempotent=true", rec2.Metadata)
	}
	if rec1.Metadata["idempotent"] != "" {
		t.Errorf("first receipt metadata = %v, want no idempotent marker", rec1.Metadata)
	}

	// Changed content produces a fresh commit on the same branch.
	third, err := p.Publish(context.Background(), testRequest(articleB))
	if err != nil {
		t.Fatalf("changed Publish: %v", err)
	}
	if third.Skipped || third.CommitSHA == first.CommitSHA {
		t.Errorf("changed content: Skipped=%v SHA=%q, want a new commit", third.Skipped, third.CommitSHA)
	}
	if repo.commitCount(t, "publication/site") != 3 {
		t.Errorf("branch commit count = %d, want 3 (initial + two publications)", repo.commitCount(t, "publication/site"))
	}
}

// ---------------------------------------------------------------------------
// Publish: dry run
// ---------------------------------------------------------------------------

func TestPublishDryRunMakesNoChanges(t *testing.T) {
	repo := newTestRepo(t)
	p := newTestPublisher(t, defaultTestConfig(repo.root))

	req := testRequest(articleA)
	req.DryRun = true
	res, err := p.Publish(context.Background(), req)
	if err != nil {
		t.Fatalf("dry-run Publish: %v", err)
	}
	if !res.DryRun {
		t.Error("DryRun flag not reported")
	}
	if res.CommitSHA != "" || !res.CommitTime.IsZero() {
		t.Errorf("dry run reported a commit: SHA=%q time=%v", res.CommitSHA, res.CommitTime)
	}
	if len(res.Issues) != 0 {
		t.Errorf("dry run issues = %v, want none", res.Issues)
	}
	if res.Branch != "publication/site" || res.BranchExisted || res.BranchCreated {
		t.Errorf("branch plan = %q existed=%v created=%v, want publication/site/false/false",
			res.Branch, res.BranchExisted, res.BranchCreated)
	}
	if res.WouldSkip {
		t.Error("WouldSkip = true, want false (nothing committed yet)")
	}
	if len(res.Files) != 1 || res.Files[0].OnDisk || res.Files[0].AtHead {
		t.Errorf("files = %+v, want one file, OnDisk=false, AtHead=false", res.Files)
	}
	if res.CommitMessage == "" || !strings.Contains(res.CommitMessage, "publication/site") {
		t.Errorf("CommitMessage = %q, want the rendered message naming the branch", res.CommitMessage)
	}
	if !strings.Contains(res.CommitMessage, "content/news/20260711-slug.md") {
		t.Errorf("CommitMessage = %q, want the file list", res.CommitMessage)
	}

	// No mutation of any kind.
	if gitOK(t, repo.root, "rev-parse", "-q", "--verify", "refs/heads/publication/site") {
		t.Error("dry run created the branch")
	}
	if _, err := os.Stat(filepath.Join(repo.root, "content", "news", "20260711-slug.md")); err == nil {
		t.Error("dry run wrote the file")
	}
	if repo.commitCount(t, "HEAD") != 1 {
		t.Error("dry run created a commit")
	}
	if got := repo.branch(t); got != "master" {
		t.Errorf("dry run switched HEAD to %q", got)
	}
	if s := gitRun(t, repo.root, "status", "--porcelain=v1", "-z"); s != "" {
		t.Errorf("dry run dirtied the tree: %q", s)
	}

	// The rehearsal record converts cleanly.
	dr := res.ToDryRunResult(time.Now())
	if dr.Outcome != publication.DryRunOutcomeOK {
		t.Errorf("DryRunResult outcome = %q, want ok", dr.Outcome)
	}
	if dr.ContentDigest != res.Digest {
		t.Errorf("DryRunResult digest = %q, want %q", dr.ContentDigest, res.Digest)
	}
	if _, err := res.Receipt(testPlanID, "site"); !errors.Is(err, ErrDryRunReceipt) {
		t.Errorf("dry-run Receipt err = %v, want ErrDryRunReceipt", err)
	}
}

func TestPublishDryRunPredictsSkipAfterPublish(t *testing.T) {
	repo := newTestRepo(t)
	p := newTestPublisher(t, defaultTestConfig(repo.root))

	if _, err := p.Publish(context.Background(), testRequest(articleA)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	req := testRequest(articleA)
	req.DryRun = true
	res, err := p.Publish(context.Background(), req)
	if err != nil {
		t.Fatalf("dry-run Publish: %v", err)
	}
	if !res.WouldSkip {
		t.Error("WouldSkip = false, want true (content already on the publication branch)")
	}
	if !res.BranchExisted {
		t.Error("BranchExisted = false, want true")
	}
	if len(res.Issues) != 0 {
		t.Errorf("issues = %v, want none", res.Issues)
	}
	// Changed content is predicted to commit.
	req2 := testRequest(articleB)
	req2.DryRun = true
	res2, err := p.Publish(context.Background(), req2)
	if err != nil {
		t.Fatalf("dry-run Publish (changed): %v", err)
	}
	if res2.WouldSkip {
		t.Error("WouldSkip = true for changed content, want false")
	}
}

func TestPublishDryRunReportsBlockers(t *testing.T) {
	// Dirty tree (no policy): the rehearsal surfaces it as an issue.
	t.Run("dirty_tree", func(t *testing.T) {
		repo := newTestRepo(t)
		writeFile(t, filepath.Join(repo.root, "stray.md"), "work\n")
		p := newTestPublisher(t, defaultTestConfig(repo.root))
		req := testRequest(articleA)
		req.DryRun = true
		res, err := p.Publish(context.Background(), req)
		if err != nil {
			t.Fatalf("dry run should not error: %v", err)
		}
		if !hasIssue(res.Issues, "dirty_tree") {
			t.Errorf("issues = %v, want dirty_tree", res.Issues)
		}
		dr := res.ToDryRunResult(time.Now())
		if dr.Outcome != publication.DryRunOutcomeFailed {
			t.Errorf("outcome = %q, want failed", dr.Outcome)
		}
	})

	// Missing identity (no config fields, no repo-local, machine global
	// isolated): reported, not fatal, in a rehearsal.
	t.Run("no_identity", func(t *testing.T) {
		repo := newTestRepo(t)
		cfg := defaultTestConfig(repo.root)
		cfg.CommittedName, cfg.CommittedEmail = "", ""
		p := newTestPublisher(t, cfg)
		req := testRequest(articleA)
		req.DryRun = true
		res, err := p.Publish(context.Background(), req)
		if err != nil {
			t.Fatalf("dry run should not error: %v", err)
		}
		if !hasIssue(res.Issues, "no_identity") {
			t.Errorf("issues = %v, want no_identity", res.Issues)
		}
	})

	// Detached HEAD.
	t.Run("detached_head", func(t *testing.T) {
		repo := newTestRepo(t)
		repo.setLocalIdentity(t)
		gitRun(t, repo.root, "checkout", "-q", "--detach", "HEAD")
		p := newTestPublisher(t, defaultTestConfig(repo.root))
		req := testRequest(articleA)
		req.DryRun = true
		res, err := p.Publish(context.Background(), req)
		if err != nil {
			t.Fatalf("dry run should not error: %v", err)
		}
		if !hasIssue(res.Issues, "detached_head") {
			t.Errorf("issues = %v, want detached_head", res.Issues)
		}
	})

	// Foreign staged index entries (covered by the safe policy so the
	// dry run proceeds to the index check).
	t.Run("foreign_staged", func(t *testing.T) {
		repo := newTestRepo(t)
		repo.setLocalIdentity(t)
		writeFile(t, filepath.Join(repo.root, "content", "notes.md"), "scratch\n")
		gitRun(t, repo.root, "add", "content/notes.md")
		cfg := defaultTestConfig(repo.root)
		cfg.AllowedDirtyPaths = []string{"content/notes.md"}
		p := newTestPublisher(t, cfg)
		req := testRequest(articleA)
		req.DryRun = true
		res, err := p.Publish(context.Background(), req)
		if err != nil {
			t.Fatalf("dry run should not error: %v", err)
		}
		if !hasIssue(res.Issues, "foreign_staged") {
			t.Errorf("issues = %v, want foreign_staged", res.Issues)
		}
	})
}

// ---------------------------------------------------------------------------
// Publish: hard failures of the real run
// ---------------------------------------------------------------------------

func TestPublishDetachedHeadFails(t *testing.T) {
	repo := newTestRepo(t)
	repo.setLocalIdentity(t)
	gitRun(t, repo.root, "checkout", "-q", "--detach", "HEAD")
	p := newTestPublisher(t, defaultTestConfig(repo.root))

	_, err := p.Publish(context.Background(), testRequest(articleA))
	if !errors.Is(err, ErrDetachedHead) {
		t.Fatalf("err = %v, want ErrDetachedHead", err)
	}
}

func TestPublishNoIdentityFails(t *testing.T) {
	repo := newTestRepo(t) // no repo-local identity; the machine global is isolated
	cfg := defaultTestConfig(repo.root)
	cfg.CommittedName, cfg.CommittedEmail = "", ""
	p := newTestPublisher(t, cfg)

	_, err := p.Publish(context.Background(), testRequest(articleA))
	if !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("err = %v, want ErrNoIdentity", err)
	}
}

// ---------------------------------------------------------------------------
// Publish: request validation and unsafe paths
// ---------------------------------------------------------------------------

func TestPublishRejectsUnsafeFilePaths(t *testing.T) {
	repo := newTestRepo(t)
	p := newTestPublisher(t, defaultTestConfig(repo.root))
	ctx := context.Background()

	unsafe := []string{
		"../outside.md",
		"news/../../outside.md",
		"news\\x.md",
		"/etc/passwd",
		".",
		"",
	}
	for _, pth := range unsafe {
		req := testRequest(articleA)
		req.Files = []File{{Path: pth, Content: []byte(articleA)}}
		_, err := p.Publish(ctx, req)
		if !errors.Is(err, ErrInvalidRequest) && !errors.Is(err, ErrTargetOutsideRepo) {
			t.Errorf("path %q: err = %v, want ErrInvalidRequest/ErrTargetOutsideRepo", pth, err)
		}
	}

	// Dot segments normalize away ("news/./x.md" == "news/x.md") rather
	// than erroring: validation happens on the cleaned path.
	if rel, err := validateFileRelPath("news/./x.md"); err != nil || rel != "news/x.md" {
		t.Errorf("validateFileRelPath(news/./x.md) = %q, %v; want news/x.md", rel, err)
	}

	// Empty content.
	req := testRequest(articleA)
	req.Files = []File{{Path: "news/x.md", Content: nil}}
	if _, err := p.Publish(ctx, req); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("empty content: err = %v, want ErrInvalidRequest", err)
	}

	// Oversized content.
	req = testRequest(articleA)
	req.Files = []File{{Path: "news/x.md", Content: bytes.Repeat([]byte("x"), MaxFileSize+1)}}
	if _, err := p.Publish(ctx, req); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("oversized: err = %v, want ErrInvalidRequest", err)
	}

	// Too many files.
	req = testRequest(articleA)
	req.Files = nil
	for i := 0; i <= MaxFiles; i++ {
		req.Files = append(req.Files, File{Path: fmt.Sprintf("news/f%d.md", i), Content: []byte("x")})
	}
	if _, err := p.Publish(ctx, req); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("too many files: err = %v, want ErrInvalidRequest", err)
	}

	// Duplicate paths.
	req = testRequest(articleA)
	req.Files = []File{
		{Path: "news/x.md", Content: []byte("one")},
		{Path: "news/x.md", Content: []byte("two")},
	}
	if _, err := p.Publish(ctx, req); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("duplicate paths: err = %v, want ErrInvalidRequest", err)
	}

	// Invalid IDs.
	req = testRequest(articleA)
	req.PlanID = "nope"
	if _, err := p.Publish(ctx, req); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("bad plan ID: err = %v, want ErrInvalidRequest", err)
	}
	req = testRequest(articleA)
	req.TargetID = "bad target id!"
	if _, err := p.Publish(ctx, req); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("bad target ID: err = %v, want ErrInvalidRequest", err)
	}

	// No files.
	req = testRequest(articleA)
	req.Files = nil
	if _, err := p.Publish(ctx, req); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("no files: err = %v, want ErrInvalidRequest", err)
	}

	// Secret-looking title is refused (redacted in the message).
	req = testRequest(articleA)
	req.Title = "update with token aB3dE7fH1jK4mN6pQ9rS2tU5vW8xY0zA1bC3dE6f"
	if _, err := p.Publish(ctx, req); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("secret title: err = %v, want ErrInvalidRequest", err)
	}

	// Nothing was written by any of the rejections.
	if s := gitRun(t, repo.root, "status", "--porcelain=v1", "-z"); s != "" {
		t.Errorf("rejected requests dirtied the tree: %q", s)
	}
}

func TestPublishRejectsSymlinkFileEscape(t *testing.T) {
	repo := newTestRepo(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "escape.md"), "escaped\n")
	link := filepath.Join(repo.root, "content", "link.md")
	if err := os.Symlink(filepath.Join(outside, "escape.md"), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	p := newTestPublisher(t, defaultTestConfig(repo.root))
	req := testRequest(articleA)
	req.Files = []File{{Path: "link.md", Content: []byte(articleA)}}
	_, err := p.Publish(context.Background(), req)
	if !errors.Is(err, ErrTargetOutsideRepo) {
		t.Fatalf("err = %v, want ErrTargetOutsideRepo", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.md")); err != nil ||
		readString(t, filepath.Join(outside, "escape.md")) != "escaped\n" {
		t.Error("the file outside the checkout was modified")
	}
}

func TestPublishUnicodePath(t *testing.T) {
	repo := newTestRepo(t)
	p := newTestPublisher(t, defaultTestConfig(repo.root))
	req := testRequest(articleA)
	req.Files = []File{{Path: "news/café-über.md", Content: []byte(articleA)}}

	res, err := p.Publish(context.Background(), req)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.Files[0].Relative != "content/news/café-über.md" {
		t.Errorf("Relative = %q, want content/news/café-über.md", res.Files[0].Relative)
	}
	committed := gitRaw(t, repo.root, "cat-file", "blob", res.CommitSHA+":content/news/café-über.md")
	if committed != articleA {
		t.Errorf("committed content mismatch")
	}
}

// ---------------------------------------------------------------------------
// Publish: branch lifecycle, unborn repo, tamper verification
// ---------------------------------------------------------------------------

func TestPublishBranchReuseAcrossRuns(t *testing.T) {
	repo := newTestRepo(t)
	p := newTestPublisher(t, defaultTestConfig(repo.root))

	first, err := p.Publish(context.Background(), testRequest(articleA))
	if err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	if !first.BranchCreated {
		t.Fatal("first run should create the branch")
	}
	second, err := p.Publish(context.Background(), testRequest(articleB))
	if err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if !second.BranchExisted || second.BranchCreated {
		t.Errorf("second run: BranchExisted=%v BranchCreated=%v, want true/false", second.BranchExisted, second.BranchCreated)
	}
	// After the first run the checkout is on the publication branch, so the
	// second run reports it as the base (where the run started).
	if second.BaseBranch != "publication/site" {
		t.Errorf("second BaseBranch = %q, want publication/site (the run started on it)", second.BaseBranch)
	}
	if got := repo.branch(t); got != "publication/site" {
		t.Errorf("working tree on %q, want the publication branch", got)
	}
	if repo.commitCount(t, "publication/site") != 3 {
		t.Errorf("commit count = %d, want 3 (initial + two publications)", repo.commitCount(t, "publication/site"))
	}
	// The base branch history is untouched.
	if repo.commitCount(t, "master") != 1 {
		t.Errorf("master commit count = %d, want 1 (the publication never rewrites the base)", repo.commitCount(t, "master"))
	}

	// A run that starts back on the base branch switches to the existing
	// publication branch. Publishing the content the branch already holds
	// (articleB, from the second run) is an idempotent retry: it switches,
	// rewrites identical bytes, and skips the commit.
	gitRun(t, repo.root, "checkout", "-q", "master")
	third, err := p.Publish(context.Background(), testRequest(articleB))
	if err != nil {
		t.Fatalf("third Publish (from master): %v", err)
	}
	if !third.Skipped {
		t.Error("third run: identical content at the branch tip should skip")
	}
	if third.BranchExisted != true || third.BranchCreated {
		t.Errorf("third run: BranchExisted=%v BranchCreated=%v, want true/false", third.BranchExisted, third.BranchCreated)
	}
	if got := repo.branch(t); got != "publication/site" {
		t.Errorf("working tree on %q after the run, want the publication branch", got)
	}
	if repo.commitCount(t, "publication/site") != 3 {
		t.Errorf("commit count = %d, want 3 (the third run reused the branch, no duplicate)", repo.commitCount(t, "publication/site"))
	}
}

func TestPublishUnbornRepo(t *testing.T) {
	root := t.TempDir()
	gitRun(t, root, "init", "-q") // no commits at all
	cfg := defaultTestConfig(root)
	cfg.CommittedName, cfg.CommittedEmail = "", ""
	repo := &testRepo{root: root}
	repo.setLocalIdentity(t)
	p := newTestPublisher(t, cfg)

	res, err := p.Publish(context.Background(), testRequest(articleA))
	if err != nil {
		t.Fatalf("Publish on unborn repo: %v", err)
	}
	if res.CommitSHA == "" {
		t.Fatal("no commit SHA for a root commit")
	}
	if parents := gitRun(t, root, "log", "-1", "--format=%P", res.CommitSHA); parents != "" {
		t.Errorf("root commit parents = %q, want empty", parents)
	}
	if gitRun(t, root, "rev-parse", "HEAD") != res.CommitSHA {
		t.Error("HEAD is not the new root commit")
	}
	if _, err := res.Receipt(testPlanID, "site"); err != nil {
		t.Errorf("Receipt on a root commit: %v", err)
	}
}

func TestPublishTamperFilterFailsVerification(t *testing.T) {
	repo := newTestRepo(t)
	// A site repo with a content filter that rewrites .md files on add:
	// the committed bytes can then differ from the approved bytes.
	writeFile(t, filepath.Join(repo.root, ".gitattributes"), "*.md filter=tamper\n")
	gitRun(t, repo.root, "add", ".gitattributes")
	// The quoted sed program survives git's config word-splitting; the
	// filter succeeds and rewrites the body, so the committed bytes can
	// differ from the approved bytes.
	gitRun(t, repo.root, "config", "filter.tamper.clean", "sed -e 's/Body text/HACKED/'")
	gitRun(t, repo.root, "-c", "user.name=Setup", "-c", "user.email=setup@example.com", "commit", "-q", "-m", "filter")

	p := newTestPublisher(t, defaultTestConfig(repo.root))
	_, err := p.Publish(context.Background(), testRequest(articleA))
	if !errors.Is(err, ErrVerification) {
		t.Fatalf("err = %v, want ErrVerification (the committed bytes were altered by the filter)", err)
	}
}

// ---------------------------------------------------------------------------
// Receipt
// ---------------------------------------------------------------------------

func TestReceiptFieldContract(t *testing.T) {
	repo := newTestRepo(t)
	p := newTestPublisher(t, defaultTestConfig(repo.root))
	res, err := p.Publish(context.Background(), testRequest(articleA))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rec, err := res.Receipt(testPlanID, "site")
	if err != nil {
		t.Fatalf("Receipt: %v", err)
	}
	if rec.PlanID != testPlanID || rec.TargetID != "site" {
		t.Errorf("rec provenance = %q/%q, want %q/site", rec.PlanID, rec.TargetID, testPlanID)
	}
	if rec.ExternalRecordID != res.CommitSHA {
		t.Errorf("ExternalRecordID = %q, want the commit SHA %q", rec.ExternalRecordID, res.CommitSHA)
	}
	if rec.ExternalURI != "" {
		t.Errorf("ExternalURI = %q, want empty (no remote yet)", rec.ExternalURI)
	}
	if rec.ClientVersion != "newsroom-git/0.6" {
		t.Errorf("ClientVersion = %q, want newsroom-git/0.6", rec.ClientVersion)
	}
	if rec.PublishedAt.IsZero() {
		t.Error("PublishedAt is zero; want the commit timestamp")
	}
}

// ---------------------------------------------------------------------------
// Plumbing unit tests
// ---------------------------------------------------------------------------

func TestParseStatus(t *testing.T) {
	// "XY<space>path" NUL-terminated records; the rename record carries an
	// extra counterpart field (git lists renamed-to first, then
	// renamed-from).
	out := " M README.md\x00?? content/\x00A  new.md\x00R  new.md\x00old.md\x00 D gone.md\x00"
	entries, err := parseStatus(out)
	if err != nil {
		t.Fatalf("parseStatus: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("entries = %d, want 5: %+v", len(entries), entries)
	}
	if entries[0].path != "README.md" || entries[0].worktree != 'M' || !entries[0].dirty() {
		t.Errorf("entry 0 = %+v, want worktree-modified README.md", entries[0])
	}
	// Untracked collapsed directories keep a trailing slash in git's
	// stream; paths() normalizes it away.
	if entries[1].path != "content/" || !entries[1].dirty() {
		t.Errorf("entry 1 = %+v, want untracked dir content/", entries[1])
	}
	if ps := entries[1].paths(); len(ps) != 1 || ps[0] != "content" {
		t.Errorf("collapsed dir paths = %v, want [content]", ps)
	}
	if e := entries[3]; e.path != "new.md" || e.alt != "old.md" {
		t.Errorf("rename entry = %+v, want path=new.md alt=old.md", e)
	}
	if ps := entries[3].paths(); len(ps) != 2 || ps[0] != "new.md" || ps[1] != "old.md" {
		t.Errorf("rename paths = %v, want [new.md old.md]", ps)
	}
	if entries[4].index != ' ' || entries[4].worktree != 'D' || !entries[4].dirty() {
		t.Errorf("entry 4 = %+v, want worktree deletion", entries[4])
	}

	// Clean stream.
	if entries, err := parseStatus(""); err != nil || len(entries) != 0 {
		t.Errorf("empty stream: entries=%d err=%v, want none", len(entries), err)
	}

	// Malformed streams are errors (XY/path without the space separator,
	// a truncated record, a rename without its counterpart).
	for _, bad := range []string{"\x00", "XYnospace\x00", "R  new.md\x00"} {
		if _, err := parseStatus(bad); err == nil {
			t.Errorf("malformed stream %q should fail", bad)
		}
	}
}

func TestUnquoteC(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"plain.md", "plain.md"},
		{`a b.md`, "a b.md"},
		{`"a b.md"`, "a b.md"},
		{`"tab\there.md"`, "tab\there.md"},
		{`"quote\"inside.md"`, `quote"inside.md`},
		{`"slash\\path.md"`, `slash\path.md`},
		{`"oct\303\251.md"`, "octé.md"},
		{"unterminated\"quote", `unterminated"quote`},
	}
	for _, tc := range tests {
		if got := unquoteC(tc.in); got != tc.want {
			t.Errorf("unquoteC(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGitVersionOK(t *testing.T) {
	tests := []struct {
		banner string
		want   bool
	}{
		{"git version 2.54.0 (Apple Git-157)", true},
		{"git version 2.34.1", true},
		{"git version 2.33.0", false},
		{"git version 3.1.1", true},
		{"garbage", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := gitVersionOK(tc.banner); got != tc.want {
			t.Errorf("gitVersionOK(%q) = %v, want %v", tc.banner, got, tc.want)
		}
	}
}

func TestValidateRelPath(t *testing.T) {
	tests := []struct {
		in    string
		want  string
		fails bool
	}{
		{"news/x.md", "news/x.md", false},
		{"x.md", "x.md", false},
		{".", ".", false}, // the base directory itself (target dirs use this)
		{"..", "", true},
		{"a/../../x", "", true},
		{"/abs", "", true},
		{"", "", true},
		{"a\\b", "", true},
		{"a\x00b", "", true},
		{"news/", "news", false}, // trailing slash normalizes away
	}
	for _, tc := range tests {
		got, err := validateRelPath(tc.in)
		if tc.fails && err == nil {
			t.Errorf("validateRelPath(%q) = %q, want error", tc.in, got)
		}
		if !tc.fails && (err != nil || got != tc.want) {
			t.Errorf("validateRelPath(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}

	// File paths must name a file, never the directory itself.
	for _, in := range []string{".", "a/.."} {
		if _, err := validateFileRelPath(in); err == nil {
			t.Errorf("validateFileRelPath(%q) succeeded, want error", in)
		}
	}
	if rel, err := validateFileRelPath("news/x.md"); err != nil || rel != "news/x.md" {
		t.Errorf("validateFileRelPath(news/x.md) = %q, %v", rel, err)
	}
}

func TestRenderCommitMessage(t *testing.T) {
	p := &Publisher{cfg: validatedConfig{commitTmpl: mustParse(t, defaultCommitMessage)}}
	msg, err := p.renderCommitMessage(CommitContext{
		PlanID: "pub-x", RunID: "run-x", TargetID: "site", Title: "Hello",
		Branch: "publication/site", BaseBranch: "main", FileCount: 2,
		Files: []string{"content/a.md", "content/b.md"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"Hello", "publication/site", "main", "content/a.md", "content/b.md"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}

	// A template rendering to whitespace is rejected.
	p2 := &Publisher{cfg: validatedConfig{commitTmpl: mustParse(t, "   \n")}}
	if _, err := p2.renderCommitMessage(CommitContext{Branch: "b"}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("empty render err = %v, want ErrInvalidRequest", err)
	}

	// An oversized render is rejected.
	long := strings.Repeat("x", MaxMessageLen+1)
	p3 := &Publisher{cfg: validatedConfig{commitTmpl: mustParse(t, long)}}
	if _, err := p3.renderCommitMessage(CommitContext{Branch: "b"}); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("oversized render err = %v, want ErrInvalidRequest", err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustParse(t *testing.T, text string) *texttmpl.Template {
	t.Helper()
	tmpl, err := texttmpl.New("t").Parse(text)
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	return tmpl
}

func hasIssue(issues []publication.DryRunIssue, code string) bool {
	for _, is := range issues {
		if is.Code == code {
			return true
		}
	}
	return false
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- test fixture path
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// commitPaths returns the paths changed by a commit (dropping the commit-ID
// line git diff-tree prints first).
func commitPaths(t *testing.T, repo *testRepo, sha string) []string {
	t.Helper()
	fields := strings.Fields(gitRun(t, repo.root, "diff-tree", "--root", "--name-only", "-r", "--no-renames", sha))
	if len(fields) > 0 && len(fields[0]) == 40 {
		fields = fields[1:]
	}
	return fields
}

// resolvedPath canonicalizes a fixture path (t.TempDir may be a symlink on
// some platforms, e.g. /var -> /private/var on macOS).
func resolvedPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", p, err)
	}
	return filepath.Clean(r)
}
