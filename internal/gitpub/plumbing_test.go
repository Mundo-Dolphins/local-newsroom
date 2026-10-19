package gitpub

// Error-path and small-unit coverage for the git plumbing and helpers that
// the end-to-end fixtures cannot reach (they only exercise success paths
// and the failures that a real publication run can produce).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// mustGitPath resolves the git executable for direct client construction.
func mustGitPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not available: %v", err)
	}
	return p
}

// newClient builds a gitClient bound to dir with the isolation files in
// place (cleanup registered).
func newClient(t *testing.T, dir string) *gitClient {
	t.Helper()
	c := &gitClient{bin: mustGitPath(t), checkout: dir}
	if err := c.mkIsolation(); err != nil {
		t.Fatalf("mkIsolation: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestClientErrorPathsOutsideRepo(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	c := newClient(t, t.TempDir()) // a directory that is not a repository

	if inside, err := c.isInsideWorkTree(ctx, t.TempDir()); err == nil || inside {
		t.Errorf("isInsideWorkTree(non-repo) = %v, %v; want false, an error", inside, err)
	}
	if inside, err := c.isInsideWorkTree(ctx, repo.root); err != nil || !inside {
		t.Errorf("isInsideWorkTree(real repo) = %v, %v; want true, nil", inside, err)
	}
	if _, err := c.toplevel(ctx, c.checkout); err == nil {
		t.Error("toplevel(non-repo) succeeded, want an error")
	}
	if exists, err := c.headExists(ctx); err == nil || exists {
		t.Errorf("headExists(non-repo) = %v, %v; want error", exists, err)
	}
	if _, attached, err := c.currentBranch(ctx); err == nil || attached {
		t.Errorf("currentBranch(non-repo) = attached=%v, %v; want error", attached, err)
	}
	if _, err := c.branchExists(ctx, "main"); err == nil {
		t.Error("branchExists(non-repo) succeeded, want an error")
	}
	if _, err := c.branchValid(ctx, "bad name!"); err == nil {
		t.Error("branchValid(invalid name, non-repo) succeeded, want an error (git exits 128)")
	}
	if _, err := c.status(ctx); err == nil {
		t.Error("status(non-repo) succeeded, want an error")
	}
	if _, err := c.stagedPaths(ctx); err == nil {
		t.Error("stagedPaths(non-repo) succeeded, want an error")
	}
	if err := c.stage(ctx, "x.md"); err == nil {
		t.Error("stage(non-repo) succeeded, want an error")
	}
	if err := c.switchTo(ctx, "nope"); err == nil {
		t.Error("switchTo(non-repo) succeeded, want an error")
	}
	if err := c.createBranch(ctx, "nope"); err == nil {
		t.Error("createBranch(non-repo) succeeded, want an error")
	}
	if err := c.commit(ctx, "message", nil); err == nil {
		t.Error("commit(non-repo) succeeded, want an error")
	}
	if _, err := c.headSHA(ctx); !errors.Is(err, ErrGitCommand) {
		t.Errorf("headSHA(non-repo) err = %v, want ErrGitCommand", err)
	}
	if _, err := c.commitTime(ctx, "bogus"); err == nil {
		t.Error("commitTime(bogus, non-repo) succeeded, want an error")
	}
	if _, err := c.commitChangedPaths(ctx, "bogus"); err == nil {
		t.Error("commitChangedPaths(bogus, non-repo) succeeded, want an error")
	}
	if _, err := c.commitParents(ctx, "bogus"); err == nil {
		t.Error("commitParents(bogus, non-repo) succeeded, want an error")
	}
	if _, err := c.blobContent(ctx, "bogus", "x.md"); !errors.Is(err, ErrGitCommand) {
		t.Errorf("blobContent(bogus, non-repo) err = %v, want ErrGitCommand", err)
	}
	if _, err := c.showAt(ctx, "bogus", "x.md"); err == nil {
		t.Error("showAt(bogus, non-repo) succeeded, want an error")
	}
	// config with the isolated (empty) global config: unset, not an error.
	if v, err := c.configValue(ctx, "user.name"); err != nil || v != "" {
		t.Errorf("configValue(user.name, non-repo) = %q, %v; want empty, nil", v, err)
	}
	name, email, err := c.identity(ctx)
	if err != nil || name != "" || email != "" {
		t.Errorf("identity(non-repo) = %q, %q, %v; want empty, empty, nil", name, email, err)
	}
}

func TestClientErrorPathsInsideRepo(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	c := newClient(t, repo.root)

	// A commit with an empty index exits 1 ("nothing to commit").
	if err := c.commit(ctx, "message", nil); err == nil {
		t.Error("commit with nothing staged succeeded, want an error")
	}
	// Creating an already-existing branch exits 231.
	if err := c.createBranch(ctx, "master"); err == nil {
		t.Error("createBranch(existing) succeeded, want an error")
	}
	// Checking out a missing branch exits 1.
	if err := c.switchTo(ctx, "definitely-missing"); err == nil {
		t.Error("switchTo(missing) succeeded, want an error")
	}
	// cat-file on a missing path: the runErr path with a git message.
	if _, err := c.blobContent(ctx, strings.Repeat("0", 40), "x.md"); err == nil {
		t.Error("blobContent(missing path) succeeded, want an error")
	}
}

func TestClientUnbornHeadStates(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t)
	c := newClient(t, repo.root)

	// headExists maps the -q verify exit codes: 0 = exists, 1 = unborn.
	if exists, err := c.headExists(ctx); err != nil || !exists {
		t.Errorf("headExists(populated) = %v, %v; want true, nil", exists, err)
	}
	unborn := gitInitRepo(t)
	uc := newClient(t, unborn)
	if exists, err := uc.headExists(ctx); err != nil || exists {
		t.Errorf("headExists(unborn) = %v, %v; want false, nil", exists, err)
	}
	if _, attached, err := uc.currentBranch(ctx); err != nil || !attached {
		t.Errorf("currentBranch(unborn) = attached=%v, %v; want true, nil", attached, err)
	}
}

func TestRunErrSurface(t *testing.T) {
	// The runErr text is redacted, bounded, and Is-matches the sentinel.
	e := &runErr{
		sub:  "show",
		code: 128,
		out:  "token=aB3dE7fH1jK4mN6pQ9rS2tU5vW8xY0zA1bC3dE6f",
		errb: "key=supersecretvalue1234567890supersecretvalue1234567890supersecret",
	}
	msg := e.Error()
	if !strings.Contains(msg, "git show exited 128") {
		t.Errorf("Error() = %q, want it to name the subcommand and code", msg)
	}
	if strings.Contains(msg, "aB3dE7fH1jK4mN6pQ9rS2tU5vW8xY0zA1bC3dE6f") {
		t.Error("Error() leaked a secret-looking value")
	}
	if len(msg) > maxErrOutput+64 {
		t.Errorf("Error() length %d exceeds the bound", len(msg))
	}
	if !errors.Is(e, ErrGitCommand) {
		t.Error("runErr must Is-match ErrGitCommand")
	}
	if errors.Is(e, ErrVerification) {
		t.Error("runErr must not Is-match unrelated sentinels")
	}
	// No output at all: just the base message.
	plain := (&runErr{sub: "rev-parse", code: 1}).Error()
	if plain != "git command failed: git rev-parse exited 1" {
		t.Errorf("plain Error() = %q", plain)
	}
}

func TestErrorStrings(t *testing.T) {
	dirty := &DirtyTreeError{
		Offenders: make([]string, 40),
		Allowed:   []string{"generated/", "state.json"},
	}
	for i := range dirty.Offenders {
		dirty.Offenders[i] = fmt.Sprintf("offender-%02d.md", i)
	}
	msg := dirty.Error()
	if !strings.Contains(msg, "40 path(s)") {
		t.Errorf("DirtyTreeError.Error() = %q, want the offender count", msg)
	}
	if !strings.Contains(msg, "allowed dirty paths") {
		t.Errorf("DirtyTreeError.Error() = %q, want the allowed policy rendered", msg)
	}
	if !errors.Is(errAny(dirty), ErrDirtyTree) {
		t.Error("*DirtyTreeError must Is-match ErrDirtyTree")
	}

	small := &DirtyTreeError{Offenders: []string{"a.md"}}
	if !strings.Contains(small.Error(), "must be clean") {
		t.Errorf("no-policy DirtyTreeError.Error() = %q, want the clean-tree hint", small.Error())
	}

	foreign := &ForeignStagedError{Paths: []string{"a.md", "b.md"}}
	if !strings.Contains(foreign.Error(), "a.md, b.md") {
		t.Errorf("ForeignStagedError.Error() = %q", foreign.Error())
	}
	if !errors.Is(errAny(foreign), ErrStagedForeign) {
		t.Error("*ForeignStagedError must Is-match ErrStagedForeign")
	}

	det := &detachedHeadError{}
	if det.Error() != ErrDetachedHead.Error() || !errors.Is(errAny(det), ErrDetachedHead) {
		t.Errorf("detachedHeadError: Error()=%q Is=%v", det.Error(), errors.Is(errAny(det), ErrDetachedHead))
	}
}

// errAny adapts an error value to the interface for errors.Is checks.
func errAny(e error) error { return e }

func TestHeadN(t *testing.T) {
	three := []string{"a", "b", "c"}
	if got := headN(three, 5); len(got) != 3 || got[0] != "a" {
		t.Errorf("headN(3, 5) = %v, want the input as-is", got)
	}
	five := []string{"a", "b", "c", "d", "e"}
	got := headN(five, 3)
	if len(got) != 3 || got[2] != "c" {
		t.Errorf("headN(5, 3) = %v, want the first three", got)
	}
	if got := headN(nil, 3); len(got) != 0 {
		t.Errorf("headN(nil) = %v, want empty", got)
	}
}

func TestProbeFile(t *testing.T) {
	dir := t.TempDir()

	onDisk, unchanged := probeFile(filepath.Join(dir, "missing.md"), []byte("x"))
	if onDisk || unchanged {
		t.Errorf("probeFile(missing) = %v, %v; want false, false", onDisk, unchanged)
	}
	writeFile(t, filepath.Join(dir, "same.md"), "content")
	onDisk, unchanged = probeFile(filepath.Join(dir, "same.md"), []byte("content"))
	if !onDisk || !unchanged {
		t.Errorf("probeFile(identical) = %v, %v; want true, true", onDisk, unchanged)
	}
	writeFile(t, filepath.Join(dir, "diff.md"), "original")
	onDisk, unchanged = probeFile(filepath.Join(dir, "diff.md"), []byte("changed"))
	if !onDisk || unchanged {
		t.Errorf("probeFile(different) = %v, %v; want true, false", onDisk, unchanged)
	}
}

func TestAtomicWriteFailureModes(t *testing.T) {
	dir := t.TempDir()

	// A pre-existing DIRECTORY at the target name makes the final rename
	// fail; the directory must be left untouched and no temp residue left.
	sub := filepath.Join(dir, "target.md")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(dir, "target.md", []byte("x"), 0o644); err == nil {
		t.Fatal("atomicWrite onto an existing directory succeeded, want a rename error")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 || entries[0].Name() != "target.md" {
		t.Errorf("dir entries = %v, want only the pre-existing directory (no temp residue)", entries)
	}

	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block writes")
	}
	ro := t.TempDir()
	_ = os.Chmod(ro, 0o555)
	defer func() { _ = os.Chmod(ro, 0o755) }()
	if err := atomicWrite(ro, "f.md", []byte("x"), 0o644); err == nil {
		t.Error("atomicWrite into a read-only directory succeeded, want an error")
	}
}

func TestAccessors(t *testing.T) {
	repo := newTestRepo(t)
	cfg := defaultTestConfig(repo.root)
	cfg.Branch = "publication/custom"
	cfg.TargetDir = "content"
	cfg.CommitMessage = "custom: {{.Title}}"
	cfg.CommittedName = "N"
	cfg.CommittedEmail = "e@example.com"
	cfg.AllowedDirtyPaths = []string{"gen/"}
	p := newTestPublisher(t, cfg)

	got := p.Config()
	if resolvedPath(t, got.RepoRoot) != resolvedPath(t, cfg.RepoRoot) || got.Branch != cfg.Branch || got.TargetDir != cfg.TargetDir ||
		got.CommitMessage != cfg.CommitMessage || got.CommittedName != cfg.CommittedName ||
		got.CommittedEmail != cfg.CommittedEmail || len(got.AllowedDirtyPaths) != 1 {
		t.Errorf("Config() round-trip mismatch: %+v", got)
	}
	if resolvedPath(t, p.RepoRoot()) != resolvedPath(t, cfg.RepoRoot) {
		t.Errorf("RepoRoot() = %q, want the resolved configured root %q", p.RepoRoot(), cfg.RepoRoot)
	}
	if resolvedPath(t, p.CheckoutRoot()) != resolvedPath(t, p.RepoRoot()) {
		t.Errorf("CheckoutRoot() = %q, want the resolved repository root %q", p.CheckoutRoot(), p.RepoRoot())
	}
}

func TestVerifyCommitGuards(t *testing.T) {
	repo := newTestRepo(t)
	p := newTestPublisher(t, defaultTestConfig(repo.root))
	req := testRequest(articleA)
	res, err := p.Publish(context.Background(), req)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	ctx := context.Background()
	rel := res.Files[0].Relative
	staged := []string{rel}
	content := map[string][]byte{rel: []byte(articleA)}
	initial := gitRun(t, repo.root, "rev-parse", "master")

	// A wrong expected parent fails verification (nothing was committed
	// under the claimed parent).
	if err := p.verifyCommit(ctx, res.CommitSHA, res.CommitSHA, staged, content); !errors.Is(err, ErrVerification) {
		t.Errorf("verifyCommit(wrong parent) err = %v, want ErrVerification", err)
	}
	// A staged set wider than the publication files fails verification.
	if err := p.verifyCommit(ctx, res.CommitSHA, initial, append(staged, "foreign.md"), content); !errors.Is(err, ErrVerification) {
		t.Errorf("verifyCommit(wrong staged set) err = %v, want ErrVerification", err)
	}
	// Bytes that differ from the approved content fail verification.
	if err := p.verifyCommit(ctx, res.CommitSHA, initial, staged, map[string][]byte{rel: []byte("tampered")}); !errors.Is(err, ErrVerification) {
		t.Errorf("verifyCommit(tampered bytes) err = %v, want ErrVerification", err)
	}
	// The true post-commit state verifies cleanly.
	if err := p.verifyCommit(ctx, res.CommitSHA, initial, staged, content); err != nil {
		t.Errorf("verifyCommit(consistent) err = %v, want nil", err)
	}
}

func TestMkIsolationTempFailure(t *testing.T) {
	// MkdirTemp honors TMPDIR: pointing it at a regular file forces the
	// isolation-directory failure branch.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", file) // point the temp dir at a regular file
	c := &gitClient{bin: mustGitPath(t), checkout: t.TempDir()}
	if err := c.mkIsolation(); err == nil {
		t.Fatal("mkIsolation with an unusable TMPDIR succeeded, want an error")
	}
	if c.base != "" {
		t.Error("mkIsolation failure must leave base unset")
	}
}

func TestUnquoteCEdgeCases(t *testing.T) {
	cases := map[string]string{
		`"abc"`:       "abc",
		`"a b"`:       "a b",
		`"\n"`:        "\n",
		`"\t"`:        "\t",
		`"\b"`:        "\b",
		`"\f"`:        "\f",
		`"\r"`:        "\r",
		`"\""`:        `"`,
		`"\\"`:        `\`,
		`"\101"`:      "A",
		`"\10"`:       "\x08",
		`"\0"`:        "\x00",
		`"\z"`:        `\z`,
		`"trailing\"`: `trailing\`,
		`"":`:         `"":`, // not fully quoted: returned as-is
		`x"`:          `x"`,
	}
	for in, want := range cases {
		if got := unquoteC(in); got != want {
			t.Errorf("unquoteC(%q) = %q, want %q", in, got, want)
		}
	}
}

// gitInitRepo creates an empty (unborn) repository in a temp dir.
func gitInitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "master")
	return dir
}
