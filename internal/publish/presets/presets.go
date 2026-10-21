// Package presets bridges the external editorial publication preset
// (internal/editorial) to the publication engine (internal/publish) for one
// concrete run.
//
// The bridge resolves the preset into publication destinations. Each
// destination is exactly one plan target and consists of:
//
//   - the plan's target spec (a conservative, credential-free identity:
//     target ID, name, type, provider, destination),
//   - the canonical content bytes the plan baselines to, and
//   - the metadata the executable publisher needs (adapter config, file
//     path, title).
//
// Content preparation is deterministic and side-effect-free:
//
//   - the hugo destination converts the run's final article into its staged
//     Hugo file through the pure Hugo renderer, and baselines to the staged
//     bytes (front matter + body) exactly as the git publisher will commit
//     them;
//   - the bluesky destination re-renders the run's write artifact into its
//     thread through the deterministic thread renderer and baselines to the
//     canonical NUL-joined thread content (publish.ThreadContent).
//
// A destination whose content cannot be prepared for the run (a thread
// destination on an article-only run, a missing Hugo project) is kept with
// its error, so the presentation layer can report it and exclude it from
// the plan.
//
// Executable publishers are built through a Factory. The production
// factory wraps the real adapters (git publish for the site, the Bluesky
// posting API for the thread); the CLI and the tests may substitute fakes,
// so this package stays testable without a checkout or a network.
package presets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/bluesky"
	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/editorial"
	"github.com/Mundo-Dolphins/local-newsroom/internal/gitpub"
	"github.com/Mundo-Dolphins/local-newsroom/internal/hugo"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/publish"
	"github.com/Mundo-Dolphins/local-newsroom/internal/renderers"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// Destination keys, the values the CLI's --target flag accepts.
const (
	// HugoKey is the site destination (the Hugo project published through
	// git).
	HugoKey = "hugo"
	// BlueskyKey is the thread destination (the Bluesky posting API).
	BlueskyKey = "bluesky"
)

// AllKeys lists every destination key in canonical order (site before
// thread: a post announcing the article comes after the site commit).
var AllKeys = []string{HugoKey, BlueskyKey}

// ErrNoDestinations reports a preset that declares no usable destination.
var ErrNoDestinations = errors.New("publication preset declares no destination")

// Store is the persistence surface the bridge reads the run through: the
// final write artifact and the manifest. workspace.Store satisfies it.
type Store interface {
	LoadArtifact(id run.RunID, relPath string) ([]byte, error)
	LoadManifest(id run.RunID) (*run.Manifest, string, error)
}

// Factory builds the executable publisher for one destination of one plan.
// The publisher binds its receipts to the plan and target given.
type Factory func(ctx context.Context, d *Destination, planID publication.PublicationID) (publish.Publisher, error)

// Option customizes preset resolution.
type Option func(*Preset)

// WithFactory replaces the publisher factory. The production default builds
// the real adapters; the CLI injects fakes in tests.
func WithFactory(f Factory) Option {
	return func(p *Preset) {
		if f != nil {
			p.factory = f
		}
	}
}

// Preset is the publication preset resolved for one run: the preset
// identity plus one destination per declared publication target.
type Preset struct {
	// Name is the publication name (the preset's directory name).
	Name string
	// Hash is the digest of the resolved non-secret preset content.
	Hash string
	// BundleRoot is the editorial bundle directory the preset was loaded
	// from (relative destination paths resolve against it).
	BundleRoot string
	// Destinations holds one destination per declared publication target,
	// in canonical order (hugo, then bluesky).
	Destinations []*Destination

	factory Factory
}

// Destination is one publication target of the preset, resolved for the
// run: the plan's target spec, the canonical content the plan baselines to,
// and the metadata the publisher needs.
type Destination struct {
	// Key is the destination key ("hugo" or "bluesky").
	Key string
	// Target is the plan's target spec (exactly one target per plan).
	Target publish.TargetSpec
	// RunID is the originating run.
	RunID run.RunID
	// ContentName is the file name of the canonical content within the
	// run's publication/content directory (for display and persistence).
	ContentName string
	// Content is the canonical content bytes (nil when ContentErr is set).
	Content []byte
	// Digest is the canonical digest of Content.
	Digest publication.Digest
	// ContentErr is set when the content could not be prepared for this
	// destination (e.g. a thread destination on an article-only run).
	ContentErr error
	// Facts are display-only, non-secret destination facts for the
	// preview (site URL, handle, branch, post count, ...).
	Facts map[string]string

	// Adapter binding used by the production publisher factory.
	gitCfg     gitpub.Config
	gitRelPath string
	blueskyCfg bluesky.Config
	title      string
}

// ContentPath returns the run-relative path of the destination's canonical
// content (publication/content/<name>).
func (d *Destination) ContentPath() string {
	return path.Join(run.PublicationContentDir, d.ContentName)
}

// New resolves the editorial preset into a Preset for the run.
//
// It reads the run's final write artifact and manifest through the store,
// and computes every destination's canonical content deterministically. It
// performs no persistence (the presentation layer persists content through
// the engine before planning) and no external side effect.
func New(p *editorial.Preset, bundleRoot string, runID run.RunID, store Store, opts ...Option) (*Preset, error) {
	if p == nil {
		return nil, errors.New("no editorial preset to resolve")
	}
	man, _, err := store.LoadManifest(runID)
	if err != nil {
		return nil, fmt.Errorf("loading run %s: %v", runID, err)
	}
	raw, err := store.LoadArtifact(runID, run.ArtifactPathArtifact)
	if err != nil {
		return nil, fmt.Errorf("loading the run's write artifact (%s): %v", run.ArtifactPathArtifact, err)
	}
	var art contracts.EditorialArtifact
	if err := json.Unmarshal(raw, &art); err != nil {
		return nil, fmt.Errorf("parsing the write artifact: %v", err)
	}
	if err := art.Validate(); err != nil {
		return nil, fmt.Errorf("the run's write artifact is invalid: %v", err)
	}

	pr := &Preset{
		Name:       p.Name,
		Hash:       p.Hash,
		BundleRoot: bundleRoot,
		factory:    DefaultFactory,
	}
	for _, opt := range opts {
		opt(pr)
	}
	if p.Hugo != nil {
		pr.Destinations = append(pr.Destinations, newHugoDestination(p, pr.BundleRoot, runID, man, &art, store))
	}
	if p.Bluesky != nil {
		pr.Destinations = append(pr.Destinations, newBlueskyDestination(p, runID, &art))
	}
	if len(pr.Destinations) == 0 {
		return nil, fmt.Errorf("%w: preset %q", ErrNoDestinations, p.Name)
	}
	return pr, nil
}

// Keys returns the destination keys in canonical order.
func (p *Preset) Keys() []string {
	keys := make([]string, 0, len(p.Destinations))
	for _, d := range p.Destinations {
		keys = append(keys, d.Key)
	}
	return keys
}

// Select filters the destinations by key (empty selection: all of them).
//
// A key names a destination ("hugo", "bluesky") or an exact target ID
// ("site", "thread"). Unknown keys fail with the list of valid keys, so a
// typo in automation is a hard error, never a silent no-op.
func (p *Preset) Select(keys ...string) ([]*Destination, error) {
	if len(keys) == 0 {
		return p.Destinations, nil
	}
	var sel []*Destination
	for _, k := range keys {
		matched := false
		for _, d := range p.Destinations {
			if d.Key == k || string(d.Target.ID) == k {
				sel = append(sel, d)
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("unknown target %q: valid targets for preset %q are %s", k, p.Name, strings.Join(p.Keys(), ", "))
		}
	}
	return sel, nil
}

// Publishers builds the executable publishers for the given plans, one per
// plan, in the plans' order. Each publisher binds its receipts to its plan
// and the destination's target.
//
// A destination that failed content preparation cannot publish: building
// its publisher fails, so the caller reports the plan as blocked.
func (p *Preset) Publishers(ctx context.Context, plans []*publication.Plan) ([]publish.Publisher, error) {
	out := make([]publish.Publisher, 0, len(plans))
	for _, plan := range plans {
		if len(plan.Targets) == 0 {
			return nil, fmt.Errorf("plan %s has no targets", plan.ID)
		}
		var d *Destination
		for _, dd := range p.Destinations {
			if string(dd.Target.ID) == string(plan.Targets[0].ID) {
				d = dd
				break
			}
		}
		if d == nil {
			return nil, fmt.Errorf("plan %s has no destination for target %q in preset %q", plan.ID, plan.Targets[0].ID, p.Name)
		}
		if d.ContentErr != nil {
			return nil, fmt.Errorf("publisher for target %q: %v", d.Target.ID, d.ContentErr)
		}
		pub, err := p.factory(ctx, d, plan.ID)
		if err != nil {
			return nil, fmt.Errorf("publisher for target %q: %v", d.Target.ID, err)
		}
		out = append(out, pub)
	}
	return out, nil
}

// DefaultFactory is the production publisher wiring: the git publish bridge
// for the Hugo destination, the Bluesky posting bridge for the thread
// destination.
func DefaultFactory(ctx context.Context, d *Destination, planID publication.PublicationID) (publish.Publisher, error) {
	switch d.Key {
	case HugoKey:
		pub, err := gitpub.New(ctx, d.gitCfg)
		if err != nil {
			return nil, fmt.Errorf("hugo project: %v", err)
		}
		return publish.NewGitPublisher(pub, planID, d.Target.ID, d.gitRelPath, d.title, string(d.RunID)), nil
	case BlueskyKey:
		pub, err := bluesky.New(d.blueskyCfg)
		if err != nil {
			return nil, fmt.Errorf("bluesky account: %v", err)
		}
		return publish.NewBlueskyPublisher(pub, planID, d.Target.ID), nil
	}
	return nil, fmt.Errorf("no publisher for destination %q", d.Key)
}

// ---------------------------------------------------------------------------
// Destination construction
// ---------------------------------------------------------------------------

// newHugoDestination resolves the Hugo destination for the run: the final
// article converted into its staged Hugo file through the pure renderer.
//
// The plan baselines to the staged bytes (front matter + body): exactly
// what the git publisher commits. The stage is deterministic (slug, date
// from the run, body from the rendered output), never the wall clock.
func newHugoDestination(p *editorial.Preset, bundleRoot string, runID run.RunID, man *run.Manifest, art *contracts.EditorialArtifact, store Store) *Destination {
	root := p.Hugo.DestinationPath(bundleRoot)
	d := &Destination{
		Key:         HugoKey,
		RunID:       runID,
		ContentName: path.Join("hugo", "content", "staged.md"),
		Target: publish.TargetSpec{
			ID:          "site",
			Name:        "Hugo site",
			Type:        publication.TargetTypeRepository,
			Provider:    "git",
			Destination: p.Hugo.BaseURL,
		},
		Facts:      map[string]string{},
		gitCfg:     gitpub.Config{RepoRoot: root, TargetDir: "content"},
		title:      art.Title,
		gitRelPath: "content/staged.md",
	}
	if d.Target.Destination == "" {
		d.Target.Destination = root
	}

	// The render stage's output is the article body the stage wraps.
	markdown, err := store.LoadArtifact(runID, run.ArtifactPathArticle)
	if err != nil {
		d.ContentErr = fmt.Errorf("the run produced no rendered article (%s): %v; the hugo destination cannot be prepared", run.ArtifactPathArticle, err)
		return d
	}

	cfg := hugo.Config{
		Root:       root,
		BaseURL:    p.Hugo.BaseURL,
		ContentDir: "content",
	}
	if p.Hugo.Layout != "" {
		cfg.Defaults = map[string]string{"layout": p.Hugo.Layout}
	}
	draft := p.Hugo.Drafts
	cfg.Draft = &draft

	pub, err := hugo.New(cfg)
	if err != nil {
		d.ContentErr = fmt.Errorf("hugo project: %v", err)
		return d
	}

	date := man.StartedAt.Time()
	if date.IsZero() {
		date = man.UpdatedAt.Time()
	}
	staged, err := pub.Render(hugo.Article{
		Title:   art.Title,
		Body:    string(markdown),
		Date:    date,
		Summary: art.Subtitle,
		RunID:   string(runID),
	})
	if err != nil {
		d.ContentErr = fmt.Errorf("staging the article: %v", err)
		return d
	}

	// The staged path inside the project (e.g. content/news/20260711-slug.md).
	d.ContentName = path.Join("hugo", staged.Relative)
	d.gitRelPath = path.Clean(staged.Relative)
	if i := strings.IndexByte(d.gitRelPath, '/'); i >= 0 {
		// File.Path is relative to Config.TargetDir (the content dir).
		d.gitRelPath = d.gitRelPath[i+1:]
	}
	d.Content = staged.Content
	d.Digest = publication.DigestOf(staged.Content)
	d.Facts["path"] = staged.Relative
	if p.Hugo.BaseURL != "" {
		d.Facts["site"] = p.Hugo.BaseURL
	}
	if staged.Draft {
		d.Facts["draft"] = "true"
	} else {
		d.Facts["draft"] = "false"
	}
	return d
}

// newBlueskyDestination resolves the Bluesky destination for the run: the
// thread re-rendered from the write artifact, canonical NUL-joined content.
//
// Re-rendering the final artifact (rather than parsing the rendered
// thread.txt back into posts) makes the baseline deterministic: the render
// stage rendered the same artifact with the same deterministic renderer, so
// the re-render reproduces the posts exactly, and the canonical NUL-joined
// form is the one the Bluesky publisher digests and posts.
func newBlueskyDestination(p *editorial.Preset, runID run.RunID, art *contracts.EditorialArtifact) *Destination {
	d := &Destination{
		Key:         BlueskyKey,
		RunID:       runID,
		ContentName: path.Join("bluesky", "thread.txt"),
		Target: publish.TargetSpec{
			ID:          "thread",
			Name:        "Bluesky thread",
			Type:        publication.TargetTypePost,
			Provider:    "bluesky",
			Destination: p.Bluesky.Handle,
		},
		Facts: map[string]string{},
		blueskyCfg: bluesky.Config{
			Handle:      p.Bluesky.Handle,
			APIEndpoint: p.Bluesky.APIEndpoint,
			APIKeyEnv:   p.Bluesky.APIKey.Env,
		},
	}

	renderer := renderers.NewThreadRenderer()
	posts, err := renderer.Render(art)
	if err != nil {
		d.ContentErr = fmt.Errorf("rendering the thread from the write artifact: %v", err)
		return d
	}
	if len(posts) == 0 {
		d.ContentErr = errors.New("the thread has no posts")
		return d
	}
	content, err := publish.ThreadContent(posts...)
	if err != nil {
		d.ContentErr = fmt.Errorf("building the canonical thread content: %v", err)
		return d
	}
	d.Content = content
	d.Digest = publication.DigestOf(content)
	d.Facts["posts"] = strconv.Itoa(len(posts))
	if p.Bluesky.Handle != "" {
		d.Facts["handle"] = p.Bluesky.Handle
	}
	return d
}
