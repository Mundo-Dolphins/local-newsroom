// Package workspace implements filesystem persistence for v0.5 run manifests
// and stage artifacts.
//
// A workspace is a directory on local disk. Every pipeline run is persisted
// beneath it as a self-contained run directory:
//
//	<workspace root>/
//	  runs/
//	    <run-id>/
//	      manifest.json
//	      search-plan.json
//	      discovery.json
//	      dossier.json
//	      verification.json
//	      artifact.json
//	      final-check.json
//	      output/
//	        article.md
//	        bluesky-thread.json
//
// Design principles (mirroring the run package):
//
//   - Atomic writes: every file (manifest or artifact) is written to a
//     temporary name in the same directory, synced, and renamed into place.
//     A file on disk is therefore always either the previous complete
//     version or the new complete version — never a torn write. A partially
//     completed run remains readable after the process terminates.
//
//   - Secret-free, fail closed: local-newsroom never intentionally persists
//     credentials or detected secrets. This store is the single
//     sanitization boundary for all persisted data — no caller, stage, or
//     artifact kind is exempt, and the boundary runs before any byte
//     reaches disk (temporary files included), so callers never have to
//     remember to redact their own structures:
//
//   - Every artifact — manifests, search plans, discovery results,
//     fetched/normalized source and document content, dossiers,
//     verification results, editorial artifacts, final-check results,
//     review bundles, rendered output, warnings/errors, attempt history,
//     custom artifacts — is sanitized by the store's run.Sanitizer
//     before writing: configured exact secret values (e.g. OMLX_API_KEY,
//     SEARXNG_API_KEY) are replaced wherever they appear, and the run
//     package's pattern detectors (bearer tokens, basic-auth tokens, PEM
//     private-key blocks, assignments to secret-like keys, URL
//     credentials, high-entropy token runs) apply to text content.
//
//   - Source and document artifacts are no longer persisted verbatim:
//     if a fetched page or a normalized document contains a detected
//     secret, the persisted copy is redacted and a sidecar file
//     ("<name>.redaction.json") records that (redacted: true,
//     redaction_count: N). Source fidelity may be reduced by redaction;
//     that is an accepted security trade-off in place of
//     byte-identical raw persistence, and no unredacted copy is written
//     anywhere in the run tree.
//
//   - After sanitization the store re-checks the bytes. If they still
//     contain a detected secret — a sanitization failure — the content is
//     NOT written (ErrSanitization) and a clear error is returned that
//     names the failure without including any secret material.
//
//   - Binary content (not valid UTF-8, or containing NUL bytes) receives
//     exact-value byte replacement for every configured secret value;
//     the pattern detectors are defined for text, so binary blobs without
//     a configured exact value pass through unchanged. The only content
//     class that may cross the boundary unmodified.
//
//   - The manifest is the shareable record of a run: it is validated in
//     full (free-text fields — topic, config values, notes, model
//     metadata — are pattern-checked individually) and additionally
//     gated byte-for-byte against every configured exact secret value;
//     a manifest that still contains a detected secret is refused
//     (ErrManifestInvalid), never rewritten.
//     Exact-value byte matching — and not the pattern detectors — is the
//     byte-level gate because pattern heuristics can false-positive on
//     benign structured strings (filesystem paths, generated run IDs);
//     the field-level pattern checks plus the exact-value gate together
//     cover the manifest without rejecting ordinary ones.
//
//   - Overwrite-safe: a run directory is claimed at creation time. Creating
//     a run whose directory already holds a valid manifest of the *same* run
//     is refused (ErrRunExists); a directory holding a manifest of a
//     *different* run — or an unparseable manifest — is refused
//     (ErrRunConflict); and a manifest may never regress the on-disk
//     updated_at (ErrManifestRegression).
//
//   - Safe permissions: run trees are 0700 directories and 0600 files, set
//     explicitly so the result does not depend on the process umask.
//
//   - Contained: artifact paths are relative to the run directory and may
//     never traverse outside of it (including via symbolic links).
//
//   - Independent of workflow logic and of the TUI: this package knows only
//     about run manifests, directories, and files.
//
// Verification note: during an active run, in-memory originals remain
// available to the producing stage; stages and tools that read persisted
// content (LoadArtifact, after a restart or not) always observe the
// sanitized representation. Exact-byte reconstruction of redacted content
// after a restart is deliberately impossible — that is the price of the
// no-secret persistence guarantee.
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// ---------------------------------------------------------------------------
// Layout constants
// ---------------------------------------------------------------------------

// RunsDirName is the directory under the workspace root that holds all runs.
const RunsDirName = "runs"

// ManifestFileName is the name of the run manifest file inside a run
// directory.
const ManifestFileName = "manifest.json"

// RedactionMetaSuffix is appended to a redacted artifact's path to form the
// path of its redaction metadata sidecar (e.g. "sources/raw-1.md" redacts
// to "sources/raw-1.md.redaction.json"). The sidecar records that the
// persisted copy is a redacted representation and how many replacements
// were made; it never records redacted values. A sidecar's absence means
// the persisted content was written unmodified.
const RedactionMetaSuffix = ".redaction.json"

// dirPerm and filePerm are applied explicitly (via chmod) after creation so
// that resulting permissions do not depend on the process umask.
const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	// ErrWorkspaceRoot indicates an invalid workspace root or a path that
	// escapes the workspace tree.
	ErrWorkspaceRoot = errors.New("invalid workspace root")

	// ErrRunExists indicates that the run is already persisted in the
	// workspace (its directory holds a valid manifest of the same run).
	// The run must be loaded, not recreated: terminal runs are immutable.
	ErrRunExists = errors.New("run already exists in workspace")

	// ErrRunConflict indicates that the run directory exists but holds a
	// manifest of a different run, or a manifest that cannot be parsed or
	// validated. The directory is left untouched.
	ErrRunConflict = errors.New("run directory exists with a manifest of a different run")

	// ErrRunNotFound indicates that the run directory (or its manifest) does
	// not exist in the workspace.
	ErrRunNotFound = errors.New("run not found in workspace")

	// ErrManifestInvalid indicates that a manifest failed the run package's
	// validation (structure or redaction rules) and will not be persisted.
	ErrManifestInvalid = errors.New("run manifest is invalid")

	// ErrManifestRegression indicates that the persisted manifest is strictly
	// newer (updated_at) than the one being saved; saving would lose state.
	ErrManifestRegression = errors.New("refusing to persist an older manifest over a newer one")

	// ErrArtifactPath indicates an artifact path that is absolute, traverses
	// outside the run directory, or is otherwise malformed.
	ErrArtifactPath = errors.New("invalid artifact path")

	// ErrSanitization indicates that sanitization failed to remove every
	// detected secret from content that was about to be persisted. The
	// content was NOT written (fail closed). The error deliberately names
	// the failure without including any secret material.
	ErrSanitization = errors.New("sanitization failed: content may contain a secret; refusing to persist unredacted content")
)

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// Store persists runs beneath a single workspace root.
//
// A Store is safe for concurrent use. Concurrent writers of *different* runs
// never touch the same files; concurrent writers of the *same* run are
// protected by the manifest regression check (a stale in-memory manifest is
// rejected, not applied) rather than by locks.
//
// The store's sanitizer (Sanitizer) is its secret-protection policy: every
// byte written through the store is sanitized by it before hitting disk.
type Store struct {
	root      string // absolute, cleaned workspace root
	runs      string // absolute path of the runs directory (root/runs)
	sanitizer *run.Sanitizer
}

// NewStore opens (creating it if necessary) a workspace at root.
//
// The root and its runs directory are created with 0700 permissions. The
// resulting Store's root is the absolute, cleaned path, so relative roots
// resolve against the current working directory.
//
// The store is created with a pattern-only sanitizer (run.NewSanitizer with
// no exact values). Callers that know configured secret values at runtime
// (e.g. the resolved OMLX_API_KEY / SEARXNG_API_KEY / EMBEDDING_API_KEY)
// should use NewStoreWithSanitizer so exact values are redacted everywhere.
func NewStore(root string) (*Store, error) {
	return NewStoreWithSanitizer(root, run.NewSanitizer())
}

// NewStoreWithSanitizer is NewStore with an explicit sanitizer.
//
// The sanitizer is the store's redaction policy for all persisted data
// (artifacts of every kind and the manifest gate). A nil sanitizer is
// treated as the pattern-only default. Typical wiring with a resolved
// config:
//
//	workspace.NewStoreWithSanitizer(root,
//		run.NewSanitizer(cfg.SecretValues(overrides)...))
func NewStoreWithSanitizer(root string, sanitizer *run.Sanitizer) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: root path is required", ErrWorkspaceRoot)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWorkspaceRoot, err)
	}
	abs = filepath.Clean(abs)

	info, err := os.Stat(abs)
	switch {
	case err == nil:
		if !info.IsDir() {
			return nil, fmt.Errorf("%w: %s is not a directory", ErrWorkspaceRoot, abs)
		}
	case errors.Is(err, os.ErrNotExist):
		// created below
	default:
		return nil, fmt.Errorf("%w: %v", ErrWorkspaceRoot, err)
	}

	runs := filepath.Join(abs, RunsDirName)
	for _, dir := range []string{abs, runs} {
		if err := os.MkdirAll(dir, dirPerm); err != nil {
			return nil, fmt.Errorf("creating workspace directory %s: %w", dir, err)
		}
		if err := os.Chmod(dir, dirPerm); err != nil {
			return nil, fmt.Errorf("setting permissions on %s: %w", dir, err)
		}
	}
	return &Store{root: abs, runs: runs, sanitizer: sanitizer}, nil
}

// Sanitizer returns the store's redaction boundary (never nil: a nil
// *run.Sanitizer is the pattern-only default).
func (s *Store) Sanitizer() *run.Sanitizer {
	if s.sanitizer == nil {
		return run.NewSanitizer()
	}
	return s.sanitizer
}

// SetSanitizer replaces the store's redaction boundary. Use it to attach
// exact configured secret values after construction (e.g. once the config
// has been resolved). A nil argument restores the pattern-only default.
func (s *Store) SetSanitizer(san *run.Sanitizer) {
	s.sanitizer = san
}

// Root returns the absolute path of the workspace root.
func (s *Store) Root() string { return s.root }

// RunsDir returns the absolute path of the directory holding all runs.
func (s *Store) RunsDir() string { return s.runs }

// RunDir returns the absolute path of the run directory for id, without
// creating it.
func (s *Store) RunDir(id run.RunID) (string, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	return filepath.Join(s.runs, id.String()), nil
}

// ---------------------------------------------------------------------------
// Run creation and manifest I/O
// ---------------------------------------------------------------------------

// CreateRun provisions the on-disk record for a run.
//
// It claims the run directory beneath <root>/runs/<run-id>/ and writes the
// initial manifest atomically, returning the run directory path. The run
// directory is created before any stage executes, so every subsequent state
// has a place to be persisted.
//
// When the directory already exists:
//
//   - a valid manifest of the *same* run yields ErrRunExists — the run was
//     already persisted and must be loaded (and left alone), not recreated;
//   - a manifest of a *different* run, or a manifest that cannot be parsed
//     or validated, yields ErrRunConflict — an unrelated or corrupted
//     directory is never clobbered;
//   - a directory without a manifest (a creation that died between
//     directory creation and the manifest write) is adopted: the manifest is
//     written and the run becomes readable. Run IDs are derived from the
//     workspace name, topic, and start time, so the same ID is the same run.
func (s *Store) CreateRun(m *run.Manifest) (string, error) {
	if err := s.assertManifestStorable(m); err != nil {
		return "", err
	}
	dir, err := s.RunDir(m.ID)
	if err != nil {
		return "", err
	}
	if err := s.assertInsideRoot(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return "", fmt.Errorf("creating run directory for %s: %w", m.ID, err)
	}
	if err := os.Chmod(dir, dirPerm); err != nil {
		return "", fmt.Errorf("setting permissions on %s: %w", dir, err)
	}

	// Record where the workspace lives (traceability; the field is optional).
	if m.Workspace.Path == "" {
		m.Workspace.Path = s.root
	}

	if data, err := os.ReadFile(manifestPath(dir)); err == nil {
		// A manifest already exists: the run is either a duplicate or a
		// conflict, so this always returns an error.
		return "", s.inspectExistingManifest(m, data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("reading existing manifest of %s: %w", m.ID, err)
	}

	if _, err := s.saveManifest(m, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// inspectExistingManifest applies the create-time overwrite guards. A
// manifest file is always present when this is called, and an existing
// manifest always disallows (re)creation, so it never returns nil.
func (s *Store) inspectExistingManifest(m *run.Manifest, data []byte) error {
	existing, err := run.UnmarshalManifest(data)
	if err != nil {
		return fmt.Errorf("%w: existing manifest is unreadable or invalid (%v); refusing to overwrite", ErrRunConflict, err)
	}
	if existing.ID != m.ID {
		return fmt.Errorf("%w: directory of %s holds manifest of %s", ErrRunConflict, m.ID, existing.ID)
	}
	return fmt.Errorf("%w: %s", ErrRunExists, m.ID)
}

// SaveManifest atomically (re)writes manifest.json for the run.
//
// The manifest is validated in full (including the redaction rules, so a
// secret-looking value can never be persisted). When a manifest is already on
// disk, the new one must not be older than it (updated_at regression), so a
// stale in-memory copy can never clobber newer on-disk state. An unparseable
// on-disk manifest (a torn write from a crashed process) is allowed to be
// overwritten: re-saving is the recovery path.
//
// It returns the absolute path of the manifest file.
func (s *Store) SaveManifest(m *run.Manifest) (string, error) {
	if err := s.assertManifestStorable(m); err != nil {
		return "", err
	}
	dir, err := s.RunDir(m.ID)
	if err != nil {
		return "", err
	}
	if err := s.assertRunDir(dir); err != nil {
		return "", err
	}
	if err := s.assertInsideRoot(dir); err != nil {
		return "", err
	}

	if data, err := os.ReadFile(manifestPath(dir)); err == nil {
		existing, uerr := run.UnmarshalManifest(data)
		switch {
		case uerr != nil:
			// Corrupted on-disk manifest: allow the overwrite (recovery).
		case existing.ID != m.ID:
			return "", fmt.Errorf("%w: directory of %s holds manifest of %s", ErrRunConflict, m.ID, existing.ID)
		case m.UpdatedAt.Before(existing.UpdatedAt):
			return "", fmt.Errorf("%w: disk manifest is at %s, new manifest is at %s",
				ErrManifestRegression, existing.UpdatedAt, m.UpdatedAt)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("reading existing manifest of %s: %w", m.ID, err)
	}

	return s.saveManifest(m, dir)
}

// LoadManifest reads, parses, and fully validates the manifest of a
// persisted run, returning it together with the run directory path.
//
// This is the recovery entry point: a run whose process died mid-execution is
// loaded exactly as far as its last persisted state change got.
func (s *Store) LoadManifest(id run.RunID) (*run.Manifest, string, error) {
	dir, err := s.RunDir(id)
	if err != nil {
		return nil, "", err
	}
	if err := s.assertRunDir(dir); err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(manifestPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("%w: %s (no %s)", ErrRunNotFound, id, ManifestFileName)
		}
		return nil, "", fmt.Errorf("reading manifest of %s: %w", id, err)
	}
	m, err := run.UnmarshalManifest(data)
	if err != nil {
		return nil, "", fmt.Errorf("manifest of %s: %w", id, err)
	}
	return m, dir, nil
}

// ListRuns returns the IDs of all persisted runs (run directories that hold
// a manifest file), in sorted order. Directories left without a manifest by a
// creation that died mid-flight are not yet readable runs and are excluded.
func (s *Store) ListRuns() ([]run.RunID, error) {
	entries, err := os.ReadDir(s.runs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing runs: %w", err)
	}
	ids := make([]run.RunID, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := run.RunID(e.Name())
		if err := id.Validate(); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.runs, e.Name(), ManifestFileName)); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// ---------------------------------------------------------------------------
// Artifacts
// ---------------------------------------------------------------------------

// SaveArtifact atomically writes an artifact file inside the run directory.
//
// ref.Path is the artifact path relative to the run directory (e.g.
// "dossier.json" or "output/article.md"); it must be relative and may never
// traverse outside the run directory. The returned value is the canonical
// slash-separated relative path, ready to be recorded in the manifest's
// ArtifactRef.
//
// EVERY artifact is sanitized before persisting — including fetched source
// and document content. There is no verbatim exception: a source or document
// that contains a detected secret is persisted redacted, with a redaction
// metadata sidecar (RedactionMetaSuffix) recording redacted: true and the
// replacement count; content that is clean is written byte-for-byte
// unmodified. Sanitization fails closed: if the sanitized bytes still contain
// a detected secret, no file is written and ErrSanitization is returned.
//
// The content passed to data is treated as untrusted input: it is the only
// input to this method that can ever contain a secret, and the store — not
// the caller — guarantees what reaches disk.
func (s *Store) SaveArtifact(id run.RunID, ref run.ArtifactRef, data []byte) (string, error) {
	// Normalize the path first (a path that only contains ".." segments that
	// cancel back inside the run directory is accepted in its cleaned form),
	// then re-validate the ref with the canonical path.
	rel, err := canonicalRelPath(ref.Path)
	if err != nil {
		return "", err
	}
	cleanRef := ref
	cleanRef.Path = rel
	if err := cleanRef.Validate(); err != nil {
		return "", fmt.Errorf("%w: %v", ErrArtifactPath, err)
	}
	dir, err := s.RunDir(id)
	if err != nil {
		return "", err
	}
	if err := s.assertRunDir(dir); err != nil {
		return "", err
	}
	if err := s.assertInsideRoot(dir); err != nil {
		return "", err
	}

	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := s.assertInsideRoot(full); err != nil {
		return "", err
	}
	parent := filepath.Dir(full)
	if err := os.MkdirAll(parent, dirPerm); err != nil {
		return "", fmt.Errorf("creating directory for %s: %w", rel, err)
	}
	if err := chmodChain(parent, dir, dirPerm); err != nil {
		return "", fmt.Errorf("setting permissions for %s: %w", rel, err)
	}

	// The single sanitization boundary: no byte crosses to disk (temporary
	// file included) without passing through it, for every artifact kind.
	sanitized, count, err := s.sanitizeForPersistence(data)
	if err != nil {
		// Fail closed: the artifact was NOT written. The error deliberately
		// names the failure without including any secret material.
		return "", fmt.Errorf("persisting artifact %s: %w", rel, err)
	}
	if err := atomicWrite(parent, filepath.Base(full), sanitized, filePerm); err != nil {
		return "", fmt.Errorf("writing artifact %s: %w", rel, err)
	}
	if err := s.syncRedactionMeta(parent, full, rel, count); err != nil {
		return "", err
	}
	return rel, nil
}

// LoadArtifact reads an artifact file from the run directory.
//
// Like SaveArtifact, the joined path is re-validated after resolving symbolic
// links, so a symlinked subdirectory inside the run directory pointing
// outside the workspace cannot be read through.
//
// The returned bytes are the sanitized persisted representation: for a
// source or document that was redacted on write, callers read the redacted
// content, not the original fetch. Stages that need the unredacted original
// must keep it in memory for the duration of the active run.
func (s *Store) LoadArtifact(id run.RunID, relPath string) ([]byte, error) {
	rel, err := canonicalRelPath(relPath)
	if err != nil {
		return nil, err
	}
	dir, err := s.RunDir(id)
	if err != nil {
		return nil, err
	}
	if err := s.assertRunDir(dir); err != nil {
		return nil, err
	}
	if err := s.assertInsideRoot(dir); err != nil {
		return nil, err
	}

	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := s.assertInsideRoot(full); err != nil {
		return nil, err
	}
	// #nosec:G304 -- rel is validated by canonicalRelPath (relative, no "..",
	// no backslashes, no NUL) and the joined path is re-validated by
	// assertInsideRoot (symlink-resolved, must stay inside the workspace root)
	// immediately above.
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("reading artifact %s: %w", rel, err)
	}
	return data, nil
}

// sanitizeForPersistence is the store's single sanitization boundary: it
// returns the bytes that are allowed to reach disk for this store, together
// with the number of secret replacements sanitization performed.
//
// The run package's Sanitizer (Sanitizer) is the enforcement point: configured
// exact secret values are replaced wherever they appear, and the pattern
// detectors apply to text content. After sanitization the bytes are re-checked
// (Sanitizer.CleanBytes): if they still contain a detected secret, sanitization
// has failed and the content is refused (fail closed) — nothing, not even a
// temporary file, is written. The returned error never includes the sensitive
// value.
func (s *Store) sanitizeForPersistence(data []byte) ([]byte, int, error) {
	san := s.Sanitizer()
	out, count := san.SanitizeBytes(data)
	if san.CleanBytes(out) {
		return out, count, nil
	}
	return nil, 0, ErrSanitization
}

// syncRedactionMeta records (or clears) the redaction metadata sidecar for a
// persisted artifact, in the artifact's own directory.
//
// After a write that redacted content (count > 0), the sidecar
// "<name>"+RedactionMetaSuffix records {"redacted": true,
// "redaction_count": N} — metadata only, never the redacted values. After a
// clean write, a stale sidecar is removed (best effort) so that a sidecar's
// presence always means "the persisted copy is a redacted representation".
// The sidecar itself passes through the same sanitization boundary before
// being written.
func (s *Store) syncRedactionMeta(dir, full, rel string, count int) error {
	metaName := filepath.Base(full) + RedactionMetaSuffix
	metaPath := filepath.Join(dir, metaName)
	if err := s.assertInsideRoot(metaPath); err != nil {
		return fmt.Errorf("artifact %s: %w", rel, err)
	}
	if count > 0 {
		meta := fmt.Sprintf("{\n  \"redacted\": true,\n  \"redaction_count\": %d\n}\n", count)
		metaBytes, _, err := s.sanitizeForPersistence([]byte(meta))
		if err != nil {
			return fmt.Errorf("artifact %s: %w", rel, err)
		}
		if err := atomicWrite(dir, metaName, metaBytes, filePerm); err != nil {
			return fmt.Errorf("writing redaction metadata for %s: %w", rel, err)
		}
		return nil
	}
	// Clean write: remove a stale sidecar, if any. Failure to remove is not
	// a secret risk (the metadata is just a boolean and a count), so the
	// not-exist case is the only one that matters.
	if err := os.Remove(metaPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing stale redaction metadata for %s: %w", rel, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Path safety
// ---------------------------------------------------------------------------

// canonicalRelPath validates and normalizes a path relative to the run
// directory. The result uses forward slashes (the manifest wire format) and
// contains no "." or ".." segments.
func canonicalRelPath(rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("%w: path is empty", ErrArtifactPath)
	}
	if strings.ContainsRune(rel, '\x00') {
		return "", fmt.Errorf("%w: path contains a NUL byte", ErrArtifactPath)
	}
	if path.IsAbs(rel) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %q must be relative to the run directory", ErrArtifactPath, rel)
	}
	if strings.ContainsRune(rel, '\\') {
		return "", fmt.Errorf("%w: %q must not contain backslashes", ErrArtifactPath, rel)
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || path.IsAbs(cleaned) {
		return "", fmt.Errorf("%w: %q resolves to the run directory itself", ErrArtifactPath, rel)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %q must not traverse outside the run directory", ErrArtifactPath, rel)
	}
	return cleaned, nil
}

// manifestPath joins a run directory and the manifest file name.
func manifestPath(dir string) string {
	return filepath.Join(dir, ManifestFileName)
}

// assertManifestStorable validates the manifest in full (structure and
// redaction) before any of its bytes reach disk.
func (s *Store) assertManifestStorable(m *run.Manifest) error {
	if m == nil {
		return fmt.Errorf("%w: manifest is nil", ErrManifestInvalid)
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrManifestInvalid, err)
	}
	return nil
}

// assertRunDir reports ErrRunNotFound when the run directory is missing or is
// not a directory.
func (s *Store) assertRunDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrRunNotFound, dir)
		}
		return fmt.Errorf("%w: %v", ErrRunNotFound, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrRunNotFound, dir)
	}
	return nil
}

// assertInsideRoot reports an error when path, after resolving symbolic
// links, is not the workspace tree or a sub-path of it. This prevents a
// symlinked run directory (or a symlinked artifact path) from writing outside
// the workspace.
func (s *Store) assertInsideRoot(path string) error {
	resolved, err := resolveSymlinks(path)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve %s: %v", ErrWorkspaceRoot, path, err)
	}
	rootResolved, err := resolveSymlinks(s.root)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve workspace root %s: %v", ErrWorkspaceRoot, s.root, err)
	}
	if resolved != rootResolved && !strings.HasPrefix(resolved, rootResolved+string(os.PathSeparator)) {
		return fmt.Errorf("%w: %s resolves outside the workspace root %s", ErrWorkspaceRoot, path, s.root)
	}
	return nil
}

// resolveSymlinks resolves the symbolic links of the deepest existing
// ancestor of p and re-appends the not-yet-existing suffix, so paths that can
// still be created are resolvable as well.
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
			// ancestor (e.g. an empty path); nothing to resolve.
			return p, nil
		}
		target = parent
	}
}

// chmodChain sets perm on every directory from dir up to (but excluding)
// stop, so that the created tree's permissions do not depend on the process
// umask.
func chmodChain(dir, stop string, perm os.FileMode) error {
	d := dir
	for {
		if err := os.Chmod(d, perm); err != nil {
			return fmt.Errorf("chmod %s: %w", d, err)
		}
		if d == stop {
			return nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return nil
		}
		d = parent
	}
}

// ---------------------------------------------------------------------------
// Atomic file writes
// ---------------------------------------------------------------------------

// saveManifest marshals and atomically writes the manifest to dir.
//
// Before any byte reaches disk the marshaled document is gated byte-for-byte
// against every configured exact secret value: a manifest that still
// contains one is refused with ErrManifestInvalid. Free-text manifest fields
// (topic, config values, notes, model metadata) are pattern-checked
// individually during validation; the exact-value gate is deliberately the
// only byte-level pattern-free check because pattern heuristics can
// false-positive on benign structured strings (filesystem paths, generated
// run IDs). The manifest is a strict, validated contract — unlike artifact
// content it is never silently rewritten — so the boundary fails closed
// instead.
func (s *Store) saveManifest(m *run.Manifest, dir string) (string, error) {
	data, err := run.MarshalManifest(m)
	if err != nil {
		return "", fmt.Errorf("marshaling manifest of %s: %w", m.ID, err)
	}
	if s.Sanitizer().ContainsExactBytes(data) {
		return "", fmt.Errorf("%w: manifest of %s contains a configured secret value; refusing to persist", ErrManifestInvalid, m.ID)
	}
	if err := atomicWrite(dir, ManifestFileName, data, filePerm); err != nil {
		return "", fmt.Errorf("writing manifest of %s: %w", m.ID, err)
	}
	return manifestPath(dir), nil
}

// atomicWrite writes data to dir/name atomically: it creates a temporary file
// in the same directory, writes and syncs it, sets the final permissions, and
// renames it over the destination. Readers therefore only ever observe a
// complete file — either the previous version or the new one. The temporary
// file is removed if any step fails.
func atomicWrite(dir, name string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup: close and, if the temp file was never renamed
		// into place, remove it. The error from closing an already-closed
		// file is intentionally discarded.
		_ = tmp.Close()
		if tmpName != "" {
			_ = os.Remove(tmpName)
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
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("renaming temp file into place: %w", err)
	}
	tmpName = "" // renamed; do not remove
	return nil
}
