package hugo

// Unit tests for the unexported path / serialization seams. These live in
// the internal test package (hugo, not hugo_test) so they can exercise the
// helpers that the public API does not expose directly.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestValidateRelPath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"news", "news", false},
		{"news/2026/07", "news/2026/07", false},
		{"a/../b", "b", false}, // cleans to a safe relative path
		{"a//b", "a/b", false},
		{".", "", true},
		{"", "", true},
		{"   ", "", true},
		{"/absolute", "", true},
		{"../outside", "", true},
		{"news/../../outside", "", true},
		{"a\\b", "", true},
		{"a\x00b", "", true},
		{"a/b/../c/.", "a/c", false}, // no dot segments after cleaning
	}
	for _, tc := range cases {
		got, err := validateRelPath(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateRelPath(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if err == nil && got != tc.want {
			t.Errorf("validateRelPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAssertInsideRoot pins the symlink-resolved containment proof.
func TestAssertInsideRoot(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := assertInsideRoot(root, inside); err != nil {
		t.Errorf("inside path rejected: %v", err)
	}
	if err := assertInsideRoot(root, root); err != nil {
		t.Errorf("the root itself is allowed: %v", err)
	}
	outside := t.TempDir()
	if err := assertInsideRoot(root, outside); err == nil {
		t.Error("outside path accepted")
	}

	// A symlink pointing outside must be refused.
	link := filepath.Join(root, "evil-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := assertInsideRoot(root, filepath.Join(link, "x.md")); !errors.Is(err, ErrPathEscape) {
		t.Errorf("path through an escaping symlink: err = %v, want ErrPathEscape", err)
	}
}

func errorsIs(err, target error) bool { return errors.Is(err, target) }

// TestResolveSymlinksDeepCovers the not-yet-existing suffix case.
func TestResolveSymlinksDeep(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "content")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// The path extends beyond the existing tree.
	got, err := resolveSymlinks(filepath.Join(dir, "news", "2026", "x.md"))
	if err != nil {
		t.Fatalf("resolveSymlinks: %v", err)
	}
	// The resolved form of the existing prefix (t.TempDir lives behind a
	// symlink on macOS: /var -> /private/var).
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, resolvedDir) || !strings.HasSuffix(got, "news/2026/x.md") {
		t.Errorf("resolved %q", got)
	}
}

// TestEnsureDirBranches covers creation, pre-existing directories (never
// chmodded), and a file masquerading as a directory.
func TestEnsureDirBranches(t *testing.T) {
	root := t.TempDir()

	// Nested creation: the new chain gets dirPerm.
	deep := filepath.Join(root, "content", "news")
	if err := ensureDir(root, deep); err != nil {
		t.Fatalf("ensureDir: %v", err)
	}
	for _, d := range []string{filepath.Join(root, "content"), deep} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != dirPerm {
			t.Errorf("%s has mode %v, want %v", d, got, dirPerm)
		}
	}

	// A pre-existing directory with other permissions is left alone.
	pre := filepath.Join(root, "content", "old")
	if err := os.Mkdir(pre, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureDir(root, filepath.Join(pre, "new")); err != nil {
		t.Fatalf("ensureDir below pre-existing: %v", err)
	}
	fi, _ := os.Stat(pre)
	if got := fi.Mode().Perm(); got != 0o755 {
		t.Errorf("pre-existing directory was chmodded: %v", got)
	}

	// A file where a directory is expected: refused.
	file := filepath.Join(root, "content", "file.md")
	if err := os.WriteFile(file, []byte("x"), filePerm); err != nil {
		t.Fatal(err)
	}
	if err := ensureDir(root, filepath.Join(file, "x")); err == nil {
		t.Error("ensureDir below a file: accepted, want error")
	}
	// The file survives untouched.
	if b, _ := os.ReadFile(file); string(b) != "x" {
		t.Error("the pre-existing file was disturbed")
	}
}

// TestAtomicWriteSeams covers the write seam directly: success, replacement
// of an existing file, a directory at the destination, and a read-only
// directory.
func TestAtomicWriteSeams(t *testing.T) {
	dir := t.TempDir()

	// Success.
	if err := atomicWrite(dir, "a.md", []byte("hello"), filePerm); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != filePerm {
		t.Errorf("mode = %v, want %v", got, filePerm)
	}

	// Replacement of an existing file.
	if err := atomicWrite(dir, "a.md", []byte("replaced"), filePerm); err != nil {
		t.Fatalf("atomicWrite replacement: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.md")); string(b) != "replaced" {
		t.Errorf("content = %q", b)
	}

	// A directory at the destination: the rename must fail, the original
	// content preserved.
	keep := []byte("keep me")
	if err := atomicWrite(dir, "a.md", keep, filePerm); err != nil {
		t.Fatalf("atomicWrite to existing file: %v", err)
	}
	sub := filepath.Join(dir, "a.md")
	// (a.md is a file now; remove it and re-create it as a directory)
	_ = os.Remove(sub)
	if err := os.MkdirAll(sub, dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "inner"), []byte("x"), filePerm); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(dir, "a.md", []byte("nope"), filePerm); err == nil {
		t.Error("atomicWrite over a directory: accepted, want error")
	}
	if b, _ := os.ReadFile(filepath.Join(sub, "inner")); string(b) != "x" {
		t.Error("the pre-existing directory was disturbed")
	}

	// Read-only directory: the temp file creation must fail.
	if os.Geteuid() == 0 {
		t.Skip("running as root: read-only permissions do not apply")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := atomicWrite(dir, "b.md", []byte("x"), filePerm); err == nil {
		t.Error("atomicWrite into a read-only directory: accepted, want error")
	}
	// No temp residue.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp residue %s", e.Name())
		}
	}
}

// TestMarshalFrontMatterDispatch pins the format switch of the unexported
// seam: YAML and its empty alias render identically; TOML/JSON and unknown
// formats fail with their own error classes.
func TestMarshalFrontMatterDispatch(t *testing.T) {
	fields := []fmField{
		{key: "title", node: yamlStringNode("Hi: there")},
		{key: "draft", node: yamlBoolNode(true)},
	}
	yamlOut, err := marshalFrontMatter(FormatYAML, fields)
	if err != nil {
		t.Fatalf("yaml: %v", err)
	}
	aliasOut, err := marshalFrontMatter("", fields)
	if err != nil {
		t.Fatalf("empty alias: %v", err)
	}
	if !bytes.Equal(yamlOut, aliasOut) {
		t.Error("the empty format alias must behave exactly like YAML")
	}
	if !bytes.Contains(yamlOut, []byte("title:")) || !bytes.Contains(yamlOut, []byte("draft: true")) {
		t.Errorf("unexpected bytes: %s", yamlOut)
	}
	if _, err := marshalFrontMatter(FormatTOML, fields); err == nil || !errorsIs(err, ErrUnsupportedFormat) {
		t.Errorf("toml: err = %v, want ErrUnsupportedFormat", err)
	}
	if _, err := marshalFrontMatter(FormatJSON, fields); err == nil || !errorsIs(err, ErrUnsupportedFormat) {
		t.Errorf("json: err = %v, want ErrUnsupportedFormat", err)
	}
	if _, err := marshalFrontMatter("weird", fields); err == nil || !errorsIs(err, ErrInvalidConfig) {
		t.Errorf("weird: err = %v, want ErrInvalidConfig", err)
	}
}

// TestMarshalDeterministic: identical fields always produce identical bytes.
func TestMarshalDeterministic(t *testing.T) {
	build := func() []fmField {
		return []fmField{
			{key: "title", node: yamlStringNode("Los Dolphins ganan")},
			{key: "tags", node: yamlStringListNode([]string{"b", "a"})},
			{key: "newsroom", node: yamlMappingNode([][2]string{{"z", "1"}, {"a", "2"}})},
		}
	}
	a, err := marshalFrontMatter(FormatYAML, build())
	if err != nil {
		t.Fatal(err)
	}
	b, err := marshalFrontMatter(FormatYAML, build())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("marshal is not deterministic")
	}
	// The provenance mapping sorts its keys; reserved values ("2", "1") are
	// quoted by the serializer. The canonical list keeps preset order.
	if !bytes.Contains(a, []byte("a: \"2\"")) || !bytes.Contains(a, []byte("z: \"1\"")) {
		t.Errorf("mapping keys not emitted sorted/quoted:\n%s", a)
	}
	if strings.Index(string(a), "b") < strings.Index(string(a), "a") {
		t.Error("list entries were reordered (they must keep preset order)")
	}
}

// TestYamlStringNodeReservedQuoting pins the YAML 1.1 reserved-word quoting
// at the node level.
func TestYamlStringNodeReservedQuoting(t *testing.T) {
	for _, v := range []string{"yes", "No", "ON", "off", "Y", "n", "true", "null", "~"} {
		n := yamlStringNode(v)
		if n.Style != yaml.SingleQuotedStyle {
			t.Errorf("%q: style = %v, want single-quoted", v, n.Style)
		}
	}
	// Ordinary strings must not be forced-quoted by the reserved rule (the
	// emitter still quotes what needs it; nothing here does).
	for _, v := range []string{"hello", "2026-07-11: a date-ish string with a colon", "a b"} {
		n := yamlStringNode(v)
		if n.Style != 0 {
			t.Errorf("%q: style = %v, want plain (0)", v, n.Style)
		}
	}
}
