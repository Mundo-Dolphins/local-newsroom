package main

// Focused tests for the run command's remaining branches: editorial profiles
// (full run through the real profiles loader), resume of an already
// succeeded run, and the small pure helpers.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/orchestrator"
	"github.com/Mundo-Dolphins/local-newsroom/internal/profiles"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// writeProfileTree creates a minimal publication profile tree:
//
//	root/<publication>/common/style.md
//	root/<publication>/authors/<author>/style.md
func writeProfileTree(t *testing.T, root, publication, author string) {
	t.Helper()
	commonDir := filepath.Join(root, publication, "common")
	authorDir := filepath.Join(root, publication, "authors", author)
	for _, dir := range []string{commonDir, authorDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(commonDir, "style.md"), []byte("Calm, factual register. Short sentences."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authorDir, "style.md"), []byte("Prefers plain vocabulary. No jargon."), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunWithProfile runs a full run with a loaded editorial profile and
// asserts the profile is recorded on the manifest.
func TestRunWithProfile(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	root := t.TempDir()
	writeProfileTree(t, root, "mundodolphins", "javi")

	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--profile-root", root,
		"--profile", "mundodolphins/javi",
	)
	if err := runCommand(t, args...); err != nil {
		t.Fatalf("run with profile should succeed: %v", err)
	}
	m := h.loadManifest(t)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("manifest status = %s, want succeeded", m.Status)
	}
	if m.Profile == nil || m.Profile.ID != "mundodolphins.javi" {
		t.Errorf("manifest profile = %+v, want id mundodolphins.javi", m.Profile)
	}
	if m.Profile != nil && m.Profile.Hash == "" {
		t.Error("manifest profile should carry a content hash")
	}
}

// TestRunProfileMissingRoot: a profile pointing at a nonexistent directory
// fails with the loader's error, before any run is created.
func TestRunProfileMissingRoot(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--profile-root", filepath.Join(h.workspace, "does-not-exist"),
		"--profile", "pub/author",
	)
	err := runCommand(t, args...)
	if err == nil {
		t.Fatal("missing profile root should fail")
	}
	if !strings.Contains(err.Error(), "pub/author") {
		t.Errorf("error should name the profile: %v", err)
	}
}

// TestRunResumeAlreadySucceeded: resuming a completed run is a no-op that
// exits cleanly and re-prints the summary.
func TestRunResumeAlreadySucceeded(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")

	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--url", h.articleURLs[1],
	)
	if err := runCommand(t, args...); err != nil {
		t.Fatalf("first run should succeed: %v", err)
	}
	m := h.loadManifest(t)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("first run status = %s", m.Status)
	}

	err := runCommand(t, "run", "--resume", string(m.ID),
		"--llm-base-url", h.llmSrv.URL,
		"--llm-api-key", "test-key",
		"--workspace-root", h.workspace,
	)
	if err != nil {
		t.Fatalf("resuming a succeeded run must exit cleanly: %v", err)
	}
	m = h.loadManifest(t)
	if m.Status != run.RunStatusSucceeded {
		t.Errorf("manifest status after resume = %s, want succeeded", m.Status)
	}
}

// TestRunInvalidWorkspaceName: a traversal-y workspace name is rejected
// before anything is written.
func TestRunInvalidWorkspaceName(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--workspace", "../evil",
	)
	err := runCommand(t, args...)
	if err == nil {
		t.Fatal("invalid workspace name should fail")
	}
	if !strings.Contains(err.Error(), "workspace") {
		t.Errorf("error should mention the workspace: %v", err)
	}
}

// TestUserFacingRunError covers the exit-error mapping for every run
// terminal state.
func TestUserFacingRunError(t *testing.T) {
	if err := userFacingRunError(&orchestrator.Result{Status: run.RunStatusSucceeded}, nil); err != nil {
		t.Errorf("succeeded run: %v", err)
	}
	// A succeeded run tolerates a post-success error.
	if err := userFacingRunError(&orchestrator.Result{Status: run.RunStatusSucceeded}, context.DeadlineExceeded); err != nil {
		t.Errorf("succeeded with trailing error: %v", err)
	}
	cancelled := userFacingRunError(&orchestrator.Result{RunID: "r1", Status: run.RunStatusCancelled}, context.Canceled)
	if cancelled == nil || !strings.Contains(cancelled.Error(), "cancelled") {
		t.Errorf("cancelled run = %v", cancelled)
	}
	failed := userFacingRunError(&orchestrator.Result{RunID: "r2", Status: run.RunStatusFailed}, errors.New("stage_failed: write: boom"))
	if failed == nil || !strings.Contains(failed.Error(), "failed") || !strings.Contains(failed.Error(), "resume") {
		t.Errorf("failed run = %v", failed)
	}
	raw := errors.New("plain")
	if got := userFacingRunError(nil, raw); got != raw {
		t.Errorf("nil result should pass the error through: %v", got)
	}
}

// TestMaxSourcesOrDefault checks the default-bound logic.
func TestMaxSourcesOrDefault(t *testing.T) {
	if got := maxSourcesOrDefault(0); got != orchestrator.DefaultMaxSources {
		t.Errorf("zero = %d, want the default %d", got, orchestrator.DefaultMaxSources)
	}
	if got := maxSourcesOrDefault(-4); got != orchestrator.DefaultMaxSources {
		t.Errorf("negative = %d, want the default", got)
	}
	if got := maxSourcesOrDefault(3); got != 3 {
		t.Errorf("positive = %d, want 3", got)
	}
}

// TestDedupeURLs checks trimming, dedup, and empty filtering.
func TestDedupeURLs(t *testing.T) {
	got := dedupeURLs([]string{" http://a ", "http://a", "http://b", "", "   "})
	if len(got) != 2 || got[0] != "http://a" || got[1] != "http://b" {
		t.Errorf("dedupe = %v", got)
	}
}

// TestProfileHash is deterministic and content-sensitive.
func TestProfileHash(t *testing.T) {
	p := profiles.Profile{
		Style:      "s",
		Vocabulary: "v",
		Examples:   map[string]string{"b": "2", "a": "1"},
	}
	h1 := profileHash(p)
	h2 := profileHash(p)
	if h1 == "" || h1 != h2 {
		t.Errorf("hash not deterministic: %q vs %q", h1, h2)
	}
	p2 := p
	p2.Style = "different"
	if profileHash(p2) == h1 {
		t.Error("hash must change with the content")
	}
}

// TestLoadRunProfileAuthorOnly maps a bare author name onto the root.
func TestLoadRunProfileAuthorOnly(t *testing.T) {
	setHermeticEnv(t)
	root := t.TempDir()
	writeProfileTree(t, root, ".", "javi") // publication "." == root itself

	// No profile configured at all: nothing is loaded.
	ref, inst, err := loadRunProfile(context.Background(), root, "", false, "")
	if err != nil || ref != nil || inst != nil {
		t.Fatalf("no spec should produce no profile: ref=%+v inst=%v err=%v", ref, inst, err)
	}

	// A bare author name maps onto the root itself.
	ref, inst, err = loadRunProfile(context.Background(), root, "", false, "javi")
	if err != nil {
		t.Fatalf("author-only profile: %v", err)
	}
	if ref == nil || ref.ID != "javi" || inst == nil {
		t.Errorf("author-only profile: ref=%+v inst=%v", ref, inst)
	}
}
