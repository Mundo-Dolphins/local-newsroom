package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// captureStdout swaps os.Stdout for a buffer for the duration of fn and
// returns everything written (the commands print with fmt.Print, which reads
// the os.Stdout variable at call time).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	var out strings.Builder
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return out.String()
}

// doRunForReview runs the full run command and returns the run ID (the run
// outcome is asserted by the caller).
func doRunForReview(t *testing.T, h *runHarness) run.RunID {
	t.Helper()
	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--url", h.articleURLs[1],
	)
	if err := runCommand(t, args...); err != nil {
		t.Fatalf("run command: %v", err)
	}
	return h.loadManifest(t).ID
}

// TestReviewAfterSucceededRun: a completed run gets a review.md in the run
// directory during `newsroom run`, and `newsroom review` prints the same
// document.
func TestReviewAfterSucceededRun(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	id := doRunForReview(t, h)
	runDir := h.findRunDir(t)

	// The run itself attached the bundle.
	reviewPath := filepath.Join(runDir, "review.md")
	data, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatalf("run must write review.md: %v", err)
	}
	if !strings.Contains(string(data), "**READY**") {
		t.Fatalf("a succeeded, passing run must render READY:\n%s", data)
	}
	if strings.Contains(string(data), "**NOT READY**") {
		t.Fatal("a succeeded, passing run must not render NOT READY")
	}
	// The bundle is a run-directory file, not a manifest artifact.
	m := h.loadManifest(t)
	for _, a := range m.Artifacts {
		if a.Path == "review.md" {
			t.Fatal("review.md must not be referenced on the manifest")
		}
	}

	// The review command prints the same document and refreshes the file.
	out := captureStdout(t, func() {
		err := runCommand(t, "review", string(id), "--workspace-root", h.workspace)
		if err != nil {
			t.Fatalf("review command: %v", err)
		}
	})
	if out != string(data) {
		t.Fatalf("review command output differs from the run's review.md:\n--- command ---\n%s\n--- file ---\n%s", out, data)
	}
	for _, want := range []string{
		"# Run Review: Miami Dolphins rookie",
		string(id),
		"## Sources",
		"## Claims & verification",
		"## Final factual check",
		"**READY**",
		"newsroom review " + string(id),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("review output lacks %q", want)
		}
	}
}

// TestReviewAfterFailedRun: a run that failed at the final check gets a
// review bundle that is explicit about the failure and the NOT READY output.
func TestReviewAfterFailedRun(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "fail")

	args := append(h.baseArgs(),
		"--url", h.articleURLs[0],
		"--url", h.articleURLs[1],
	)
	if err := runCommand(t, args...); err == nil {
		t.Fatal("run should fail (check verdict fail)")
	}
	id := h.loadManifest(t).ID
	runDir := h.findRunDir(t)

	// A failed run still gets a review.md.
	data, err := os.ReadFile(filepath.Join(runDir, "review.md"))
	if err != nil {
		t.Fatalf("a failed run must still get review.md: %v", err)
	}
	doc := string(data)
	if strings.Contains(doc, "**READY**") {
		t.Fatal("a failed run must not render READY")
	}
	if !strings.Contains(doc, "**NOT READY**") {
		t.Fatal("a failed run must render NOT READY")
	}
	for _, want := range []string{
		"final factual check",
		"finding-run-001", // the checker's finding is surfaced
		"## Final factual check",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("failed-run review lacks %q:\n%s", want, doc)
		}
	}

	// The review command works on failed runs too.
	out := captureStdout(t, func() {
		if err := runCommand(t, "review", string(id), "--workspace-root", h.workspace); err != nil {
			t.Fatalf("review command on a failed run: %v", err)
		}
	})
	if out != doc {
		t.Fatal("review command output differs from the persisted review.md")
	}
}

// TestReviewNoSaveDoesNotWrite: --no-save prints without touching the file.
func TestReviewNoSaveDoesNotWrite(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	id := doRunForReview(t, h)
	runDir := h.findRunDir(t)
	reviewPath := filepath.Join(runDir, "review.md")
	original, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Remove the bundle so a (dis)allowed write is observable.
	if err := os.Remove(reviewPath); err != nil {
		t.Fatalf("remove: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runCommand(t, "review", string(id), "--workspace-root", h.workspace, "--no-save"); err != nil {
			t.Fatalf("review --no-save: %v", err)
		}
	})
	if !strings.Contains(out, "# Run Review:") {
		t.Fatal("--no-save must still print the document")
	}
	if fileExistsPath(reviewPath) {
		t.Fatal("--no-save must not write review.md")
	}
	// And the default path saves again.
	captureStdout(t, func() {
		if err := runCommand(t, "review", string(id), "--workspace-root", h.workspace); err != nil {
			t.Fatalf("review: %v", err)
		}
	})
	if !fileExistsPath(reviewPath) {
		t.Fatal("default review must refresh review.md")
	}
	data, _ := os.ReadFile(reviewPath)
	if string(data) != string(original) {
		t.Fatal("refreshed review.md must be byte-identical (deterministic)")
	}
}

// TestReviewUnknownRun: an unknown run ID is a clean error naming the run.
func TestReviewUnknownRun(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	err := runCommand(t, "review", "run-20250101T000000Z-00000000000000", "--workspace-root", h.workspace)
	if err == nil {
		t.Fatal("reviewing an unknown run should fail")
	}
	if !strings.Contains(err.Error(), "run-20250101T000000Z-00000000000000") {
		t.Fatalf("error should name the run: %v", err)
	}
}

// TestReviewInvalidID: a malformed run ID is a usage error.
func TestReviewInvalidID(t *testing.T) {
	setHermeticEnv(t)
	h := newRunHarness(t, "pass")
	err := runCommand(t, "review", "not-a-run-id", "--workspace-root", h.workspace)
	if err == nil {
		t.Fatal("reviewing a malformed ID should fail")
	}
}
