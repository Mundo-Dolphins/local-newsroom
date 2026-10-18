package editorial_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/editorial"
)

// writeFile creates dir (and parents) and writes path with content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// fullBundle writes a complete editorial bundle:
//
//	root/
//	  common/style.md
//	  authors/javi/style.md
//	  publications/mundodolphins/publication.yaml (+ hugo.yaml, bluesky.yaml)
//
// and returns its root. The content is deliberately recognizable (the
// "PRIVATE STYLE TEXT" / handle / URL strings) so tests can assert that
// resolved values round-trip and that no content leaks into records.
func fullBundle(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "common", "style.md"), "## Style\nPRIVATE STYLE TEXT\n")
	writeFile(t, filepath.Join(root, "common", "facts-policy.md"), "Verify all claims.\n")
	writeFile(t, filepath.Join(root, "authors", "javi", "style.md"), "Author voice.\n")
	writeFile(t, filepath.Join(root, "publications", "mundodolphins", "publication.yaml"),
		"profile: javi\nlanguage: es\nrenderer: thread\nhugo: hugo.yaml\nbluesky: bluesky.yaml\n")
	writeFile(t, filepath.Join(root, "publications", "mundodolphins", "hugo.yaml"),
		"destination: sites/mundodolphins\nbase_url: https://www.mundodolphins.example\nlayout: article\ndrafts: true\n")
	writeFile(t, filepath.Join(root, "publications", "mundodolphins", "bluesky.yaml"),
		// "@" is a reserved YAML indicator and must be quoted.
		"handle: \"@mundodolphins\"\napi_endpoint: https://public.api.bsky.example\napi_key:\n  env: BLUESKY_API_KEY\n")
	return root
}

// TestNewLoaderRootValidation covers the bundle root rules: a missing root,
// a non-directory, a nonexistent path, a directory, and ".." traversal.
func TestNewLoaderRootValidation(t *testing.T) {
	dir := t.TempDir()

	if _, err := editorial.NewLoader(editorial.Config{}); !errors.Is(err, editorial.ErrRootEmpty) {
		t.Errorf("empty root: got %v, want ErrRootEmpty", err)
	}

	file := filepath.Join(dir, "a-file.txt")
	writeFile(t, file, "x")
	if _, err := editorial.NewLoader(editorial.Config{Root: file}); !errors.Is(err, editorial.ErrRootNotDirectory) {
		t.Errorf("file root: got %v, want ErrRootNotDirectory", err)
	}

	missing := filepath.Join(dir, "nope")
	if _, err := editorial.NewLoader(editorial.Config{Root: missing}); !errors.Is(err, editorial.ErrRootNotExists) {
		t.Errorf("missing root: got %v, want ErrRootNotExists", err)
	}

	// A relative path with a ".." segment is traversal and must be rejected.
	if _, err := editorial.NewLoader(editorial.Config{Root: filepath.Join("..", "..", "etc")}); !errors.Is(err, editorial.ErrRootTraversal) {
		t.Errorf("traversal root: got %v, want ErrRootTraversal", err)
	}

	l, err := editorial.NewLoader(editorial.Config{Root: dir})
	if err != nil {
		t.Fatalf("valid directory root: %v", err)
	}
	if got, want := l.Root(), filepath.Clean(dir); got != want {
		t.Errorf("Root() = %q, want %q", got, want)
	}
}

// TestLoadEmptyBundle: a bundle without publications/ is a valid, empty
// bundle (missing optional layout).
func TestLoadEmptyBundle(t *testing.T) {
	root := t.TempDir()
	l, err := editorial.NewLoader(editorial.Config{Root: root})
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	b, err := l.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Names) != 0 || len(b.Presets) != 0 {
		t.Errorf("empty bundle: names=%v presets=%v", b.Names, b.Presets)
	}
	if b.Hash == "" {
		t.Error("bundle hash must be set (a digest over the zero presets)")
	}

	names, err := l.Publications()
	if err != nil || names != nil {
		t.Errorf("Publications = %v, %v; want nil, nil", names, err)
	}
}

// TestLoadPresetFullPreset resolves every preset field, including both
// destination configs.
func TestLoadPresetFullPreset(t *testing.T) {
	root := fullBundle(t)
	l, err := editorial.NewLoader(editorial.Config{Root: root})
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	p, err := l.LoadPreset("mundodolphins")
	if err != nil {
		t.Fatalf("LoadPreset: %v", err)
	}
	if p.Name != "mundodolphins" {
		t.Errorf("Name = %q", p.Name)
	}
	if p.Profile != "javi" {
		t.Errorf("Profile = %q, want javi", p.Profile)
	}
	if p.Language != "es" {
		t.Errorf("Language = %q, want es", p.Language)
	}
	if p.Renderer != "thread" {
		t.Errorf("Renderer = %q, want thread", p.Renderer)
	}
	if p.Hugo == nil {
		t.Fatal("Hugo is nil")
	}
	if p.Hugo.Destination != "sites/mundodolphins" || p.Hugo.BaseURL != "https://www.mundodolphins.example" || p.Hugo.Layout != "article" || !p.Hugo.Drafts {
		t.Errorf("Hugo = %+v", p.Hugo)
	}
	wantDest := filepath.Join(root, "sites", "mundodolphins")
	if got := p.Hugo.DestinationPath(l.Root()); got != wantDest {
		t.Errorf("Hugo.DestinationPath = %q, want %q", got, wantDest)
	}
	if p.Bluesky == nil {
		t.Fatal("Bluesky is nil")
	}
	if p.Bluesky.Handle != "@mundodolphins" || p.Bluesky.APIEndpoint != "https://public.api.bsky.example" {
		t.Errorf("Bluesky = %+v", p.Bluesky)
	}
	if p.Bluesky.APIKey.Env != "BLUESKY_API_KEY" {
		t.Errorf("Bluesky.APIKey.Env = %q, want BLUESKY_API_KEY", p.Bluesky.APIKey.Env)
	}
	if len(p.Files) != 3 {
		t.Errorf("Files = %v, want 3 entries", p.Files)
	}
}

// TestLoadPresetMissingOptionalFiles: a preset without destination
// references, and a bundle whose layout lacks the optional profile
// directories, all load fine.
func TestLoadPresetMissingOptionalFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "publications", "plain", "publication.yaml"),
		"language: es\n")
	l, err := editorial.NewLoader(editorial.Config{Root: root})
	if err != nil {
		t.Fatalf("NewLoader: %v", err)
	}
	p, err := l.LoadPreset("plain")
	if err != nil {
		t.Fatalf("LoadPreset: %v", err)
	}
	if p.Language != "es" || p.Hugo != nil || p.Bluesky != nil || p.Profile != "" {
		t.Errorf("preset = %+v (hugo/bluesky/profile must be absent)", p)
	}

	// An unreferenced destination file may exist or not; either way the
	// preset loads.
	writeFile(t, filepath.Join(root, "publications", "plain", "hugo.yaml"), "destination: x\n")
	p, err = l.LoadPreset("plain")
	if err != nil {
		t.Fatalf("LoadPreset with unreferenced file: %v", err)
	}
	if p.Hugo != nil {
		t.Errorf("unreferenced hugo.yaml must not be attached: %+v", p.Hugo)
	}
}

// TestLoadPresetDanglingReference: a preset that references a missing
// destination config file is a misconfiguration and must fail.
func TestLoadPresetDanglingReference(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "publications", "broken", "publication.yaml"),
		"hugo: hugo.yaml\n")
	l, _ := editorial.NewLoader(editorial.Config{Root: root})
	_, err := l.LoadPreset("broken")
	if err == nil {
		t.Fatal("a referenced-but-missing destination file must fail")
	}
	if !errors.Is(err, editorial.ErrFileNotFound) {
		t.Errorf("error = %v, want ErrFileNotFound", err)
	}
}

// TestLoadBundleListsAllPresets: Load reads every preset in sorted order and
// bundles a hash over them.
func TestLoadBundleListsAllPresets(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "publications", "zeta", "publication.yaml"), "language: de\n")
	writeFile(t, filepath.Join(root, "publications", "alpha", "publication.yaml"), "language: en\n")
	writeFile(t, filepath.Join(root, "publications", "dotfile"), "language: xx\n") // ignored: not a directory
	writeFile(t, filepath.Join(root, "publications", ".hidden", "publication.yaml"), "language: xx\n")

	l, _ := editorial.NewLoader(editorial.Config{Root: root})
	b, err := l.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(b.Names) != 2 || b.Names[0] != "alpha" || b.Names[1] != "zeta" {
		t.Fatalf("Names = %v, want [alpha zeta]", b.Names)
	}
	if b.Presets["alpha"].Language != "en" || b.Presets["zeta"].Language != "de" {
		t.Errorf("presets = %+v %+v", b.Presets["alpha"], b.Presets["zeta"])
	}
	if b.Hash == "" {
		t.Error("bundle hash must be set")
	}
	if b.Root != l.Root() {
		t.Errorf("bundle root = %q, want %q", b.Root, l.Root())
	}
}

// TestLoadBundleRejectsInvalidPresetName: a preset directory whose name is
// not a conservative identifier fails the whole load.
func TestLoadBundleRejectsInvalidPresetName(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "publications", "bad name"), 0o755); err != nil {
		t.Fatal(err)
	}
	l, _ := editorial.NewLoader(editorial.Config{Root: root})
	if _, err := l.Load(context.Background()); !errors.Is(err, editorial.ErrInvalidName) {
		t.Errorf("Load = %v, want ErrInvalidName", err)
	}
}

// TestLoadPresetUnknownKeys: unknown YAML keys (typos, stale fields) are
// rejected.
func TestLoadPresetUnknownKeys(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "publications", "p", "publication.yaml"),
		"language: es\nbogus_key: 1\n")
	l, _ := editorial.NewLoader(editorial.Config{Root: root})
	_, err := l.LoadPreset("p")
	if err == nil {
		t.Fatal("unknown key must be rejected")
	}
	if !strings.Contains(err.Error(), "bogus_key") {
		t.Errorf("error should name the key: %v", err)
	}
}

// TestLoadPresetBadSchemaVersion.
func TestLoadPresetBadSchemaVersion(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "publications", "p", "publication.yaml"),
		"schema_version: 99\nlanguage: es\n")
	l, _ := editorial.NewLoader(editorial.Config{Root: root})
	if _, err := l.LoadPreset("p"); !errors.Is(err, editorial.ErrInvalidYAML) {
		t.Errorf("unsupported schema_version: %v, want ErrInvalidYAML", err)
	}
}

// TestLoadPresetInvalidValues: out-of-range language, renderer, and profile
// spec values are rejected.
func TestLoadPresetInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"bad language", "language: espanol-foo-9999\n"},
		{"explicitly empty language", "language: \"\"\n"},
		{"bad renderer", "renderer: podcast\n"},
		{"traversal profile spec", "profile: ../etc\n"},
		{"dotdot profile spec", "profile: \"..\"\n"},
		{"three-segment profile spec", "profile: a/b/c\n"},
		{"oversized profile spec", "profile: " + strings.Repeat("a", 101) + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "publications", "p", "publication.yaml"), tc.body)
			l, _ := editorial.NewLoader(editorial.Config{Root: dir})
			if _, err := l.LoadPreset("p"); err == nil {
				t.Fatalf("%s: must be rejected", tc.name)
			}
		})
	}
}

// TestLoadPresetLiteralSecret: a literal credential in any bundle YAML file
// is a load-time error, and the error never echoes the secret.
func TestLoadPresetLiteralSecret(t *testing.T) {
	secret := "supersecretblueskytoken"
	cases := []struct {
		name   string
		file   string
		body   string
		preset string
	}{
		{
			name:   "literal api_key",
			file:   "bluesky.yaml",
			body:   "handle: \"@x\"\napi_key: " + secret + "\n",
			preset: "bluesky: bluesky.yaml\n",
		},
		{
			name:   "secret-like token key",
			file:   "bluesky.yaml",
			body:   "handle: \"@x\"\ntoken: " + secret + "\n",
			preset: "bluesky: bluesky.yaml\n",
		},
		{
			name:   "camelCase apiKey key",
			file:   "bluesky.yaml",
			body:   "handle: \"@x\"\napiKey: " + secret + "\n",
			preset: "bluesky: bluesky.yaml\n",
		},
		{
			name:   "literal password in hugo file",
			file:   "hugo.yaml",
			body:   "destination: x\npassword: " + secret + "\n",
			preset: "hugo: hugo.yaml\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "publications", "p", "publication.yaml"), tc.preset)
			writeFile(t, filepath.Join(root, "publications", "p", tc.file), tc.body)
			l, _ := editorial.NewLoader(editorial.Config{Root: root})
			_, err := l.LoadPreset("p")
			if err == nil {
				t.Fatal("literal secret must be rejected")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("error echoes the secret: %v", err)
			}
		})
	}

	// The env-reference form is accepted.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "publications", "p", "publication.yaml"), "bluesky: bluesky.yaml\n")
	writeFile(t, filepath.Join(root, "publications", "p", "bluesky.yaml"),
		"handle: \"@x\"\napi_key:\n  env: BLUESKY_API_KEY\n")
	l, _ := editorial.NewLoader(editorial.Config{Root: root})
	if _, err := l.LoadPreset("p"); err != nil {
		t.Errorf("env reference must be accepted: %v", err)
	}
}

// TestLoadPresetBadEnvName: an env reference with an invalid variable name
// is rejected.
func TestLoadPresetBadEnvName(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "publications", "p", "publication.yaml"), "bluesky: bluesky.yaml\n")
	writeFile(t, filepath.Join(root, "publications", "p", "bluesky.yaml"),
		"handle: \"@x\"\napi_key:\n  env: not a valid name\n")
	l, _ := editorial.NewLoader(editorial.Config{Root: root})
	if _, err := l.LoadPreset("p"); err == nil {
		t.Fatal("an invalid environment variable name must be rejected")
	}
}

// TestLoadPresetFileReferenceRules: destination config references must be
// plain file names inside the preset directory — no separators, no leading
// dots, and a dangling reference fails.
func TestLoadPresetFileReferenceRules(t *testing.T) {
	cases := []struct {
		name   string
		preset string
	}{
		{"path separator", "hugo: ../hugo.yaml\n"},
		{"absolute path", "hugo: /etc/hugo.yaml\n"},
		{"leading dot", "hugo: .hidden.yaml\n"},
		{"space", "hugo: my file.yaml\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "publications", "p", "publication.yaml"), tc.preset)
			l, _ := editorial.NewLoader(editorial.Config{Root: root})
			if _, err := l.LoadPreset("p"); !errors.Is(err, editorial.ErrInvalidName) {
				t.Errorf("got %v, want ErrInvalidName", err)
			}
		})
	}
}

// TestLoadPresetHugoValidation: destination path traversal and non-URL
// base_url values are rejected.
func TestLoadPresetHugoValidation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "publications", "p", "publication.yaml"), "hugo: hugo.yaml\n")
	writeFile(t, filepath.Join(root, "publications", "p", "hugo.yaml"),
		"destination: ../../etc\n")
	l, _ := editorial.NewLoader(editorial.Config{Root: root})
	if _, err := l.LoadPreset("p"); err == nil {
		t.Fatal("destination traversal must be rejected")
	}

	writeFile(t, filepath.Join(root, "publications", "p", "hugo.yaml"),
		"base_url: not-a-url\n")
	if _, err := l.LoadPreset("p"); err == nil {
		t.Fatal("a non-URL base_url must be rejected")
	}

	// Absolute destinations are fine.
	writeFile(t, filepath.Join(root, "publications", "p", "hugo.yaml"),
		"destination: /srv/sites/mundodolphins\n")
	p, err := l.LoadPreset("p")
	if err != nil {
		t.Fatalf("absolute destination: %v", err)
	}
	if p.Hugo.DestinationPath(root) != "/srv/sites/mundodolphins" {
		t.Errorf("DestinationPath = %q", p.Hugo.DestinationPath(root))
	}
}

// TestPresetHashDeterministic: the same resolved preset always hashes the
// same (independent of YAML formatting), and any resolved value change
// changes the hash.
func TestPresetHashDeterministic(t *testing.T) {
	mk := func(t *testing.T, preset string) string {
		t.Helper()
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "publications", "mundodolphins", "publication.yaml"), preset)
		l, err := editorial.NewLoader(editorial.Config{Root: root})
		if err != nil {
			t.Fatalf("NewLoader: %v", err)
		}
		p, err := l.LoadPreset("mundodolphins")
		if err != nil {
			t.Fatalf("LoadPreset: %v", err)
		}
		return p.Hash
	}

	// Different formatting, same resolved values: same hash.
	h1 := mk(t, "language: es\nrenderer: thread\n")
	h2 := mk(t, "  renderer: thread\n  language: es  # reordered + comment\n")
	if h1 != h2 {
		t.Errorf("hash must be formatting-independent: %q vs %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("hash = %q, want 64 hex chars", h1)
	}

	h3 := mk(t, "language: en\nrenderer: thread\n")
	if h3 == h1 {
		t.Error("hash must change when a value changes")
	}
}

// TestPresetHashCoversSecretReference: the env reference participates in the
// hash (reproducibility), but a literal secret can never reach it — the
// decoder rejects literals first.
func TestPresetHashCoversSecretReference(t *testing.T) {
	mk := func(t *testing.T, bluesky string) string {
		t.Helper()
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "publications", "p", "publication.yaml"), "bluesky: bluesky.yaml\n")
		writeFile(t, filepath.Join(root, "publications", "p", "bluesky.yaml"), bluesky)
		l, _ := editorial.NewLoader(editorial.Config{Root: root})
		p, err := l.LoadPreset("p")
		if err != nil {
			t.Fatalf("LoadPreset: %v", err)
		}
		return p.Hash
	}
	h1 := mk(t, "handle: \"@x\"\napi_key:\n  env: BLUESKY_API_KEY\n")
	h2 := mk(t, "handle: \"@x\"\napi_key:\n  env: OTHER_KEY\n")
	if h1 == h2 {
		t.Error("hash must cover the referenced environment variable name")
	}
}

// TestLoadPresetNotAvailable lists the available presets on a miss.
func TestLoadPresetNotAvailable(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "publications", "real", "publication.yaml"), "language: en\n")
	l, _ := editorial.NewLoader(editorial.Config{Root: root})
	_, err := l.LoadPreset("nope")
	if err == nil {
		t.Fatal("unknown preset must fail")
	}
	if !strings.Contains(err.Error(), "real") {
		t.Errorf("error should list the available presets: %v", err)
	}
}
