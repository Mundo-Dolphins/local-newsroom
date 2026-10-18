package hugo_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/local-newsroom/internal/hugo"
)

// testDate is the fixed publication date behind every fixture: a zero date
// is rejected by the engine, and a fixed date keeps output deterministic.
var testDate = time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC)

// newHugoTree builds a minimal Hugo project in a temp dir: the project root
// with a hugo.yaml and an (otherwise empty) content directory.
func newHugoTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hugo.yaml"),
		[]byte("baseURL: https://example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "content"), 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

// testConfig is a deterministic config against the given tree.
func testConfig(root string) hugo.Config {
	return hugo.Config{
		Root:            root,
		SectionTemplate: "news",
		FilenamePattern: `{{.Date.Format "20060102"}}-{{.Slug}}.md`,
	}
}

// testArticle is the canonical READY article used across the suite.
func testArticle() hugo.Article {
	return hugo.Article{
		Title:     "Los Dolphins ganan el Super Bowl — ¡Increíble!",
		Body:      "# Los Dolphins ganan el Super Bowl — ¡Increíble!\n\nEl equipo de Miami celebró una noche histórica.\n",
		Date:      testDate,
		Author:    "Javi T.",
		Summary:   "Una noche para el recuerdo en Miami.",
		SourceURL: "https://www.example.com/2026/07/11/super-bowl",
		Tags:      []string{"dolphins", "super-bowl"},
	}
}

// decodeFrontMatter splits the staged file at its closing fence and decodes
// the front matter block into a generic map.
func decodeFrontMatter(t *testing.T, content []byte) map[string]any {
	t.Helper()
	s := string(content)
	if !strings.HasPrefix(s, "---\n") {
		t.Fatalf("content does not start with a front matter fence: %q", s[:min(len(s), 20)])
	}
	rest := s[len("---\n"):]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		t.Fatal("content has no closing front matter fence")
	}
	var fm map[string]any
	if err := yaml.Unmarshal([]byte(rest[:idx]), &fm); err != nil {
		t.Fatalf("front matter is not valid YAML: %v", err)
	}
	return fm
}

func readStaged(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading staged file %s: %v", path, err)
	}
	return b
}

// noTmpLeftovers asserts that the target directory holds no .tmp-* residue.
func noTmpLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temporary file %s left behind in %s", e.Name(), dir)
		}
	}
}

// ---------------------------------------------------------------------------
// Article creation
// ---------------------------------------------------------------------------

func TestStageCreatesValidArticle(t *testing.T) {
	root := newHugoTree(t)
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := p.Stage(testArticle(), hugo.Options{})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if !res.Created {
		t.Error("Created not set")
	}
	if res.Draft != true {
		t.Errorf("draft = %v, want true (engine default)", res.Draft)
	}
	if res.Slug != "los-dolphins-ganan-el-super-bowl-increíble" {
		t.Errorf("slug = %q", res.Slug)
	}
	want := filepath.Join(root, "content", "news", "20260711-los-dolphins-ganan-el-super-bowl-increíble.md")
	if res.Path != want {
		t.Errorf("path = %q, want %q", res.Path, want)
	}

	data := readStaged(t, res.Path)
	if !bytes.Equal(data, res.Content) {
		t.Error("on-disk bytes differ from Result.Content")
	}
	if hugo.DigestOf(data) != res.Digest {
		t.Errorf("digest %q does not match on-disk bytes", res.Digest)
	}
	if !bytes.HasSuffix(data, []byte("celebró una noche histórica.\n")) {
		t.Error("body was not appended verbatim")
	}

	fm := decodeFrontMatter(t, data)
	if fm["title"] != testArticle().Title {
		t.Errorf("front matter title = %v", fm["title"])
	}
	if fm["date"] != "2026-07-11" {
		t.Errorf("front matter date = %v", fm["date"])
	}
	if fm["draft"] != true {
		t.Errorf("front matter draft = %v (and must decode as a bool)", fm["draft"])
	}
	if fm["author"] != "Javi T." {
		t.Errorf("front matter author = %v", fm["author"])
	}
	if fm["description"] != "Una noche para el recuerdo en Miami." {
		t.Errorf("front matter description = %v", fm["description"])
	}
	if fm["source"] != "https://www.example.com/2026/07/11/super-bowl" {
		t.Errorf("front matter source = %v", fm["source"])
	}
	tags, _ := fm["tags"].([]interface{})
	if len(tags) != 2 || tags[0] != "dolphins" || tags[1] != "super-bowl" {
		t.Errorf("front matter tags = %v", fm["tags"])
	}
	if _, has := fm["newsroom"]; has {
		t.Error("provenance block present although IncludeProvenance is false")
	}
}

func TestStageDeterministicAndIdempotent(t *testing.T) {
	root := newHugoTree(t)
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}

	first, err := p.Stage(testArticle(), hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	original := readStaged(t, first.Path)

	// Identical re-stage: a no-op with an identical digest and bytes.
	again, err := p.Stage(testArticle(), hugo.Options{})
	if err != nil {
		t.Fatalf("re-stage: %v", err)
	}
	if !again.Unchanged || again.Created || again.Updated {
		t.Errorf("expected an idempotent no-op, got created=%v updated=%v unchanged=%v",
			again.Created, again.Updated, again.Unchanged)
	}
	if again.Digest != first.Digest {
		t.Errorf("digest changed between identical renders: %s != %s", first.Digest, again.Digest)
	}
	if !bytes.Equal(again.Content, first.Content) {
		t.Error("render output is not byte-identical for identical input")
	}
	if after := readStaged(t, first.Path); !bytes.Equal(after, original) {
		t.Error("on-disk file changed by an identical re-stage")
	}
}

func TestStageUnicodeTitleAndSlug(t *testing.T) {
	root := newHugoTree(t)
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}

	a := testArticle()
	a.Title = "Cañón: ¡Día de éslo! 🏈 (2026)"
	res, err := p.Stage(a, hugo.Options{})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if res.Slug != "cañón-día-de-éslo-2026" {
		t.Errorf("slug = %q, want %q", res.Slug, "cañón-día-de-éslo-2026")
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("staged file missing: %v", err)
	}
	fm := decodeFrontMatter(t, readStaged(t, res.Path))
	if fm["title"] != a.Title {
		t.Errorf("unicode title did not round-trip: got %v", fm["title"])
	}

	// CJK title: unicode letters are kept in the slug.
	a.Title = "海豚夺得冠军"
	res, err = p.Stage(a, hugo.Options{})
	if err != nil {
		t.Fatalf("Stage (CJK): %v", err)
	}
	if res.Slug != "海豚夺得冠军" {
		t.Errorf("CJK slug = %q", res.Slug)
	}
}

// ---------------------------------------------------------------------------
// Duplicates and overwrite policy
// ---------------------------------------------------------------------------

func TestDuplicateFile(t *testing.T) {
	root := newHugoTree(t)
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}

	first, err := p.Stage(testArticle(), hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	original := readStaged(t, first.Path)

	// Different content, no overwrite: refused, file untouched.
	changed := testArticle()
	changed.Body += "An extra, unapproved paragraph.\n"
	_, err = p.Stage(changed, hugo.Options{})
	if err == nil {
		t.Fatal("Stage with a different existing file: accepted, want ErrExists")
	}
	if !errors.Is(err, hugo.ErrExists) {
		t.Fatalf("error = %v, want ErrExists", err)
	}
	if after := readStaged(t, first.Path); !bytes.Equal(after, original) {
		t.Error("existing file was modified without Overwrite")
	}
	noTmpLeftovers(t, filepath.Dir(first.Path))

	// With Overwrite: replaced deliberately.
	res, err := p.Stage(changed, hugo.Options{Overwrite: true})
	if err != nil {
		t.Fatalf("Stage with Overwrite: %v", err)
	}
	if !res.Updated {
		t.Error("Updated not set on overwrite")
	}
	if after := readStaged(t, first.Path); !bytes.Equal(after, res.Content) {
		t.Error("overwritten file does not match the offered content")
	}
}

func TestDuplicateIdenticalFileIsNoOp(t *testing.T) {
	root := newHugoTree(t)
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Stage(testArticle(), hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	original := readStaged(t, first.Path)
	res, err := p.Stage(testArticle(), hugo.Options{})
	if err != nil {
		t.Fatalf("identical re-stage: %v", err)
	}
	if !res.Unchanged {
		t.Error("expected Unchanged for identical content")
	}
	if after := readStaged(t, res.Path); !bytes.Equal(after, original) {
		t.Error("identical re-stage rewrote the file")
	}
}

// ---------------------------------------------------------------------------
// Path safety
// ---------------------------------------------------------------------------

func TestPathTraversalSectionTemplate(t *testing.T) {
	root := newHugoTree(t)
	cfg := testConfig(root)
	cfg.SectionTemplate = "../evil" // static traversal
	if _, err := hugo.New(cfg); err == nil {
		t.Fatal("config with a traversing section template was accepted")
	}

	cfg.SectionTemplate = "{{.Title}}" // dynamic: a hostile title
	p, err := hugo.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := testArticle()
	a.Title = "../../etc/passwd"
	if _, stageErr := p.Stage(a, hugo.Options{}); stageErr == nil {
		t.Fatal("traversing rendered section path was accepted")
	} else if !errors.Is(stageErr, hugo.ErrPathEscape) {
		t.Errorf("error = %v, want ErrPathEscape", stageErr)
	}
	if _, err := os.Stat(filepath.Join(root, "evil")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a directory outside content/ was created")
	}
}

func TestPathTraversalFilenameTemplate(t *testing.T) {
	root := newHugoTree(t)
	cfg := testConfig(root)
	cfg.FilenamePattern = `{{.Title}}.md` // a title may contain "/"
	p, err := hugo.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := testArticle()
	a.Title = "news/../../escape"
	if _, stageErr := p.Stage(a, hugo.Options{}); stageErr == nil {
		t.Fatal("filename rendered with path separators was accepted")
	} else if !errors.Is(stageErr, hugo.ErrPathEscape) {
		t.Errorf("error = %v, want ErrPathEscape", stageErr)
	}
}

func TestSymlinkEscapeIsRefused(t *testing.T) {
	root := newHugoTree(t)
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(root, "content")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "content")); err != nil {
		t.Skipf("cannot create symlink in this environment: %v", err)
	}
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Stage(testArticle(), hugo.Options{})
	if err == nil {
		t.Fatal("stage through a symlinked content dir was accepted")
	}
	if !errors.Is(err, hugo.ErrPathEscape) {
		t.Errorf("error = %v, want ErrPathEscape", err)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Errorf("the outside directory was written through the symlink: %v", entries)
	}
}

// ---------------------------------------------------------------------------
// Dry run
// ---------------------------------------------------------------------------

func TestDryRunWritesNothing(t *testing.T) {
	root := newHugoTree(t)
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}

	res, err := p.DryRun(testArticle())
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if res.Created || res.Updated {
		t.Error("dry run reported a write")
	}
	if res.Path == "" || res.Digest == "" || res.Preview == "" {
		t.Errorf("dry run result incomplete: path=%q digest=%q preview=%q", res.Path, res.Digest, res.Preview)
	}
	if _, err := os.Stat(res.Path); !errors.Is(err, os.ErrNotExist) {
		t.Error("dry run wrote a file")
	}
	if _, err := os.Stat(filepath.Join(root, "content", "news")); !errors.Is(err, os.ErrNotExist) {
		t.Error("dry run created directories")
	}
	if !strings.Contains(res.Preview, "title:") {
		t.Errorf("preview does not look like front matter: %q", res.Preview)
	}
	if len(res.Preview) > hugo.PreviewBytes {
		t.Errorf("preview longer than the bound: %d", len(res.Preview))
	}

	// A dry run must also enforce path safety.
	evil := testArticle()
	evil.Title = "../../etc/passwd"
	evilCfg := testConfig(root)
	evilCfg.SectionTemplate = "{{.Title}}"
	evilPub, err := hugo.New(evilCfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evilPub.DryRun(evil); err == nil {
		t.Fatal("dry run accepted a traversing path")
	}
}

func TestDryRunWithExistingTargetReports(t *testing.T) {
	root := newHugoTree(t)
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Stage(testArticle(), hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}

	// Existing identical file: no error, Exists + Unchanged reported.
	same, err := p.DryRun(testArticle())
	if err != nil {
		t.Fatalf("DryRun (identical existing): %v", err)
	}
	if !same.Exists || !same.Unchanged {
		t.Errorf("expected Exists+Unchanged, got exists=%v unchanged=%v", same.Exists, same.Unchanged)
	}

	// Existing different file: still no error — the rehearsal informs.
	changed := testArticle()
	changed.Body += "New content.\n"
	diff, err := p.DryRun(changed)
	if err != nil {
		t.Fatalf("DryRun (different existing): %v", err)
	}
	if !diff.Exists || diff.Unchanged {
		t.Errorf("expected Exists without Unchanged, got exists=%v unchanged=%v", diff.Exists, diff.Unchanged)
	}
	if diff.Digest == first.Digest {
		t.Error("dry run digest for changed content equals the old digest")
	}
	if after := readStaged(t, first.Path); !bytes.Equal(after, first.Content) {
		t.Error("dry run modified the file")
	}
}

// ---------------------------------------------------------------------------
// Draft / publish modes
// ---------------------------------------------------------------------------

func TestDraftDefaultAndPublishMode(t *testing.T) {
	root := newHugoTree(t)

	// Engine default: draft true.
	pDraft, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	res, err := pDraft.Stage(testArticle(), hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Draft {
		t.Error("expected draft=true by default")
	}
	if !strings.Contains(string(readStaged(t, res.Path)), "draft: true") {
		t.Error("staged file lacks 'draft: true'")
	}

	// Explicit publish: the preset/plan requests otherwise.
	cfg := testConfig(root).WithDraft(false)
	pLive, err := hugo.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if *pLive.Config().Draft {
		t.Fatal("WithDraft(false) did not set Draft=false")
	}
	a := testArticle()
	a.Title = "Second article for the publish test"
	res2, err := pLive.Stage(a, hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Draft {
		t.Error("expected draft=false in publish mode")
	}
	data := readStaged(t, res2.Path)
	if !strings.Contains(string(data), "draft: false") {
		t.Error("staged file lacks 'draft: false'")
	}
	fm := decodeFrontMatter(t, data)
	if d, ok := fm["draft"].(bool); !ok || d {
		t.Errorf("draft decoded as %v (%T), want bool false", fm["draft"], fm["draft"])
	}
}

// ---------------------------------------------------------------------------
// Front matter serialization
// ---------------------------------------------------------------------------

func TestFrontMatterSerializationHostileValues(t *testing.T) {
	root := newHugoTree(t)
	cfg := testConfig(root)
	cfg.SectionTemplate = "" // straight into the content root
	p, err := hugo.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	hostile := []string{
		"yes", "no", "on", "off", "y", "n",
		"true", "123", "null", "~",
		"2026-07-11", "0x1F", "0o700",
		"A: B", "has # hash", "  leading", "trailing  ",
		"multi\nline", "tab\tinside", "emoji 🏈",
		`"quoted"`, "%directive", "@at", "&anchor", "*alias",
		"| pipe", "> gt", "- leading dash", "? question",
		"'single", "key: value: more",
	}
	for _, title := range hostile {
		a := testArticle()
		a.Title = title
		res, err := p.Stage(a, hugo.Options{Overwrite: true})
		if err != nil {
			t.Fatalf("Stage(%q): %v", title, err)
		}
		data := readStaged(t, res.Path)
		fm := decodeFrontMatter(t, data)
		// The engine trims metadata on the way in; the round-trip must be
		// exact for the trimmed value.
		if fm["title"] != strings.TrimSpace(title) {
			t.Errorf("title %q did not round-trip: got %v", title, fm["title"])
		}
	}

	// Reserved YAML 1.1 boolean words must be explicitly quoted in the bytes,
	// so parsers from every era read them as strings.
	a := testArticle()
	a.Title = "yes"
	res, err := p.Stage(a, hugo.Options{Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	data := readStaged(t, res.Path)
	if !bytes.Contains(data, []byte("'yes'")) {
		t.Errorf("reserved word 'yes' was not explicitly quoted:\n%s", data)
	}
}

// ---------------------------------------------------------------------------
// Provenance, canonical URL, defaults, required fields, date conventions
// ---------------------------------------------------------------------------

func TestProvenanceBlock(t *testing.T) {
	root := newHugoTree(t)
	cfg := testConfig(root)
	cfg.IncludeProvenance = true
	p, err := hugo.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := testArticle()
	a.RunID = "run-feed-20260711T100000Z-abcdef012345"
	a.Digest = "sha256:" + strings.Repeat("ab", 32)
	res, err := p.Stage(a, hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fm := decodeFrontMatter(t, readStaged(t, res.Path))
	prov, ok := fm["newsroom"].(map[string]any)
	if !ok {
		t.Fatalf("newsroom block missing or not a mapping: %v", fm["newsroom"])
	}
	if prov["run"] != a.RunID || prov["digest"] != a.Digest {
		t.Errorf("provenance = %v", prov)
	}

	// Disabled by default: no block at all.
	p2, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	a2 := testArticle()
	a2.Title = "Article without provenance"
	a2.RunID = a.RunID
	res2, err := p2.Stage(a2, hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fm2 := decodeFrontMatter(t, readStaged(t, res2.Path))
	if _, has := fm2["newsroom"]; has {
		t.Error("newsroom block present although IncludeProvenance is false")
	}
}

func TestCanonicalAndSource(t *testing.T) {
	root := newHugoTree(t)
	cfg := testConfig(root)
	cfg.BaseURL = "https://mundodolphins.es"
	p, err := hugo.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := testArticle()
	a.Tags = nil // trim the fixture to the essentials
	res, err := p.Stage(a, hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	fm := decodeFrontMatter(t, readStaged(t, res.Path))
	if got, want := fm["canonical"], "https://mundodolphins.es/news/20260711-los-dolphins-ganan-el-super-bowl-increíble.md"; got != want {
		t.Errorf("canonical = %v, want %v", got, want)
	}
	if fm["source"] != a.SourceURL {
		t.Errorf("source = %v", fm["source"])
	}
}

func TestDefaultsAndRequiredFields(t *testing.T) {
	root := newHugoTree(t)
	cfg := testConfig(root)
	cfg.Author = "" // force the default to apply
	cfg.Defaults = map[string]string{
		"layout": "news-detail",
		"geo":    "Miami, FL",
		"author": "Fallback Author",
	}
	cfg.Required = []string{"author"}
	p, err := hugo.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Article with no author: the default satisfies the required field.
	a := testArticle()
	a.Author = ""
	res, err := p.Stage(a, hugo.Options{})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	fm := decodeFrontMatter(t, readStaged(t, res.Path))
	if fm["author"] != "Fallback Author" {
		t.Errorf("author = %v, want the default", fm["author"])
	}
	if fm["layout"] != "news-detail" || fm["geo"] != "Miami, FL" {
		t.Errorf("defaults not applied: layout=%v geo=%v", fm["layout"], fm["geo"])
	}

	// Article WITH an author: it wins over the same-named default.
	b := testArticle()
	b.Author = "Real Author"
	res2, err := p.Stage(b, hugo.Options{Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	fm2 := decodeFrontMatter(t, readStaged(t, res2.Path))
	if fm2["author"] != "Real Author" {
		t.Errorf("explicit author lost to the default: %v", fm2["author"])
	}

	// A required field no source supplies must fail at render time.
	cfg2 := testConfig(root)
	cfg2.Required = []string{"author"}
	cfg2.Author = ""
	p2, err := hugo.New(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	c := testArticle()
	c.Author = ""
	c.Title = "Article missing its required author"
	if _, stageErr := p2.Stage(c, hugo.Options{}); stageErr == nil {
		t.Fatal("Stage accepted an article missing a required field")
	} else if !errors.Is(stageErr, hugo.ErrInvalidArticle) {
		t.Errorf("error = %v, want ErrInvalidArticle", stageErr)
	}
}

func TestDateLayoutConventions(t *testing.T) {
	root := newHugoTree(t)
	cfg := testConfig(root)
	cfg.DateLayout = "20060102"
	cfg.SectionTemplate = `news/{{.Date.Format "2006/01"}}`
	cfg.FilenamePattern = `{{.Slug}}.md`
	p, err := hugo.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := testArticle()
	a.Title = "Date layout article"
	res, err := p.Stage(a, hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "content", "news", "2026/07", "date-layout-article.md")
	if res.Path != want {
		t.Errorf("path = %q, want %q", res.Path, want)
	}
	fm := decodeFrontMatter(t, readStaged(t, res.Path))
	if fm["date"] != "20260711" {
		t.Errorf("date = %v, want 20260711 (custom layout)", fm["date"])
	}
}

// ---------------------------------------------------------------------------
// Content hash and preview
// ---------------------------------------------------------------------------

func TestContentHashAndPreview(t *testing.T) {
	root := newHugoTree(t)
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	a := testArticle()
	a.Body = strings.Repeat("Una palabra larga y repetitiva. ", 300) // > 4 KiB

	res, err := p.Stage(a, hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(res.Preview), hugo.PreviewBytes; got > want {
		t.Errorf("preview length = %d, want <= %d", got, want)
	}
	if !strings.HasPrefix(string(res.Content), res.Preview) {
		t.Error("preview is not a byte prefix of the content")
	}
	// The preview must cut on a rune boundary: it must be valid UTF-8.
	if !isPrintableUTF8(res.Preview) {
		t.Error("preview cuts a rune (invalid UTF-8)")
	}

	data := readStaged(t, res.Path)
	if hugo.DigestOf(data) != res.Digest {
		t.Error("digest does not match the on-disk bytes")
	}
	// Deterministic digest: the same input always hashes the same.
	res2, err := p.Render(a)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Digest != res.Digest {
		t.Errorf("render digest %q differs from staged digest %q", res2.Digest, res.Digest)
	}
}

// isPrintableUTF8 reports whether s is well-formed UTF-8.
func isPrintableUTF8(s string) bool {
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c < 0x80:
			i++
		case c&0xE0 == 0xC0 && i+1 < len(s):
			i += 2
		case c&0xF0 == 0xE0 && i+2 < len(s):
			i += 3
		case c&0xF8 == 0xF0 && i+3 < len(s):
			i += 4
		default:
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Config validation
// ---------------------------------------------------------------------------

func TestConfigValidation(t *testing.T) {
	root := newHugoTree(t)

	cases := []struct {
		name    string
		cfg     hugo.Config
		wantErr error
	}{
		{"empty root", hugo.Config{Root: "   "}, hugo.ErrInvalidConfig},
		{"missing root", hugo.Config{Root: filepath.Join(root, "nope")}, hugo.ErrRoot},
	}
	for _, tc := range cases {
		if _, err := hugo.New(tc.cfg); err == nil {
			t.Errorf("%s: accepted", tc.name)
		} else if !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.wantErr)
		}
	}

	// File, not directory, as root.
	fileRoot := filepath.Join(t.TempDir(), "hugo.yaml")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hugo.New(hugo.Config{Root: fileRoot}); !errors.Is(err, hugo.ErrRoot) {
		t.Errorf("file root: error = %v, want ErrRoot", err)
	}

	// Traversing content dir.
	cfg := testConfig(root)
	cfg.ContentDir = "../outside"
	if _, err := hugo.New(cfg); !errors.Is(err, hugo.ErrInvalidConfig) {
		t.Errorf("traversing content dir: error = %v, want ErrInvalidConfig", err)
	}

	// Filename pattern without .md.
	cfg = testConfig(root)
	cfg.FilenamePattern = "{{.Slug}}.txt"
	if _, err := hugo.New(cfg); !errors.Is(err, hugo.ErrInvalidConfig) {
		t.Errorf("non-md filename: error = %v, want ErrInvalidConfig", err)
	}

	// Unsupported front matter format.
	cfg = testConfig(root)
	cfg.FrontMatterFormat = hugo.FormatTOML
	if _, err := hugo.New(cfg); !errors.Is(err, hugo.ErrUnsupportedFormat) {
		t.Errorf("toml: error = %v, want ErrUnsupportedFormat", err)
	}
	cfg.FrontMatterFormat = "weird"
	if _, err := hugo.New(cfg); !errors.Is(err, hugo.ErrInvalidConfig) {
		t.Errorf("weird format: error = %v, want ErrInvalidConfig", err)
	}

	// Invalid date layout.
	cfg = testConfig(root)
	cfg.DateLayout = "ZZZ"
	if _, err := hugo.New(cfg); !errors.Is(err, hugo.ErrInvalidConfig) {
		t.Errorf("bad date layout: error = %v, want ErrInvalidConfig", err)
	}

	// Malformed template.
	cfg = testConfig(root)
	cfg.SectionTemplate = "{{.Slug"
	if _, err := hugo.New(cfg); !errors.Is(err, hugo.ErrInvalidConfig) {
		t.Errorf("malformed template: error = %v, want ErrInvalidConfig", err)
	}

	// Bad base URL.
	cfg = testConfig(root)
	cfg.BaseURL = "not-a-url"
	if _, err := hugo.New(cfg); !errors.Is(err, hugo.ErrInvalidConfig) {
		t.Errorf("bad base url: error = %v, want ErrInvalidConfig", err)
	}

	// A secret in the config.
	cfg = testConfig(root)
	cfg.Author = "auth_token: aB3dE7fH1jK4mN6pQ9rS2tU5vW8xY0zA1bC3dE6f"
	if _, err := hugo.New(cfg); !errors.Is(err, hugo.ErrSecret) {
		t.Errorf("secret author: error = %v, want ErrSecret", err)
	}

	// Invalid default key.
	cfg = testConfig(root)
	cfg.Defaults = map[string]string{"bad key!": "v"}
	if _, err := hugo.New(cfg); !errors.Is(err, hugo.ErrInvalidConfig) {
		t.Errorf("bad default key: error = %v, want ErrInvalidConfig", err)
	}

	// A secret in a default value.
	cfg = testConfig(root)
	cfg.Defaults = map[string]string{"geo": "omlx_api_key: aB3dE7fH1jK4mN6pQ9rS2tU5vW8xY0zA1bC3dE6f"}
	if _, err := hugo.New(cfg); !errors.Is(err, hugo.ErrSecret) {
		t.Errorf("secret default: error = %v, want ErrSecret", err)
	}
}

// ---------------------------------------------------------------------------
// Article validation
// ---------------------------------------------------------------------------

func TestArticleValidation(t *testing.T) {
	root := newHugoTree(t)
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}

	a := testArticle()
	a.Title = ""
	if _, err := p.Render(a); !errors.Is(err, hugo.ErrInvalidArticle) {
		t.Errorf("empty title: error = %v", err)
	}

	a = testArticle()
	a.Body = "   "
	if _, err := p.Render(a); !errors.Is(err, hugo.ErrInvalidArticle) {
		t.Errorf("empty body: error = %v", err)
	}

	a = testArticle()
	a.Date = time.Time{}
	if _, err := p.Render(a); !errors.Is(err, hugo.ErrInvalidArticle) {
		t.Errorf("zero date: error = %v", err)
	}

	// A secret in the title: refused fail-closed.
	a = testArticle()
	a.Title = "Result with token aB3dE7fH1jK4mN6pQ9rS2tU5vW8xY0zA1bC3dE6f inside"
	_, err = p.Render(a)
	if !errors.Is(err, hugo.ErrSecret) {
		t.Errorf("secret title: error = %v, want ErrSecret", err)
	}
	if err != nil && strings.Contains(err.Error(), "aB3dE7fH1jK4mN6pQ9rS2tU5vW8xY0zA1bC3dE6f") {
		t.Error("error message leaks the raw secret")
	}

	// A secret in a tag: refused.
	a = testArticle()
	a.Tags = []string{"ok", "Bearer aB3dE7fH1jK4mN6pQ9rS2tU5vW8xY0zA1bC3dE6f"}
	if _, err := p.Render(a); !errors.Is(err, hugo.ErrSecret) {
		t.Errorf("secret tag: error = %v, want ErrSecret", err)
	}

	// Malformed provenance references.
	a = testArticle()
	a.RunID = "not a run id!!"
	if _, err := p.Render(a); !errors.Is(err, hugo.ErrInvalidArticle) {
		t.Errorf("bad run id: error = %v", err)
	}
	a = testArticle()
	a.Digest = "sha1:abc"
	if _, err := p.Render(a); !errors.Is(err, hugo.ErrInvalidArticle) {
		t.Errorf("bad digest: error = %v", err)
	}

	// Non-URL source URL.
	a = testArticle()
	a.SourceURL = "ftp://nope"
	if _, err := p.Render(a); !errors.Is(err, hugo.ErrInvalidArticle) {
		t.Errorf("bad source url: error = %v", err)
	}

	// Bounded lists.
	a = testArticle()
	for i := 0; i < 40; i++ {
		a.Tags = append(a.Tags, "tag")
	}
	if _, err := p.Render(a); !errors.Is(err, hugo.ErrInvalidArticle) {
		t.Errorf("too many tags: error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Atomic write failure
// ---------------------------------------------------------------------------

func TestAtomicWriteRefusesDirectoryAtTarget(t *testing.T) {
	root := newHugoTree(t)
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	// The exact target path exists as a directory (crash residue).
	a := testArticle()
	a.Title = "Article hitting a directory"
	// Compute the real target via a dry run, then corrupt it.
	res, err := p.DryRun(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(res.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(res.Path, "inner"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stage(a, hugo.Options{}); err == nil {
		t.Fatal("Stage with a directory at the target: accepted")
	} else if !errors.Is(err, hugo.ErrExists) {
		t.Errorf("error = %v, want ErrExists", err)
	}
	if _, err := os.ReadFile(filepath.Join(res.Path, "inner")); err != nil {
		t.Errorf("the pre-existing directory was disturbed: %v", err)
	}
}

func TestAtomicWriteFailureLeavesNoTrace(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: read-only permissions do not apply")
	}
	root := newHugoTree(t)
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}

	first, err := p.Stage(testArticle(), hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	original := readStaged(t, first.Path)
	sectionDir := filepath.Dir(first.Path)

	// A read-only section directory makes the temp-file step fail before
	// anything touches the existing file.
	if err := os.Chmod(sectionDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sectionDir, 0o700) })

	changed := testArticle()
	changed.Body += "Another sentence.\n"
	if _, err := p.Stage(changed, hugo.Options{Overwrite: true}); err == nil {
		t.Fatal("Stage into a read-only directory: accepted, want error")
	}
	if after := readStaged(t, first.Path); !bytes.Equal(after, original) {
		t.Error("the existing file was disturbed by a failed write")
	}
	noTmpLeftovers(t, sectionDir)
}

func TestStageCreatesMissingContentDirs(t *testing.T) {
	root := t.TempDir() // no content/ at all
	if err := os.WriteFile(filepath.Join(root, "hugo.yaml"), []byte("baseURL: https://example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := hugo.New(testConfig(root))
	if err != nil {
		t.Fatal(err)
	}
	a := testArticle()
	a.Title = "Deep section"
	res, err := p.Stage(a, hugo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(res.Path)
	if err != nil || info.IsDir() {
		t.Fatalf("staged file missing: %v", err)
	}
	// The created chain uses safe permissions.
	contentDir := filepath.Join(root, "content")
	fi, err := os.Stat(contentDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o700 {
		t.Errorf("created content dir has mode %v, want 0700", got)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("created file has mode %v, want 0600", got)
	}
}
