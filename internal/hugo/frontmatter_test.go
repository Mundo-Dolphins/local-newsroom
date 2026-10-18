package hugo_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/hugo"
)

// TestDigestOfKnownVector pins the canonical digest form against the
// well-known SHA-256 of the empty string.
func TestDigestOfKnownVector(t *testing.T) {
	want := "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := hugo.DigestOf(nil); got != want {
		t.Errorf("DigestOf(nil) = %q, want %q", got, want)
	}
	// Deterministic: same bytes, same digest; different bytes, different.
	if hugo.DigestOf([]byte("uno")) == hugo.DigestOf([]byte("dos")) {
		t.Error("different content produced equal digests")
	}
	if got, again := hugo.DigestOf([]byte("uno")), hugo.DigestOf([]byte("uno")); got != again {
		t.Errorf("same content produced different digests: %q vs %q", got, again)
	}
}

// TestPreviewBounds checks the preview cut: no-op when short, rune-safe when
// long.
func TestPreviewBounds(t *testing.T) {
	short := "hola"
	if got := hugo.Preview([]byte(short), hugo.PreviewBytes); got != short {
		t.Errorf("short preview altered: %q", got)
	}

	// A long run of a 3-byte rune: the 4096-byte cut lands mid-rune (4096 %
	// 3 == 1) and must walk back to a boundary.
	long := strings.Repeat("海", 2000) // 6000 bytes
	got := hugo.Preview([]byte(long), hugo.PreviewBytes)
	if len(got) > hugo.PreviewBytes {
		t.Errorf("preview length %d exceeds the bound", len(got))
	}
	if len(got)%3 != 0 {
		t.Errorf("preview of %d bytes splits a 3-byte rune", len(got))
	}
	if len(got) == len(long) {
		t.Error("the cut did not happen")
	}
	if !strings.HasPrefix(long, got) {
		t.Error("preview is not a prefix of the content")
	}
}

// TestExistsErrorFormatting covers ExistsError.Error(): the message must carry
// the path and both shortened digests, without leaking file contents.
func TestExistsErrorFormatting(t *testing.T) {
	old := hugo.DigestOf([]byte("old content"))
	new_ := hugo.DigestOf([]byte("new content"))
	err := &hugo.ExistsError{Path: "/tmp/site/content/news/x.md", ExistingDigest: old, NewDigest: new_}
	if !errors.Is(err, hugo.ErrExists) {
		t.Fatal("ExistsError does not wrap ErrExists")
	}
	msg := err.Error()
	if !strings.Contains(msg, "/tmp/site/content/news/x.md") {
		t.Errorf("message lacks the path: %q", msg)
	}
	if !strings.Contains(msg, old[:13]) || !strings.Contains(msg, new_[:13]) {
		t.Errorf("message lacks the shortened digests: %q", msg)
	}
	if strings.Contains(msg, "old content") || strings.Contains(msg, "new content") {
		t.Error("message leaks file contents")
	}
}

// TestPublisherAccessors pins the convenience accessors and the applied
// config defaults.
func TestPublisherAccessors(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "content"), 0o700); err != nil {
		t.Fatal(err)
	}
	p, err := hugo.New(hugo.Config{
		Root:            root,
		ContentDir:      "content",
		SectionTemplate: "news",
		FilenamePattern: `{{.Date.Format "20060102"}}-{{.Slug}}.md`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Root() != root {
		t.Errorf("Root() = %q, want %q", p.Root(), root)
	}
	if p.ContentRoot() == "" || !strings.HasPrefix(p.ContentRoot(), root) {
		t.Errorf("ContentRoot() = %q", p.ContentRoot())
	}
	cfg := p.Config()
	if cfg.ContentDir != "content" || cfg.FrontMatterFormat != hugo.FormatYAML ||
		cfg.DateLayout != "2006-01-02" || cfg.FilenamePattern != `{{.Date.Format "20060102"}}-{{.Slug}}.md` {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	resolvedDraft := cfg.Draft == nil || *cfg.Draft
	if !resolvedDraft {
		t.Error("the engine must default to draft=true")
	}
}
