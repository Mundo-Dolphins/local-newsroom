package workspace_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// baseTime is the fixed timestamp for deterministic run IDs.
var baseTime = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

// newTestManifest builds a valid pending manifest with a deterministic ID.
func newTestManifest(t *testing.T) *run.Manifest {
	t.Helper()
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace: run.Workspace{Name: "testws"},
		Topic:     "EU AI Act enters into force",
		StartedAt: baseTime,
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	return m
}

// secretValue is a string that the redaction detectors must catch.
const secretValue = "token = deadbeefdeadbeefdeadbeef1234"

// ---------------------------------------------------------------------------
// NewStore
// ---------------------------------------------------------------------------

func TestNewStoreCreatesLayoutWithSafePermissions(t *testing.T) {
	root := t.TempDir()

	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	for dir := range map[string]string{
		store.Root(): "root",
		filepath.Join(store.Root(), workspace.RunsDirName): "runs",
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("%s permissions = %v, want 0700", dir, got)
		}
	}

	// Relative roots resolve against the working directory.
	t.Chdir(t.TempDir())
	storeRel, err := workspace.NewStore("relws")
	if err != nil {
		t.Fatalf("NewStore(relative): %v", err)
	}
	if filepath.Base(storeRel.Root()) != "relws" {
		t.Errorf("relative root = %q, want .../relws", storeRel.Root())
	}
}

func TestNewStoreRejectsBadRoots(t *testing.T) {
	if _, err := workspace.NewStore("  "); !errors.Is(err, workspace.ErrWorkspaceRoot) {
		t.Errorf("blank root: err = %v, want ErrWorkspaceRoot", err)
	}

	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.NewStore(file); !errors.Is(err, workspace.ErrWorkspaceRoot) {
		t.Errorf("file root: err = %v, want ErrWorkspaceRoot", err)
	}
}

// ---------------------------------------------------------------------------
// CreateRun
// ---------------------------------------------------------------------------

func TestCreateRunPersistsManifestAndDirectory(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)

	dir, err := store.CreateRun(m)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	wantDir := filepath.Join(root, workspace.RunsDirName, m.ID.String())
	if dir != wantDir {
		t.Fatalf("dir = %q, want %q", dir, wantDir)
	}

	// Run tree permissions.
	for p, want := range map[string]os.FileMode{
		wantDir: 0o700,
		filepath.Join(wantDir, workspace.ManifestFileName): 0o600,
	} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("permissions %s = %v, want %v", p, got, want)
		}
	}

	// The manifest round-trips through a fresh read.
	loaded, gotDir, err := store.LoadManifest(m.ID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if gotDir != wantDir {
		t.Errorf("LoadManifest dir = %q, want %q", gotDir, wantDir)
	}
	if loaded.ID != m.ID || loaded.Status != run.RunStatusPending {
		t.Errorf("loaded = %s/%s, want %s/pending", loaded.ID, loaded.Status, m.ID)
	}
	if loaded.Workspace.Path != store.Root() {
		t.Errorf("workspace path = %q, want %q", loaded.Workspace.Path, store.Root())
	}

	ids, err := store.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != m.ID {
		t.Errorf("ListRuns = %v, want [%s]", ids, m.ID)
	}
}

func TestCreateRunRefusesDuplicate(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)

	if _, err := store.CreateRun(m); err != nil {
		t.Fatalf("first CreateRun: %v", err)
	}
	manifestFile := filepath.Join(store.RunsDir(), m.ID.String(), workspace.ManifestFileName)
	before, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}

	m2 := newTestManifest(t) // same derivation inputs → same ID
	if _, err := store.CreateRun(m2); !errors.Is(err, workspace.ErrRunExists) {
		t.Fatalf("second CreateRun: err = %v, want ErrRunExists", err)
	}
	after, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("CreateRun(duplicate) modified the existing manifest")
	}
}

func TestCreateRunRefusesForeignManifest(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)

	// Hand-craft a run directory whose manifest belongs to a different run.
	dir := filepath.Join(store.RunsDir(), m.ID.String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign, err := run.MarshalManifest(differentManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, workspace.ManifestFileName), foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(m); !errors.Is(err, workspace.ErrRunConflict) {
		t.Fatalf("CreateRun over foreign manifest: err = %v, want ErrRunConflict", err)
	}

	// A directory with an unparseable manifest is also a conflict.
	if err := os.WriteFile(filepath.Join(dir, workspace.ManifestFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(m); !errors.Is(err, workspace.ErrRunConflict) {
		t.Fatalf("CreateRun over corrupt manifest: err = %v, want ErrRunConflict", err)
	}
}

func TestCreateRunAdoptsOrphanedDirectory(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)

	// Simulate a creation that died between MkdirAll and the manifest write.
	dir := filepath.Join(store.RunsDir(), m.ID.String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(m); err != nil {
		t.Fatalf("CreateRun over orphaned dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, workspace.ManifestFileName)); err != nil {
		t.Errorf("manifest not written into adopted dir: %v", err)
	}
}

func TestCreateRunRejectsSecretManifestWithoutSideEffects(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	// Bypass the redacting Set() to simulate an unredacted secret.
	m.Config.Values = map[string]string{"omlx_api_key": "sk-deadbeefdeadbeefdeadbeef1234"}

	if _, err := store.CreateRun(m); !errors.Is(err, workspace.ErrManifestInvalid) {
		t.Fatalf("CreateRun with secret: err = %v, want ErrManifestInvalid", err)
	}
	if _, err := os.Stat(filepath.Join(store.RunsDir(), m.ID.String())); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("run directory was created for a rejected manifest")
	}
}

// ---------------------------------------------------------------------------
// SaveManifest
// ---------------------------------------------------------------------------

func TestSaveManifestUpdatesAndRoundTrips(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}

	if err := m.Start(baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}
	loaded, _, err := store.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != run.RunStatusRunning {
		t.Errorf("loaded status = %s, want running", loaded.Status)
	}
}

func TestSaveManifestRefusesRegression(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveManifest(m); err != nil {
		t.Fatal(err)
	}
	manifestFile := filepath.Join(store.RunsDir(), m.ID.String(), workspace.ManifestFileName)
	saved, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}

	// An older manifest must not clobber the newer on-disk one.
	stale := newTestManifest(t) // started_at = baseTime < baseTime+1s
	if _, err := store.SaveManifest(stale); !errors.Is(err, workspace.ErrManifestRegression) {
		t.Fatalf("SaveManifest(stale): err = %v, want ErrManifestRegression", err)
	}
	after, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, after) {
		t.Error("rejected save modified the manifest")
	}

	// An equal manifest (idempotent rewrite) is allowed.
	same := newTestManifest(t)
	if err := same.Start(baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveManifest(same); err != nil {
		t.Fatalf("SaveManifest(equal): %v", err)
	}
}

func TestSaveManifestOverwritesCorruptFile(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}

	// Simulate a torn write from a crashed process.
	manifestFile := filepath.Join(store.RunsDir(), m.ID.String(), workspace.ManifestFileName)
	if err := os.WriteFile(manifestFile, []byte("{torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest over corrupt file: %v", err)
	}
	if _, _, err := store.LoadManifest(m.ID); err != nil {
		t.Errorf("manifest unreadable after recovery save: %v", err)
	}
}

func TestSaveManifestRejectsSecrets(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}
	manifestFile := filepath.Join(store.RunsDir(), m.ID.String(), workspace.ManifestFileName)
	safe, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}

	// A mutation that smuggles a raw secret back into the manifest.
	m.Warnings = append(m.Warnings, run.Warning{
		Code:       "leak",
		Message:    "client requested token = deadbeefdeadbeefdeadbeef1234",
		OccurredAt: run.NewTimestamp(baseTime.Add(2 * time.Second)),
	})
	if _, err := store.SaveManifest(m); !errors.Is(err, workspace.ErrManifestInvalid) {
		t.Fatalf("SaveManifest with secret: err = %v, want ErrManifestInvalid", err)
	}
	after, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(safe, after) {
		t.Error("rejected save modified the manifest")
	}
}

// ---------------------------------------------------------------------------
// Artifacts
// ---------------------------------------------------------------------------

func TestSaveArtifactPersistsInsideRunDir(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}

	ref := run.ArtifactRef{
		Kind:       run.ArtifactKindRendered,
		ID:         "article",
		Stage:      run.CanonicalStages()[7],
		Path:       "output/article.md",
		ProducedAt: run.NewTimestamp(baseTime),
	}
	rel, err := store.SaveArtifact(m.ID, ref, []byte("# Headline\n"))
	if err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}
	if rel != "output/article.md" {
		t.Errorf("rel = %q, want output/article.md", rel)
	}

	want := filepath.Join(store.RunsDir(), m.ID.String(), "output", "article.md")
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("artifact not on disk: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("artifact permissions = %v, want 0600", got)
	}
	data, err := store.LoadArtifact(m.ID, "output/article.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# Headline\n" {
		t.Errorf("content = %q", data)
	}
}

func TestSaveArtifactSanitizesAllContent(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(store.RunsDir(), m.ID.String())

	dossierRef := run.ArtifactRef{
		Kind:       run.ArtifactKindDossier,
		ID:         "dossier",
		Stage:      run.CanonicalStages()[3],
		Path:       "dossier.json",
		ProducedAt: run.NewTimestamp(baseTime),
	}
	if _, err := store.SaveArtifact(m.ID, dossierRef, []byte("{\"note\": \"agent logged "+secretValue+" to the console\"}")); err != nil {
		t.Fatal(err)
	}
	data, err := store.LoadArtifact(m.ID, "dossier.json")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("deadbeefdeadbeefdeadbeef1234")) {
		t.Errorf("derived artifact leaked the secret: %s", data)
	}
	if !bytes.Contains(data, []byte(run.RedactedPlaceholder)) {
		t.Errorf("derived artifact was not redacted: %s", data)
	}

	// Source artifacts are no longer persisted verbatim: a page containing a
	// detected secret is redacted, and a sidecar records the redaction.
	sourceRef := run.ArtifactRef{
		Kind:       run.ArtifactKindSource,
		ID:         "source-1",
		Stage:      run.CanonicalStages()[1],
		Path:       "sources/raw-1.md",
		ProducedAt: run.NewTimestamp(baseTime),
	}
	raw := []byte("page text: " + secretValue)
	if _, err := store.SaveArtifact(m.ID, sourceRef, raw); err != nil {
		t.Fatal(err)
	}
	data, err = store.LoadArtifact(m.ID, "sources/raw-1.md")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("deadbeefdeadbeefdeadbeef1234")) {
		t.Errorf("source artifact leaked the secret: %s", data)
	}
	if !bytes.Contains(data, []byte(run.RedactedPlaceholder)) {
		t.Errorf("source artifact was not redacted: %s", data)
	}
	sidecar := filepath.Join(runDir, "sources", "raw-1.md"+workspace.RedactionMetaSuffix)
	if _, err := os.Stat(sidecar); err != nil {
		t.Errorf("redaction sidecar missing: %v", err)
	}

	// Clean source content stays byte-for-byte identical, with no sidecar.
	clean := []byte("a perfectly clean source page\n")
	cleanRef := run.ArtifactRef{
		Kind:       run.ArtifactKindSource,
		ID:         "source-2",
		Stage:      run.CanonicalStages()[1],
		Path:       "sources/raw-2.md",
		ProducedAt: run.NewTimestamp(baseTime),
	}
	if _, err := store.SaveArtifact(m.ID, cleanRef, clean); err != nil {
		t.Fatal(err)
	}
	data, err = store.LoadArtifact(m.ID, "sources/raw-2.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, clean) {
		t.Errorf("clean source content was modified: %s", data)
	}
	if _, err := os.Stat(filepath.Join(runDir, "sources", "raw-2.md"+workspace.RedactionMetaSuffix)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unexpected redaction sidecar for clean artifact (stat err = %v)", err)
	}

	// Binary content without a known exact secret value passes through
	// unchanged (pattern detectors are defined for text).
	bin := []byte{0x00, 0x01, 0xff}
	binRef := run.ArtifactRef{
		Kind:       run.ArtifactKindDocument,
		ID:         "blob",
		Stage:      run.CanonicalStages()[2],
		Path:       "documents/blob.bin",
		ProducedAt: run.NewTimestamp(baseTime),
	}
	if _, err := store.SaveArtifact(m.ID, binRef, bin); err != nil {
		t.Fatal(err)
	}
	data, err = store.LoadArtifact(m.ID, "documents/blob.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, bin) {
		t.Errorf("binary content was modified")
	}
	if _, err := os.Stat(filepath.Join(runDir, "documents", "blob.bin"+workspace.RedactionMetaSuffix)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unexpected redaction sidecar for binary artifact (stat err = %v)", err)
	}
}

func TestSaveArtifactPathSafety(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}

	badPaths := []string{
		"",
		"  ",
		"/etc/passwd",
		"..",
		"../escape.json",
		"a/../../escape.json",
		`win\path.json`,
		"bad\x00name.json",
		".",
	}
	for _, p := range badPaths {
		ref := run.ArtifactRef{
			Kind:       run.ArtifactKindDossier,
			ID:         "x",
			Stage:      run.CanonicalStages()[3],
			Path:       p,
			ProducedAt: run.NewTimestamp(baseTime),
		}
		if _, err := store.SaveArtifact(m.ID, ref, []byte("x")); err == nil {
			t.Errorf("SaveArtifact(%q): accepted, want error", p)
		}
	}

	// A path that normalizes back inside the run dir is fine.
	ref := run.ArtifactRef{
		Kind:       run.ArtifactKindDossier,
		ID:         "x",
		Stage:      run.CanonicalStages()[3],
		Path:       "output/../dossier.json",
		ProducedAt: run.NewTimestamp(baseTime),
	}
	if rel, err := store.SaveArtifact(m.ID, ref, []byte("x")); err != nil {
		t.Errorf("SaveArtifact(normalizing path): %v", err)
	} else if rel != "dossier.json" {
		t.Errorf("canonical rel = %q, want dossier.json", rel)
	}

	// Nothing may have escaped the run directory.
	if _, err := os.Stat(filepath.Join(root, "escape.json")); !errors.Is(err, os.ErrNotExist) {
		t.Error("artifact escaped the run directory")
	}
	if _, err := os.Stat(filepath.Join(root, "..", "escape.json")); !errors.Is(err, os.ErrNotExist) {
		t.Error("artifact escaped the workspace root")
	}
}

func TestSaveArtifactRefusesSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	runDir, err := store.CreateRun(m)
	if err != nil {
		t.Fatal(err)
	}

	// A symlink inside the run directory pointing outside the workspace.
	if err := os.Symlink(outside, filepath.Join(runDir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ref := run.ArtifactRef{
		Kind:       run.ArtifactKindDossier,
		ID:         "x",
		Stage:      run.CanonicalStages()[3],
		Path:       "link/escape.json",
		ProducedAt: run.NewTimestamp(baseTime),
	}
	if _, err := store.SaveArtifact(m.ID, ref, []byte("x")); err == nil {
		t.Fatal("SaveArtifact through a symlink pointing outside: accepted, want error")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.json")); !errors.Is(err, os.ErrNotExist) {
		t.Error("artifact was written outside the workspace via symlink")
	}
}

func TestLoadArtifactRefusesSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	runDir, err := store.CreateRun(m)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(outside, filepath.Join(runDir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.LoadArtifact(m.ID, "link/secret.txt"); err == nil {
		t.Fatal("LoadArtifact through a symlink pointing outside: accepted, want error")
	}
}

func TestCreateRunRefusesSymlinkedRunDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)

	dir := filepath.Join(store.RunsDir(), m.ID.String())
	if err := os.Symlink(outside, dir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.CreateRun(m); err == nil {
		t.Fatal("CreateRun into a symlinked-out run dir: accepted, want error")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Errorf("symlinked target was written: %v", entries)
	}
}

func TestSaveArtifactRequiresExistingRun(t *testing.T) {
	store, err := workspace.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	ref := run.ArtifactRef{
		Kind:       run.ArtifactKindDossier,
		ID:         "x",
		Stage:      run.CanonicalStages()[3],
		Path:       "dossier.json",
		ProducedAt: run.NewTimestamp(baseTime),
	}
	if _, err := store.SaveArtifact(m.ID, ref, []byte("x")); !errors.Is(err, workspace.ErrRunNotFound) {
		t.Errorf("SaveArtifact(unknown run): err = %v, want ErrRunNotFound", err)
	}
}

func TestManifestAccessOnMissingRun(t *testing.T) {
	store, err := workspace.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)

	if _, _, err := store.LoadManifest(m.ID); !errors.Is(err, workspace.ErrRunNotFound) {
		t.Errorf("LoadManifest(unknown): err = %v, want ErrRunNotFound", err)
	}
	if _, err := store.SaveManifest(m); !errors.Is(err, workspace.ErrRunNotFound) {
		t.Errorf("SaveManifest(unknown): err = %v, want ErrRunNotFound", err)
	}
	if _, err := store.LoadArtifact(m.ID, "dossier.json"); !errors.Is(err, workspace.ErrRunNotFound) {
		t.Errorf("LoadArtifact(unknown): err = %v, want ErrRunNotFound", err)
	}
}

func TestRunDirRejectsInvalidID(t *testing.T) {
	store, err := workspace.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RunDir(run.RunID("not a run id")); err == nil {
		t.Error("RunDir accepted an invalid ID")
	}
}

// ---------------------------------------------------------------------------
// ListRuns
// ---------------------------------------------------------------------------

func TestListRunsfiltersAndSorts(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}

	m1 := newTestManifest(t)
	// m2 uses a distinct topic and start time so it gets a distinct, later ID.
	m2, err := run.NewManifest(run.NewManifestParams{
		Workspace: run.Workspace{Name: "testws"},
		Topic:     "A different topic entirely",
		StartedAt: baseTime.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(m1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(m2); err != nil {
		t.Fatal(err)
	}

	// Noise that must be ignored: a stray file and a directory without a
	// manifest (an orphaned, half-created run).
	if err := os.WriteFile(filepath.Join(store.RunsDir(), "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(store.RunsDir(), "run-20260101T000000Z-ffffffffffffffff"), 0o700); err != nil {
		t.Fatal(err)
	}

	ids, err := store.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != m1.ID || ids[1] != m2.ID {
		t.Errorf("ListRuns = %v, want [%s %s]", ids, m1.ID, m2.ID)
	}
}

// ---------------------------------------------------------------------------
// Recovery: interrupted and failed runs
// ---------------------------------------------------------------------------

func TestInterruptedRunRemainsReadableAfterRestart(t *testing.T) {
	root := t.TempDir()

	// Process 1: create the run, persist progress, then "die".
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveManifest(m); err != nil {
		t.Fatal(err)
	}
	if err := m.StartStage(run.CanonicalStages()[0], baseTime.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveManifest(m); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveArtifact(m.ID, run.ArtifactRef{
		Kind: run.ArtifactKindSource, ID: "candidates", Stage: run.CanonicalStages()[0],
		Path: "discovery.json", ProducedAt: run.NewTimestamp(baseTime.Add(2 * time.Second)),
	}, []byte(`{"candidates":["https://example.com/a"]}`)); err != nil {
		t.Fatal(err)
	}
	// The process dies here: the discovery stage is left "running".
	_ = store

	// Process 2: everything is still readable and consistent.
	reopened, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, dir, err := reopened.LoadManifest(m.ID)
	if err != nil {
		t.Fatalf("LoadManifest after restart: %v", err)
	}
	if loaded.Status != run.RunStatusRunning {
		t.Errorf("status = %s, want running", loaded.Status)
	}
	if loaded.Stages[0].Status != run.StageStatusRunning {
		t.Errorf("discovery stage = %s, want running", loaded.Stages[0].Status)
	}
	if len(loaded.Stages[0].Attempts) != 1 {
		t.Errorf("attempts = %d, want 1", len(loaded.Stages[0].Attempts))
	}
	data, err := reopened.LoadArtifact(m.ID, "discovery.json")
	if err != nil {
		t.Fatalf("LoadArtifact after restart: %v", err)
	}
	if len(data) == 0 {
		t.Error("artifact is empty after restart")
	}
	ids, err := reopened.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != m.ID {
		t.Errorf("ListRuns after restart = %v, want [%s]", ids, m.ID)
	}
	// No torn temp files left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" && len(e.Name()) > 5 && e.Name()[:1] == "." {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestFailedRunKeepsDiagnosticsAndArtifacts(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}

	// research produces a dossier, then fails with a secret-laced error.
	if err := m.Start(baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := m.StartStage(run.CanonicalStages()[3], baseTime.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveArtifact(m.ID, run.ArtifactRef{
		Kind: run.ArtifactKindDossier, ID: "dossier", Stage: run.CanonicalStages()[3],
		Path: "dossier.json", ProducedAt: run.NewTimestamp(baseTime.Add(2 * time.Second)),
	}, []byte(`{"claims":[]}`)); err != nil {
		t.Fatal(err)
	}
	failAt := baseTime.Add(3 * time.Second)
	if err := m.FailStage(run.CanonicalStages()[3], failAt, "llm_error", "provider 500: token = deadbeefdeadbeefdeadbeef1234"); err != nil {
		t.Fatal(err)
	}
	if err := m.Fail(failAt, "llm_error", "provider 500: token = deadbeefdeadbeefdeadbeef1234"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveManifest(m); err != nil {
		t.Fatalf("SaveManifest(failed run): %v", err)
	}

	// Reopen and verify the failure is fully inspectable.
	reopened, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, _, err := reopened.LoadManifest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != run.RunStatusFailed {
		t.Errorf("status = %s, want failed", loaded.Status)
	}
	if len(loaded.Errors) == 0 || loaded.Errors[0].Message == "" {
		t.Fatal("failed run lost its diagnostics")
	}
	if bytes.Contains(mustManifestBytes(t, reopened, m.ID), []byte("deadbeefdeadbeefdeadbeef1234")) {
		t.Error("failed run manifest contains the raw secret")
	}
	if !bytes.Contains(mustManifestBytes(t, reopened, m.ID), []byte(run.RedactedPlaceholder)) {
		t.Error("expected [REDACTED] in the persisted failure diagnostics")
	}
	if _, err := reopened.LoadArtifact(m.ID, "dossier.json"); err != nil {
		t.Errorf("pre-failure artifact lost: %v", err)
	}
}

func mustManifestBytes(t *testing.T, store *workspace.Store, id run.RunID) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(store.RunsDir(), id.String(), workspace.ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// differentManifest builds a manifest whose ID differs (different topic, so
// the derived run ID differs). Used to simulate a manifest that belongs to
// another run.
func differentManifest(t *testing.T) *run.Manifest {
	t.Helper()
	n, err := run.NewManifest(run.NewManifestParams{
		Workspace: run.Workspace{Name: "testws"},
		Topic:     "A completely different topic",
		StartedAt: baseTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
