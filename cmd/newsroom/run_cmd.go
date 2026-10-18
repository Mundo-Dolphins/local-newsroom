package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archive"
	"github.com/Mundo-Dolphins/local-newsroom/internal/archiverev"
	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/discovery"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor"
	"github.com/Mundo-Dolphins/local-newsroom/internal/extractor/html"
	"github.com/Mundo-Dolphins/local-newsroom/internal/fetcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/finalchecker"
	"github.com/Mundo-Dolphins/local-newsroom/internal/followup"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/Mundo-Dolphins/local-newsroom/internal/orchestrator"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/profiles"
	"github.com/Mundo-Dolphins/local-newsroom/internal/renderers"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/search/searxng"
	"github.com/Mundo-Dolphins/local-newsroom/internal/verifier"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
	"github.com/Mundo-Dolphins/local-newsroom/internal/writer"
)

// runCmd is the "newsroom run" subcommand: the single end-to-end command of
// v0.5.
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the complete editorial workflow end to end",
	Long: `Run a complete editorial workflow end to end: discover or fetch sources,
research them, verify the claims, optionally follow up on unresolved claims,
write the story, run the final factual check, and render the output.

Every stage persists its output to the run workspace, so the whole run is
inspectable and resumable:

  newsroom run --topic "..." --profile mundodolphins/javi --format article

The command is non-interactive and exits non-zero when the run fails (for
example when the final factual check does not pass). It never publishes: the
rendered output is written to the run directory, and the summary says where.

Source modes:
  - explicit:    --url (repeatable) only
  - discovered:  --search-base-url (SearXNG) only
  - supplement:  --url and --search-base-url together

Interrupt (Ctrl-C) cancels the run cleanly; re-run with --resume <run-id> to
continue from where it stopped.`,
	Example: `  # Explicit sources, article output
  newsroom run --topic "Miami Dolphins roster changes" \
    --url https://example.com/story1 --url https://example.com/story2 \
    --profile mundodolphins/javi --format article

  # Automatic discovery via SearXNG, thread output
  newsroom run --topic "NFL Week 5 injury report" \
    --search-base-url http://raspberrypi:8080 --format thread

  # Resume a run that was interrupted or failed
  newsroom run --resume <run-id>`,
	RunE: doRun,
}

// runFlags holds the flags for the run command.
type runFlags struct {
	topic         string
	urls          []string
	profile       string
	profileRoot   string
	publication   string
	bundleRoot    string
	format        string
	workspace     string
	workspaceRoot string
	followUp      bool
	resume        string
	quality       float64

	// LLM configuration (resolved through the precedence chain).
	llmBaseURL string
	llmModel   string
	llmAPIKey  string
	llmTimeout float64

	// Search configuration.
	searchBaseURL    string
	searchLanguage   string
	searchTimeRange  string
	searchMaxQueries int
	searchResults    int
	maxSources       int

	// Fetch configuration.
	fetchTimeout  float64
	fetchMaxSize  int64
	fetchMaxWords int

	// Archive configuration.
	archiveDB   string
	archiveTopK int

	// Generation parameters.
	language    string
	temperature float64
	maxTokens   int
	tone        string
}

// rf are the run command flags.
var rf runFlags

func init() {
	f := runCmd.Flags()
	f.StringVarP(&rf.topic, "topic", "t", "", "editorial topic (required unless --resume is given)")
	f.StringArrayVarP(&rf.urls, "url", "u", nil, "explicit source URL (repeatable)")
	f.StringVar(&rf.profile, "profile", "", "editorial profile: <publication>/<author> or <author> (default: profiles.name from the configuration chain)")
	f.StringVar(&rf.format, "format", "", "output format: article, thread, or hybrid (default: output_format from the configuration chain)")
	f.StringVar(&rf.workspace, "workspace", "default", "workspace name")
	f.StringVar(&rf.workspaceRoot, "workspace-root", ".newsroom", "workspace root directory")
	f.StringVar(&rf.profileRoot, "profile-root", "", "profiles root directory (default: profiles.root from the configuration chain)")
	f.StringVar(&rf.publication, "publication", "", "publication preset in the editorial bundle (default: editorial.publication from the configuration chain)")
	f.StringVar(&rf.bundleRoot, "bundle-root", "", "editorial bundle directory (default: editorial.bundle_root from the configuration chain)")
	f.BoolVar(&rf.followUp, "follow-up", false, "enable bounded follow-up research for unresolved claims (requires search)")
	f.StringVar(&rf.resume, "resume", "", "resume an existing run by ID instead of creating a new run")
	f.Float64Var(&rf.quality, "quality-threshold", 0, "verification quality score (0-100) below which a warning is recorded (default 60)")

	f.StringVar(&rf.llmBaseURL, "llm-base-url", "", "LLM API base URL (default: OMLX_BASE_URL env or llm.base_url)")
	f.StringVar(&rf.llmModel, "llm-model", "", "LLM model name (default: OMLX_MODEL env or llm.model)")
	f.StringVar(&rf.llmAPIKey, "llm-api-key", "", "LLM API key (default: OMLX_API_KEY env or llm.api_key)")
	f.Float64Var(&rf.llmTimeout, "llm-timeout", 120, "LLM request timeout in seconds (default: llm.timeout)")

	f.StringVar(&rf.searchBaseURL, "search-base-url", "", "SearXNG base URL (default: SEARXNG_BASE_URL env or search.base_url)")
	f.StringVar(&rf.searchLanguage, "search-language", "", "search language (default: search.language, or --language)")
	f.StringVar(&rf.searchTimeRange, "search-time-range", "", "search time range (default: search.time_range)")
	f.IntVar(&rf.searchMaxQueries, "search-max-queries", 0, "maximum search queries to execute (default: search.max_queries)")
	f.IntVar(&rf.searchResults, "search-results-per-query", 0, "maximum results per search query (default: search.results_per_query)")
	f.IntVar(&rf.maxSources, "max-sources", 0, "maximum source URLs selected from discovery (default: search.max_sources or 5)")

	f.Float64Var(&rf.fetchTimeout, "fetch-timeout", 0, "HTTP fetch timeout in seconds (default: fetch.timeout)")
	f.Int64Var(&rf.fetchMaxSize, "fetch-max-size", 0, "maximum fetch response size in bytes (default: fetch.max_size)")
	f.IntVar(&rf.fetchMaxWords, "fetch-max-words", 0, "maximum words per extracted document (default: fetch.max_words)")

	f.StringVar(&rf.archiveDB, "archive-db-path", "", "archive database path (default: archive.db_path; archive retrieval is disabled when unset)")
	f.IntVar(&rf.archiveTopK, "archive-top-k", 0, "maximum archived documents added to research (default: archive.top_k)")

	f.StringVar(&rf.language, "language", "", "language for generation (default: defaults.language)")
	f.Float64Var(&rf.temperature, "temperature", 0, "LLM temperature (default: defaults.temperature)")
	f.IntVar(&rf.maxTokens, "max-tokens", 0, "maximum LLM output tokens (default: defaults.max_tokens)")
	f.StringVar(&rf.tone, "tone", "", "editorial tone (default: defaults.tone)")

	rootCmd.AddCommand(runCmd)
}

// runSettingOverride builds a config override from a flag, using the flag
// value only when the flag was explicitly set.
func runSettingOverride(cmd *cobra.Command, flagName string) config.Override {
	f := cmd.Flags().Lookup(flagName)
	if f == nil || !f.Changed {
		return config.Override{}
	}
	return config.FromFlag(f.Value.String(), true)
}

// buildRunOverrides collects the config overrides from every explicitly set
// run flag.
func buildRunOverrides(cmd *cobra.Command) map[string]config.Override {
	m := make(map[string]config.Override)
	pairs := [][2]string{
		{config.SettingLLMBaseURL, "llm-base-url"},
		{config.SettingLLMModel, "llm-model"},
		{config.SettingLLMAPIKey, "llm-api-key"},
		{config.SettingLLMTimeout, "llm-timeout"},
		{config.SettingSearchBaseURL, "search-base-url"},
		{config.SettingSearchLanguage, "search-language"},
		{config.SettingSearchTimeRange, "search-time-range"},
		{config.SettingSearchMaxQueries, "search-max-queries"},
		{config.SettingSearchResults, "search-results-per-query"},
		{config.SettingSearchMaxSources, "max-sources"},
		{config.SettingFetchTimeout, "fetch-timeout"},
		{config.SettingFetchMaxSize, "fetch-max-size"},
		{config.SettingFetchMaxWords, "fetch-max-words"},
		{config.SettingArchiveDBPath, "archive-db-path"},
		{config.SettingArchiveTopK, "archive-top-k"},
		{config.SettingProfilesRoot, "profile-root"},
		{config.SettingEditorialBundle, "bundle-root"},
		{config.SettingEditorialPublication, "publication"},
		{config.SettingDefaultsLanguage, "language"},
		{config.SettingTemperature, "temperature"},
		{config.SettingMaxTokens, "max-tokens"},
		{config.SettingTone, "tone"},
		{config.SettingOutputFormat, "format"},
	}
	for _, p := range pairs {
		m[p[0]] = runSettingOverride(cmd, p[1])
	}
	return m
}

// runResolved is every run-relevant setting resolved through the full
// precedence chain (CLI flags > environment variables > config file >
// built-in defaults). Both a new run and a resume resolve the same values,
// so a resumed run uses the same LLM, search, fetch, archive, and
// generation settings the current environment provides — with one
// deliberate exception: the output format of a resumed run is pinned to the
// value recorded in the run's manifest snapshot (see formatForResume), so a
// format changed after the failure cannot reshape the run's artifact.
type runResolved struct {
	llmBaseURL       string
	llmModel         string
	llmAPIKey        string
	llmTimeout       float64
	searchBaseURL    string
	searchLanguage   string
	searchTimeRange  string
	searchMaxQueries int
	searchResults    int
	maxSources       int
	fetchTimeout     float64
	fetchMaxSize     int
	fetchMaxWords    int
	archiveDB        string
	archiveSet       bool
	archiveTopK      int
	profilesRoot     string
	profilesName     string
	profilesNameSet  bool
	bundleRoot       string
	publication      string
	language         string
	languageSource   string
	temperature      float64
	maxTokens        int
	tone             string
	format           string
	formatSource     string
	quality          float64
}

// newLLMClient builds the shared OpenAI-compatible LLM client from the
// resolved run settings.
func (r *runResolved) newLLMClient() *llm.OpenAICompatClient {
	return llm.NewClient(llm.Config{
		BaseURL:         r.llmBaseURL,
		Model:           r.llmModel,
		APIKey:          r.llmAPIKey,
		Timeout:         time.Duration(r.llmTimeout * float64(time.Second)),
		Temperature:     r.temperature,
		MaxOutputTokens: r.maxTokens,
	})
}

// doRun implements the run command.
func doRun(cmd *cobra.Command, _ []string) error {
	// Context cancelled by Ctrl-C / SIGTERM so every in-flight HTTP and LLM
	// call can stop promptly and the run is recorded as cancelled.
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if strings.TrimSpace(rf.resume) != "" {
		return doRunResume(ctx, cmd, run.RunID(strings.TrimSpace(rf.resume)))
	}
	if strings.TrimSpace(rf.topic) == "" {
		return errors.New("a topic is required: use --topic, or --resume <run-id> to continue an existing run")
	}
	return doRunNew(ctx, cmd)
}

// doRunResume resumes an existing run. The run is fully described by its
// persisted manifest, so no topic is required; the config chain is resolved
// the same way as for a new run so the re-executed stages use the same
// services. The output format is the one exception: the run's recorded
// snapshot value wins (see planRunResume), so a --format flag passed here
// does not reshape the run's artifact.
func doRunResume(ctx context.Context, cmd *cobra.Command, id run.RunID) error {
	overrides := buildRunOverrides(cmd)
	root, err := filepath.Abs(rf.workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolving workspace root: %w", err)
	}
	intent := runIntent{
		ResumeID:    id,
		Workspace:   run.Workspace{Path: root},
		ProfileSpec: rf.profile,
	}
	plan, err := planRun(ctx, intent, overrides)
	if err != nil {
		return err
	}
	res, err := plan.Orch.ResumeID(ctx, id)
	if res != nil && res.Manifest != nil {
		printRunSummary(plan.Store, res.Manifest)
		writeReviewBundle(plan.Store, res.Manifest)
	}
	return userFacingRunError(res, err)
}

// doRunNew creates and executes a new end-to-end run.
func doRunNew(ctx context.Context, cmd *cobra.Command) error {
	root, err := filepath.Abs(rf.workspaceRoot)
	if err != nil {
		return fmt.Errorf("resolving workspace root: %w", err)
	}
	wsName := rf.workspace
	if wsName == "" {
		wsName = "default"
	}
	intent := runIntent{
		Topic:            rf.topic,
		ProfileSpec:      rf.profile,
		Format:           rf.format,
		URLs:             rf.urls,
		FollowUp:         rf.followUp,
		QualityThreshold: rf.quality,
		Workspace:        run.Workspace{Name: wsName, Path: root},
	}
	plan, err := planRun(ctx, intent, buildRunOverrides(cmd))
	if err != nil {
		return err
	}
	// The runner claims the run directory and persists the initial manifest
	// (CreateRun); the claim happens once, inside the run, so the command
	// does not pre-create it (a double create is rejected by the store).
	fmt.Fprintf(os.Stderr, "creating run %s in workspace %q (%s)\n", plan.Manifest.ID, plan.Manifest.Workspace.Name, root)

	res, err := plan.Orch.Run(ctx, plan.Manifest, false)
	if res == nil || res.Manifest == nil {
		return fmt.Errorf("run %s: %w", plan.Manifest.ID, err)
	}
	printRunSummary(plan.Store, res.Manifest)
	writeReviewBundle(plan.Store, res.Manifest)
	return userFacingRunError(res, err)
}

// serviceInputs carries the resolved inputs for stage service construction.
type serviceInputs struct {
	llm            *llm.OpenAICompatClient
	resolved       *runResolved
	followUp       bool
	archiveEnabled bool
	profile        profiles.Injector
}

// buildRunServices constructs the production stage services and returns the
// completed orchestrator config. followUp and archiveEnabled gate the
// optional services (they mirror what the run's manifest snapshot records,
// so a resumed run builds the same stack).
func buildRunServices(ctx context.Context, cfg orchestrator.Config, in serviceInputs) (orchestrator.Config, error) {
	_ = ctx
	r := in.resolved

	plannerService, err := planner.New(orchestrator.NewPlannerClient(in.llm, r.llmModel), r.llmModel, planner.Config{
		MaxQueries:      r.searchMaxQueries,
		Temperature:     r.temperature,
		MaxOutputTokens: r.maxTokens,
	})
	if err != nil {
		return cfg, fmt.Errorf("planner: %w", err)
	}
	cfg.Planner = plannerService

	if r.searchBaseURL != "" {
		provider, err := searxng.NewProvider(&searxng.Config{
			BaseURL:   r.searchBaseURL,
			UserAgent: "local-newsroom/0.5",
		})
		if err != nil {
			return cfg, fmt.Errorf("search provider: %w", err)
		}
		cfg.SearchProvider = provider
		cfg.Discovery = discovery.NewDiscovery(plannerService, provider, discovery.Config{
			MaxSearchQueries:         r.searchMaxQueries,
			ResultsPerQuery:          r.searchResults,
			MaxCandidates:            cfg.MaxSources,
			EnableDuplicateReporting: true,
		})
	}

	cfg.Fetcher = orchestrator.NewFetchAdapter(fetcher.NewClient(fetcher.Config{
		Timeout:   time.Duration(r.fetchTimeout * float64(time.Second)),
		MaxSize:   int64(r.fetchMaxSize),
		UserAgent: "local-newsroom/0.5",
	}))
	cfg.Extractor = orchestrator.NewExtractAdapter(html.New(extractor.Config{
		MaxWordCount:       r.fetchMaxWords,
		MaxPlainTextLength: 1024 * 1024,
		MaxTitleLength:     1000,
	}))

	cfg.Research, err = researcher.New(in.llm, r.llmModel, researcher.ClientConfig{
		Temperature:     r.temperature,
		MaxOutputTokens: r.maxTokens,
	})
	if err != nil {
		return cfg, fmt.Errorf("researcher: %w", err)
	}

	cfg.Verify, err = verifier.New(in.llm, r.llmModel, verifier.VerifierConfig{
		Temperature:     0,
		MaxOutputTokens: r.maxTokens,
	})
	if err != nil {
		return cfg, fmt.Errorf("verifier: %w", err)
	}

	writerConfig := writer.WriterConfig{
		Temperature:     r.temperature,
		MaxOutputTokens: r.maxTokens,
		Language:        r.language,
		Tone:            r.tone,
	}
	if in.profile != nil {
		writerConfig.ProfileInjector = in.profile
	}
	// The writer produces the artifact shape the render stage expects: the
	// run's output format maps directly onto the artifact type, so a
	// --format thread run gets a thread artifact (with posts) and --format
	// hybrid gets both body and posts. cfg.Format carries the run's format:
	// the current configuration for a new run, and the run's recorded
	// snapshot for a resume (see formatForResume), which keeps the writer
	// and the render stage in agreement across a format change.
	switch cfg.Format {
	case orchestrator.FormatThread:
		writerConfig.ArtifactType = string(contracts.ArtifactTypeThread)
	case orchestrator.FormatHybrid:
		writerConfig.ArtifactType = string(contracts.ArtifactTypeHybrid)
	default:
		writerConfig.ArtifactType = string(contracts.ArtifactTypeArticle)
	}
	writeService, err := writer.New(in.llm, r.llmModel, writerConfig)
	if err != nil {
		return cfg, fmt.Errorf("writer: %w", err)
	}
	cfg.Write = orchestrator.NewWriteAdapter(writeService)

	cfg.Check, err = finalchecker.New(in.llm, r.llmModel, finalchecker.CheckerConfig{
		Temperature: 0,
		Language:    r.language,
	})
	if err != nil {
		return cfg, fmt.Errorf("final checker: %w", err)
	}

	cfg.Markdown = renderers.NewMarkdownRenderer()
	cfg.Thread = renderers.NewThreadRenderer()

	if in.archiveEnabled {
		if r.archiveDB == "" {
			return cfg, errors.New("archive retrieval is enabled for this run but no archive database is configured (archive.db_path)")
		}
		sqliteStore, err := archive.NewSQLiteStore(r.archiveDB)
		if err != nil {
			return cfg, fmt.Errorf("opening archive: %w", err)
		}
		cfg.Archive = orchestrator.NewArchiveAdapter(archiverev.New(archiverev.Config{
			ArchiveStore:       sqliteStore,
			MaxArchivedSources: cfg.ArchiveTopK,
		}))
	}

	if in.followUp {
		if cfg.SearchProvider == nil {
			return cfg, errors.New("follow-up is enabled for this run but no search provider is configured (search.base_url)")
		}
		cfg.FollowUp = orchestrator.NewFollowUpService(
			followup.New(followup.FollowUpConfig{TimeNow: time.Now}),
			cfg.Planner,
			cfg.SearchProvider,
			cfg.Fetcher,
			cfg.Extractor,
			cfg.Research,
		)
	}
	return cfg, nil
}

// printRunSummary prints the concise run summary to stdout.
func printRunSummary(store *workspace.Store, m *run.Manifest) {
	s, err := orchestrator.BuildSummary(store, m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not build run summary: %v\n", err)
		return
	}
	fmt.Print(s.String())
}

// writeReviewBundle attaches the deterministic human review bundle to a run
// that reached a terminal state: it renders review.md into the run
// directory and tells the user where to look. The run's own record remains
// the source of truth, so a review that cannot be built or written is a
// stderr warning — it never fails the command.
func writeReviewBundle(store *workspace.Store, m *run.Manifest) {
	if m == nil {
		return
	}
	switch m.Status {
	case run.RunStatusSucceeded, run.RunStatusFailed, run.RunStatusCancelled:
	default:
		// A non-terminal run has no review: there is nothing final to state.
		return
	}
	b, err := review.Build(store, m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not build the review bundle: %v\n", err)
		return
	}
	path, err := review.Save(store, b)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write %s: %v\n", run.ReviewFileName, err)
		return
	}
	fmt.Fprintf(os.Stderr, "review bundle: %s\n", path)
}

// userFacingRunError converts a run result into the command's exit error.
//
// A failed or cancelled run is reported with a clear message; the run
// workspace keeps the full record. When the final factual check failed, the
// user is pointed at the resume path.
func userFacingRunError(res *orchestrator.Result, err error) error {
	if err == nil {
		return nil
	}
	if res != nil && res.Status == run.RunStatusSucceeded {
		return nil
	}
	if res != nil && res.Status == run.RunStatusCancelled {
		return fmt.Errorf("run %s cancelled (interrupted); resume with: newsroom run --resume %s", res.RunID, res.RunID)
	}
	if res != nil {
		return fmt.Errorf("run %s failed: %v (inspect the run workspace or resume with: newsroom run --resume %s)", res.RunID, err, res.RunID)
	}
	return err
}

// dedupeURLs removes duplicate and empty URLs, preserving order.
func dedupeURLs(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, u := range in {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

// maxSourcesOrDefault applies the default source bound.
func maxSourcesOrDefault(n int) int {
	if n <= 0 {
		return orchestrator.DefaultMaxSources
	}
	return n
}
