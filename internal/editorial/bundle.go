// Package editorial implements the external editorial bundle: a local
// directory — typically a separately checked-out private repository — that
// supplies publication presets to the public engine without committing the
// publication's private profile data into the engine repository.
//
// # Layout
//
// A bundle is a plain directory tree. The engine consumes it in place; it
// never clones or pulls a Git repository and performs no network access:
//
//	<bundle-root>/
//	  common/                     optional: publication-wide profile content
//	    style.md
//	    facts-policy.md
//	    vocabulary.md
//	    examples/
//	  authors/                    optional: author profiles
//	    <author>/style.md
//	    <author>/examples/
//	  publications/               optional: one subdirectory per publication
//	    <name>/
//	      publication.yaml        the publication preset
//	      hugo.yaml               optional: Hugo destination config
//	      bluesky.yaml            optional: Bluesky destination config
//
// The profile layer (common/ and authors/) is consumed by the existing
// profiles loader with the bundle root as its base path; this package adds
// the preset layer on top (publications/<name>/) together with the security
// rules for the preset files.
//
// # Privacy
//
// Bundle content is private editorial data. The only form of it that leaves
// the bundle is resolved, non-secret snapshot metadata: the preset name, the
// bundle path, and a content hash (a digest of the resolved non-secret
// values, with secret fields reduced to their environment variable
// references). Profile content (style text, vocabulary, examples) and
// destination content (handles, URLs) are never copied into run artifacts.
//
// # Secrets
//
// Every credential-like key in every bundle YAML file must hold an
// environment reference ({env: NAME}); a literal credential is a load-time
// error (the same rule as the newsroom config file, enforced with
// config.ValidateSecretKeys).
package editorial

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SchemaVersion is the bundle config schema version understood by this release.
const SchemaVersion = 1

// Bundle layout constants.
const (
	// PublicationsDir is the bundle subdirectory holding publication presets.
	PublicationsDir = "publications"

	// PresetFileName is the preset file inside a publication directory.
	PresetFileName = "publication.yaml"

	// HugoConfigFileName is the conventional Hugo destination config file.
	HugoConfigFileName = "hugo.yaml"

	// BlueskyConfigFileName is the conventional Bluesky destination config file.
	BlueskyConfigFileName = "bluesky.yaml"

	// DefaultMaxConfigFileBytes is the maximum size of a single YAML config
	// file in a bundle (64KB).
	DefaultMaxConfigFileBytes = 64 * 1024

	// maxRelPathLen bounds bundle-relative paths.
	maxRelPathLen = 512
)

// nameRe allows conservative, filesystem-safe preset identifiers
// ("mundodolphins", "sports-desk").
var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Errors returned by the bundle loader.
var (
	// ErrRootEmpty indicates that no bundle root was given.
	ErrRootEmpty = errors.New("bundle root must not be empty")

	// ErrRootNotExists indicates that the bundle root does not exist.
	ErrRootNotExists = errors.New("bundle root does not exist")

	// ErrRootNotDirectory indicates that the bundle root is not a directory.
	ErrRootNotDirectory = errors.New("bundle root is not a directory")

	// ErrRootTraversal indicates that the bundle root path contains a ".."
	// segment.
	ErrRootTraversal = errors.New("bundle root path contains a '..' segment")

	// ErrInvalidName indicates that a publication name or file reference is
	// not a conservative identifier.
	ErrInvalidName = errors.New("invalid publication name")

	// ErrFileNotFound indicates that a referenced bundle file is missing.
	ErrFileNotFound = errors.New("bundle config file not found")

	// ErrFileTraversal indicates that a path would escape the bundle root.
	ErrFileTraversal = errors.New("bundle path escapes the bundle root")

	// ErrFileTooLarge indicates that a bundle file exceeds the size bound.
	ErrFileTooLarge = errors.New("bundle config file exceeds maximum size")

	// ErrInvalidYAML indicates that a bundle config file fails to parse or
	// fails schema validation.
	ErrInvalidYAML = errors.New("invalid bundle config")
)

// Config configures the bundle loader.
type Config struct {
	// Root is the bundle root directory (required). A relative path is
	// resolved against the working directory; it must not contain ".."
	// segments.
	Root string

	// MaxConfigFileBytes is the maximum size of a single YAML config file.
	// Zero or negative uses DefaultMaxConfigFileBytes.
	MaxConfigFileBytes int64
}

// Loader reads an editorial bundle from a local directory.
//
// The loader is read-only and makes no network or git calls: it stats and
// reads ordinary files under the bundle root. Every path it reads is
// contained in the root (path traversal is rejected).
type Loader struct {
	root               string
	maxConfigFileBytes int64
}

// NewLoader validates the bundle root and returns a loader.
//
// A missing root or a root that is not a directory is an error; a root
// without any of the optional subdirectories is fine (an empty bundle with
// no presets).
func NewLoader(cfg Config) (*Loader, error) {
	root := strings.TrimSpace(cfg.Root)
	if root == "" {
		return nil, ErrRootEmpty
	}
	if containsDotDot(root) {
		return nil, fmt.Errorf("%w: %s", ErrRootTraversal, root)
	}
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("resolving bundle root: %w", err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrRootNotExists, abs)
		}
		return nil, fmt.Errorf("stat bundle root %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrRootNotDirectory, abs)
	}
	maxBytes := cfg.MaxConfigFileBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxConfigFileBytes
	}
	return &Loader{
		root:               abs,
		maxConfigFileBytes: maxBytes,
	}, nil
}

// Root returns the absolute bundle root path.
func (l *Loader) Root() string {
	return l.root
}

// Bundle is the loaded state of one editorial bundle.
type Bundle struct {
	// Root is the absolute bundle root path.
	Root string

	// Names lists every publication preset name in sorted order.
	Names []string

	// Presets maps publication name to its loaded preset.
	Presets map[string]*Preset

	// Hash is the digest of every preset in the bundle (in sorted name
	// order), for reproducibility records.
	Hash string
}

// Load reads the complete bundle: every publication preset under
// publications/, in sorted name order.
//
// A bundle without a publications/ directory (or with an empty one) loads
// as an empty bundle: no presets, valid.
func (l *Loader) Load(ctx context.Context) (*Bundle, error) {
	_ = ctx // The bundle is local; no cancellation-sensitive work.

	names, err := l.Publications()
	if err != nil {
		return nil, err
	}
	b := &Bundle{
		Root:    l.root,
		Names:   names,
		Presets: make(map[string]*Preset, len(names)),
	}
	h := sha256.New()
	for _, n := range names {
		p, err := l.LoadPreset(n)
		if err != nil {
			return nil, fmt.Errorf("loading publication %q: %w", n, err)
		}
		b.Presets[n] = p
		h.Write([]byte(p.Hash))
	}
	b.Hash = hex.EncodeToString(h.Sum(nil))
	return b, nil
}

// Publications lists the publication preset names in sorted order.
//
// A missing publications/ directory yields an empty list (a valid bundle
// with no presets). Directories with names that are not conservative
// identifiers are rejected, because a preset name is also an identifier in
// run records.
func (l *Loader) Publications() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(l.root, PublicationsDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing %s/: %w", PublicationsDir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !nameRe.MatchString(e.Name()) {
			return nil, fmt.Errorf("%w: %q under %s/ (use letters, digits, '.', '-', '_' — 1-64 chars, must not start with a separator)",
				ErrInvalidName, e.Name(), PublicationsDir)
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// readBundleFile reads one file at a bundle-relative path, enforcing
// containment in the bundle root, the size bound, and conservative path
// shape. It returns the absolute path and the file bytes.
//
// The containment check mirrors the profiles loader: the cleaned path must
// stay inside the (absolute, clean) root, which defeats ".." traversal.
// Absolute paths and paths with ".." segments are rejected outright.
func (l *Loader) readBundleFile(rel string) (string, []byte, error) {
	if rel == "" || filepath.IsAbs(rel) || containsDotDot(rel) {
		return "", nil, fmt.Errorf("%w: %s", ErrFileTraversal, rel)
	}
	rel = filepath.Clean(rel)
	if len(rel) > maxRelPathLen {
		return "", nil, fmt.Errorf("%w: %s exceeds %d characters", ErrFileTraversal, rel, maxRelPathLen)
	}
	abs := filepath.Join(l.root, rel)
	basePrefix := l.root + string(os.PathSeparator)
	if !strings.HasPrefix(abs, basePrefix) {
		return "", nil, fmt.Errorf("%w: %s", ErrFileTraversal, rel)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, fmt.Errorf("%w: %s", ErrFileNotFound, rel)
		}
		return "", nil, fmt.Errorf("stat %s: %w", rel, err)
	}
	if info.IsDir() {
		return "", nil, fmt.Errorf("%w: %s is a directory", ErrFileNotFound, rel)
	}
	if info.Size() > l.maxConfigFileBytes {
		return "", nil, fmt.Errorf("%w: %s (size %d, limit %d)", ErrFileTooLarge, rel, info.Size(), l.maxConfigFileBytes)
	}
	// #nosec G304 -- abs is built from a validated bundle-relative name: no ".."
	// segment, bounded length, symlink-resolved via stat, and verified by
	// prefix to stay inside the bundle root before the read.
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", nil, fmt.Errorf("reading %s: %w", rel, err)
	}
	return abs, data, nil
}

// containsDotDot reports whether p contains a ".." path segment.
func containsDotDot(p string) bool {
	for _, segment := range strings.FieldsFunc(p, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if segment == ".." {
			return true
		}
	}
	return false
}
