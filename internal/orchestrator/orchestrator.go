// Package orchestrator implements the v0.5 end-to-end editorial run behind
// the `newsroom run` command.
//
// It composes the existing components — discovery, fetch, extract, research,
// verify, follow-up, write, final check, and render — into the canonical
// stage plan and executes that plan through the runner, persisting every
// stage's output into the run workspace (internal/workspace) via the run
// manifest (internal/run). The orchestrator itself contains no editorial
// business logic: each stage delegates its work to a component that
// implements the stage's rules. Its own responsibilities are the run-level
// mechanics no single component owns:
//
//   - materializing the source URL list (explicit, discovered, or
//     supplemented) as the first persisted artifact, so every later stage —
//     and any resume of the run — reads its input back from the workspace
//     instead of re-deriving it;
//   - connecting each stage's output to the next stage's input through the
//     persisted artifacts;
//   - gating: the render stage only produces output after the final factual
//     check passed, so a run whose final check fails or needs review ends
//     with a failed run and no usable artifact;
//   - surfacing a machine-readable summary of the finished (or failed) run.
//
// It performs no publication of its own: the render stage writes rendered
// files into the run directory only, and the run command prints where they
// are.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archiverev"
	"github.com/Mundo-Dolphins/local-newsroom/internal/contentplan"
	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/discovery"
	"github.com/Mundo-Dolphins/local-newsroom/internal/followup"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/recipes"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/resume"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
	"github.com/Mundo-Dolphins/local-newsroom/internal/verifier"
)

// Run-level settings recorded in the manifest's config snapshot. The
// orchestrator snapshots these at run creation and reads them back on resume
// so a resumed run behaves exactly like the original one.
const (
	SettingRunFormat         = "run.format"
	SettingRunFollowUp       = "run.follow_up"
	SettingRunExplicitURLs   = "run.explicit_urls"
	SettingRunQualityThresh  = "run.quality_threshold"
	SettingRunLanguage       = "run.language"
	SettingRunArchiveTopK    = "run.archive_top_k"
	SettingRunArchiveEnabled = "run.archive_enabled"
	SettingRunMaxSources     = "run.max_sources"
	SettingRunPublication    = "run.publication"
)

// Output format values accepted by the render stage.
const (
	FormatArticle = "article"
	FormatThread  = "thread"
	FormatHybrid  = "hybrid"
)

// DefaultQualityThreshold is the verification quality score (0-100) below
// which a low-quality warning is recorded.
const DefaultQualityThreshold = 60.0

// ---------------------------------------------------------------------------
// Stage service interfaces
//
// Every interface below is satisfied by the corresponding production
// component (or a thin adapter around it, see adapters.go). Tests inject
// deterministic fakes. The orchestrator depends only on these shapes, so it
// can be exercised end to end without any LLM, network, or storage beyond
// the injected workspace store.
// ---------------------------------------------------------------------------

// Planner plans search queries for a topic. Satisfied by *planner.Planner.
type Planner interface {
	Generate(ctx context.Context, topic string, language string, hint *planner.TimeRangeHint) (*planner.SearchPlan, error)
}

// DiscoveryService discovers candidate source URLs for a topic. Satisfied by
// *discovery.Discovery.
//
// The variadic options (v0.6 editorial recipes) only narrow discovery
// (seed queries, budgets); they are never required and a plain call
// (no options) behaves exactly as before.
type DiscoveryService interface {
	Discover(ctx context.Context, topic string, language string, hint *planner.TimeRangeHint, opts ...discovery.Option) (*discovery.DiscoveryResult, error)
}

// FetchedSource is the orchestrator's view of one URL retrieval. The raw
// body (RawBody) is persisted as a dedicated artifact file; the index file
// records only the metadata fields.
type FetchedSource struct {
	URL         string    `json:"url"`
	Title       string    `json:"title,omitempty"`
	Status      int       `json:"status"`
	ContentType string    `json:"content_type,omitempty"`
	OK          bool      `json:"ok"`
	Error       string    `json:"error,omitempty"`
	RetrievedAt time.Time `json:"retrieved_at"`

	// RawBody is the fetched content; it is excluded from the JSON index.
	RawBody []byte `json:"-"`
}

// SourceFetcher retrieves raw content from a batch of URLs. Satisfied by
// NewFetchAdapter around *fetcher.Client.
type SourceFetcher interface {
	FetchMany(ctx context.Context, urls []string) []FetchedSource
}

// SourceExtractor normalizes one fetched source into a plain-text document.
// Satisfied by NewExtractAdapter around the html extractor.
type SourceExtractor interface {
	Extract(ctx context.Context, src FetchedSource) (*types.Document, error)
}

// ResearchService synthesizes documents into a research dossier. Satisfied
// by *researcher.Researcher.
type ResearchService interface {
	Generate(ctx context.Context, topic string, documents []types.Document) (*researcher.ResearchDossier, error)
}

// ArchiveService retrieves archived documents relevant to a topic. Satisfied
// by *archiverev.Client (see NewArchiveAdapter).
type ArchiveService interface {
	Retrieve(ctx context.Context, topic string, liveURLs []string) (*archiverev.RetrieveResult, error)
}

// VerifyService verifies dossier claims against their sources. Satisfied by
// *verifier.Verifier.
type VerifyService interface {
	Verify(ctx context.Context, in verifier.Input) (*contracts.VerificationResult, error)
}

// FollowUpService runs the bounded follow-up research loop for the
// verification's unresolved claims and returns the updated dossier plus its
// audit trail. Satisfied by NewFollowUpService.
type FollowUpService interface {
	Run(ctx context.Context, dossier *researcher.ResearchDossier, verification *contracts.VerificationResult) (*followup.FollowUpResult, error)
}

// WriteService writes the editorial artifact from the verified claims.
// Satisfied by the write adapter around *writer.Writer; the editorial
// profile (when loaded) is baked into the writer's own configuration.
type WriteService interface {
	Generate(ctx context.Context, verification *contracts.VerificationResult) (*contracts.EditorialArtifact, error)
}

// PlannedWriteService is the optional v0.6 extension of [WriteService]:
// writers that accept a recipe content plan (the expanded output blueprint
// with per-unit claim traceability) implement it. Runs without a recipe
// never call it, so existing writers stay untouched.
type PlannedWriteService interface {
	WriteService
	GenerateWithPlan(ctx context.Context, verification *contracts.VerificationResult, plan *contracts.ContentPlan) (*contracts.EditorialArtifact, error)
}

// CheckService performs the final factual check. Satisfied by
// *finalchecker.FinalFactualChecker.
type CheckService interface {
	Check(ctx context.Context, artifact *contracts.EditorialArtifact, verification *contracts.VerificationResult) (*contracts.FactualCheckResult, error)
}

// MarkdownRenderer renders an artifact as a Markdown document. Satisfied by
// *renderers.MarkdownRenderer.
type MarkdownRenderer interface {
	Render(artifact *contracts.EditorialArtifact) (string, error)
}

// ThreadRenderer renders an artifact as a Bluesky post thread. Satisfied by
// *renderers.ThreadRenderer.
type ThreadRenderer interface {
	Render(artifact *contracts.EditorialArtifact) ([]string, error)
}

// ---------------------------------------------------------------------------
// Config and Orchestrator
// ---------------------------------------------------------------------------

// Config carries the run-level parameters and the stage services.
//
// The services are the only production dependencies: an Orchestrator built
// from fakes runs the full plan in-process. The parameters (Format,
// ExplicitURLs, ...) are used at run creation time; on resume the manifest's
// config snapshot takes precedence, so a resumed run is deterministic.
type Config struct {
	// Store persists the run (manifest + artifacts). Required.
	Store runner.Store

	// Run-level parameters.
	Format       string   // article, thread, or hybrid
	ExplicitURLs []string // explicit source URLs (source mode explicit/supplement)
	MaxSources   int      // bound on sources selected from discovery (0 = DefaultMaxSources)

	// Stage services. Research, Verify, Write, Check, Markdown, and Thread
	// are required; the rest are optional and gate their stages.
	Planner   Planner
	Discovery DiscoveryService
	// SearchProvider is the raw search provider, used to wire the follow-up
	// round searches (discovery itself owns a planner+provider pair).
	SearchProvider search.Provider
	Fetcher        SourceFetcher
	Extractor      SourceExtractor
	Research       ResearchService
	Archive        ArchiveService // optional: nil disables archive retrieval
	Verify         VerifyService
	FollowUp       FollowUpService
	Write          WriteService
	Check          CheckService
	Markdown       MarkdownRenderer
	Thread         ThreadRenderer

	// ArchiveTopK bounds the archived documents added to the research stage
	// (0 or negative uses the archiverev client's own limits).
	ArchiveTopK int

	// VerifierID is recorded in the verification artifact.
	VerifierID string

	// QualityThreshold (0-100) below which a low-quality warning is recorded.
	QualityThreshold float64

	// Recipe is the resolved editorial recipe for the run (v0.6). Nil means
	// a standard run: no recipe file is written, discovery research and
	// write stages behave exactly as before. When set, the discovery stage
	// persists it (recipe/resolved.json) and the research/write stages apply
	// its budgets, instructions, and blueprint.
	Recipe *recipes.Resolved

	// RecipeInputs are the resolved input values of the configured recipe
	// (the values a run's --input flags resolved to). Used to render recipe
	// templates; empty for standard runs.
	RecipeInputs []recipes.ResolvedInput

	// ContentPlanner computes the content plan for recipe runs (v0.6).
	// Optional: nil disables plan-driven writing (the recipe blueprint is
	// ignored with a warning rather than failing).
	ContentPlanner contentplan.Service

	// Clock supplies timestamps. Defaults to time.Now (UTC).
	Clock runner.Clock
}

// Orchestrator executes end-to-end editorial runs.
type Orchestrator struct {
	cfg Config
}

// New validates the configuration and returns an Orchestrator.
func New(cfg Config) (*Orchestrator, error) {
	if cfg.Store == nil {
		return nil, errors.New("orchestrator: Store is required")
	}
	if cfg.Research == nil {
		return nil, errors.New("orchestrator: Research service is required")
	}
	if cfg.Verify == nil {
		return nil, errors.New("orchestrator: Verify service is required")
	}
	if cfg.Write == nil {
		return nil, errors.New("orchestrator: Write service is required")
	}
	if cfg.Check == nil {
		return nil, errors.New("orchestrator: Check service is required")
	}
	if cfg.Markdown == nil {
		return nil, errors.New("orchestrator: Markdown renderer is required")
	}
	if cfg.Thread == nil {
		return nil, errors.New("orchestrator: Thread renderer is required")
	}
	if cfg.Format == "" {
		cfg.Format = FormatArticle
	}
	switch cfg.Format {
	case FormatArticle, FormatThread, FormatHybrid:
	default:
		return nil, fmt.Errorf("orchestrator: unsupported output format %q (want %q, %q, or %q)",
			cfg.Format, FormatArticle, FormatThread, FormatHybrid)
	}
	if cfg.FollowUp != nil && cfg.Discovery == nil {
		return nil, errors.New("orchestrator: FollowUp requires a Discovery service (it researches new sources for unresolved claims)")
	}
	if cfg.QualityThreshold <= 0 {
		cfg.QualityThreshold = DefaultQualityThreshold
	}
	if cfg.VerifierID == "" {
		cfg.VerifierID = "verifier"
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	return &Orchestrator{cfg: cfg}, nil
}

// followUpEnabledFor reports whether the follow-up stage runs for the given
// manifest: the manifest snapshot wins over the creation-time configuration.
func (o *Orchestrator) followUpEnabledFor(m *run.Manifest) bool {
	if m != nil {
		if v, ok := m.Config.Get(SettingRunFollowUp); ok {
			if b, err := parseBool(v); err == nil {
				return b
			}
		}
	}
	return o.cfg.FollowUp != nil
}

// BuildStages returns the canonical stage plan with one executor per stage.
// The executors read their inputs from the persisted artifacts of the
// earlier stages, so the plan can be executed fresh or resumed. The manifest
// selects per-run stage behavior (notably whether the follow-up stage runs),
// which keeps a resumed run faithful to the original one.
func (o *Orchestrator) BuildStages(m *run.Manifest) []runner.Stage {
	followUpStage := runner.Stage{Name: run.StageFollowUp, Fn: o.runFollowUp}
	if !o.followUpEnabledFor(m) {
		followUpStage.Fn = nil
		followUpStage.SkipReason = "follow-up research is disabled for this run"
	}
	return []runner.Stage{
		{Name: run.StageDiscovery, Fn: o.runDiscovery},
		{Name: run.StageFetch, Fn: o.runFetch},
		{Name: run.StageExtract, Fn: o.runExtract},
		{Name: run.StageResearch, Fn: o.runResearch},
		{Name: run.StageVerify, Fn: o.runVerify},
		followUpStage,
		{Name: run.StageWrite, Fn: o.runWrite},
		{Name: run.StageFinalCheck, Fn: o.runFinalCheck},
		{Name: run.StageRender, Fn: o.runRender},
	}
}

// Result is the terminal state of an orchestrated run, normalized from the
// runner's (fresh runs) and resume's (resumed runs) results.
type Result struct {
	// RunID identifies the run.
	RunID run.RunID

	// RunDir is the absolute path of the run directory.
	RunDir string

	// Manifest is the final manifest state.
	Manifest *run.Manifest

	// Status is the terminal run status (succeeded, failed, or cancelled).
	Status run.RunStatus

	// Err is nil when the run succeeded; otherwise it explains why the run
	// stopped. The durable record of the failure is the manifest on disk.
	Err error
}

// Run executes the full plan for the given manifest. With resume=false the
// manifest must be a freshly created (pending) run; with resume=true the
// manifest must already be in a resumable state.
func (o *Orchestrator) Run(ctx context.Context, m *run.Manifest, resume bool) (*Result, error) {
	r, err := runner.Run(ctx, runner.Config{
		Manifest: m,
		Store:    o.cfg.Store,
		Stages:   o.BuildStages(m),
		Clock:    o.cfg.Clock,
		Resume:   resume,
	})
	if r == nil {
		return nil, err
	}
	return &Result{RunID: r.RunID, RunDir: r.RunDir, Manifest: r.Manifest, Status: r.Status, Err: err}, err
}

// ResumeID resumes an existing run by ID: it verifies the persisted manifest
// and its artifacts, reopens a terminal run, and executes every stage that
// did not finish, reusing the completed ones. It is the engine behind
// `newsroom run --resume <id>`.
func (o *Orchestrator) ResumeID(ctx context.Context, id run.RunID) (*Result, error) {
	m, _, err := o.cfg.Store.LoadManifest(id)
	if err != nil {
		return &Result{RunID: id, Err: err}, fmt.Errorf("orchestrator: loading run %s: %w", id, err)
	}
	r, err := resume.Resume(ctx, o.cfg.Store, id, o.BuildStages(m), resume.Options{Clock: o.cfg.Clock})
	if r == nil {
		return nil, err
	}
	return &Result{RunID: r.RunID, RunDir: r.RunDir, Manifest: r.Manifest, Status: r.Status, Err: err}, err
}

// stageFile marshals a stage value into a stage output file with a fresh
// artifact ID.
func (o *Orchestrator) stageFile(kind run.ArtifactKind, path string, v any) (runner.StageFile, error) {
	data, err := jsonMarshalIndent(v)
	if err != nil {
		return runner.StageFile{}, fmt.Errorf("marshaling stage file %s: %w", path, err)
	}
	return runner.StageFile{Kind: kind, ID: uuid.NewString(), Path: path, Data: data}, nil
}

// loadStageJSON reads and unmarshals a stage artifact by its run-relative
// path from the workspace.
func loadStageJSON[T any](in *runner.StageInput, path string) (*T, error) {
	data, err := in.Store.LoadArtifact(in.RunID, path)
	if err != nil {
		return nil, fmt.Errorf("loading stage artifact %s: %w", path, err)
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("parsing stage artifact %s: %w", path, err)
	}
	return &v, nil
}

// parseBool parses a snapshot boolean ("true"/"false").
func parseBool(v string) (bool, error) {
	return strconv.ParseBool(v)
}
