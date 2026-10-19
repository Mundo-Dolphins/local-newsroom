package gitpub

// gitClient is the tightly controlled boundary around the installed git
// executable. Everything in this file is plumbing: fixed argv, a minimal
// environment, documented exit codes, and no shell anywhere.
//
// Safety envelope (applied to every invocation):
//
//   - argv is a fixed list constructed here (a -C / -c prefix plus
//     adapter-built arguments over validated paths); there is no shell, so
//     no path value can inject options or code;
//   - the environment is a minimal, explicit allowlist: LC_ALL, the config
//     isolation variables, no terminal prompt, and the parent's PATH.
//     GIT_CONFIG_NOSYSTEM disables the system config and GIT_CONFIG_GLOBAL
//     points at an empty temporary file, so no global/system identity,
//     hooks, aliases, or filters can leak in. GIT_DIR / GIT_WORK_TREE and
//     the like are deliberately NOT inherited, so a foreign process
//     environment cannot steer the adapter at another repository. The
//     PATH is inherited so the site repo's own content filters (clean/
//     smudge commands, e.g. a line-ending normalizer) resolve exactly as
//     they would in the operator's own git; their byte effect is then
//     caught by the post-commit content verification, which requires the
//     committed blobs to be byte-identical to the approved content.
//   - -c core.hooksPath=<empty dir> runs on every command: no hook of any
//     kind (pre/post checkout, pre-commit, commit-msg, post-commit) can
//     execute, even if the repository-local config points hooks elsewhere.
//   - both -C and the working directory are the resolved checkout root.
//
// Commands this adapter may invoke (and only these): --version, rev-parse,
// symbolic-ref, status, check-ref-format, config, checkout, add, diff,
// commit, show, cat-file.
//
// It never invokes push, fetch, merge, rebase, reset, clean, gc,
// branch -D, or any force/remote/destructive operation. (No --no-verify is
// passed: Apple's git build does not accept it on checkout/switch, and the
// hooksPath override makes it redundant.)
import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// maxErrOutput bounds the redacted output carried in a git error so a
// pathological stderr cannot bloat a receipt or a plan error log.
const maxErrOutput = 2048

// gitClient executes git in one resolved checkout.
type gitClient struct {
	bin       string // absolute path of the git executable
	checkout  string // resolved checkout root (git toplevel)
	hooksDir  string // empty temporary directory (hooks disabled)
	globalCfg string // empty temporary config file (GIT_CONFIG_GLOBAL)
	base      string // temporary directory owning hooksDir + globalCfg
	path      string // inherited PATH (so repo-configured filters resolve)
}

// mkIsolation creates the temporary isolation files (empty hooks directory
// and empty global config file); the caller must call Close. The client's
// bin and checkout must already be set.
func (c *gitClient) mkIsolation() error {
	dir, err := os.MkdirTemp("", "gitpub-*")
	if err != nil {
		return fmt.Errorf("creating git isolation directory: %w", err)
	}
	c.base = dir
	c.hooksDir = filepath.Join(dir, "hooks")
	c.globalCfg = filepath.Join(dir, "config")
	if err := os.MkdirAll(c.hooksDir, 0o700); err != nil {
		_ = os.RemoveAll(dir) // #nosec G307 -- best-effort cleanup; failure only leaks our temp dir
		c.base = ""
		return fmt.Errorf("creating git isolation directory: %w", err)
	}
	// An empty (zero-byte) global config file: valid to git, and it
	// contributes no hooks, filters, aliases, or identity.
	f, err := os.OpenFile(c.globalCfg, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir) // #nosec G307 -- best-effort cleanup; failure only leaks our temp dir
		c.base = ""
		return fmt.Errorf("creating git isolation config: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.RemoveAll(dir) // #nosec G307 -- best-effort cleanup; failure only leaks our temp dir
		c.base = ""
		return fmt.Errorf("creating git isolation config: %w", err)
	}
	// The parent's PATH is the only inherited variable: it lets the site
	// repo's own filter commands resolve, without exposing HOME, SHELL, or
	// any other inherited state to the git processes.
	c.path = os.Getenv("PATH")
	return nil
}

// Close removes the temporary isolation directory (empty hooks dir and
// empty global config). Idempotent.
func (c *gitClient) Close() error {
	if c.base == "" {
		return nil
	}
	err := os.RemoveAll(c.base) // #nosec G307 -- temporary dir created in mkIsolation
	c.base = ""
	return err
}

// env is the complete environment of every git process.
func (c *gitClient) env() []string {
	return []string{
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + c.globalCfg,
		"GIT_TERMINAL_PROMPT=0",
		"PATH=" + c.path,
	}
}

// runErr is a git invocation that exited non-zero. The caller (not runExit)
// decides whether the exit code is a documented, expected one: several git
// commands signal their result through the exit code (diff --quiet: 1 =
// differences; rev-parse -q --verify: 1 = missing).
type runErr struct {
	// sub is the git subcommand (e.g. "commit"), for the message.
	sub string
	// code is the exit code (always != 0 here).
	code int
	// out / errb are the redacted outputs.
	out  string
	errb string
}

// Error implements error.
func (e *runErr) Error() string {
	msg := fmt.Sprintf("%s: git %s exited %d", ErrGitCommand, e.sub, e.code)
	if s := trimOutput(e.errb); s != "" {
		msg += ": " + s
	} else if s := trimOutput(e.out); s != "" {
		msg += ": " + s
	}
	return msg
}

// Is makes a runErr whose sub matches target match errors.Is(target).
func (e *runErr) Is(target error) bool {
	return target == ErrGitCommand
}

// trimOutput redacts secret-looking material and bounds the length.
func trimOutput(s string) string {
	s = run.RedactSecrets(s)
	s = strings.TrimSpace(s)
	if len(s) > maxErrOutput {
		s = s[:maxErrOutput] + "…"
	}
	return s
}

// runExit executes git with the full safety envelope and reports the exit
// code. A non-exit failure (spawn error, context cancellation) is returned
// as an error; a non-zero exit is returned as (stdout, code, nil) for the
// caller to interpret against the subcommand's documented codes.
func (c *gitClient) runExit(ctx context.Context, topFlags []string, args ...string) (string, int, error) {
	argv := make([]string, 0, len(topFlags)+len(args)+3)
	argv = append(argv, "-C", c.checkout, "-c", "core.hooksPath="+c.hooksDir)
	argv = append(argv, topFlags...)
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, c.bin, argv...) // #nosec G204 -- argv is fully constructed above: fixed -C/-c prefix plus adapter-built arguments; there is no shell.
	cmd.Dir = c.checkout
	cmd.Env = c.env()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		return out.String(), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), ee.ExitCode(), nil
	}
	return "", -1, fmt.Errorf("%s: running %s %s: %w (redacted stderr: %s)",
		ErrGitCommand, c.bin, strings.Join(args, " "), err, trimOutput(errb.String()))
}

// run is runExit with the success case only: any non-zero exit is an error.
func (c *gitClient) run(ctx context.Context, topFlags []string, args ...string) (string, error) {
	out, code, err := c.runExit(ctx, topFlags, args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		sub := "git"
		if len(args) > 0 {
			sub = args[0]
		}
		return "", &runErr{sub: sub, code: code, out: out, errb: ""}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Git operations (each maps the subcommand's documented exit codes)
// ---------------------------------------------------------------------------

// isInsideWorkTree reports whether dir belongs to a non-bare git working
// tree.
func (c *gitClient) isInsideWorkTree(ctx context.Context, dir string) (bool, error) {
	argv := []string{"-C", dir, "-c", "core.hooksPath=" + c.hooksDir, "rev-parse", "--is-inside-work-tree"}
	cmd := exec.CommandContext(ctx, c.bin, argv...) // #nosec G204 -- fixed argv; dir is validated before use.
	cmd.Env = c.env()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return false, fmt.Errorf("%w: %s is not a git working tree (%s)", ErrNotAGitRepo, dir, trimOutput(errb.String()))
		}
		return false, fmt.Errorf("%s: %w: %v", ErrGitCommand, err, trimOutput(errb.String()))
	}
	return strings.TrimSpace(out.String()) == "true", nil
}

// toplevel returns the checkout root of the working tree that contains dir.
func (c *gitClient) toplevel(ctx context.Context, dir string) (string, error) {
	argv := []string{"-C", dir, "-c", "core.hooksPath=" + c.hooksDir, "rev-parse", "--show-toplevel"}
	cmd := exec.CommandContext(ctx, c.bin, argv...) // #nosec G204 -- fixed argv; dir is validated before use.
	cmd.Env = c.env()
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("%w: %s is not a git working tree (%s)", ErrNotAGitRepo, dir, trimOutput(errb.String()))
		}
		return "", fmt.Errorf("%s: %w: %v", ErrGitCommand, err, trimOutput(errb.String()))
	}
	top := strings.TrimSpace(out.String())
	if top == "" {
		return "", fmt.Errorf("%w: %s is not a git working tree", ErrNotAGitRepo, dir)
	}
	return top, nil
}

// headExists reports whether the repository has at least one commit.
func (c *gitClient) headExists(ctx context.Context) (bool, error) {
	_, code, err := c.runExit(ctx, nil, "rev-parse", "-q", "--verify", "HEAD")
	if err != nil {
		return false, err
	}
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil // documented: -q --verify exits 1 when the ref is missing
	default:
		return false, fmt.Errorf("%w: git rev-parse exited %d", ErrGitCommand, code)
	}
}

// currentBranch returns the symbolic HEAD branch ("refs/heads/<name>" form
// stripped). Exit 1 means unborn or detached; the caller distinguishes them
// with headExists.
func (c *gitClient) currentBranch(ctx context.Context) (string, bool, error) {
	out, code, err := c.runExit(ctx, nil, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return "", false, err
	}
	switch code {
	case 0:
		branch := strings.TrimSpace(out)
		branch = strings.TrimPrefix(branch, "refs/heads/")
		return branch, true, nil
	case 1:
		return "", false, nil // unborn or detached (documented -q behavior)
	default:
		return "", false, fmt.Errorf("%w: git symbolic-ref exited %d", ErrGitCommand, code)
	}
}

// branchExists reports whether refs/heads/name exists.
func (c *gitClient) branchExists(ctx context.Context, name string) (bool, error) {
	_, code, err := c.runExit(ctx, nil, "rev-parse", "-q", "--verify", "refs/heads/"+name)
	if err != nil {
		return false, err
	}
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf("%w: git rev-parse exited %d", ErrGitCommand, code)
	}
}

// branchValid asks git's own ref rules whether name is a branch name.
func (c *gitClient) branchValid(ctx context.Context, name string) (bool, error) {
	_, code, err := c.runExit(ctx, nil, "check-ref-format", "--branch", name)
	if err != nil {
		return false, err
	}
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil // documented: non-zero when the name is invalid
	default:
		return false, fmt.Errorf("%w: git check-ref-format exited %d", ErrGitCommand, code)
	}
}

// statusEntry is one working-tree entry of "git status --porcelain=v1 -z".
//
// The -z stream is a sequence of NUL-terminated records "XY<space>path";
// a rename/copy record carries one extra NUL-terminated field, the
// counterpart path (git lists the renamed-to name in the record, the
// renamed-from name in the extra field). Paths in -z mode are raw bytes
// (no locale quoting); a defensive C-unquote handles quoted records from
// older builds. Untracked directories are reported collapsed with a
// trailing slash, which paths() trims.
type statusEntry struct {
	// index and worktree are the two XY state characters.
	index    byte
	worktree byte
	// path is the record's path relative to the checkout root (for a
	// rename, the renamed-to name).
	path string
	// alt is the rename/copy counterpart (the renamed-from name).
	alt string
}

// dirty reports whether the entry records any difference from HEAD.
func (e statusEntry) dirty() bool {
	return e.index != ' ' || e.worktree != ' '
}

// paths returns every path the entry touches, normalized (trailing slash
// trimmed), so both endpoints of a rename are subject to the dirty policy.
func (e statusEntry) paths() []string {
	p := strings.TrimSuffix(e.path, "/")
	ps := []string{p}
	if a := strings.TrimSuffix(e.alt, "/"); a != "" && a != p {
		ps = append(ps, a)
	}
	return ps
}

// status returns the working-tree state. The -z stream is NUL-terminated
// records "XY<NUL>path<NUL>[dest<NUL>]"; paths in -z mode are raw bytes (no
// locale quoting), and a defensive C-unquote is applied to quoted records.
func (c *gitClient) status(ctx context.Context) ([]statusEntry, error) {
	out, code, err := c.runExit(ctx, nil, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, &runErr{sub: "status", code: code, out: out}
	}
	return parseStatus(out)
}

// parseStatus splits a -z porcelain stream into entries. Each record is
// "XY<space>path" (NUL-terminated); rename/copy records carry one extra
// NUL-terminated counterpart field.
func parseStatus(out string) ([]statusEntry, error) {
	if out == "" {
		return nil, nil
	}
	var entries []statusEntry
	recs := strings.Split(out, "\x00")
	// git NUL-terminates every record, so a well-formed stream ends with a
	// single empty element; it is the only empty element allowed.
	if len(recs) > 0 && recs[len(recs)-1] == "" {
		recs = recs[:len(recs)-1]
	}
	for len(recs) > 0 {
		head := recs[0]
		if len(head) == 0 || len(head) < 3 || head[2] != ' ' {
			return nil, fmt.Errorf("%w: malformed status record %q", ErrGitCommand, head)
		}
		e := statusEntry{index: head[0], worktree: head[1], path: unquoteC(head[3:])}
		recs = recs[1:]
		if e.index == 'R' || e.worktree == 'R' || e.index == 'C' || e.worktree == 'C' {
			if len(recs) == 0 {
				return nil, fmt.Errorf("%w: rename status record missing counterpart path", ErrGitCommand)
			}
			e.alt = unquoteC(recs[0])
			recs = recs[1:]
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// unquoteC removes git's C-style quoting when present (paths in -z mode are
// normally raw bytes; older builds may quote; both forms are handled).
func unquoteC(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	body := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if ch != '\\' {
			b.WriteByte(ch)
			continue
		}
		i++
		if i >= len(body) {
			b.WriteByte(ch)
			break
		}
		switch body[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'r':
			b.WriteByte('\r')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		default:
			if body[i] >= '0' && body[i] <= '7' {
				// Octal escape: up to three digits.
				val := 0
				digits := 0
				for digits < 3 && i < len(body) && body[i] >= '0' && body[i] <= '7' {
					val = val*8 + int(body[i]-'0')
					i++
					digits++
				}
				i-- // the loop above already consumed; re-center
				b.WriteByte(byte(val))
			} else {
				b.WriteByte('\\')
				b.WriteByte(body[i])
			}
		}
	}
	return b.String()
}

// stagedPaths lists the paths whose index entry differs from HEAD
// ("git diff --cached --name-only -z"). Raw, NUL-separated.
func (c *gitClient) stagedPaths(ctx context.Context) ([]string, error) {
	out, code, err := c.runExit(ctx, nil, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, &runErr{sub: "diff", code: code, out: out}
	}
	if out == "" {
		return nil, nil
	}
	paths := make([]string, 0, strings.Count(out, "\x00"))
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, unquoteC(p))
		}
	}
	return paths, nil
}

// commitPrep creates the branch (or switches to it) and stages exactly the
// given checkout-relative paths.
func (c *gitClient) createBranch(ctx context.Context, name string) error {
	_, code, err := c.runExit(ctx, nil, "checkout", "-b", name)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%w: git checkout -b %s exited %d", ErrGitCommand, name, code)
	}
	return nil
}

// switchTo moves the working tree to the existing branch name.
func (c *gitClient) switchTo(ctx context.Context, name string) error {
	_, code, err := c.runExit(ctx, nil, "checkout", name)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%w: git checkout %s exited %d", ErrGitCommand, name, code)
	}
	return nil
}

// stage adds exactly the given checkout-relative paths to the index. The
// trailing "--" keeps the paths out of rev/option position, and the
// ":(literal)" pathspec magic (git >= 2.34) disables every magic prefix
// (":/", ":(...)") so a generated file named ":top.md" or "(x).md" is
// taken exactly literally. The paths git records (and reports from
// diff --cached) are the plain checkout-relative paths.
func (c *gitClient) stage(ctx context.Context, relPaths ...string) error {
	specs := make([]string, len(relPaths))
	for i, p := range relPaths {
		specs[i] = ":(literal)" + p
	}
	argv := append([]string{"add", "--"}, specs...)
	_, code, err := c.runExit(ctx, nil, argv...)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%w: git add exited %d", ErrGitCommand, code)
	}
	return nil
}

// identity returns the configured committer name/email from the
// repository-local config (global and system config are disabled by the
// client's environment). Missing values are empty strings.
func (c *gitClient) identity(ctx context.Context) (name, email string, err error) {
	n, e1 := c.configValue(ctx, "user.name")
	m, e2 := c.configValue(ctx, "user.email")
	return n, m, errors.Join(e1, e2)
}

// configValue reads one config variable (empty string when unset: git
// config exits 1 for an unresolvable key).
func (c *gitClient) configValue(ctx context.Context, key string) (string, error) {
	out, code, err := c.runExit(ctx, nil, "config", key)
	if err != nil {
		return "", err
	}
	switch code {
	case 0:
		return strings.TrimSpace(out), nil
	case 1:
		return "", nil // documented: key unset
	default:
		return "", fmt.Errorf("%w: git config %s exited %d", ErrGitCommand, key, code)
	}
}

// commit creates the commit of the current index with the given message.
// extraTopFlags carries the optional per-invocation identity (-c user.name
// / -c user.email) and must be even-length flag/value pairs placed before
// the subcommand.
func (c *gitClient) commit(ctx context.Context, message string, extraTopFlags []string) error {
	_, code, err := c.runExit(ctx, extraTopFlags, "commit", "--no-verify", "-m", message)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%w: git commit exited %d", ErrGitCommand, code)
	}
	return nil
}

// headSHA returns the full SHA of HEAD.
func (c *gitClient) headSHA(ctx context.Context) (string, error) {
	out, err := c.run(ctx, nil, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// commitTime returns the commit's author timestamp (unix seconds) via
// "git show -s --format=%ct <sha>".
func (c *gitClient) commitTime(ctx context.Context, sha string) (string, error) {
	out, err := c.run(ctx, nil, "show", "-s", "--format=%ct", sha)
	return strings.TrimSpace(out), err
}

// commitChangedPaths lists every path changed by the commit (versus its
// single parent, or the empty tree for a root commit). Renames are reported
// as their delete+add pair (--no-renames), which is what makes foreign
// content detectable.
func (c *gitClient) commitChangedPaths(ctx context.Context, sha string) ([]string, error) {
	out, code, err := c.runExit(ctx, nil, "diff-tree", "--root", "--name-only", "-r", "--no-renames", sha)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%w: git diff-tree exited %d", ErrGitCommand, code)
	}
	if out == "" {
		return nil, nil
	}
	lines := strings.Split(out, "\n")
	// git diff-tree <commit> prints the commit ID as its first line; the
	// remaining lines are the changed paths.
	if len(lines) > 0 && isCommitSHA(lines[0]) {
		lines = lines[1:]
	}
	var paths []string
	for _, p := range lines {
		if p = unquoteC(strings.TrimSpace(p)); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// isCommitSHA reports whether s is a 40-hex commit ID.
func isCommitSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// commitParents returns the raw parent-SHA list ("%P") of a commit.
func (c *gitClient) commitParents(ctx context.Context, sha string) (string, error) {
	out, err := c.run(ctx, nil, "log", "-1", "--format=%P", sha)
	return strings.TrimSpace(out), err
}

// blobContent returns the committed bytes of relPath at sha
// ("git cat-file blob <sha>:<path>").
func (c *gitClient) blobContent(ctx context.Context, sha, relPath string) ([]byte, error) {
	out, code, err := c.runExit(ctx, nil, "cat-file", "blob", sha+":"+relPath)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%w: git cat-file %s exited %d", ErrGitCommand, relPath, code)
	}
	return []byte(out), nil
}

// showAt returns the file bytes of relPath in the given ref
// ("git show <ref>:<path>"); the error is ErrGitCommand-like when the path
// does not exist in that ref.
func (c *gitClient) showAt(ctx context.Context, ref, relPath string) ([]byte, error) {
	out, code, err := c.runExit(ctx, nil, "show", ref+":"+relPath)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("%w: git show %s:%s exited %d", ErrGitCommand, ref, relPath, code)
	}
	return []byte(out), nil
}

// parseUnixTime parses a "%ct" unix-seconds value.
func parseUnixTime(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}
