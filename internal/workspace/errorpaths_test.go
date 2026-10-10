package workspace_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// Error paths that need filesystem states the happy path cannot produce: a
// file where a directory is expected, a directory where a file is expected,
// and an atomic rename that cannot be completed.

func TestNewStoreRefusesNonDirectoryRuns(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, workspace.RunsDirName), []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.NewStore(root); err == nil {
		t.Fatal("NewStore accepted a workspace whose runs path is a file")
	}
}

func TestCreateRunRefusesRunDirFile(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)

	file := filepath.Join(store.RunsDir(), m.ID.String())
	content := []byte("a file, not a run directory")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(m); err == nil {
		t.Fatal("CreateRun accepted a run path that is a file")
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, content) {
		t.Error("existing file was modified")
	}
}

// A manifest path that is a directory (an extreme crash residue) must make
// every manifest operation fail cleanly without disturbing the directory.
func TestManifestPathDirectoryFailsAllManifestIO(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(store.RunsDir(), m.ID.String(), workspace.ManifestFileName)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inner"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := store.CreateRun(m); err == nil {
		t.Error("CreateRun with a directory manifest: accepted, want error")
	}
	if _, err := store.SaveManifest(m); err == nil {
		t.Error("SaveManifest with a directory manifest: accepted, want error")
	}
	if _, _, err := store.LoadManifest(m.ID); err == nil {
		t.Error("LoadManifest with a directory manifest: accepted, want error")
	}
	if _, err := store.LoadArtifact(m.ID, workspace.ManifestFileName); err == nil {
		t.Error("LoadArtifact with a directory file: accepted, want error")
	}
	if _, err := os.Stat(filepath.Join(dir, "inner")); err != nil {
		t.Error("manifest directory was disturbed")
	}
}

func TestLoadManifestUnparsableFile(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(store.RunsDir(), m.ID.String(), workspace.ManifestFileName)
	if err := os.WriteFile(manifest, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LoadManifest(m.ID); err == nil {
		t.Fatal("LoadManifest accepted a corrupt manifest")
	}
}

// A run path that is a regular file must make every run-scoped operation
// fail with ErrRunNotFound, never with a write.
func TestRunDirFileFailsAllRunIO(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if err := os.WriteFile(filepath.Join(store.RunsDir(), m.ID.String()), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	ref := run.ArtifactRef{
		Kind: run.ArtifactKindDossier, ID: "x",
		Stage: run.CanonicalStages()[3], Path: "dossier.json",
		ProducedAt: run.NewTimestamp(baseTime),
	}
	if _, err := store.SaveArtifact(m.ID, ref, []byte("x")); !errors.Is(err, workspace.ErrRunNotFound) {
		t.Errorf("SaveArtifact(run dir is a file): err = %v, want ErrRunNotFound", err)
	}
	if _, err := store.LoadArtifact(m.ID, "dossier.json"); !errors.Is(err, workspace.ErrRunNotFound) {
		t.Errorf("LoadArtifact(run dir is a file): err = %v, want ErrRunNotFound", err)
	}
	if _, _, err := store.LoadManifest(m.ID); !errors.Is(err, workspace.ErrRunNotFound) {
		t.Errorf("LoadManifest(run dir is a file): err = %v, want ErrRunNotFound", err)
	}
	if _, err := store.SaveManifest(m); !errors.Is(err, workspace.ErrRunNotFound) {
		t.Errorf("SaveManifest(run dir is a file): err = %v, want ErrRunNotFound", err)
	}
}

// An artifact destination that is a non-empty directory cannot be replaced by
// the atomic rename: the write must fail, leave the directory untouched, and
// not leak a temp file.
func TestSaveArtifactRenameRefusedByExistingDirectory(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	runDir, err := store.CreateRun(m)
	if err != nil {
		t.Fatal(err)
	}

	badDir := filepath.Join(runDir, "output", "article.md")
	if err := os.MkdirAll(badDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "inner"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	ref := run.ArtifactRef{
		Kind: run.ArtifactKindRendered, ID: "article",
		Stage: run.CanonicalStages()[7], Path: "output/article.md",
		ProducedAt: run.NewTimestamp(baseTime),
	}
	if _, err := store.SaveArtifact(m.ID, ref, []byte("# x\n")); err == nil {
		t.Fatal("SaveArtifact over a non-empty directory: accepted, want error")
	}
	if info, err := os.Stat(badDir); err != nil || !info.IsDir() {
		t.Errorf("destination directory was disturbed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(badDir, "inner")); err != nil {
		t.Error("destination content was disturbed")
	}
	entries, err := os.ReadDir(runDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// redactArtifactContent edge inputs: empty content and valid UTF-8 that
// contains a NUL byte (treated as binary, stored verbatim).
func TestRedactionEdgeCases(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}
	plan := run.CanonicalStages()[3]

	emptyRef := run.ArtifactRef{
		Kind: run.ArtifactKindDossier, ID: "e", Stage: plan,
		Path: "empty.json", ProducedAt: run.NewTimestamp(baseTime),
	}
	if _, err := store.SaveArtifact(m.ID, emptyRef, nil); err != nil {
		t.Fatal(err)
	}
	data, err := store.LoadArtifact(m.ID, "empty.json")
	if err != nil || len(data) != 0 {
		t.Errorf("empty artifact: err = %v, data = %q", err, data)
	}

	nul := []byte("a\x00b")
	nulRef := run.ArtifactRef{
		Kind: run.ArtifactKindDossier, ID: "n", Stage: plan,
		Path: "nul.json", ProducedAt: run.NewTimestamp(baseTime),
	}
	if _, err := store.SaveArtifact(m.ID, nulRef, nul); err != nil {
		t.Fatal(err)
	}
	data, err = store.LoadArtifact(m.ID, "nul.json")
	if err != nil || !bytes.Equal(data, nul) {
		t.Errorf("NUL content was not stored verbatim: err = %v, data = %q", err, data)
	}
}

func TestSaveManifestConflictAndNilManifest(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	mA := newTestManifest(t)
	if _, err := store.CreateRun(mA); err != nil {
		t.Fatal(err)
	}

	// A manifest of a different run saved into A's directory must not be
	// overwritten by A's manifest.
	mB := differentManifest(t)
	foreign, err := run.MarshalManifest(mB)
	if err != nil {
		t.Fatal(err)
	}
	manifestA := filepath.Join(store.RunsDir(), mA.ID.String(), workspace.ManifestFileName)
	if err := os.WriteFile(manifestA, foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveManifest(mA); !errors.Is(err, workspace.ErrRunConflict) {
		t.Errorf("SaveManifest over a foreign manifest: err = %v, want ErrRunConflict", err)
	}
	after, err := os.ReadFile(manifestA)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, foreign) {
		t.Error("foreign manifest was modified")
	}

	// Nil manifests are rejected before any filesystem access.
	if _, err := store.SaveManifest(nil); !errors.Is(err, workspace.ErrManifestInvalid) {
		t.Errorf("SaveManifest(nil): err = %v, want ErrManifestInvalid", err)
	}
	if _, err := store.CreateRun(nil); !errors.Is(err, workspace.ErrManifestInvalid) {
		t.Errorf("CreateRun(nil): err = %v, want ErrManifestInvalid", err)
	}
}
