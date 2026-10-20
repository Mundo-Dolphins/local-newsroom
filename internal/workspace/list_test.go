package workspace_test

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// TestListArtifacts pins the publication listing primitive: regular files
// directly in a run-relative directory, in sorted run-relative form, with
// redaction sidecars and subdirectories excluded and the root escaped
// fail-closed.
func TestListArtifacts(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	saved := []string{}
	for _, name := range []string{"b.txt", "a.txt", "thread.txt"} {
		ref := run.ArtifactRef{
			Kind:       run.ArtifactKindRendered,
			ID:         "publication-content-" + name[:1],
			Path:       filepath.Join("publication", "content", name),
			ProducedAt: run.NewTimestamp(baseTime),
		}
		if _, err := store.SaveArtifact(m.ID, ref, []byte("hello "+name)); err != nil {
			t.Fatalf("SaveArtifact %s: %v", name, err)
		}
		saved = append(saved, name)
	}

	sort.Strings(saved)
	got, err := store.ListArtifacts(m.ID, filepath.Join("publication", "content"))
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	want := make([]string, len(saved))
	for i, name := range saved {
		want[i] = filepath.Join("publication", "content", name)
	}
	if len(got) != len(want) {
		t.Fatalf("ListArtifacts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListArtifacts = %v, want %v", got, want)
		}
	}
}

// TestListArtifactsExcludesSidecarsAndSubdirectories proves that redaction
// sidecars and nested directories never appear as artifacts.
func TestListArtifactsExcludesSidecarsAndSubdirectories(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	runDir, err := store.CreateRun(m)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// A sidecar next to a real artifact.
	sidecarDir := filepath.Join(runDir, "publication", "content")
	if err := os.MkdirAll(sidecarDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sidecarDir, "note.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sidecarDir, "note.md"+workspace.RedactionMetaSuffix), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A nested directory.
	if err := os.MkdirAll(filepath.Join(sidecarDir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListArtifacts(m.ID, filepath.Join("publication", "content"))
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if len(got) != 1 || got[0] != filepath.Join("publication", "content", "note.md") {
		t.Fatalf("ListArtifacts = %v, want only [publication/content/note.md]", got)
	}
}

// TestListArtifactsRejectsBadPaths pins the fail-closed path handling.
func TestListArtifactsRejectsBadPaths(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if _, err := store.ListArtifacts(m.ID, "../outside"); !errors.Is(err, workspace.ErrArtifactPath) {
		t.Fatalf("ListArtifacts(../outside) err = %v, want ErrArtifactPath", err)
	}
	if _, err := store.ListArtifacts(m.ID, ""); err == nil {
		t.Fatal("ListArtifacts(\"\") must reject the empty path")
	}
	if _, err := store.ListArtifacts(m.ID, "does-not-exist"); err == nil {
		t.Fatal("ListArtifacts of a missing directory must error")
	}
}
