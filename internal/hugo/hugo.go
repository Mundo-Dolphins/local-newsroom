// Package hugo implements the v0.6 Hugo / static-site publication adapter:
// it converts a READY, rendered article into a site-ready Markdown content
// file inside a local Hugo project.
//
// This package stages content only. Its single side effect is one file in a
// local directory tree; it performs no git operations, no network I/O, and
// no LLM calls, and it holds no credentials — a Hugo project is a directory
// on disk. Publishing a staged file to the outside world (site deploy, git
// push, ...) is the job of later v0.6 publishers acting on a publication
// plan (internal/publication).
//
// The input is the existing final rendered article (the persisted
// "rendered" run artifact) plus plain publication metadata. The Writer is
// never invoked again by this package.
//
// Design principles:
//
//   - Deterministic: the same config + article always produces the same
//     bytes (canonical field order, canonical YAML escaping, no wall-clock
//     reads — the publication date is explicit input).
//
//   - Draft by default: a staged article is draft=true unless the
//     publication config (a preset / plan decision) explicitly sets
//     Draft=false.
//
//   - Never silently overwrite: an existing target with different content
//     is refused (ErrExists); identical content is a no-op; a replacement
//     happens only when Options.Overwrite is set explicitly.
//
//   - Atomic writes: the file is written to a temporary name in the same
//     directory, synced, and renamed into place, so a crash never leaves a
//     torn content file behind.
//
//   - Contained: no generated path may escape the configured Hugo root,
//     including via symbolic links.
//
//   - Secret-free: metadata values (title, author, summary, URL, tags,
//     provenance) and config values are checked on the way in, and any
//     value that looks like a credential fails the validation fail-closed
//     (the error shows the redacted form — never the raw value). The body
//     is trusted to be the output of the run's sanitization boundary
//     (internal/workspace), which already guarantees it carries no
//     detected secret.
//
//   - Site conventions stay external: Mundo Dolphins–specific front matter
//     fields, sections, and filename schemes belong in a publication preset
//     (internal/editorial), not in this engine.
package hugo

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

var (
	// ErrInvalidConfig indicates that the publication config is invalid.
	ErrInvalidConfig = errors.New("invalid Hugo publication config")

	// ErrInvalidArticle indicates that the article input is invalid.
	ErrInvalidArticle = errors.New("invalid article")

	// ErrExists indicates that the target file already exists with
	// different content. The file is left untouched; staging again with
	// Options.Overwrite replaces it deliberately.
	ErrExists = errors.New("target file already exists")

	// ErrUnsupportedFormat indicates that the requested front matter format
	// is not implemented (YAML is the only supported format in v0.6).
	ErrUnsupportedFormat = errors.New("unsupported front matter format")

	// ErrPathEscape indicates that a generated path would write outside the
	// configured Hugo root.
	ErrPathEscape = errors.New("path escapes the Hugo root")

	// ErrRoot indicates that the configured Hugo root is invalid (missing
	// or not a directory).
	ErrRoot = errors.New("invalid Hugo root")

	// ErrSecret indicates that a value about to be written looks like a
	// credential. Nothing is written (fail closed); the error shows the
	// redacted form, never the raw value.
	ErrSecret = errors.New("refusing to write content that contains a secret")
)

// ExistsError is the typed ErrExists result: it names the target and the
// two content digests so an operator (or tooling) can see exactly what
// would be replaced by what.
type ExistsError struct {
	// Path is the absolute target path.
	Path string

	// ExistingDigest is the digest of the file already on disk.
	ExistingDigest string

	// NewDigest is the digest of the content that was offered.
	NewDigest string
}

// Error implements the error interface.
func (e *ExistsError) Error() string {
	return fmt.Sprintf("%s: %s already exists (on-disk %s differs from offered %s); set Overwrite to replace it deliberately",
		ErrExists, e.Path, shortDigest(e.ExistingDigest), shortDigest(e.NewDigest))
}

// Is makes ExistsError match the ErrExists sentinel via errors.Is.
func (e *ExistsError) Is(target error) bool {
	return target == ErrExists
}

// ---------------------------------------------------------------------------
// Front matter format
// ---------------------------------------------------------------------------

// FrontMatterFormat selects the front matter serialization of the staged
// file.
type FrontMatterFormat string

// Front matter format values.
const (
	// FormatYAML is the supported front matter format (YAML is what Hugo
	// sites use in practice; it is also the only format the v0.6 adapter
	// implements).
	FormatYAML FrontMatterFormat = "yaml"

	// FormatTOML is accepted by the enum for forward compatibility but not
	// yet implemented (New rejects it).
	FormatTOML FrontMatterFormat = "toml"

	// FormatJSON is accepted by the enum for forward compatibility but not
	// yet implemented (New rejects it).
	FormatJSON FrontMatterFormat = "json"
)

// AllFrontMatterFormats lists every enum value.
var AllFrontMatterFormats = []FrontMatterFormat{FormatYAML, FormatTOML, FormatJSON}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Config is the Hugo publication configuration for one site.
//
// Everything here is a *convention*, not a site-specific fact: a publication
// preset (internal/editorial) is the place where a given site's section,
// filename scheme, author, and default fields are defined. The engine only
// interprets them generically. All values are non-secret.
type Config struct {
	// Root is the Hugo project root (the directory containing the content
	// directory and hugo.yaml). Required. A relative path is resolved
	// against the current working directory. The directory must exist.
	Root string

	// ContentDir is the content directory relative to Root
	// (default "content"). Must be relative and non-traversing.
	ContentDir string

	// SectionTemplate is the section path template, e.g. "news" or
	// "noticias/{{.Date.Format \"2006/01\"}}". The empty string places
	// articles directly in the content root. Go template syntax; the
	// context fields are {{.Slug}}, {{.Title}}, and {{.Date}} (a
	// time.Time, so {{.Date.Format "..."}} is available).
	SectionTemplate string

	// FilenamePattern is the file name template, e.g.
	// "{{.Date.Format \"20060102\"}}-{{.Slug}}.md" (default "{{.Slug}}.md").
	// It must render to a single path segment ending in ".md".
	FilenamePattern string

	// FrontMatterFormat selects the front matter serialization
	// (default FormatYAML). TOML/JSON are enum values for the future;
	// New rejects them for now.
	FrontMatterFormat FrontMatterFormat

	// DateLayout is the Go reference layout of the front matter date
	// (default "2006-01-02").
	DateLayout string

	// Draft is the explicit draft state. nil means the engine default,
	// which is true: a first publication is staged as a draft unless the
	// publication preset / plan explicitly requests otherwise.
	Draft *bool

	// Author is the default author used when the article has none.
	Author string

	// BaseURL is the site's public base URL (e.g. "https://mundodolphins.es"),
	// when set: the staged file then carries a "canonical" front matter
	// field derived from it and the article's relative path.
	BaseURL string

	// IncludeProvenance records the run ID and source artifact digest in a
	// non-public "newsroom" front matter block. Default false: provenance
	// stays in the run record unless the preset asks for it to travel with
	// the file.
	IncludeProvenance bool

	// Defaults are additional front matter fields (string values) applied
	// when no other source supplies a value (e.g. {"layout": "news"}).
	// Keys must be conservative identifiers.
	Defaults map[string]string

	// Required lists front matter field names that must resolve to a
	// non-empty value (title and date are always required by the engine).
	Required []string
}

// fieldRe allows conservative front matter field names.
var fieldRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9._-]{0,63}$`)

// Validate checks the raw config and returns a normalized copy with all
// defaults applied.
func (c Config) Validate() (Config, error) {
	var errs []error
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: "+format, append([]any{ErrInvalidConfig}, args...)...))
	}

	c.Root = strings.TrimSpace(c.Root)
	if c.Root == "" {
		errs = append(errs, fmt.Errorf("%w: root is required", ErrInvalidConfig))
	} else if fi, err := os.Stat(c.Root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("%w: root %q does not exist", ErrRoot, c.Root))
		} else {
			errs = append(errs, fmt.Errorf("%w: root %q: %v", ErrRoot, c.Root, err))
		}
	} else if !fi.IsDir() {
		errs = append(errs, fmt.Errorf("%w: root %q is not a directory", ErrRoot, c.Root))
	}

	if c.ContentDir == "" {
		c.ContentDir = "content"
	}
	if rel, err := validateRelPath(c.ContentDir); err != nil {
		errs = append(errs, fmt.Errorf("%w: content dir %q: %v", ErrInvalidConfig, c.ContentDir, err))
	} else {
		c.ContentDir = rel
	}

	if c.FilenamePattern == "" {
		c.FilenamePattern = "{{.Slug}}.md"
	}
	if !strings.HasSuffix(c.FilenamePattern, ".md") {
		errs = append(errs, fmt.Errorf("%w: filename pattern %q must end in \".md\"", ErrInvalidConfig, c.FilenamePattern))
	}

	switch c.FrontMatterFormat {
	case "":
		c.FrontMatterFormat = FormatYAML
	case FormatYAML:
		// supported
	case FormatTOML, FormatJSON:
		errs = append(errs, fmt.Errorf("%w: %q is not implemented yet; use %q", ErrUnsupportedFormat, c.FrontMatterFormat, FormatYAML))
	default:
		errs = append(errs, fmt.Errorf("%w: unknown front matter format %q (use %q)", ErrInvalidConfig, c.FrontMatterFormat, FormatYAML))
	}

	if c.DateLayout == "" {
		c.DateLayout = "2006-01-02"
	}
	// A valid date layout round-trips a probe timestamp through
	// Format + Parse: this rejects malformed layouts ("ZZZ") and layouts
	// that cannot preserve the date (a pure-literal layout reparses to the
	// zero time; a layout without a year loses the year).
	probeTime := time.Date(2006, 1, 2, 0, 0, 0, 0, time.UTC)
	probe := probeTime.Format(c.DateLayout)
	if probe == "" {
		errs = append(errs, fmt.Errorf("%w: date layout %q formats to an empty string", ErrInvalidConfig, c.DateLayout))
	} else if got, err := time.Parse(c.DateLayout, probe); err != nil {
		errs = append(errs, fmt.Errorf("%w: date layout %q does not round-trip: %v", ErrInvalidConfig, c.DateLayout, err))
	} else if !got.Equal(probeTime) {
		errs = append(errs, fmt.Errorf("%w: date layout %q does not preserve the probe date %q (got %q)", ErrInvalidConfig, c.DateLayout, probe, got.Format("2006-01-02")))
	}

	c.Author = strings.TrimSpace(c.Author)
	if c.Author != "" && run.LooksLikeSecret(c.Author) {
		errs = append(errs, fmt.Errorf("%w: author contains secret-looking material (redacted: %q)", ErrSecret, run.RedactSecrets(c.Author)))
	}

	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("%w: base URL %q must be an absolute http(s) URL", ErrInvalidConfig, c.BaseURL))
		}
	}

	if len(c.Defaults) > 32 {
		addf("config holds %d default fields, exceeding the bound of 32", len(c.Defaults))
	}
	for k, v := range c.Defaults {
		v = strings.TrimSpace(v)
		if !fieldRe.MatchString(k) {
			addf("default field %q is invalid: use a conservative identifier (letter first, letters/digits/'.'/'-'/'_', 1-64 chars)", k)
		}
		if v == "" {
			addf("default value for %q must not be empty", k)
		} else if run.LooksLikeSecret(v) {
			errs = append(errs, fmt.Errorf("%w: default value for %q contains secret-looking material (redacted: %q)", ErrSecret, k, run.RedactSecrets(v)))
		}
		c.Defaults[k] = v
	}

	for i, name := range c.Required {
		if !fieldRe.MatchString(name) {
			addf("required field %d (%q) is invalid: use a conservative identifier", i, name)
		}
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, nil
}

// WithDraft returns a copy of the config with an explicit draft state.
// This is how a publication plan / preset expresses "publish live" (false)
// over the engine's draft-by-default rule.
func (c Config) WithDraft(draft bool) Config {
	c.Draft = &draft
	return c
}

// ---------------------------------------------------------------------------
// Article
// ---------------------------------------------------------------------------

// Article is the publication input: the existing final rendered article
// plus the metadata that describes it.
//
// Body is the rendered Markdown exactly as persisted by the run
// (the "rendered" artifact). This adapter wraps it; it never re-derives
// content, and no LLM is involved.
type Article struct {
	// Title is the article title. Required.
	Title string

	// Body is the rendered Markdown body (front matter excluded).
	// Required, non-empty.
	Body string

	// Date is the publication date. Required: a zero time is rejected, so
	// output can never depend on the wall clock.
	Date time.Time

	// Author is the article author, when known (falls back to Config.Author).
	Author string

	// Summary is a short description (front matter "description").
	Summary string

	// SourceURL is the canonical source URL (front matter "source").
	SourceURL string

	// Tags, when supplied by the publication preset / plan.
	Tags []string

	// Categories, when supplied by the publication preset / plan.
	Categories []string

	// RunID is the originating run ID (recorded only when
	// Config.IncludeProvenance).
	RunID string

	// Digest is the source artifact digest, canonical "sha256:<hex>" form
	// (recorded only when Config.IncludeProvenance).
	Digest string
}

// Validate checks the article: required fields, secret-free metadata, and
// bounded lists. It returns a copy with metadata redacted and trimmed, so
// the validated form is the one that renders.
func (a Article) Validate() (Article, error) {
	var errs []error
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: "+format, append([]any{ErrInvalidArticle}, args...)...))
	}

	a.Title = strings.TrimSpace(a.Title)
	if a.Title == "" {
		errs = append(errs, fmt.Errorf("%w: title is required", ErrInvalidArticle))
	} else if len(a.Title) > 512 {
		errs = append(errs, fmt.Errorf("%w: title exceeds 512 characters", ErrInvalidArticle))
	}
	a.Title = redactChecked("title", a.Title, &errs)

	if strings.TrimSpace(a.Body) == "" {
		errs = append(errs, fmt.Errorf("%w: body is required (the rendered article must not be empty)", ErrInvalidArticle))
	}
	if len(a.Body) > 1<<20 {
		errs = append(errs, fmt.Errorf("%w: body exceeds 1 MiB", ErrInvalidArticle))
	}

	if a.Date.IsZero() {
		errs = append(errs, fmt.Errorf("%w: publication date is required (a zero date would make output depend on the wall clock)", ErrInvalidArticle))
	}

	a.Author = redactChecked("author", strings.TrimSpace(a.Author), &errs)
	if len(a.Author) > 200 {
		addf("author exceeds 200 characters")
	}
	a.Summary = redactChecked("summary", strings.TrimSpace(a.Summary), &errs)
	if len(a.Summary) > 512 {
		addf("summary exceeds 512 characters")
	}
	a.SourceURL = redactChecked("source url", strings.TrimSpace(a.SourceURL), &errs)
	if a.SourceURL != "" {
		if u, err := url.Parse(a.SourceURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			addf("source url %q must be an absolute http(s) URL", a.SourceURL)
		}
		if len(a.SourceURL) > 1024 {
			addf("source url exceeds 1024 characters")
		}
	}

	a.Tags = redactCheckedList("tags", a.Tags, &errs)
	if len(a.Categories) > 32 {
		addf("categories hold %d entries, exceeding the bound of 32", len(a.Categories))
	}
	a.Categories = redactCheckedList("categories", a.Categories, &errs)
	if len(a.Tags) > 32 {
		addf("tags hold %d entries, exceeding the bound of 32", len(a.Tags))
	}
	for _, tag := range a.Tags {
		if len(tag) > 100 {
			addf("tag %q exceeds 100 characters", tag)
		}
	}
	for _, cat := range a.Categories {
		if len(cat) > 100 {
			addf("category %q exceeds 100 characters", cat)
		}
	}

	a.RunID = strings.TrimSpace(a.RunID)
	if a.RunID != "" {
		if err := run.RunID(a.RunID).Validate(); err != nil {
			addf("run id %q is not a well-formed run ID: %v", a.RunID, err)
		}
	}
	a.Digest = strings.TrimSpace(a.Digest)
	if a.Digest != "" && !digestRe.MatchString(a.Digest) {
		addf("digest must be %q plus 64 lowercase hex characters (e.g. %q)", digestAlgPrefix, "sha256:0123…")
	}

	if len(errs) > 0 {
		return Article{}, errors.Join(errs...)
	}
	return a, nil
}

// digestRe and digestAlgPrefix describe the canonical content digest form
// (the same form as internal/publication.Digest, kept as a plain string so
// this package stays independent of the plan contract).
var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

const digestAlgPrefix = "sha256:"

// redactChecked is the metadata secret gate: it checks the ORIGINAL value
// and rejects the field when it looks like a credential (fail closed).
//
// It deliberately does NOT silently redact article metadata: this adapter
// stages content a human already approved, and a silent substitution would
// change what was approved (and what the content digest covers) without
// anyone noticing. A detected secret is a hard error, and the error shows
// the redacted value — never the raw one.
//
// The body is exempt by design: it is trusted to be the output of the run's
// sanitization boundary (internal/workspace), which already guarantees it
// carries no detected secret.
func redactChecked(kind, s string, errs *[]error) string {
	if s == "" {
		return ""
	}
	if run.LooksLikeSecret(s) {
		*errs = append(*errs, fmt.Errorf("%w: %s contains secret-looking material (redacted: %q)", ErrSecret, kind, run.RedactSecrets(s)))
		return ""
	}
	return s
}

// redactCheckedList applies the secret gate to every entry of a metadata
// list; a field with any detected secret fails the validation (the list is
// returned without the offending entries, so a rejected render could never
// carry them even if the error were ignored).
func redactCheckedList(kind string, ss []string, errs *[]error) []string {
	out := make([]string, 0, len(ss))
	for i, s := range ss {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if run.LooksLikeSecret(s) {
			*errs = append(*errs, fmt.Errorf("%w: %s[%d] contains secret-looking material (redacted: %q)", ErrSecret, kind, i, run.RedactSecrets(s)))
			continue
		}
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------------------
// Options / Result
// ---------------------------------------------------------------------------

// Options controls one Stage call.
type Options struct {
	// DryRun performs the full render and validation but writes nothing.
	// The result carries the target path, preview, and content hash of
	// what would be written.
	DryRun bool

	// Overwrite permits replacing an existing target whose content differs.
	// Without it, a different existing file is refused (ErrExists) —
	// content is never silently overwritten.
	Overwrite bool
}

// Result is the outcome of one Render / Stage / DryRun.
type Result struct {
	// Path is the absolute target path inside the Hugo root.
	Path string

	// Relative is the target path relative to the project root
	// (forward slashes, e.g. "content/news/20260711-slug.md").
	Relative string

	// Slug is the deterministic slug derived from the title.
	Slug string

	// Date is the resolved publication date (echo of the input).
	Date time.Time

	// Draft is the resolved draft state of the staged file.
	Draft bool

	// Content is the full staged file (front matter + body).
	Content []byte

	// Digest is the canonical "sha256:<hex>" digest of Content.
	Digest string

	// Preview is the first PreviewBytes of Content, cut on a rune
	// boundary.
	Preview string

	// Exists reports whether a file was present at Path before the
	// operation (always recorded, dry run or not).
	Exists bool

	// Unchanged reports that the existing file already held byte-identical
	// content (no write performed; idempotent re-stage).
	Unchanged bool

	// Created reports that a new file was written.
	Created bool

	// Updated reports that an existing file was deliberately replaced
	// (Overwrite was set and the content differed).
	Updated bool
}

// ---------------------------------------------------------------------------
// Publisher
// ---------------------------------------------------------------------------

// Publisher stages articles into one Hugo project.
//
// A Publisher is immutable and safe for concurrent use.
type Publisher struct {
	cfg          Config
	root         string // absolute, cleaned project root
	contentRoot  string // absolute content directory (root/contentDir)
	sectionTmpl  *template
	filenameTmpl *template
}

// New builds a Publisher, validating and normalizing the config.
//
// The root must exist and be a directory. Templates are parsed up front so
// a misconfigured preset fails at construction, not mid-stage.
func New(cfg Config) (*Publisher, error) {
	c, err := cfg.Validate()
	if err != nil {
		return nil, err
	}
	root := filepath.Clean(c.Root)
	if !filepath.IsAbs(root) {
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("%w: cannot resolve root %q: %v", ErrRoot, c.Root, err)
		}
		root = abs
	}
	sectionTmpl, err := parsePathTemplate("section", c.SectionTemplate)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	filenameTmpl, err := parsePathTemplate("filename", c.FilenamePattern)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	// Probe the templates with a benign context: a static traversal or a
	// path separator baked into the template fails at construction, not
	// mid-stage. (Render-time validation still applies to values that flow
	// through the templates, e.g. a hostile title.)
	probeCtx := pathContext{Slug: "probe", Title: "probe", Date: time.Date(2006, 1, 2, 0, 0, 0, 0, time.UTC)}
	if s, err := sectionTmpl.execute(probeCtx); err != nil {
		return nil, fmt.Errorf("%w: rendering section template: %v", ErrInvalidConfig, err)
	} else if s != "" {
		if _, verr := validateRelPath(s); verr != nil {
			return nil, fmt.Errorf("%w: section template renders unsafe path %q: %v", ErrInvalidConfig, s, verr)
		}
	}
	if f, err := filenameTmpl.execute(probeCtx); err != nil {
		return nil, fmt.Errorf("%w: rendering filename template: %v", ErrInvalidConfig, err)
	} else if strings.ContainsRune(f, '/') {
		return nil, fmt.Errorf("%w: filename template renders path separators in %q", ErrInvalidConfig, f)
	}
	contentRoot := filepath.Join(root, c.ContentDir)
	return &Publisher{
		cfg:          c,
		root:         root,
		contentRoot:  contentRoot,
		sectionTmpl:  sectionTmpl,
		filenameTmpl: filenameTmpl,
	}, nil
}

// Config returns the validated, normalized config.
func (p *Publisher) Config() Config { return p.cfg }

// Root returns the absolute Hugo project root.
func (p *Publisher) Root() string { return p.root }

// ContentRoot returns the absolute content directory.
func (p *Publisher) ContentRoot() string { return p.contentRoot }

// Render converts the article into its staged-file representation without
// touching the filesystem: the target path, the full bytes, the digest,
// and a preview. Rendering is pure and deterministic.
func (p *Publisher) Render(a Article) (*Result, error) {
	article, err := a.Validate()
	if err != nil {
		return nil, err
	}
	slug := Slugify(article.Title)

	rel, err := p.relativePath(article, slug)
	if err != nil {
		return nil, err
	}
	full, err := p.absolutePath(rel)
	if err != nil {
		return nil, err
	}

	draft := true
	if p.cfg.Draft != nil {
		draft = *p.cfg.Draft
	}

	fields, _, err := p.buildFields(article, rel, draft)
	if err != nil {
		return nil, err
	}
	fm, err := marshalFrontMatter(p.cfg.FrontMatterFormat, fields)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArticle, err)
	}

	body := article.Body
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	content := append([]byte("---\n"+string(fm)+"---\n\n"), body...)
	// front matter always ends with a newline; the body was normalized to
	// end with one; the file is front matter, blank line, body.

	res := &Result{
		Path:     full,
		Relative: p.projectRelative(rel),
		Slug:     slug,
		Date:     article.Date,
		Draft:    draft,
		Content:  content,
		Digest:   DigestOf(content),
		Preview:  Preview(content, PreviewBytes),
	}
	// Existence probe (read-only). Absent: nothing to record. Present and
	// readable: record it. Present but unreadable (e.g. a directory, which
	// ReadFile rejects): record existence via Stat so Stage applies its
	// overwrite / directory policy rather than failing on the read.
	switch b, err := os.ReadFile(full); { // #nosec:G304 -- full is the validated, containment-checked target path.
	case err == nil:
		res.Exists = true
		if string(b) == string(content) {
			res.Unchanged = true
		}
	case errors.Is(err, os.ErrNotExist):
		// absent: the common first-publication path
	default:
		if _, serr := os.Stat(full); serr == nil {
			res.Exists = true
		} else if !errors.Is(serr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspecting existing target %s: %w", full, err)
		}
	}
	return res, nil
}

// relativePath renders the section and filename templates and assembles the
// target path relative to the content directory.
func (p *Publisher) relativePath(a Article, slug string) (string, error) {
	ctx := pathContext{Slug: slug, Title: a.Title, Date: a.Date}
	section, err := p.sectionTmpl.execute(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: rendering section path: %v", ErrInvalidArticle, err)
	}
	if section != "" {
		if _, err := validateRelPath(section); err != nil {
			return "", fmt.Errorf("%w: rendered section path %q is unsafe: %v", ErrPathEscape, section, err)
		}
	}
	filename, err := p.filenameTmpl.execute(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: rendering filename: %v", ErrInvalidArticle, err)
	}
	if strings.ContainsRune(filename, '/') || filename == "." || filename == ".." {
		return "", fmt.Errorf("%w: rendered filename %q must be a single path segment", ErrPathEscape, filename)
	}
	if !strings.HasSuffix(filename, ".md") {
		return "", fmt.Errorf("%w: rendered filename %q must end in \".md\"", ErrInvalidArticle, filename)
	}

	// path.Join drops empty components, so an empty section places the
	// file directly in the content root.
	return path.Join(section, filename), nil
}

// projectRelative returns the target path relative to the project root
// (content directory + content-relative path), for Result.Relative.
func (p *Publisher) projectRelative(rel string) string {
	return filepath.ToSlash(path.Join(p.cfg.ContentDir, rel))
}

// absolutePath joins the content root and the relative path, then proves the
// result stays inside the Hugo root (symlinks resolved).
func (p *Publisher) absolutePath(rel string) (string, error) {
	full := filepath.Join(p.contentRoot, filepath.FromSlash(rel))
	if err := assertInsideRoot(p.root, full); err != nil {
		return "", err
	}
	return full, nil
}

// Stage renders the article and writes it into the Hugo tree, applying the
// overwrite policy:
//
//   - target absent: the file is created (Result.Created);
//   - target present with identical bytes: no write (Result.Unchanged);
//   - target present with different bytes: refused (ErrExists) unless
//     Options.Overwrite, in which case it is replaced (Result.Updated).
//
// With Options.DryRun the method performs everything except the write (and
// the directory creation) and reports what would happen: an existing
// differing file is reported via Result.Exists without an error.
func (p *Publisher) Stage(a Article, opts Options) (*Result, error) {
	res, err := p.Render(a)
	if err != nil {
		return nil, err
	}
	if opts.DryRun {
		// A dry run informs; it never fails for the existence of the
		// target (the operator reads Result.Exists / Result.Unchanged).
		return res, nil
	}

	// Re-check on the floor: the file may have appeared or changed since
	// Render's existence probe.
	var existing []byte
	switch st, err := os.Stat(res.Path); {
	case err != nil && errors.Is(err, os.ErrNotExist):
		existing = nil
	case err != nil:
		return nil, fmt.Errorf("inspecting target %s: %w", res.Path, err)
	default:
		if st.IsDir() {
			return nil, fmt.Errorf("%w: target %s exists and is a directory", ErrExists, res.Path)
		}
		b, err := os.ReadFile(res.Path) // #nosec:G304 -- res.Path is the validated, containment-checked target path.
		if err != nil {
			return nil, fmt.Errorf("reading existing target %s: %w", res.Path, err)
		}
		existing = b
	}

	if existing == nil {
		if err := p.write(res); err != nil {
			return nil, err
		}
		res.Created = true
		return res, nil
	}

	if string(existing) == string(res.Content) {
		res.Unchanged = true // idempotent: identical content, nothing to do
		return res, nil
	}

	if !opts.Overwrite {
		return nil, &ExistsError{Path: res.Path, ExistingDigest: DigestOf(existing), NewDigest: res.Digest}
	}
	if err := p.write(res); err != nil {
		return nil, err
	}
	res.Updated = true
	return res, nil
}

// write creates the target directory chain (if needed) and atomically writes
// the staged bytes over res.Path.
func (p *Publisher) write(res *Result) error {
	parent := filepath.Dir(res.Path)
	if err := ensureDir(p.root, parent); err != nil {
		return err
	}
	if err := atomicWrite(parent, filepath.Base(res.Path), res.Content, filePerm); err != nil {
		return fmt.Errorf("writing %s: %w", res.Path, err)
	}
	return nil
}

// DryRun is a convenience wrapper: a full rehearsal of Stage with no write.
func (p *Publisher) DryRun(a Article) (*Result, error) {
	return p.Stage(a, Options{DryRun: true})
}

// DigestOf computes the canonical "sha256:<64 lowercase hex>" digest of the
// exact bytes given — the same form used by internal/publication, so a
// staged-file digest can travel in plan documents unchanged.
func DigestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return digestAlgPrefix + hex.EncodeToString(sum[:])
}

// PreviewBytes bounds the preview carried in a Result.
const PreviewBytes = 4096

// Preview returns the first n bytes of data, cut on a UTF-8 rune boundary.
func Preview(data []byte, n int) string {
	if len(data) <= n {
		return string(data)
	}
	cut := n
	// Walk back off any trailing continuation bytes of a split rune.
	for cut > 0 && data[cut]&0xC0 == 0x80 {
		cut--
	}
	return string(data[:cut])
}

// shortDigest shortens a canonical digest for error messages.
func shortDigest(d string) string {
	if len(d) > 13 {
		return d[:13] + "…"
	}
	return d
}
