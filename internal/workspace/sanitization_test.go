package workspace_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// Unmistakable sentinels: any of these strings appearing in a persisted file
// is a test failure by construction.
//
//   - sentinelA is a low-entropy value: only the EXACT-value path (configured
//     secrets) and high-confidence pattern contexts (Bearer tokens, key
//     assignments) catch it.
//   - sentinelB is a high-entropy mixed-class value: pattern detectors catch
//     it even in bare prose, in addition to the exact-value path.
const (
	sentinelA = "TEST_SECRET_DO_NOT_PERSIST_123456"
	sentinelB = "TEST_SECRET_DO_NOT_PERSIST_xK9mQ2vN8pR5tW1yZ4cD7fG0hJ"
)

// newSentinelStore opens a workspace whose sanitizer knows both sentinels as
// exact configured secret values — the path real deployments use for
// OMLX_API_KEY / SEARXNG_API_KEY / EMBEDDING_API_KEY.
func newSentinelStore(t *testing.T) *workspace.Store {
	t.Helper()
	store, err := workspace.NewStoreWithSanitizer(t.TempDir(), run.NewSanitizer(sentinelA, sentinelB))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// requireNoSentinels fails the test if either sentinel appears in data.
func requireNoSentinels(t *testing.T, data []byte) {
	t.Helper()
	if bytes.Contains(data, []byte(sentinelA)) || bytes.Contains(data, []byte(sentinelB)) {
		t.Errorf("persisted content contains a sentinel:\n%s", data)
	}
}

func newArtifactRef(i int, kind run.ArtifactKind, path string) run.ArtifactRef {
	return run.ArtifactRef{
		Kind:       kind,
		ID:         fmt.Sprintf("art-%02d", i),
		Stage:      run.CanonicalStages()[1],
		Path:       path,
		ProducedAt: run.NewTimestamp(baseTime),
	}
}

// TestArtifactKindsSanitizeBeforePersist covers the artifact-side of the
// 14 required scenarios: every artifact kind a run can persist is sanitized
// by the store before writing, with a redaction sidecar when content
// changed.
func TestArtifactKindsSanitizeBeforePersist(t *testing.T) {
	store := newSentinelStore(t)
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}

	scenarios := []struct {
		kind run.ArtifactKind
		path string
		body string
	}{
		{run.ArtifactKindCustom, "search-plan.json", `{"query": "EU AI Act", "api_token": "` + sentinelA + `"}`},
		{run.ArtifactKindCustom, "discovery.json", `{"results": [{"url": "https://example.com/?key=` + sentinelB + `"}]}`},
		{run.ArtifactKindSource, "sources/raw-1.md", "page text " + sentinelA + " more text\n"},
		{run.ArtifactKindDocument, "documents/doc-1.md", "# Doc\n\n" + sentinelB + "\n"},
		{run.ArtifactKindDossier, "dossier.json", `{"claims": ["the source said "` + sentinelA + `"]}`},
		{run.ArtifactKindVerification, "verification.json", `{"ok": true, "token": "` + sentinelB + `"}`},
		{run.ArtifactKindEditorial, "editorial.json", `{"draft": "auth bearer ` + sentinelA + ` quoted by the model}`},
		{run.ArtifactKindFactualCheck, "final-check.json", `{"checked": true, "password": "` + sentinelA + `"}`},
		{run.ArtifactKindCustom, "review-bundle.md", "Reviewer 1: the page embeds " + sentinelB + "\n"},
		{run.ArtifactKindRendered, "output/article.html", `<html><body>leak: <code>` + sentinelA + `</code></body></html>`},
		{run.ArtifactKindCustom, "warnings.json", `{"message": "fetch saw Authorization: Bearer ` + sentinelA + `"}`},
		{run.ArtifactKindCustom, "attempts.json", `{"attempt": 1, "client_secret": "` + sentinelB + `"}`},
		{run.ArtifactKindCustom, "notes.md", "scratch: " + sentinelB + " " + sentinelA + "\n"},
	}

	for i, sc := range scenarios {
		if _, err := store.SaveArtifact(m.ID, newArtifactRef(i, sc.kind, sc.path), []byte(sc.body)); err != nil {
			t.Fatalf("scenario %d (%s): SaveArtifact: %v", i, sc.path, err)
		}
		data, err := store.LoadArtifact(m.ID, sc.path)
		if err != nil {
			t.Fatalf("scenario %d: LoadArtifact: %v", i, err)
		}
		requireNoSentinels(t, data)
		if !bytes.Contains(data, []byte(run.RedactedPlaceholder)) {
			t.Errorf("scenario %d (%s): no redaction placeholder in persisted content:\n%s", i, sc.path, data)
		}
		if _, err := os.Stat(filepath.Join(store.RunsDir(), m.ID.String(), sc.path+workspace.RedactionMetaSuffix)); err != nil {
			t.Errorf("scenario %d (%s): redaction sidecar missing: %v", i, sc.path, err)
		}
	}
}

// TestManifestRejectsConfiguredSecretValue covers scenario 1 (the manifest):
// a manifest that would persist a detected secret is refused without
// side effects.
func TestManifestRejectsConfiguredSecretValue(t *testing.T) {
	store := newSentinelStore(t)

	// A topic embedding a configured exact secret value (bare prose: only the
	// exact-value path detects it) is refused by the byte-level gate before
	// any manifest file is written.
	leaky := newTestManifest(t)
	leaky.Topic = "EU AI Act " + sentinelA + " enters into force"
	_, err := store.CreateRun(leaky)
	if err == nil {
		t.Fatal("CreateRun accepted a manifest containing a configured secret value")
	}
	if !errors.Is(err, workspace.ErrManifestInvalid) {
		t.Fatalf("CreateRun error = %v, want ErrManifestInvalid", err)
	}
	if strings.Contains(err.Error(), sentinelA) {
		t.Errorf("error message leaks the secret value: %v", err)
	}
	manifestPath := filepath.Join(store.RunsDir(), leaky.ID.String(), workspace.ManifestFileName)
	if _, statErr := os.Stat(manifestPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("manifest file exists after rejection (stat err = %v)", statErr)
	}

	// The same value in a Bearer context is caught by pattern redaction at
	// manifest construction: the run is created, and the persisted manifest
	// carries the placeholder, not the secret.
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace: run.Workspace{Name: "testws"},
		Topic:     "EU AI Act enters into force (Authorization: Bearer " + sentinelA + ")",
		StartedAt: baseTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(m); err != nil {
		t.Fatalf("CreateRun with pattern-redacted topic: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(store.RunsDir(), m.ID.String(), workspace.ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	requireNoSentinels(t, data)
	if !bytes.Contains(data, []byte(run.RedactedPlaceholder)) {
		t.Errorf("manifest not redacted: %s", data)
	}

	// A configured secret value in the config snapshot is refused by
	// validation.
	m2 := newTestManifest(t)
	m2.Config = run.ConfigSnapshot{Values: map[string]string{"OMLX_API_KEY": sentinelB}}
	if _, err := store.CreateRun(m2); err == nil {
		t.Fatal("CreateRun accepted a manifest with a secret config value")
	}
}

// TestSourceAndDocumentHaveNoVerbatimException is the core policy change:
// fetched source and normalized document content is sanitized like every
// other artifact. Low-entropy values in bare prose (invisible to the pattern
// detectors) are still removed via the exact-value path.
func TestSourceAndDocumentHaveNoVerbatimException(t *testing.T) {
	store := newSentinelStore(t)
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}

	src := []byte("fetched page body: " + sentinelA + " trailing words\n")
	if _, err := store.SaveArtifact(m.ID, newArtifactRef(0, run.ArtifactKindSource, "sources/raw-1.md"), src); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadArtifact(m.ID, "sources/raw-1.md")
	if err != nil {
		t.Fatal(err)
	}
	requireNoSentinels(t, got)
	if bytes.Equal(got, src) {
		t.Error("source content persisted verbatim despite containing a configured secret value")
	}
	if _, err := os.Stat(filepath.Join(store.RunsDir(), m.ID.String(), "sources", "raw-1.md"+workspace.RedactionMetaSuffix)); err != nil {
		t.Errorf("redaction sidecar missing: %v", err)
	}

	doc := []byte("# Normalized document\n\nThe page echoed " + sentinelB + " verbatim.\n")
	if _, err := store.SaveArtifact(m.ID, newArtifactRef(1, run.ArtifactKindDocument, "documents/doc-1.md"), doc); err != nil {
		t.Fatal(err)
	}
	got, err = store.LoadArtifact(m.ID, "documents/doc-1.md")
	if err != nil {
		t.Fatal(err)
	}
	requireNoSentinels(t, got)
	if bytes.Equal(got, doc) {
		t.Error("document content persisted verbatim despite containing a configured secret value")
	}
}

// TestBinaryContentExactValueRedaction pins the binary contract: binary
// bytes receive exact-value replacement, a sidecar is written, and no
// sentinel survives.
func TestBinaryContentExactValueRedaction(t *testing.T) {
	store := newSentinelStore(t)
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}

	bin := append([]byte{0x00, 0x01}, []byte(sentinelB)...)
	bin = append(bin, 0x02, 0xff)
	if _, err := store.SaveArtifact(m.ID, newArtifactRef(0, run.ArtifactKindDocument, "documents/blob.bin"), bin); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadArtifact(m.ID, "documents/blob.bin")
	if err != nil {
		t.Fatal(err)
	}
	requireNoSentinels(t, got)
	if !bytes.Contains(got, []byte(run.RedactedPlaceholder)) {
		t.Errorf("binary was not redacted: % x", got)
	}
	if _, err := os.Stat(filepath.Join(store.RunsDir(), m.ID.String(), "documents", "blob.bin"+workspace.RedactionMetaSuffix)); err != nil {
		t.Errorf("redaction sidecar missing: %v", err)
	}
}

// TestPatternOnlyStoreRedactsAtBoundary proves the boundary works even
// without exact values registered (pattern detection), and documents the
// detection limit that motivates exact-value registration.
func TestPatternOnlyStoreRedactsAtBoundary(t *testing.T) {
	store, err := workspace.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}
	ref := newArtifactRef(0, run.ArtifactKindSource, "sources/raw-1.md")

	// Bearer context: caught by the pattern detectors without any exact
	// values registered.
	if _, err := store.SaveArtifact(m.ID, ref, []byte("saw Authorization: Bearer "+sentinelA+" in the log\n")); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadArtifact(m.ID, "sources/raw-1.md")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte(sentinelA)) {
		t.Errorf("bearer-context secret was not redacted: %s", got)
	}

	// Bare low-entropy value with no exact registration: below the pattern
	// detection bar and written as-is — the documented detection boundary,
	// and exactly why configured secret values must be registered as exact
	// values (newSentinelStore).
	if _, err := store.SaveArtifact(m.ID, ref, []byte("body: "+sentinelA+"\n")); err != nil {
		t.Fatal(err)
	}
	got, err = store.LoadArtifact(m.ID, "sources/raw-1.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte(sentinelA)) {
		t.Errorf("unexpected redaction of bare low-entropy value without exact registration: %s", got)
	}
}

// TestRunTreeContainsNoSentinelRecursive is the workspace-level sweep: a
// representative run where every artifact kind embeds at least one sentinel
// in a different context, followed by a recursive scan of the entire run
// directory. No persisted byte — artifact, sidecar, or manifest — may carry
// a sentinel.
func TestRunTreeContainsNoSentinelRecursive(t *testing.T) {
	store := newSentinelStore(t)
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}

	bodies := []struct {
		kind run.ArtifactKind
		path string
		body string
	}{
		{run.ArtifactKindCustom, "search-plan.json", `{"q": "AI", "api_key": "` + sentinelA + `"}`},
		{run.ArtifactKindCustom, "discovery.json", `{"r": "https://example.com/?token=` + sentinelB + `&x=1"}`},
		{run.ArtifactKindSource, "sources/raw-1.md", "page: " + sentinelA + " end\n"},
		{run.ArtifactKindSource, "sources/raw-2.md", "page2 Authorization: Bearer " + sentinelB + " end\n"},
		{run.ArtifactKindDocument, "documents/doc-1.md", "# doc " + sentinelB + "\n"},
		{run.ArtifactKindDossier, "dossier.json", `{"claims": ["` + sentinelA + `"]}`},
		{run.ArtifactKindVerification, "verification.json", `{"ok": true, "client_id_secret": "` + sentinelB + `"}`},
		{run.ArtifactKindEditorial, "editorial.json", `{"draft": "bearer ` + sentinelA + `"}`},
		{run.ArtifactKindFactualCheck, "final-check.json", `{"pass": true, "password": "` + sentinelA + `"}`},
		{run.ArtifactKindCustom, "review-bundle.md", "review: " + sentinelB + " seen\n"},
		{run.ArtifactKindRendered, "output/article.html", "<p>" + sentinelA + "</p>"},
		{run.ArtifactKindCustom, "warnings.json", `{"message": "Bearer ` + sentinelB + `"}`},
		{run.ArtifactKindCustom, "attempts.json", `{"n": 1, "refresh_token": "` + sentinelA + `"}`},
		{run.ArtifactKindCustom, "notes.md", "both " + sentinelA + " " + sentinelB + "\n"},
		// A clean artifact too: the sweep must still pass and the content
		// must survive byte-identical.
		{run.ArtifactKindSource, "sources/clean.md", "a clean page with no secrets at all\n"},
	}
	for i, b := range bodies {
		if _, err := store.SaveArtifact(m.ID, newArtifactRef(i, b.kind, b.path), []byte(b.body)); err != nil {
			t.Fatalf("saving %s: %v", b.path, err)
		}
	}

	// Recursive sweep over the whole run directory.
	runDir := filepath.Join(store.RunsDir(), m.ID.String())
	sidecars := 0
	err := filepath.Walk(runDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		if bytes.Contains(data, []byte(sentinelA)) || bytes.Contains(data, []byte(sentinelB)) {
			t.Errorf("file %s contains a sentinel:\n%s", p, data)
		}
		if strings.HasSuffix(p, workspace.RedactionMetaSuffix) {
			sidecars++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking run dir: %v", err)
	}
	if sidecars == 0 {
		t.Error("no redaction sidecars were written despite redacted artifacts")
	}

	// The clean artifact survives byte-identical.
	got, err := store.LoadArtifact(m.ID, "sources/clean.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("a clean page with no secrets at all\n")) {
		t.Errorf("clean artifact was modified: %s", got)
	}

	// And the manifest carries no sentinel.
	data, err := os.ReadFile(filepath.Join(runDir, workspace.ManifestFileName))
	if err != nil {
		t.Fatalf("reading manifest: %v", err)
	}
	requireNoSentinels(t, data)
}

// TestReloadSeesSanitizedRepresentation pins the verification/reload
// contract: after the store is reopened (the restart case), persisted reads
// return the sanitized representation and the redaction metadata.
func TestReloadSeesSanitizedRepresentation(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.NewStoreWithSanitizer(root, run.NewSanitizer(sentinelA, sentinelB))
	if err != nil {
		t.Fatal(err)
	}
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveArtifact(m.ID, newArtifactRef(0, run.ArtifactKindSource, "sources/raw-1.md"),
		[]byte("page: "+sentinelA+" done\n")); err != nil {
		t.Fatal(err)
	}

	// Reopen the workspace (simulating a process restart).
	reopened, err := workspace.NewStoreWithSanitizer(root, run.NewSanitizer(sentinelA, sentinelB))
	if err != nil {
		t.Fatal(err)
	}
	list, err := reopened.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0] != m.ID {
		t.Fatalf("ListRuns = %v, want one run %s", list, m.ID)
	}
	got, err := reopened.LoadArtifact(m.ID, "sources/raw-1.md")
	if err != nil {
		t.Fatal(err)
	}
	requireNoSentinels(t, got)
	if !bytes.Contains(got, []byte(run.RedactedPlaceholder)) {
		t.Errorf("reloaded source not redacted: %s", got)
	}
	meta, err := reopened.LoadArtifact(m.ID, "sources/raw-1.md"+workspace.RedactionMetaSuffix)
	if err != nil {
		t.Fatalf("redaction metadata not loadable: %v", err)
	}
	if !bytes.Contains(meta, []byte(`"redacted": true`)) || !bytes.Contains(meta, []byte(`"redaction_count"`)) {
		t.Errorf("redaction metadata malformed: %s", meta)
	}
}

// TestStaleRedactionSidecarIsRemoved covers re-saving a previously redacted
// path with clean content: the sidecar must disappear so that its presence
// always means "redacted representation".
func TestStaleRedactionSidecarIsRemoved(t *testing.T) {
	store := newSentinelStore(t)
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}
	ref := newArtifactRef(0, run.ArtifactKindSource, "sources/raw-1.md")
	if _, err := store.SaveArtifact(m.ID, ref, []byte("page: "+sentinelA+"\n")); err != nil {
		t.Fatal(err)
	}
	metaPath := filepath.Join(store.RunsDir(), m.ID.String(), "sources", "raw-1.md"+workspace.RedactionMetaSuffix)
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("sidecar missing after redacted save: %v", err)
	}
	if _, err := store.SaveArtifact(m.ID, ref, []byte("page: now clean\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(metaPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale sidecar not removed (stat err = %v)", err)
	}
}

// TestSaveManifestGateRejectsConfiguredValue pins the reachable byte-level
// manifest gate: a manifest updated after creation to embed a configured
// exact secret value is refused (ErrManifestInvalid) and the on-disk
// manifest keeps the previous clean version.
func TestSaveManifestGateRejectsConfiguredValue(t *testing.T) {
	store := newSentinelStore(t)
	m := newTestManifest(t)
	if _, err := store.CreateRun(m); err != nil {
		t.Fatal(err)
	}

	// A new manifest object for the same run: later timestamp (so the
	// regression check passes) and a topic embedding the exact value.
	leaky, err := run.NewManifest(run.NewManifestParams{
		ID:        m.ID,
		Workspace: run.Workspace{Name: "testws"},
		Topic:     "EU AI Act " + sentinelA + " enters into force",
		StartedAt: baseTime.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.SaveManifest(leaky)
	if err == nil {
		t.Fatal("SaveManifest accepted a manifest containing a configured secret value")
	}
	if !errors.Is(err, workspace.ErrManifestInvalid) {
		t.Fatalf("SaveManifest error = %v, want ErrManifestInvalid", err)
	}
	if strings.Contains(err.Error(), sentinelA) {
		t.Errorf("error message leaks the secret: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(store.RunsDir(), m.ID.String(), workspace.ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	requireNoSentinels(t, data)
}

// TestSanitizationFailureContract documents the fail-closed error surface:
// if sanitization ever fails to remove every detected secret, the store
// refuses to write and returns ErrSanitization with a message that names the
// failure — and only the failure. (With the current exhaustive detector set
// the gate cannot trip through the public API; the reachable analogue, the
// manifest gate, is covered by TestManifestRejectsConfiguredSecretValue. This
// test pins the error contract so the guarantee survives future detector
// changes.)
func TestSanitizationFailureContract(t *testing.T) {
	msg := workspace.ErrSanitization.Error()
	if !strings.Contains(msg, "sanitization failed") {
		t.Errorf("ErrSanitization message = %q", msg)
	}
	for _, s := range []string{sentinelA, sentinelB} {
		if strings.Contains(msg, s) {
			t.Errorf("ErrSanitization message contains secret material: %q", msg)
		}
	}
}
