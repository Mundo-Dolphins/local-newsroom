// Package gitpub is the v0.6 Git publication adapter: it commits a staged
// site publication (e.g. a Hugo content file written by internal/hugo)
// into an existing local Git checkout, on a dedicated branch, as a
// reviewable commit — and returns a publication receipt naming the commit
// SHA and the branch.
//
// It closes the local-Git half of the v0.6 publication story:
// internal/hugo turns a READY rendered article into a site-ready content
// file; this package turns that file into a local commit a human can
// review. Together: stage, then commit — and the engine stops there. The
// human pushes (or never does); a pull request is their choice. The
// engine never pushes, merges, or merges-away.
//
// What it is / what it is not:
//
//   - A *committing* adapter over an existing checkout: it writes and
//     stages an explicit list of generated files (path + content), commits
//     them on a dedicated branch, and returns a receipt.
//   - Not a deployer: no push, no fetch, no merge, no rebase, no reset, no
//     clean, no force operation of any kind, and no branch is ever deleted
//     or rewound. The installed git executable is invoked through a
//     tightly controlled boundary (internal to this package) — fixed argv,
//     a minimal environment, no hooks, and a post-commit content
//     verification — rather than re-implementing Git.
//   - Plan-aware: it produces *publication* receipts
//     (internal/publication) whose external record ID is the commit SHA,
//     whose metadata carries the branch, and whose content digest is
//     digest-bound to the committed bytes.
//   - Format-agnostic: it does not import internal/hugo. The caller passes
//     plain files (a Hugo publisher passes the staged content file); this
//     package only knows paths and bytes.
//
// Safety model (fail closed):
//
//   - The destination must be an existing git working tree; the configured
//     target directory and every file must resolve *inside* that checkout
//     (symbolic links resolved; escapes are refused).
//   - The working tree must be clean. The single exception is an explicit
//     safe policy (Config.AllowedDirtyPaths) naming known generated paths
//     the operator expects to be dirty; every other dirty path is a hard
//     error. Index entries staged by someone else are refused at commit
//     time (and reported in a dry run) so an unrelated file can never ride
//     along in the commit; the adapter never unstages them on its own.
//   - Only the requested files are written (atomic temp+fsync+chmod+rename,
//     0644/0755 site conventions) and staged (git add with an explicit
//     pathspec). Nothing else is ever added, removed, or rewritten.
//   - After the commit, the adapter verifies it: the commit's changed-path
//     set must equal exactly the staged publication files, every committed
//     blob must be byte-identical to the approved content (this defeats any
//     clean/smudge filter the site repo might define), and the commit must
//     sit directly on the pre-publication HEAD.
//   - No git config is written anywhere: the committer identity is either
//     read from the repository-local config or supplied per invocation via
//     -c flags (never persisted); the global and system config are disabled
//     for every git process, and no hook of any kind can execute.
//
// Idempotency:
//
//   - After writing, if the index is byte-identical to HEAD (the exact
//     content is already committed on the publication branch), no new
//     commit is created: the receipt points at the existing commit with
//     its original timestamp, so retrying the same publication re-derives
//     the same receipt and never duplicates history.
//
// The working tree is left on the publication branch after a real run:
// the commit is a reviewable local change, and the operator inspects
// (git log / git diff) and pushes by their own decision.
package gitpub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	texttmpl "text/template"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// Provider is the v0.6 provider name of this adapter (target.Provider).
const Provider = "git"

// clientVersion is recorded in receipts (audit trail).
const clientVersion = "newsroom-git/0.6"

// defaultBranchPrefix prefixes the derived publication branch name.
const defaultBranchPrefix = "publication/"

// Bounds for one publication request.
const (
	// MaxFiles bounds the file list of one request.
	MaxFiles = 64
	// MaxFileSize bounds one publication file (a staged article is far
	// smaller; this is a sanity guard, not a content limit).
	MaxFileSize = 4 << 20
	// MaxMessageLen bounds the rendered commit message.
	MaxMessageLen = 8192
	// maxDirtyReport bounds the dirty-path list carried in errors/issues.
	maxDirtyReport = 32
)

// filePerm and dirPerm are the site-file conventions the adapter applies
// to files and directories it creates (a content tree is world-readable
// site material once committed; the executable bit is intentionally not
// set, so git records 100644).
const (
	filePerm = 0o644
	dirPerm  = 0o755
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	// ErrInvalidConfig indicates the publication config is invalid.
	ErrInvalidConfig = errors.New("invalid git publication config")

	// ErrNotAGitRepo indicates the destination is not an existing git
	// working tree.
	ErrNotAGitRepo = errors.New("destination is not a git working tree")

	// ErrTargetOutsideRepo indicates a configured path (target directory
	// or file) would land outside the checkout, including via symbolic
	// links.
	ErrTargetOutsideRepo = errors.New("target path escapes the checkout")

	// ErrDirtyTree indicates the working tree holds changes the safe
	// policy does not cover. Nothing is written or committed.
	ErrDirtyTree = errors.New("working tree is dirty")

	// ErrDetachedHead indicates the checkout is in a detached-HEAD state,
	// which the adapter refuses (a dedicated branch requires an attached
	// HEAD).
	ErrDetachedHead = errors.New("checkout is in detached-HEAD state")

	// ErrNoIdentity indicates no committer identity is available (no
	// repository-local user.name/user.email and none supplied in Config).
	ErrNoIdentity = errors.New("no committer identity is configured")

	// ErrInvalidRequest indicates the publication request is invalid
	// (bad IDs, file counts/bounds, or an unrenderable commit message).
	ErrInvalidRequest = errors.New("invalid publication request")

	// ErrBranch indicates the publication branch name is invalid by git's
	// own ref rules.
	ErrBranch = errors.New("invalid publication branch name")

	// ErrGitCommand indicates a git invocation failed (spawn failure or an
	// unexpected exit code). The error text is redacted and bounded.
	ErrGitCommand = errors.New("git command failed")

	// ErrStagedForeign indicates the index already carried staged changes
	// to paths other than the publication files; the commit is refused so
	// unrelated content never ships. The publication files are written
	// and staged (a reviewable, deliberate state) but not committed; the
	// pre-staged foreign entries are left exactly as found.
	ErrStagedForeign = errors.New("staged changes exist outside the publication files")

	// ErrVerification indicates the post-commit content verification
	// failed: the commit does not contain exactly the approved bytes.
	ErrVerification = errors.New("post-commit verification failed")

	// ErrDryRunReceipt indicates a receipt was requested for a dry run,
	// which has no commit to point at.
	ErrDryRunReceipt = errors.New("a dry run produces no receipt (no commit exists)")
)

// DirtyTreeError is the typed ErrDirtyTree result: it names the offending
// paths so an operator can see exactly what is dirty and what the policy
// allows.
type DirtyTreeError struct {
	// Offenders are checkout-relative paths with unexplained changes
	// (modified, staged, untracked, deleted, renamed).
	Offenders []string

	// Allowed is the explicit safe policy that was consulted.
	Allowed []string
}

// Error implements error.
func (e *DirtyTreeError) Error() string {
	msg := fmt.Sprintf("%s: %d path(s) with changes outside the safe policy (first: %s)",
		ErrDirtyTree, len(e.Offenders), strings.Join(headN(e.Offenders, maxDirtyReport), ", "))
	if len(e.Allowed) > 0 {
		msg += fmt.Sprintf("; allowed dirty paths: %s", strings.Join(e.Allowed, ", "))
	} else {
		msg += "; the working tree must be clean (allow known generated paths explicitly via AllowedDirtyPaths)"
	}
	return msg
}

// Is makes DirtyTreeError match the ErrDirtyTree sentinel via errors.Is.
func (e *DirtyTreeError) Is(target error) bool {
	return target == ErrDirtyTree
}

// ForeignStagedError is the typed ErrStagedForeign result.
type ForeignStagedError struct {
	// Paths are index entries (differing from HEAD) outside the
	// publication files.
	Paths []string
}

// Error implements error.
func (e *ForeignStagedError) Error() string {
	return fmt.Sprintf("%s: %s (these were already staged; unstage them and retry — the adapter never unstages on its own)",
		ErrStagedForeign, strings.Join(headN(e.Paths, maxDirtyReport), ", "))
}

// Is makes ForeignStagedError match the ErrStagedForeign sentinel.
func (e *ForeignStagedError) Is(target error) bool {
	return target == ErrStagedForeign
}

// detachedHeadError is the typed ErrDetachedHead result.
type detachedHeadError struct{}

// Error implements error.
func (e *detachedHeadError) Error() string { return ErrDetachedHead.Error() }

// Is makes detachedHeadError match the ErrDetachedHead sentinel.
func (e *detachedHeadError) Is(target error) bool { return target == ErrDetachedHead }

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Config is the git publication configuration for one site checkout.
type Config struct {
	// RepoRoot is the existing checkout (or a directory inside it, e.g.
	// the Hugo project root when the site is a subdirectory of the repo).
	// Required; must exist and be inside a git working tree. A relative
	// path is resolved against the current working directory.
	RepoRoot string

	// GitPath is the git executable (default: "git" from PATH).
	GitPath string

	// TargetDir is the directory, relative to RepoRoot, in which the
	// publication files land (default "."; for a Hugo site, the content
	// directory, e.g. "content"). It must resolve inside the checkout.
	TargetDir string

	// Branch is the dedicated publication branch (e.g.
	// "publication/site"). When empty, one is derived from the request's
	// target ID ("publication/<target-id>").
	Branch string

	// CommitMessage is a Go text/template for the commit message. Context
	// fields: PlanID, RunID, TargetID, Title, Branch, BaseBranch,
	// FileCount, Files (checkout-relative paths, sorted). When empty, a
	// built-in message is used.
	CommitMessage string

	// CommittedName / CommittedEmail supply the committer identity for
	// the commit via per-invocation -c flags (never persisted; no config
	// file is written). When empty, the repository-local git config is
	// consulted, and a missing identity is a hard error.
	CommittedName  string
	CommittedEmail string

	// AllowedDirtyPaths is the explicit safe policy for a non-clean
	// working tree: checkout-relative paths (files or directory prefixes,
	// e.g. "content") that the operator expects to hold changes. With no
	// entries, ANY dirty state fails (the default). A path of "." (allow
	// everything) is rejected as too broad.
	AllowedDirtyPaths []string
}

// nameRe is the conservative pre-screen for committer names and branch
// names: printable unicode letters, digits, spaces, and the punctuation
// git accepts (1-256 chars). Git's own ref rules (check-ref-format) are
// the final authority and are always consulted.
var nameRe = regexp.MustCompile(`^[\p{L}\p{N} /._'-]{1,256}$`)

// emailRe is a deliberately loose address check (git is more permissive);
// it exists to catch option injection, not to be an RFC.
var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+$`)

// validatedConfig is the normalized internal form of Config.
type validatedConfig struct {
	root         string // resolved (symlink-free) configured root
	targetDir    string // slash-separated, relative to root ("." allowed)
	branch       string // "" => derive from the request target
	commitRaw    string // the raw template source (for Config() round-trips)
	commitTmpl   *texttmpl.Template
	commitName   string
	commitEmail  string
	allowedDirty []string // slash-separated, relative to the checkout root
}

// validateConfig normalizes and checks the raw config: filesystem state,
// git working-tree membership (read-only probes), path containment, branch
// name, template, identity, and the dirty-safe policy.
func validateConfig(ctx context.Context, cfg Config, client *gitClient) (validatedConfig, error) {
	var errs []error
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: "+format, append([]any{ErrInvalidConfig}, args...)...))
	}

	root := strings.TrimSpace(cfg.RepoRoot)
	if root == "" {
		errs = append(errs, fmt.Errorf("%w: repo root is required", ErrInvalidConfig))
	} else if fi, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("%w: repo root %q does not exist", ErrInvalidConfig, root))
		} else {
			errs = append(errs, fmt.Errorf("%w: repo root %q: %v", ErrInvalidConfig, root, err))
		}
	} else if !fi.IsDir() {
		errs = append(errs, fmt.Errorf("%w: repo root %q is not a directory", ErrInvalidConfig, root))
	}
	if len(errs) == 0 && !filepath.IsAbs(root) {
		abs, err := filepath.Abs(root)
		if err != nil {
			errs = append(errs, fmt.Errorf("%w: cannot resolve repo root %q: %v", ErrInvalidConfig, root, err))
		} else {
			root = abs
		}
	}
	// Resolve symbolic links so every later path comparison is honest.
	if len(errs) == 0 {
		realRoot, err := resolveSymlinks(root)
		if err != nil {
			errs = append(errs, fmt.Errorf("%w: cannot resolve repo root %q: %v", ErrInvalidConfig, root, err))
		} else {
			root = filepath.Clean(realRoot)
		}
	}
	if len(errs) > 0 {
		return validatedConfig{}, errors.Join(errs...)
	}

	// The root must sit inside a git working tree; the checkout root
	// (toplevel) is canonical — everything else is measured against it.
	if inTree, err := client.isInsideWorkTree(ctx, root); err != nil {
		if !errors.Is(err, ErrNotAGitRepo) {
			errs = append(errs, fmt.Errorf("%w: probing repo root: %v", ErrInvalidConfig, err))
		} else {
			errs = append(errs, err)
		}
	} else if !inTree {
		errs = append(errs, fmt.Errorf("%w: %s", ErrNotAGitRepo, root))
	} else if top, err := client.toplevel(ctx, root); err != nil {
		errs = append(errs, fmt.Errorf("%w: probing checkout root: %v", ErrInvalidConfig, err))
	} else {
		topReal, err := resolveSymlinks(top)
		if err != nil {
			errs = append(errs, fmt.Errorf("%w: cannot resolve checkout root %q: %v", ErrInvalidConfig, top, err))
		} else if !inside(realPath(root), topReal) {
			errs = append(errs, fmt.Errorf("%w: repo root %s does not belong to checkout %s", ErrTargetOutsideRepo, root, topReal))
		} else {
			client.checkout = filepath.Clean(topReal)
		}
	}

	targetDir := strings.TrimSpace(cfg.TargetDir)
	if targetDir == "" {
		targetDir = "."
	}
	if rel, err := validateRelPath(targetDir); err != nil {
		errs = append(errs, fmt.Errorf("%w: target dir %q: %v", ErrInvalidConfig, cfg.TargetDir, err))
	} else if rel != "." && client.checkout != "" {
		// The target dir must resolve inside the checkout (symlinks
		// resolved): a symlinked subdirectory pointing outside the repo
		// must not steer publication content out of the checkout.
		abs := filepath.Join(root, filepath.FromSlash(rel))
		resolved, err := resolveSymlinks(abs)
		if err != nil {
			errs = append(errs, fmt.Errorf("%w: target dir %s: %v", ErrTargetOutsideRepo, abs, err))
		} else if !inside(resolved, client.checkout) {
			errs = append(errs, fmt.Errorf("%w: target dir %s resolves outside the checkout %s", ErrTargetOutsideRepo, resolved, client.checkout))
		} else {
			targetDir = rel
		}
	}

	branch := strings.TrimSpace(cfg.Branch)
	if branch != "" && !nameRe.MatchString(branch) {
		addf("branch %q contains invalid characters (letters, digits, space, '.', '_', '-', '/', 1-256)", branch)
	}
	// Static pre-screen; git's own ref rules are the final authority
	// (checked against the real executable at construction and request
	// time).
	if branch != "" && (strings.HasPrefix(branch, "/") || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") ||
		strings.ContainsRune(branch, '\x00') || strings.ContainsRune(branch, ' ') || strings.ContainsRune(branch, '^') ||
		strings.ContainsRune(branch, ':') || strings.ContainsRune(branch, '?') || strings.ContainsRune(branch, '*') ||
		strings.ContainsRune(branch, '[')) {
		errs = append(errs, fmt.Errorf("%w: %q is not a valid git branch name", ErrBranch, branch))
	}

	commitRaw := cfg.CommitMessage
	if commitRaw == "" {
		commitRaw = defaultCommitMessage
	}
	commitTmpl, err := texttmpl.New("commit message").Parse(commitRaw)
	if err != nil {
		errs = append(errs, fmt.Errorf("%w: commit message template: %v", ErrInvalidConfig, err))
	}

	commitName := strings.TrimSpace(cfg.CommittedName)
	commitEmail := strings.TrimSpace(cfg.CommittedEmail)
	if (commitName == "") != (commitEmail == "") {
		errs = append(errs, fmt.Errorf("%w: CommittedName and CommittedEmail must be set together", ErrNoIdentity))
	}
	if commitName != "" && (!nameRe.MatchString(commitName) || commitName != strings.TrimSpace(commitName)) {
		addf("committer name %q is invalid (printable, 1-256 chars, no leading/trailing space)", commitName)
	}
	if commitEmail != "" && !emailRe.MatchString(commitEmail) {
		addf("committer email %q is invalid (a single address, 1-256 chars)", commitEmail)
	}

	allowed := make([]string, 0, len(cfg.AllowedDirtyPaths))
	for i, p := range cfg.AllowedDirtyPaths {
		rel, err := validateRelPath(p)
		if err != nil {
			addf("allowed dirty path %d (%q): %v", i, p, err)
			continue
		}
		if rel == "." {
			addf("allowed dirty path %q would permit ANY dirty state; name the specific generated paths instead", p)
			continue
		}
		allowed = append(allowed, rel)
	}

	if len(errs) > 0 {
		return validatedConfig{}, errors.Join(errs...)
	}
	return validatedConfig{
		root:         root,
		targetDir:    targetDir,
		branch:       branch,
		commitRaw:    commitRaw,
		commitTmpl:   commitTmpl,
		commitName:   commitName,
		commitEmail:  commitEmail,
		allowedDirty: allowed,
	}, nil
}

// realPath resolves p's symbolic links (best effort; p already validated
// to exist as a directory by the caller).
func realPath(p string) string {
	if r, err := resolveSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}

// ---------------------------------------------------------------------------
// Request / File
// ---------------------------------------------------------------------------

// File is one publication file: where it lands (relative to
// Config.TargetDir) and the exact bytes to commit.
type File struct {
	// Path is the file path relative to Config.TargetDir
	// (e.g. "news/20260711-slug.md"). It must stay inside the checkout.
	Path string

	// Content is the full file content. Required, non-empty,
	// ≤ MaxFileSize.
	Content []byte
}

// Request is one publication invocation against a Publisher.
type Request struct {
	// PlanID is the owning publication plan (receipt provenance).
	// Required.
	PlanID publication.PublicationID

	// TargetID is the publication target within the plan (default branch
	// derivation and receipt provenance). Required.
	TargetID publication.TargetID

	// Title is a human title for the commit message (falls back to the
	// first file's base name when empty).
	Title string

	// RunID is the originating run (commit message provenance, optional).
	RunID string

	// Files is the exact set of files to write and stage (1..MaxFiles).
	Files []File

	// DryRun performs the full rehearsal — validation, branch planning,
	// dirty-tree and identity checks, file planning, commit message — but
	// writes nothing, stages nothing, switches nothing, and commits
	// nothing. Blockers (dirty tree, detached HEAD, missing identity,
	// foreign staged index entries) are reported in Result.Issues instead
	// of failing, and Result.WouldSkip predicts the idempotency outcome.
	DryRun bool
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------

// FileResult is the per-file detail of one publication run.
type FileResult struct {
	// Relative is the file path relative to the checkout root
	// (forward slashes, e.g. "content/news/20260711-slug.md").
	Relative string

	// Absolute is the full filesystem path.
	Absolute string

	// Digest is the canonical digest of the file content.
	Digest publication.Digest

	// OnDisk reports whether a file existed at Absolute before the run
	// (always recorded; after a real run the file holds the committed
	// bytes).
	OnDisk bool

	// Unchanged reports that the file on disk already held byte-identical
	// content before the run (the write rewrote identical bytes).
	Unchanged bool

	// AtHead reports that the reference the run commits from (the
	// publication branch when it exists, else HEAD) already carried
	// byte-identical content at Relative — the input to the idempotency
	// decision (dry run: prediction; real run: the checked reference).
	AtHead bool
}

// Result is the outcome of one Publish call (real or dry run).
type Result struct {
	// DryRun is true for a rehearsal (no filesystem or git mutation).
	DryRun bool

	// CheckoutRoot is the resolved checkout root (git toplevel).
	CheckoutRoot string

	// BaseBranch is the branch the checkout was on before the run
	// ("" for an unborn HEAD).
	BaseBranch string

	// Branch is the publication branch (the one the run commits to, or
	// would commit to in a dry run).
	Branch string

	// BranchExisted reports whether the branch already existed before the
	// run.
	BranchExisted bool

	// BranchCreated reports that a real run created the branch
	// (false in a dry run, where BranchExisted is the plan).
	BranchCreated bool

	// CommitSHA is the full SHA of the commit: the new commit of a real
	// run, or the pre-existing commit holding the exact content of an
	// idempotent skip. Empty in a dry run.
	CommitSHA string

	// CommitMessage is the rendered commit message (what was committed,
	// or what would be).
	CommitMessage string

	// CommitTime is the commit's own timestamp (zero in a dry run).
	CommitTime time.Time

	// Files is the per-file detail (checkout-relative paths, digests).
	Files []FileResult

	// Skipped reports that a real run found the exact content already
	// committed on the publication branch: no new commit was created
	// (idempotent retry).
	Skipped bool

	// WouldSkip is the dry-run prediction of Skipped (the content is
	// already committed on the planned branch). Unset in a real run.
	WouldSkip bool

	// RemainingDirty lists working-tree paths that are still dirty after
	// the run (the allowed-policy paths; informational only).
	RemainingDirty []string

	// Issues carries dry-run blockers (a real run fails on the same
	// conditions; a dry run reports them so a rehearsal can surface them
	// without erroring).
	Issues []publication.DryRunIssue

	// Digest is the canonical digest of the published content set (the
	// file contents, NUL-joined in checkout-relative path order). For a
	// single file this is exactly the file's own digest, so a plan
	// rebaselined to the staged file binds; SucceedTarget / RecordDryRun
	// verify the match against the approved artifact digest.
	Digest publication.Digest
}

// Receipt builds the publication receipt for a real run: the commit SHA as
// external record ID, the branch in metadata, digest-bound to the committed
// content. It refuses dry runs (ErrDryRunReceipt): a rehearsal published
// nothing.
//
// The published timestamp is the commit's own timestamp, so an idempotent
// retry re-derives the same receipt ID (NewReceiptID derives from plan,
// target, record ID, and time).
func (r *Result) Receipt(planID publication.PublicationID, targetID publication.TargetID) (*publication.PublicationReceipt, error) {
	if r == nil {
		return nil, errors.New("result is nil")
	}
	if r.DryRun {
		return nil, ErrDryRunReceipt
	}
	if r.CommitSHA == "" {
		return nil, errors.New("result carries no commit SHA")
	}
	publishedAt := time.Now()
	if !r.CommitTime.IsZero() {
		publishedAt = r.CommitTime
	}
	meta := map[string]string{"branch": r.Branch}
	if r.BaseBranch != "" && r.BaseBranch != r.Branch {
		meta["base_branch"] = r.BaseBranch
	}
	if r.Skipped {
		meta["idempotent"] = "true"
	}
	rec := publication.NewReceipt(planID, targetID, Provider, "", r.CommitSHA,
		r.Digest, "", clientVersion, meta, publishedAt)
	if err := rec.Validate(); err != nil {
		return nil, fmt.Errorf("built receipt is invalid: %w", err)
	}
	return rec, nil
}

// ToDryRunResult converts a dry-run Result into the plan-contract
// rehearsal record (RecordDryRun input). The orchestrator must verify the
// digest binding (RecordDryRun does this against the plan's artifact).
func (r *Result) ToDryRunResult(at time.Time) *publication.DryRunResult {
	if r == nil {
		return nil
	}
	outcome := publication.DryRunOutcomeOK
	if len(r.Issues) > 0 {
		outcome = publication.DryRunOutcomeFailed
	}
	details := fmt.Sprintf("planned branch %q, %d file(s), message %q",
		r.Branch, len(r.Files), firstLine(r.CommitMessage))
	return publication.NewDryRunResult(at, outcome, r.Digest, details, r.Issues)
}

// firstLine returns the first line of s (bounded).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// Commit message
// ---------------------------------------------------------------------------

// CommitContext is the template context of the commit message.
type CommitContext struct {
	PlanID     string
	RunID      string
	TargetID   string
	Title      string
	Branch     string
	BaseBranch string
	FileCount  int
	Files      []string // checkout-relative paths, sorted
}

// defaultCommitMessage is the built-in message template.
const defaultCommitMessage = `Publish {{.TargetID}}: {{.Title}}

Committed locally by local-newsroom (v0.6 site publication); review before pushing.
Branch: {{.Branch}}{{if .BaseBranch}} (from {{.BaseBranch}}){{end}}
{{if .RunID}}Run: {{.RunID}}
{{end}}{{if .PlanID}}Plan: {{.PlanID}}
{{end}}Files ({{.FileCount}}):
{{range .Files}}  - {{.}}
{{end}}
`

// renderCommitMessage executes the template and bounds the result.
func (p *Publisher) renderCommitMessage(ctx CommitContext) (string, error) {
	var sb strings.Builder
	if err := p.cfg.commitTmpl.Execute(&sb, ctx); err != nil {
		return "", fmt.Errorf("%w: rendering commit message: %v", ErrInvalidRequest, err)
	}
	msg := sb.String()
	if strings.TrimSpace(msg) == "" {
		return "", fmt.Errorf("%w: the commit message template renders to an empty message", ErrInvalidRequest)
	}
	if len(msg) > MaxMessageLen {
		return "", fmt.Errorf("%w: the rendered commit message exceeds %d characters (%d)", ErrInvalidRequest, MaxMessageLen, len(msg))
	}
	return msg, nil
}

// ---------------------------------------------------------------------------
// Path safety (checkout containment)
// ---------------------------------------------------------------------------

// validateRelPath validates a checkout/root-relative path: relative, no
// NUL or backslash, no traversal. "." is accepted (it denotes the base
// directory itself: valid for a target directory, rejected for a file by
// the callers). The result is cleaned and uses forward slashes.
func validateRelPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path is empty")
	}
	if strings.ContainsRune(p, '\x00') {
		return "", errors.New("path contains a NUL byte")
	}
	if strings.ContainsRune(p, '\\') {
		return "", fmt.Errorf("path %q must not contain backslashes", p)
	}
	if path.IsAbs(p) || filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q must be relative", p)
	}
	cleaned := path.Clean(p)
	if cleaned == "." {
		return ".", nil // the base directory itself (callers decide)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path %q traverses outside its base directory", p)
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("path %q contains an empty or dot segment", p)
		}
	}
	return cleaned, nil
}

// validateFileRelPath validates a publication file path: it must denote a
// file, never the base directory itself.
func validateFileRelPath(p string) (string, error) {
	rel, err := validateRelPath(p)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", fmt.Errorf("file path %q resolves to the directory itself; name the file", p)
	}
	return rel, nil
}

// resolveSymlinks resolves the symbolic links of the deepest existing
// ancestor of p and re-appends the not-yet-existing suffix, so paths that
// can still be created are resolvable as well.
func resolveSymlinks(p string) (string, error) {
	p = filepath.Clean(p)
	target := p
	for {
		resolved, err := filepath.EvalSymlinks(target)
		if err == nil {
			suffix := strings.TrimPrefix(p, target)
			return filepath.Clean(resolved + suffix), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(target)
		if parent == target {
			// Reached the filesystem root without finding an existing
			// ancestor; nothing to resolve.
			return p, nil
		}
		target = parent
	}
}

// inside reports whether p is base or a sub-path of base.
func inside(p, base string) bool {
	return p == base || strings.HasPrefix(p, base+string(os.PathSeparator))
}

// assertInside proves p (symbolic links resolved) is the checkout root or a
// sub-path of it — the check that stops a symlinked directory inside the
// repo from steering the write outside the checkout.
func assertInside(checkout, p string) error {
	resolved, err := resolveSymlinks(p)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve %s: %v", ErrTargetOutsideRepo, p, err)
	}
	if !inside(resolved, checkout) {
		return fmt.Errorf("%w: %s resolves outside the checkout %s", ErrTargetOutsideRepo, resolved, checkout)
	}
	return nil
}

// ensureDir creates dir (and missing parents) with dirPerm, chmodding only
// the directories it creates (pre-existing directories keep their
// permissions).
func ensureDir(dir string) error {
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%w: %s exists and is not a directory", ErrTargetOutsideRepo, dir)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspecting directory %s: %w", dir, err)
	}
	probe := dir
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspecting directory %s: %w", probe, err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return fmt.Errorf("%w: no existing ancestor for %s", ErrTargetOutsideRepo, dir)
		}
		probe = parent
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}
	d := dir
	for d != probe {
		if err := os.Chmod(d, dirPerm); err != nil {
			return fmt.Errorf("setting permissions on %s: %w", d, err)
		}
		d = filepath.Dir(d)
	}
	return nil
}

// atomicWrite writes data to dir/name atomically (temp + fsync + chmod +
// rename) with perm, so readers only ever observe a complete file — either
// the previous version or the new one.
func atomicWrite(dir, name string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if tmpName != "" {
			_ = os.Remove(tmpName) // #nosec G307 -- temp file created above
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("setting permissions on temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	// #nosec G301 -- dir and name are validated, containment-checked paths.
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("renaming temp file into place: %w", err)
	}
	tmpName = "" // renamed: the defer must not remove it
	return nil
}

// probeFile records the pre-write state of a target file.
func probeFile(abs string, content []byte) (onDisk, unchanged bool) {
	switch b, err := os.ReadFile(abs); { // #nosec G304 -- abs is validated and containment-checked.
	case err == nil:
		return true, bytes.Equal(b, content)
	case errors.Is(err, os.ErrNotExist):
		return false, false
	default:
		if _, serr := os.Stat(abs); serr == nil {
			return true, false
		}
		return false, false
	}
}

// ---------------------------------------------------------------------------
// Publisher
// ---------------------------------------------------------------------------

// Publisher commits publication files into one existing checkout.
//
// A Publisher is immutable and safe for concurrent use at the API level;
// the underlying checkout holds one git operation at a time (git's own
// index lock serializes real commits; a contended operation fails cleanly
// with ErrGitCommand).
type Publisher struct {
	cfg    validatedConfig
	client *gitClient
}

// New builds a Publisher: it locates the git executable, resolves the root,
// proves it is inside a git working tree, validates every path against the
// checkout, checks the branch name against git's own ref rules, parses the
// commit message template, and sets up the git isolation environment (empty
// global config, empty hooks directory). It performs no mutation of the
// repository; the caller should defer p.Close().
func New(ctx context.Context, cfg Config) (*Publisher, error) {
	bin := strings.TrimSpace(cfg.GitPath)
	if bin == "" {
		bin = "git"
	}
	absBin, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("%w: git executable %q: %v", ErrInvalidConfig, bin, err)
	}

	root := strings.TrimSpace(cfg.RepoRoot)
	if root == "" {
		return nil, fmt.Errorf("%w: repo root is required", ErrInvalidConfig)
	}
	if !filepath.IsAbs(root) {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("%w: cannot resolve repo root %q: %v", ErrInvalidConfig, root, err)
		}
		root = abs
	}
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("%w: repo root %q: %v", ErrInvalidConfig, root, err)
	}

	client := &gitClient{bin: absBin, checkout: filepath.Clean(root)}
	if err := client.mkIsolation(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	defer func() {
		if err != nil {
			_ = client.Close()
		}
	}()

	// Probe: the binary answers to --version (fails fast on a non-git
	// executable) and is recent enough for the pathspec literal magic the
	// stage() call relies on (git >= 2.34; Apple git 2.54 satisfies this).
	versionOut, code, rerr := client.runExit(ctx, nil, "--version")
	if rerr != nil || code != 0 {
		return nil, fmt.Errorf("%w: git --version: %v", ErrInvalidConfig, rerr)
	}
	if !gitVersionOK(versionOut) {
		return nil, fmt.Errorf("%w: git version is too old (got %q; git >= 2.34 is required for literal pathspecs)", ErrInvalidConfig, strings.TrimSpace(versionOut))
	}

	p := &Publisher{client: client}
	vcfg, err := validateConfig(ctx, cfg, client)
	if err != nil {
		return nil, err
	}
	p.cfg = vcfg

	// The branch name is checked against git's own ref rules.
	if vcfg.branch != "" {
		ok, berr := client.branchValid(ctx, vcfg.branch)
		if berr != nil {
			return nil, fmt.Errorf("%w: %v", ErrBranch, berr)
		}
		if !ok {
			return nil, fmt.Errorf("%w: %q is not a valid git branch name", ErrBranch, vcfg.branch)
		}
	}
	return p, nil
}

// Close releases the temporary isolation files (empty global config, empty
// hooks directory). Idempotent.
func (p *Publisher) Close() error {
	return p.client.Close()
}

// Config returns the validated config (normalized forms).
func (p *Publisher) Config() Config {
	return Config{
		RepoRoot:          p.cfg.root,
		GitPath:           p.client.bin,
		TargetDir:         p.cfg.targetDir,
		Branch:            p.cfg.branch,
		CommitMessage:     p.cfg.commitRaw,
		CommittedName:     p.cfg.commitName,
		CommittedEmail:    p.cfg.commitEmail,
		AllowedDirtyPaths: append([]string(nil), p.cfg.allowedDirty...),
	}
}

// RepoRoot returns the resolved configured root.
func (p *Publisher) RepoRoot() string { return p.cfg.root }

// CheckoutRoot returns the resolved checkout root (git toplevel).
func (p *Publisher) CheckoutRoot() string { return p.client.checkout }

// ---------------------------------------------------------------------------
// Publish
// ---------------------------------------------------------------------------

// Publish writes the request's files, stages exactly those, and commits
// them on the dedicated branch — or, with Request.DryRun, rehearses the
// whole thing without any mutation.
//
// After a real run the checkout is left on the publication branch: the
// commit is a reviewable local change. Nothing is ever pushed, merged, or
// reset, and no unrelated file is ever staged, written, or removed.
func (p *Publisher) Publish(ctx context.Context, req Request) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	prep, err := p.prepareRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	// ---- Read-only repository state (fail early, before any mutation) --
	exists, err := p.client.headExists(ctx)
	if err != nil {
		return nil, err
	}
	unborn := !exists
	branchName, attached, err := p.client.currentBranch(ctx)
	if err != nil {
		return nil, err
	}
	baseBranch := branchName // "" when unborn
	detached := !attached && !unborn

	dirty, err := p.client.status(ctx)
	if err != nil {
		return nil, err
	}
	dirtyPaths := p.offendingDirtyPaths(dirty)

	name, email, err := p.commitIdentity(ctx)
	if err != nil {
		if req.DryRun {
			prep.issues = append(prep.issues, publication.NewDryRunIssue("no_identity",
				"no committer identity is configured ("+err.Error()+")"))
		} else {
			return nil, err
		}
	}

	res := &Result{
		DryRun:        req.DryRun,
		CheckoutRoot:  p.client.checkout,
		BaseBranch:    baseBranch,
		Branch:        prep.branch,
		CommitMessage: prep.message,
	}

	// ---- Dry run: report the plan and the blockers, mutate nothing -----
	if req.DryRun {
		existed, err := p.client.branchExists(ctx, prep.branch)
		if err != nil {
			return nil, err
		}
		res.BranchExisted = existed
		res.Files = prep.files
		res.Digest = prep.digest

		// The reference a real run would commit from: the publication
		// branch when it exists, otherwise the current HEAD.
		ref := "HEAD"
		if existed {
			ref = "refs/heads/" + prep.branch
		}
		for i := range res.Files {
			f := &res.Files[i]
			f.OnDisk, f.Unchanged = probeFile(f.Absolute, prep.content[f.Relative])
			b, err := p.client.showAt(ctx, ref, f.Relative)
			if err == nil {
				f.AtHead = bytes.Equal(b, prep.content[f.Relative])
			}
		}
		if len(dirtyPaths) > 0 {
			prep.issues = append(prep.issues, publication.NewDryRunIssue("dirty_tree",
				fmt.Sprintf("working tree is dirty at %d path(s) outside the safe policy (first: %s); the real run will fail until they are committed, removed, or allowed",
					len(dirtyPaths), strings.Join(headN(dirtyPaths, 5), ", "))))
		}
		if detached {
			prep.issues = append(prep.issues, publication.NewDryRunIssue("detached_head",
				"the checkout is in detached-HEAD state; attach a branch before publishing"))
		}
		staged, err := p.client.stagedPaths(ctx)
		if err != nil {
			return nil, err
		}
		if foreign := difference(staged, prep.myPaths); len(foreign) > 0 {
			prep.issues = append(prep.issues, publication.NewDryRunIssue("foreign_staged",
				fmt.Sprintf("the index already contains staged changes outside the publication files: %s; unstage them before publishing",
					strings.Join(headN(foreign, 5), ", "))))
		} else if len(dirtyPaths) == 0 && !detached && name != "" {
			allAtHead := true
			for _, f := range res.Files {
				if !f.AtHead {
					allAtHead = false
					break
				}
			}
			res.WouldSkip = allAtHead
		}
		res.Issues = prep.issues
		res.RemainingDirty = allDirtyPaths(dirty)
		return res, nil
	}

	// ---- Real run: the same conditions are hard failures ---------------
	if len(dirtyPaths) > 0 {
		return nil, &DirtyTreeError{Offenders: dirtyPaths, Allowed: p.cfg.allowedDirty}
	}
	if detached {
		return nil, &detachedHeadError{}
	}

	// ---- Branch: create (or switch to) the dedicated branch ------------
	existed, err := p.client.branchExists(ctx, prep.branch)
	if err != nil {
		return nil, err
	}
	res.BranchExisted = existed
	res.BranchCreated = !existed
	if !existed {
		if err := p.client.createBranch(ctx, prep.branch); err != nil {
			return nil, err
		}
	} else if baseBranch != prep.branch {
		if err := p.client.switchTo(ctx, prep.branch); err != nil {
			return nil, err
		}
	}

	// Re-check the policy after the branch switch: the tree must still be
	// within the safe policy on the branch we are about to commit to.
	if dirty, err := p.client.status(ctx); err != nil {
		return nil, err
	} else if paths := p.offendingDirtyPaths(dirty); len(paths) > 0 {
		return nil, &DirtyTreeError{Offenders: paths, Allowed: p.cfg.allowedDirty}
	}

	// ---- Write the publication files ------------------------------------
	for i := range prep.files {
		f := &prep.files[i]
		content := prep.content[f.Relative]
		onDisk, unchanged := probeFile(f.Absolute, content)
		f.OnDisk = onDisk
		f.Unchanged = unchanged
		if err := ensureDir(filepath.Dir(f.Absolute)); err != nil {
			return nil, err
		}
		if err := atomicWrite(filepath.Dir(f.Absolute), filepath.Base(f.Absolute), content, filePerm); err != nil {
			return nil, fmt.Errorf("writing %s: %w", f.Relative, err)
		}
	}

	// ---- Stage exactly the publication files ----------------------------
	if err := p.client.stage(ctx, prep.myPaths...); err != nil {
		return nil, err
	}

	// The commit may contain exactly the publication files — no more.
	// Foreign entries the operator staged earlier are refused, not
	// silently unstaged (unstaging would touch their work).
	staged, err := p.client.stagedPaths(ctx)
	if err != nil {
		return nil, err
	}
	if foreign := difference(staged, prep.myPaths); len(foreign) > 0 {
		return nil, &ForeignStagedError{Paths: foreign}
	}

	// ---- Idempotency: identical content already committed ---------------
	res.Files = prep.files
	res.Digest = prep.digest
	if len(staged) == 0 {
		if unborn {
			// Unreachable in practice (non-empty files always differ from
			// the empty tree); guard anyway: nothing was committed.
			return nil, fmt.Errorf("%w: unborn HEAD cannot hold committed content", ErrVerification)
		}
		sha, err := p.client.headSHA(ctx)
		if err != nil {
			return nil, err
		}
		res.CommitSHA = sha
		res.Skipped = true
		p.fillCommitTime(ctx, res, sha)
		res.RemainingDirty = p.snapshotDirty(ctx)
		return res, nil
	}

	// ---- Commit ----------------------------------------------------------
	var preSHA string
	if !unborn {
		if preSHA, err = p.client.headSHA(ctx); err != nil {
			return nil, err
		}
	}
	if err := p.client.commit(ctx, prep.message, identityFlags(name, email)); err != nil {
		return nil, err
	}
	sha, err := p.client.headSHA(ctx)
	if err != nil {
		return nil, err
	}
	res.CommitSHA = sha

	// ---- Post-commit content verification -------------------------------
	if err := p.verifyCommit(ctx, sha, preSHA, staged, prep.content); err != nil {
		return nil, err
	}
	p.fillCommitTime(ctx, res, sha)
	res.RemainingDirty = p.snapshotDirty(ctx)
	return res, nil
}

// fillCommitTime records the commit's own timestamp on the result.
func (p *Publisher) fillCommitTime(ctx context.Context, res *Result, sha string) {
	if ts, err := p.client.commitTime(ctx, sha); err == nil {
		if sec, perr := parseUnixTime(ts); perr == nil {
			res.CommitTime = time.Unix(sec, 0).UTC()
		}
	}
}

// preparedRequest is the validated, resolved form of one request.
type preparedRequest struct {
	branch  string
	message string
	digest  publication.Digest
	files   []FileResult // Relative, Absolute, Digest filled (sorted)
	myPaths []string     // checkout-relative paths (sorted)
	content map[string][]byte
	issues  []publication.DryRunIssue
}

// prepareRequest validates the request: IDs, files (bounds, containment,
// uniqueness), the branch name against git's own ref rules, and the commit
// message.
func (p *Publisher) prepareRequest(ctx context.Context, req Request) (*preparedRequest, error) {
	var errs []error
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: "+format, append([]any{ErrInvalidRequest}, args...)...))
	}

	if err := req.PlanID.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("%w: plan ID: %v", ErrInvalidRequest, err))
	}
	if err := req.TargetID.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("%w: target ID: %v", ErrInvalidRequest, err))
	}
	if len(req.Files) < 1 {
		errs = append(errs, fmt.Errorf("%w: at least one file is required", ErrInvalidRequest))
	}
	if len(req.Files) > MaxFiles {
		addf("request holds %d files, exceeding the bound of %d", len(req.Files), MaxFiles)
	}

	title := strings.TrimSpace(req.Title)
	if title == "" && len(req.Files) > 0 {
		title = path.Base(req.Files[0].Path)
	}
	if run.LooksLikeSecret(title) {
		errs = append(errs, fmt.Errorf("%w: title contains secret-looking material (redacted: %q)", ErrInvalidRequest, run.RedactSecrets(title)))
	}
	if len(title) > 200 {
		title = title[:200]
	}

	// Branch: configured, or derived from the target ID.
	branch := p.cfg.branch
	if branch == "" {
		branch = defaultBranchPrefix + string(req.TargetID)
	}
	ok, berr := p.client.branchValid(ctx, branch)
	if berr != nil {
		errs = append(errs, fmt.Errorf("%w: %v", ErrBranch, berr))
	} else if !ok {
		errs = append(errs, fmt.Errorf("%w: %q is not a valid git branch name", ErrBranch, branch))
	}

	prep := &preparedRequest{
		branch:  branch,
		content: make(map[string][]byte, len(req.Files)),
	}

	seen := make(map[string]bool, len(req.Files))
	for i, f := range req.Files {
		rel, err := validateFileRelPath(f.Path)
		if err != nil {
			addf("file %d path %q: %v", i, f.Path, err)
			continue
		}
		if len(f.Content) == 0 {
			addf("file %d (%s): content must not be empty", i, rel)
		}
		if len(f.Content) > MaxFileSize {
			addf("file %d (%s) exceeds %d bytes (%d)", i, rel, MaxFileSize, len(f.Content))
		}
		// Containment: the resolved file must stay inside the checkout.
		abs := filepath.Join(p.cfg.root, filepath.FromSlash(p.cfg.targetDir), filepath.FromSlash(rel))
		if err := assertInside(p.client.checkout, abs); err != nil {
			errs = append(errs, err)
			continue
		}
		relTop, err := filepath.Rel(p.client.checkout, abs)
		if err != nil || filepath.IsAbs(relTop) || relTop == ".." || strings.HasPrefix(relTop, ".."+string(os.PathSeparator)) {
			errs = append(errs, fmt.Errorf("%w: file %d (%s) does not belong to the checkout %s", ErrTargetOutsideRepo, i, rel, p.client.checkout))
			continue
		}
		repoRel := filepath.ToSlash(relTop)
		if seen[repoRel] {
			addf("file path %q appears twice in the request", repoRel)
			continue
		}
		seen[repoRel] = true
		prep.files = append(prep.files, FileResult{
			Relative: repoRel,
			Absolute: abs,
			Digest:   publication.DigestOf(f.Content),
		})
		prep.content[repoRel] = f.Content
	}

	sort.Slice(prep.files, func(i, j int) bool { return prep.files[i].Relative < prep.files[j].Relative })
	for _, f := range prep.files {
		prep.myPaths = append(prep.myPaths, f.Relative)
	}

	// The published-content digest: the file contents, NUL-joined in
	// checkout-relative path order. For a single file this is exactly the
	// file's own digest, so a plan rebaselined to the staged file binds.
	var buf bytes.Buffer
	for i, rel := range prep.myPaths {
		if i > 0 {
			buf.WriteByte('\x00')
		}
		buf.Write(prep.content[rel])
	}
	prep.digest = publication.DigestOf(buf.Bytes())

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	// Commit message (after the file list is final: Files/FileCount feed
	// the template).
	msg, err := p.renderCommitMessage(CommitContext{
		PlanID:     string(req.PlanID),
		RunID:      req.RunID,
		TargetID:   string(req.TargetID),
		Title:      title,
		Branch:     prep.branch,
		BaseBranch: "", // filled by Publish when the checkout moves
		FileCount:  len(prep.myPaths),
		Files:      prep.myPaths,
	})
	if err != nil {
		return nil, err
	}
	prep.message = msg
	return prep, nil
}

// commitIdentity resolves the committer identity: the config fields (via
// per-invocation -c flags) win; otherwise the repository-local git config
// is read (global/system config are disabled in the client's environment).
// A missing identity is an error — the adapter never guesses one.
func (p *Publisher) commitIdentity(ctx context.Context) (name, email string, err error) {
	if p.cfg.commitName != "" || p.cfg.commitEmail != "" {
		if p.cfg.commitName == "" || p.cfg.commitEmail == "" {
			return "", "", fmt.Errorf("%w: both CommittedName and CommittedEmail are required when either is set", ErrNoIdentity)
		}
		return p.cfg.commitName, p.cfg.commitEmail, nil
	}
	name, email, err = p.client.identity(ctx)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrNoIdentity, err)
	}
	if name == "" || email == "" {
		return "", "", fmt.Errorf("%w: the checkout has no user.name/user.email (set them in the repository config, or supply CommittedName/CommittedEmail)", ErrNoIdentity)
	}
	return name, email, nil
}

// identityFlags builds the per-invocation -c flag pairs (never persisted).
func identityFlags(name, email string) []string {
	if name == "" && email == "" {
		return nil
	}
	var flags []string
	if name != "" {
		flags = append(flags, "-c", "user.name="+name)
	}
	if email != "" {
		flags = append(flags, "-c", "user.email="+email)
	}
	return flags
}

// verifyCommit proves the new commit contains exactly the approved content:
//
//  1. it sits directly on the pre-publication HEAD (nothing rewrote
//     history underneath it);
//  2. its changed-path set equals the staged publication paths (renames are
//     reported as delete+add, so any foreign path shows up);
//  3. every committed blob is byte-identical to the content that was
//     approved (defeats clean/smudge filters in the site repo).
func (p *Publisher) verifyCommit(ctx context.Context, sha, preSHA string, staged []string, content map[string][]byte) error {
	if preSHA != "" {
		parents, err := p.client.commitParents(ctx, sha)
		if err != nil {
			return err
		}
		first := parents
		if i := strings.IndexByte(parents, ' '); i >= 0 {
			first = parents[:i]
		}
		if first != preSHA {
			return fmt.Errorf("%w: commit %s is not on top of the pre-publication HEAD %s (found parent %s)",
				ErrVerification, shortSHA(sha), shortSHA(preSHA), shortSHA(first))
		}
	}
	names, err := p.client.commitChangedPaths(ctx, sha)
	if err != nil {
		return err
	}
	if diff := symmetricDifference(names, staged); len(diff) > 0 {
		return fmt.Errorf("%w: commit %s changes %d path(s) outside the publication set (first: %s)",
			ErrVerification, shortSHA(sha), len(diff), strings.Join(headN(diff, 5), ", "))
	}
	for _, rel := range staged {
		got, err := p.client.blobContent(ctx, sha, rel)
		if err != nil {
			return fmt.Errorf("%w: reading committed %s: %v", ErrVerification, rel, err)
		}
		if !bytes.Equal(got, content[rel]) {
			return fmt.Errorf("%w: committed bytes for %s differ from the approved content (a content filter or hook altered the file)", ErrVerification, rel)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Dirty-policy helpers
// ---------------------------------------------------------------------------

// offendingDirtyPaths returns the dirty paths (and rename destinations) the
// safe policy does not cover.
func (p *Publisher) offendingDirtyPaths(entries []statusEntry) []string {
	var out []string
	for _, e := range entries {
		if !e.dirty() {
			continue
		}
		for _, path := range e.paths() {
			if !p.isDirtyPathAllowed(path) {
				out = append(out, path)
			}
		}
	}
	return out
}

// allDirtyPaths lists every dirty path/destination (for reporting).
func allDirtyPaths(entries []statusEntry) []string {
	var out []string
	for _, e := range entries {
		if e.dirty() {
			out = append(out, e.paths()...)
		}
	}
	return out
}

// isDirtyPathAllowed reports whether path (checkout-relative) is covered by
// the explicit safe policy: an exact file match or a directory prefix.
func (p *Publisher) isDirtyPathAllowed(path string) bool {
	for _, allowed := range p.cfg.allowedDirty {
		if path == allowed || strings.HasPrefix(path, allowed+"/") {
			return true
		}
	}
	return false
}

// snapshotDirty returns the current dirty paths (post-run report).
func (p *Publisher) snapshotDirty(ctx context.Context) []string {
	entries, err := p.client.status(ctx)
	if err != nil {
		return nil
	}
	return allDirtyPaths(entries)
}

// difference returns the elements of a that are not in b.
func difference(a, b []string) []string {
	set := make(map[string]bool, len(b))
	for _, v := range b {
		set[v] = true
	}
	var out []string
	for _, v := range a {
		if !set[v] {
			out = append(out, v)
		}
	}
	return out
}

// symmetricDifference returns the elements of either list not in the other.
func symmetricDifference(a, b []string) []string {
	return append(difference(a, b), difference(b, a)...)
}

// headN returns the first n of ss.
func headN(ss []string, n int) []string {
	if len(ss) < n {
		return ss
	}
	return ss[:n]
}

// shortSHA shortens a commit SHA for error messages.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12] + "…"
	}
	return sha
}

// gitVersionOK reports whether a "git --version" banner names a git new
// enough for the adapter's requirements (>= 2.34: literal pathspecs).
func gitVersionOK(banner string) bool {
	fields := strings.Fields(banner) // "git version 2.54.0 (Apple Git-157)"
	for i, f := range fields {
		if f != "version" || i+1 >= len(fields) {
			continue
		}
		parts := strings.SplitN(fields[i+1], ".", 3)
		major, err := strconv.Atoi(parts[0])
		if err != nil {
			return false
		}
		minor := 0
		if len(parts) > 1 {
			if minor, err = strconv.Atoi(parts[1]); err != nil {
				return false
			}
		}
		return major > 2 || (major == 2 && minor >= 34)
	}
	return false
}
