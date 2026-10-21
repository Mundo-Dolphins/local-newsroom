// Tests for the editorial preset -> publication destination bridge.
//
// The store is a fake (the interface is deliberately small); the Hugo
// renderer and the thread renderer are the real, pure packages, so these
// tests verify the bridge's content canonicalization exactly as production
// computes it, without a checkout or a network.

package presets_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/editorial"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish/presets"
	"github.com/Mundo-Dolphins/local-newsroom/internal/renderers"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

var testTime = time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeStore serves the run's artifacts from an in-memory map.
type fakeStore struct {
	manifest  *run.Manifest
	artifacts map[string][]byte
}

func (s *fakeStore) LoadArtifact(_ run.RunID, relPath string) ([]byte, error) {
	data, ok := s.artifacts[relPath]
	if !ok {
		return nil, errors.New(relPath + ": no such file")
	}
	return data, nil
}

func (s *fakeStore) LoadManifest(_ run.RunID) (*run.Manifest, string, error) {
	return s.manifest, "sha256:deadbeef", nil
}

func fmtSprintInt(n int) string { return strconv.Itoa(n) }

// testManifest builds a succeeded run manifest with a fixed identity.
func testManifest(t *testing.T) *run.Manifest {
	t.Helper()
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  run.Workspace{Name: "testws"},
		Topic:      "Test topic",
		SourceMode: run.SourceModeExplicit,
		StartedAt:  testTime,
	})
	if err != nil {
		t.Fatalf("NewManifest: %v", err)
	}
	m.Status = run.RunStatusSucceeded
	return m
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

func articleArtifact() *contracts.EditorialArtifact {
	return &contracts.EditorialArtifact{
		StableID:     "artifact-test-1",
		ArtifactType: contracts.ArtifactTypeArticle,
		Title:        "Council Approves Budget as Renewables Grow",
		Subtitle:     "Budget and energy in the same week",
		Body: "The city council approved the annual budget. " +
			"Local renewable energy capacity grew 15 percent in 2023.",
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "verification-test-1",
			GeneratedAt:         testTime,
		},
	}
}

func threadArtifact() *contracts.EditorialArtifact {
	return &contracts.EditorialArtifact{
		StableID:     "artifact-test-1",
		ArtifactType: contracts.ArtifactTypeThread,
		Title:        "Budget + renewables in one week",
		Posts: []contracts.Post{
			{Order: 0, Body: "The city council approved the annual budget on January 10, 2024. (1/2)"},
			{Order: 1, Body: "Local renewable energy capacity grew 15 percent in 2023. (2/2)"},
		},
		GenerationMetadata: contracts.GenerationMetadata{
			InputVerificationID: "verification-test-1",
			GeneratedAt:         testTime,
		},
	}
}

func artifactJSON(t *testing.T, a *contracts.EditorialArtifact) []byte {
	t.Helper()
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshaling artifact: %v", err)
	}
	return data
}

// testPreset is the editorial preset the bridge resolves: both destinations.
func testPreset(siteRoot string) *editorial.Preset {
	return &editorial.Preset{
		Name: "demo",
		Hash: "abcdef0123456789",
		Hugo: &editorial.HugoDestination{
			Destination: siteRoot,
			BaseURL:     "https://example.com",
		},
		Bluesky: &editorial.BlueskyDestination{
			Handle: "@demo",
			APIKey: config.APIKey{Env: "TEST_KEY"},
		},
	}
}

// newSiteRoot creates the Hugo project root the preset points at.
func newSiteRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "content"), 0o755); err != nil {
		t.Fatalf("creating site root: %v", err)
	}
	return root
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestNew_ArticleRunResolvesBothDestinations verifies destination
// construction for an article run: canonical order, content canonicalization
// (staged Hugo file, NUL-joined thread), and digests.
func TestNew_ArticleRunResolvesBothDestinations(t *testing.T) {
	siteRoot := newSiteRoot(t)
	art := articleArtifact()
	store := &fakeStore{
		manifest: testManifest(t),
		artifacts: map[string][]byte{
			run.ArtifactPathArtifact: artifactJSON(t, art),
			run.ArtifactPathArticle:  []byte("# Council Approves Budget\n\nBody text.\n"),
		},
	}

	p, err := presets.New(testPreset(siteRoot), "/bundle", run.RunID("run-test-1"), store)
	if err != nil {
		t.Fatalf("presets.New: %v", err)
	}

	if len(p.Destinations) != 2 {
		t.Fatalf("got %d destinations, want 2", len(p.Destinations))
	}
	if got := p.Keys(); len(got) != 2 || got[0] != presets.HugoKey || got[1] != presets.BlueskyKey {
		t.Fatalf("keys = %v, want [hugo bluesky]", got)
	}
	if p.Name != "demo" || p.Hash != "abcdef0123456789" {
		t.Errorf("identity = (%q %q), want (demo abcdef0123456789)", p.Name, p.Hash)
	}

	hugoDest := p.Destinations[0]
	if hugoDest.Target.ID != "site" || hugoDest.Target.Type != publication.TargetTypeRepository || hugoDest.Target.Provider != "git" {
		t.Errorf("hugo target = %+v, want site/repository/git", hugoDest.Target)
	}
	if hugoDest.Content == nil || hugoDest.ContentErr != nil {
		t.Fatalf("hugo destination content missing or errored: %v", hugoDest.ContentErr)
	}
	// The staged file carries front matter and the article body.
	if !strings.Contains(string(hugoDest.Content), "title:") || !strings.Contains(string(hugoDest.Content), "Body text.") {
		t.Errorf("staged content lacks front matter or body:\n%s", hugoDest.Content)
	}
	// The digest is the canonical digest of the staged bytes.
	if got := publication.DigestOf(hugoDest.Content); got != hugoDest.Digest {
		t.Errorf("hugo digest %s != digest of its content %s", hugoDest.Digest, got)
	}
	// The staged path resolves relative to the content directory.
	if !strings.HasPrefix(hugoDest.ContentName, "hugo/content/") {
		t.Errorf("hugo content name = %q, want it under hugo/content/", hugoDest.ContentName)
	}

	tsky := p.Destinations[1]
	if tsky.Target.ID != "thread" || tsky.Target.Provider != "bluesky" {
		t.Errorf("bluesky target = %+v, want thread/bluesky", tsky.Target)
	}
	// The canonical thread is the NUL-joined re-render of the artifact.
	renderer := renderers.NewThreadRenderer()
	posts, err := renderer.Render(art)
	if err != nil {
		t.Fatalf("thread render: %v", err)
	}
	want, err := publish.ThreadContent(posts...)
	if err != nil {
		t.Fatalf("ThreadContent: %v", err)
	}
	if string(tsky.Content) != string(want) {
		t.Errorf("bluesky content = %q, want the canonical NUL-joined thread %q", tsky.Content, want)
	}
	if tsky.Facts["posts"] != fmtSprintInt(len(posts)) {
		t.Errorf("posts fact = %q, want %d (an article run threads from its body)", tsky.Facts["posts"], len(posts))
	}
}

// TestNew_ThreadRunLeavesHugoUnavailable: a thread artifact has
// no rendered article, so the Hugo destination is kept with its error and
// the Bluesky destination is fully prepared.
func TestNew_ThreadRunLeavesHugoUnavailable(t *testing.T) {
	siteRoot := newSiteRoot(t)
	art := threadArtifact()
	store := &fakeStore{
		manifest: testManifest(t),
		artifacts: map[string][]byte{
			// A thread run renders output/thread.txt, not output/article.md.
			run.ArtifactPathArtifact: artifactJSON(t, art),
		},
	}

	p, err := presets.New(testPreset(siteRoot), "/bundle", run.RunID("run-test-1"), store)
	if err != nil {
		t.Fatalf("presets.New: %v", err)
	}

	if p.Destinations[0].ContentErr == nil {
		t.Fatal("hugo destination on a thread run must carry a content error")
	}
	if p.Destinations[0].Content != nil {
		t.Error("hugo destination must have no content when errored")
	}
	if p.Destinations[1].ContentErr != nil || len(p.Destinations[1].Content) == 0 {
		t.Fatalf("bluesky destination should be fully prepared, got err=%v", p.Destinations[1].ContentErr)
	}
}

// TestNew_MissingWriteArtifactIsAnError: without the run's write artifact
// the preset cannot be resolved at all.
func TestNew_MissingWriteArtifactIsAnError(t *testing.T) {
	siteRoot := newSiteRoot(t)
	store := &fakeStore{
		manifest:  testManifest(t),
		artifacts: map[string][]byte{},
	}
	if _, err := presets.New(testPreset(siteRoot), "/bundle", run.RunID("run-test-1"), store); err == nil {
		t.Fatal("expected an error for a missing write artifact")
	}
}

// TestSelect verifies --target-style selection: keys, target IDs, and the
// hard error on an unknown key.
func TestSelect(t *testing.T) {
	siteRoot := newSiteRoot(t)
	art := articleArtifact()
	store := &fakeStore{
		manifest: testManifest(t),
		artifacts: map[string][]byte{
			run.ArtifactPathArtifact: artifactJSON(t, art),
			run.ArtifactPathArticle:  []byte("body"),
		},
	}
	p, err := presets.New(testPreset(siteRoot), "/bundle", run.RunID("run-test-1"), store)
	if err != nil {
		t.Fatalf("presets.New: %v", err)
	}

	if got, err := p.Select(); err != nil || len(got) != 2 {
		t.Fatalf("Select() = %d, %v; want all 2", len(got), err)
	}
	if got, err := p.Select(presets.BlueskyKey); err != nil || len(got) != 1 || got[0].Key != presets.BlueskyKey {
		t.Fatalf("Select(bluesky) = %v, %v", got, err)
	}
	if got, err := p.Select("thread"); err != nil || len(got) != 1 || got[0].Key != presets.BlueskyKey {
		t.Fatalf("Select(thread) by target ID = %v, %v", got, err)
	}
	if _, err := p.Select("mastodon"); err == nil {
		t.Fatal("Select(mastodon) must fail: unknown keys are a hard error")
	}
}

// TestPublishers verifies the factory seam: the injected factory builds the
// publisher, and a plan whose target the preset does not know fails.
func TestPublishers(t *testing.T) {
	siteRoot := newSiteRoot(t)
	art := articleArtifact()
	store := &fakeStore{
		manifest: testManifest(t),
		artifacts: map[string][]byte{
			run.ArtifactPathArtifact: artifactJSON(t, art),
			run.ArtifactPathArticle:  []byte("body"),
		},
	}
	calls := 0
	p, err := presets.New(testPreset(siteRoot), "/bundle", run.RunID("run-test-1"), store,
		presets.WithFactory(func(_ context.Context, d *presets.Destination, planID publication.PublicationID) (publish.Publisher, error) {
			calls++
			return publish.PublisherFunc(func(_ context.Context, content []byte, prior *publication.PublicationReceipt) (*publication.PublicationReceipt, error) {
				_ = prior
				return publication.NewReceipt(planID, d.Target.ID, d.Target.Provider,
					"https://example.com/x", "rec-1", publication.DigestOf(content),
					"idem-1", "test/0.6", nil, testTime), nil
			}), nil
		}))
	if err != nil {
		t.Fatalf("presets.New: %v", err)
	}

	plan := testPlan(t, p, run.RunID("run-test-1"))
	pubs, err := p.Publishers(context.Background(), []*publication.Plan{plan})
	if err != nil {
		t.Fatalf("Publishers: %v", err)
	}
	if len(pubs) != 1 {
		t.Fatalf("got %d publishers, want 1", len(pubs))
	}
	if calls != 1 {
		t.Errorf("factory called %d times, want 1", calls)
	}

	// A plan whose target the preset does not know cannot be built.
	other := &publication.Plan{
		ID:      plan.ID,
		Targets: []publication.PublicationTarget{{ID: "mastodon", Status: publication.StatusPlanned}},
	}
	if _, err := p.Publishers(context.Background(), []*publication.Plan{other}); err == nil {
		t.Fatal("Publishers for an unknown target must fail")
	}
}

// TestPublishers_ContentErroredDestinationCannotBuild: a destination that
// failed content preparation cannot be published (its publisher build fails).
func TestPublishers_ContentErroredDestinationCannotBuild(t *testing.T) {
	siteRoot := newSiteRoot(t)
	art := threadArtifact() // no rendered article -> hugo destination is errored
	store := &fakeStore{
		manifest: testManifest(t),
		artifacts: map[string][]byte{
			run.ArtifactPathArtifact: artifactJSON(t, art),
		},
	}
	p, err := presets.New(testPreset(siteRoot), "/bundle", run.RunID("run-test-1"), store,
		presets.WithFactory(func(_ context.Context, _ *presets.Destination, _ publication.PublicationID) (publish.Publisher, error) {
			return publish.PublisherFunc(func(context.Context, []byte, *publication.PublicationReceipt) (*publication.PublicationReceipt, error) {
				return nil, nil
			}), nil
		}))
	if err != nil {
		t.Fatalf("presets.New: %v", err)
	}
	hugoPlan := testPlan(t, p, run.RunID("run-test-1")) // the plan for the (errored) hugo target
	if _, err := p.Publishers(context.Background(), []*publication.Plan{hugoPlan}); err == nil {
		t.Fatal("publishers for a content-errored destination must fail")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// testPlan builds a one-target plan for the destination's target, with a
// consistent identity (the plan ID derives from the artifact, as in the
// engine).
func testPlan(t *testing.T, p *presets.Preset, runID run.RunID) *publication.Plan {
	t.Helper()
	d := p.Destinations[0]
	artifact := publication.NewArtifactIdentity(string(d.Target.ID), run.ArtifactKindCustom, d.ContentPath(), d.Content, "test/v1")
	id := publication.NewPublicationID(runID, artifact, []publication.TargetID{d.Target.ID}, testTime)
	return &publication.Plan{
		Workspace: "testws",
		ID:        id,
		RunID:     runID,
		Status:    publication.StatusPlanned,
		Artifact:  artifact,
		Targets: []publication.PublicationTarget{
			{ID: d.Target.ID, Name: d.Target.Name, Type: d.Target.Type, Provider: d.Target.Provider, Status: publication.StatusPlanned},
		},
		Gate: publication.GateEvidence{},
	}
}
