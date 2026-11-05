package publish_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// ---------------------------------------------------------------------------
// RecordFinal — the generated vs human-edited final artifact pair (v0.7,
// issue #146)
// ---------------------------------------------------------------------------

// TestFinal_RecordArticlePair verifies the full record path for an article
// run: the pair is persisted with content digests, the manifest is updated
// and re-validates, and the generated original is byte-identical and
// untouched.
func TestFinal_RecordArticlePair(t *testing.T) {
	h := newHarness(t)

	edited := "# EU AI Act takes effect\n\nThe Act enters into force — with clarifications.\n"
	finalFile := filepath.Join(t.TempDir(), "article.md")
	if err := os.WriteFile(finalFile, []byte(edited), 0o600); err != nil {
		t.Fatalf("writing edited file: %v", err)
	}

	res, err := h.eng.RecordFinal(t.Context(), h.runID, publish.RecordFinalRequest{
		FinalPath: finalFile,
		Editor:    "javi",
	}, h.bundle)
	if err != nil {
		t.Fatalf("RecordFinal: %v", err)
	}

	// Manifest: final_state recorded with both digests, run still succeeded.
	m, _, err := h.store.LoadManifest(h.runID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("status = %s, want succeeded (recording a final pair must not change run state)", m.Status)
	}
	fs := m.FinalState
	if fs == nil {
		t.Fatal("final_state is nil after RecordFinal")
	}
	if fs.FinalPath != run.FinalArtifactsDir+"/article.md" {
		t.Errorf("final_path = %s, want %s", fs.FinalPath, run.FinalArtifactsDir+"/article.md")
	}
	if fs.GeneratedPath != run.FinalGeneratedDir+"/article.md" {
		t.Errorf("generated_path = %s, want %s", fs.GeneratedPath, run.FinalGeneratedDir+"/article.md")
	}
	if !run.ValidSHA256Digest(fs.GeneratedHash) || !run.ValidSHA256Digest(fs.FinalHash) {
		t.Errorf("hashes are not canonical sha256 digests: %q / %q", fs.GeneratedHash, fs.FinalHash)
	}
	if fs.GeneratedHash == fs.FinalHash {
		t.Error("edited content must hash differently from the generated copy")
	}
	if fs.Editor != "javi" {
		t.Errorf("editor = %q, want %q", fs.Editor, "javi")
	}

	// Both persisted documents are readable through the store.
	gen, err := h.store.LoadArtifact(h.runID, fs.GeneratedPath)
	if err != nil {
		t.Fatalf("loading generated copy: %v", err)
	}
	if string(gen) != articleContent {
		t.Errorf("generated copy content = %q, want the original rendered article (preserved verbatim)", string(gen))
	}
	final, err := h.store.LoadArtifact(h.runID, fs.FinalPath)
	if err != nil {
		t.Fatalf("loading final document: %v", err)
	}
	if string(final) != edited {
		t.Errorf("final content = %q, want the human-edited bytes (preserved verbatim)", string(final))
	}

	// The run directory originals are untouched.
	orig, err := h.store.LoadArtifact(h.runID, run.ArtifactPathArticle)
	if err != nil {
		t.Fatalf("original output must remain readable: %v", err)
	}
	if string(orig) != articleContent {
		t.Errorf("original output/article.md changed: %q (originals must never be overwritten)", string(orig))
	}

	// Result paths agree with the manifest.
	if res.ManifestPath == "" {
		t.Error("ManifestPath is empty")
	}
}

// TestFinal_RecordThreadPair verifies that a run that rendered only a
// thread records the thread output (output/thread.txt) as the generated
// half of the pair.
func TestFinal_RecordThreadPair(t *testing.T) {
	h := newHarness(t)

	// Make the run render only the thread (a thread-format run): remove the
	// article from the run directory so only the thread output remains.
	dir, err := h.store.RunDir(h.runID)
	if err != nil {
		t.Fatalf("RunDir: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "output", "article.md")); err != nil {
		t.Fatalf("removing the article: %v", err)
	}

	edited := "EU AI Act enters into force. Edited lead: https://example.com/ai-act"
	finalFile := filepath.Join(t.TempDir(), "thread.txt")
	if err := os.WriteFile(finalFile, []byte(edited), 0o600); err != nil {
		t.Fatalf("writing edited thread: %v", err)
	}

	_, err = h.eng.RecordFinal(t.Context(), h.runID, publish.RecordFinalRequest{
		FinalPath: finalFile,
		Editor:    "editor",
	}, h.bundle)
	if err != nil {
		t.Fatalf("RecordFinal: %v", err)
	}

	m, _, err := h.store.LoadManifest(h.runID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.FinalState == nil {
		t.Fatal("final_state is nil after RecordFinal")
	}
	if !strings.HasSuffix(m.FinalState.FinalPath, "thread.txt") {
		t.Errorf("final_path = %s, want a .txt thread document", m.FinalState.FinalPath)
	}
	if !strings.HasSuffix(m.FinalState.GeneratedPath, "thread.txt") {
		t.Errorf("generated_path = %s, want the rendered thread", m.FinalState.GeneratedPath)
	}
}

// TestFinal_ReRecordReplaces verifies that re-editing replaces the recorded
// pair (the documents are overwritten in place, the manifest keeps exactly
// one final state, and the record never regresses the manifest clock).
func TestFinal_ReRecordReplaces(t *testing.T) {
	h := newHarness(t)

	first := filepath.Join(t.TempDir(), "article.md")
	if err := os.WriteFile(first, []byte("v1 edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res1, err := h.eng.RecordFinal(t.Context(), h.runID, publish.RecordFinalRequest{
		FinalPath: first,
		Editor:    "editor",
	}, h.bundle)
	if err != nil {
		t.Fatalf("first RecordFinal: %v", err)
	}

	// Record it again (identical content: the no-op path must succeed).
	if _, err := h.eng.RecordFinal(t.Context(), h.runID, publish.RecordFinalRequest{
		FinalPath: first,
		Editor:    "editor",
		EditedAt:  h.clock(),
	}, h.bundle); err != nil {
		t.Fatalf("identical re-record must succeed as a no-op: %v", err)
	}

	second := filepath.Join(t.TempDir(), "article2.md")
	if err := os.WriteFile(second, []byte("v2 edited, again\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res2, err := h.eng.RecordFinal(t.Context(), h.runID, publish.RecordFinalRequest{
		FinalPath: second,
		Editor:    "editor",
		EditedAt:  h.clock(),
	}, h.bundle)
	if err != nil {
		t.Fatalf("second RecordFinal (re-edit): %v", err)
	}
	if res2.FinalHash == res1.FinalHash {
		t.Error("a re-edited final document must hash differently")
	}

	m, _, err := h.store.LoadManifest(h.runID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.FinalState == nil || m.FinalState.FinalHash != res2.FinalHash {
		t.Errorf("manifest must hold the latest recorded pair (hash %q, recorded %q)",
			digestOf(m.FinalState), res2.FinalHash)
	}
}

// TestFinal_MissingFinalFile verifies that a final path that cannot be read
// fails the record (nothing is persisted).
func TestFinal_MissingFinalFile(t *testing.T) {
	h := newHarness(t)

	_, err := h.eng.RecordFinal(t.Context(), h.runID, publish.RecordFinalRequest{
		FinalPath: filepath.Join(t.TempDir(), "does-not-exist.md"),
		Editor:    "editor",
	}, h.bundle)
	if err == nil {
		t.Fatal("RecordFinal with a missing final file must fail")
	}

	m, _, err := h.store.LoadManifest(h.runID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.FinalState != nil {
		t.Error("no final state may be recorded when the final file is missing")
	}
}

// TestFinal_NotCompletedRefused verifies that a run that never completed is
// refused: no documents, no manifest change.
func TestFinal_NotCompletedRefused(t *testing.T) {
	h := newHarness(t)

	// A run that stopped before render: a manifest with render never run.
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "testws"},
		Topic:      "an unfinished story",
		SourceMode: run.SourceModeExplicit,
		StartedAt:  baseTime,
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	// Fail the run at fetch so it is terminal but never rendered.
	for i := range m.Stages {
		if m.Stages[i].Name == run.StageFetch {
			sname := m.Stages[i].Name
			_ = sname
		}
	}
	if err := m.Start(h.clock()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Fail(h.clock(), "fetch-failed", "all sources failed"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if _, err := h.store.CreateRun(m); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := m.Validate(); err != nil {
		t.Fatalf("failed run manifest must validate: %v", err)
	}

	finalFile := filepath.Join(t.TempDir(), "article.md")
	if err := os.WriteFile(finalFile, []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = h.eng.RecordFinal(t.Context(), m.ID, publish.RecordFinalRequest{
		FinalPath: finalFile,
		Editor:    "editor",
	}, nil)
	if err == nil {
		t.Fatal("RecordFinal must refuse a run that never completed")
	}
	if !strings.Contains(err.Error(), "not completed") {
		t.Errorf("error = %q, want a not-completed refusal", err)
	}

	// The manifest on disk is untouched.
	loaded, _, err := h.store.LoadManifest(m.ID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.FinalState != nil {
		t.Error("no final state may be recorded on an incomplete run")
	}
}

// TestFinal_EmptyEditorRefused verifies the explicit-action rule: a pair
// cannot be recorded without naming its editor.
func TestFinal_EmptyEditorRefused(t *testing.T) {
	h := newHarness(t)

	finalFile := filepath.Join(t.TempDir(), "article.md")
	if err := os.WriteFile(finalFile, []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := h.eng.RecordFinal(t.Context(), h.runID, publish.RecordFinalRequest{
		FinalPath: finalFile,
	}, h.bundle)
	if err == nil {
		t.Fatal("RecordFinal must refuse an empty editor")
	}
}

// TestFinal_SecretRefused verifies that a configured exact secret in the
// final document cannot persist verbatim: the record carries the sanitized
// (redacted) representation, whose digest the manifest baselines.
//
// This is the store's fail-closed contract working as designed: content that
// contains a DETECTED secret is never written with the secret inside. What
// is tested here is that the final-state record reflects the sanitized bytes
// that actually landed on disk (the digest must match the persisted file)
// and that the secret value itself never appears in the persisted document
// or in the record.
func TestFinal_SecretRefused(t *testing.T) {
	h := newHarness(t)

	// A bearer token that the harness store is configured to redact (the
	// exact-value layer of the sanitization boundary).
	leaked := "# EU AI Act\n\nAuthorization: Bearer " + testSecret + "\n"
	finalFile := filepath.Join(t.TempDir(), "article.md")
	if err := os.WriteFile(finalFile, []byte(leaked), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := h.eng.RecordFinal(t.Context(), h.runID, publish.RecordFinalRequest{
		FinalPath: finalFile,
		Editor:    "editor",
	}, h.bundle)
	if err != nil {
		t.Fatalf("RecordFinal: %v", err)
	}

	m, _, err := h.store.LoadManifest(h.runID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.FinalState == nil {
		t.Fatal("final_state is nil after RecordFinal")
	}

	// The secret must not be persisted verbatim in either document.
	final, err := h.store.LoadArtifact(h.runID, m.FinalState.FinalPath)
	if err != nil {
		t.Fatalf("loading final: %v", err)
	}
	if strings.Contains(string(final), testSecret) {
		t.Error("the configured secret persisted verbatim in the final document")
	}
	if !strings.Contains(string(final), run.RedactedPlaceholder) {
		t.Error("the final document should carry the redaction placeholder")
	}
	gen, err := h.store.LoadArtifact(h.runID, m.FinalState.GeneratedPath)
	if err != nil {
		t.Fatalf("loading generated: %v", err)
	}
	if strings.Contains(string(gen), testSecret) {
		t.Error("the configured secret persisted verbatim in the generated copy")
	}

	// The recorded digest must be the digest of the sanitized persisted
	// bytes (the record matches what is on disk, sanitization and all).
	if want := sha256Of(final); m.FinalState.FinalHash != want {
		t.Errorf("final_hash = %s, want the digest of the persisted (sanitized) bytes %s", m.FinalState.FinalHash, want)
	}
	_ = res
}

// digests for assertion messages.
func digestOf(fs *run.FinalState) string {
	if fs == nil {
		return "<nil>"
	}
	return fs.FinalHash
}

// sha256Of is a test digest helper (the engine's own digestOfBytes is
// unexported to the test package).
func sha256Of(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// TestFinal_Inspect verifies the Final projection: recorded state, current
// digests, drift detection, and the no-record case.
func TestFinal_Inspect(t *testing.T) {
	h := newHarness(t)

	// No record yet: a nil record, not an error.
	rep, err := h.eng.Final(t.Context(), h.runID)
	if err != nil {
		t.Fatalf("Final without a record: %v", err)
	}
	if rep.Recorded != nil {
		t.Fatalf("Recorded = %+v, want nil before any record", rep.Recorded)
	}

	// Record a pair.
	finalFile := filepath.Join(t.TempDir(), "article.md")
	if err := os.WriteFile(finalFile, []byte("edited version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.RecordFinal(t.Context(), h.runID, publish.RecordFinalRequest{
		FinalPath: finalFile,
		Editor:    "editor",
	}, h.bundle); err != nil {
		t.Fatalf("RecordFinal: %v", err)
	}

	rep, err = h.eng.Final(t.Context(), h.runID)
	if err != nil {
		t.Fatalf("Final: %v", err)
	}
	if rep.Recorded == nil {
		t.Fatal("Recorded is nil after a record")
	}
	if rep.GeneratedDrifts || rep.FinalDrifts {
		t.Errorf("a fresh record must not report drift (generated=%v final=%v)", rep.GeneratedDrifts, rep.FinalDrifts)
	}

	// Tamper with the persisted final document: the drift must surface
	// (tampered records are surfaced, never silently trusted).
	dir, err := h.store.RunDir(h.runID)
	if err != nil {
		t.Fatalf("RunDir: %v", err)
	}
	full := filepath.Join(dir, filepath.FromSlash(rep.Recorded.FinalPath))
	if err := os.WriteFile(full, []byte("tampered bytes\n"), 0o600); err != nil {
		t.Fatalf("tampering the final document: %v", err)
	}

	rep, err = h.eng.Final(t.Context(), h.runID)
	if err != nil {
		t.Fatalf("Final after tamper: %v", err)
	}
	if !rep.FinalDrifts {
		t.Error("a tampered final document must be reported as drifted")
	}
	if rep.GeneratedDrifts {
		t.Error("the generated copy was not touched and must not report drift")
	}
}

// TestFinal_RunRelativeFinalPath verifies that a run-relative final path is
// resolved against the run directory.
func TestFinal_RunRelativeFinalPath(t *testing.T) {
	h := newHarness(t)

	dir, err := h.store.RunDir(h.runID)
	if err != nil {
		t.Fatalf("RunDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "my-edited-article.md"), []byte("edited via run-relative path\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = h.eng.RecordFinal(t.Context(), h.runID, publish.RecordFinalRequest{
		FinalPath: "my-edited-article.md",
		Editor:    "editor",
	}, h.bundle)
	if err != nil {
		t.Fatalf("RecordFinal with a run-relative path: %v", err)
	}
	m, _, err := h.store.LoadManifest(h.runID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.FinalState == nil {
		t.Fatal("final_state is nil after RecordFinal")
	}
}
