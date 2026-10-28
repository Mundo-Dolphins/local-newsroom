package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/editorial"
	"github.com/Mundo-Dolphins/local-newsroom/internal/orchestrator"
	"github.com/Mundo-Dolphins/local-newsroom/internal/profiles"
	"github.com/Mundo-Dolphins/local-newsroom/internal/recipes"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// runIntent fully specifies one end-to-end run: either a new run (topic and
// source inputs) or a resume of an existing one (ResumeID).
//
// It is the shared input of planRun: the `newsroom run` command builds it
// from its flags, and the TUI builds it from its form. Everything downstream
// of planRun (service construction, manifest creation, execution) is
// identical for both, so no pipeline logic is duplicated between CLI and TUI.
type runIntent struct {
	// Topic is the editorial topic (required for a new run; ignored for a
	// resume).
	Topic string

	// ProfileSpec is an explicit editorial profile spec
	// ("<publication>/<author>" or "<author>"); empty falls back to the
	// profiles.name setting.
	ProfileSpec string

	// Format is the output format (article, thread, or hybrid); empty uses
	// the configured default.
	Format string

	// SourceMode is how sources are obtained. It may be left empty for a
	// new run to let planRun derive it from the URLs and the search
	// configuration (the historical CLI behavior).
	SourceMode run.SourceMode

	// URLs are the explicit source URLs (deduplicated here).
	URLs []string

	// FollowUp enables bounded follow-up research for unresolved claims.
	FollowUp bool

	// QualityThreshold overrides the verification quality threshold
	// (<= 0 uses the default).
	QualityThreshold float64

	// Workspace identifies the target workspace: Name for a new run, and
	// Path (absolute) for both new runs and resumes.
	Workspace run.Workspace

	// ResumeID names an existing run to resume instead of creating a new
	// one.
	ResumeID run.RunID

	// RecipeID selects an editorial recipe (v0.6) by id for a new run; the
	// recipe derives the topic (when --topic is empty), the output format
	// (when --format is empty), and the run's research/write guidance. It is
	// ignored for a resume (the run's manifest records the recipe it used).
	RecipeID string

	// RecipeAuthor selects the author layer for recipe resolution; empty
	// falls back to the run's profile author.
	RecipeAuthor string

	// RecipeInputs are the explicit input values (name → value) for a new
	// run's recipe; they are validated against the recipe's declarations
	// and win over the declared defaults.
	RecipeInputs map[string]string
}

// runPlan is the fully wired execution stack for one run.
type runPlan struct {
	// Manifest is the run manifest: freshly created for a new run, or the
	// loaded (and validated) manifest for a resume.
	Manifest *run.Manifest

	// Orch executes the run (Run for new runs, ResumeID for resumes).
	Orch *orchestrator.Orchestrator

	// Store is the workspace store, for post-run inspection.
	Store *workspace.Store
}

// planRun wires the production pipeline for an intent.
//
// This is the single place where a run is assembled: configuration
// resolution, source-mode validation, editorial profile loading, workspace
// and manifest creation, stage service construction, and the orchestrator
// itself. Neither the `run` command nor the TUI owns any of that logic
// separately.
func planRun(ctx context.Context, intent runIntent, ovs map[string]config.Override) (*runPlan, error) {
	resolved, err := resolveRunConfigOverrides(ovs)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(intent.Workspace.Path)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root: %w", err)
	}
	store, err := workspace.NewStoreWithSanitizer(root, run.NewSanitizer(appConfig.SecretValues(ovs)...))
	if err != nil {
		return nil, fmt.Errorf("creating workspace store: %w", err)
	}
	intent.Workspace.Path = root

	if intent.ResumeID != "" {
		return planRunResume(ctx, resolved, intent, store)
	}
	return planRunNew(ctx, resolved, intent, ovs, store)
}

// planRunResume loads an existing run and wires the services a resumed run
// needs, faithfully reusing the run's own configuration snapshot (follow-up,
// archive, and output-format gates come from the manifest, so a resume
// behaves like the original run).
func planRunResume(ctx context.Context, r *runResolved, intent runIntent, store *workspace.Store) (*runPlan, error) {
	// The manifest snapshot is the source of truth for what the run did:
	// follow-up and archive services are (re)constructed only when the run
	// originally used them, so a resume behaves like the original.
	m, _, err := store.LoadManifest(intent.ResumeID)
	if err != nil {
		return nil, fmt.Errorf("loading run %s: %w", intent.ResumeID, err)
	}
	followUpEnabled := false
	if v, ok := m.Config.Get(orchestrator.SettingRunFollowUp); ok {
		followUpEnabled, _ = strconv.ParseBool(v)
	}
	archiveEnabled := false
	if v, ok := m.Config.Get(orchestrator.SettingRunArchiveEnabled); ok {
		archiveEnabled, _ = strconv.ParseBool(v)
	}
	// The output format is snapshot-pinned too: the writer is built from it
	// below and the render stage resolves the same recorded value, so the
	// resumed run produces the original run's artifact shape even when the
	// operator changed the format since the failure.
	format := formatForResume(m, r.format)

	// The publication preset, when the run recorded one, is re-derived from
	// the manifest (not the current configuration): a resume must behave
	// like the original run, so the recorded preset name and bundle path win
	// over whatever the operator reconfigured since the failure.
	preset, err := resumePublication(m, r)
	if err != nil {
		return nil, fmt.Errorf("loading editorial publication for resume: %w", err)
	}

	// The output language is snapshot-pinned the same way the format is:
	// the writer must use the language the original run recorded (which is
	// where a publication preset's language lands).
	if v, ok := m.Config.Get(orchestrator.SettingRunLanguage); ok && v != "" {
		r.language = v
	} else if v, ok := m.Config.Get(config.SettingDefaultsLanguage); ok && v != "" {
		r.language = v
	}

	profileInst, err := loadRunProfileInstance(ctx, r, preset, intent)
	if err != nil {
		return nil, fmt.Errorf("loading editorial profile for resume: %w", err)
	}

	// v0.6 editorial recipe: recover the recipe the run started with from the
	// run's own record (persisted resolved recipe, or a re-resolution anchored
	// on the manifest hash). A resumed run must execute the same recipe.
	var recipeResolved *recipes.Resolved
	var recipeInputs []recipes.ResolvedInput
	if m.Recipe != nil {
		recipeResolved, recipeInputs, err = resumeRecipe(m, store, r)
		if err != nil {
			return nil, err
		}
	}

	orchCfg := orchestrator.Config{
		Store:            store,
		Format:           format,
		MaxSources:       maxSourcesOrDefault(r.maxSources),
		ArchiveTopK:      r.archiveTopK,
		QualityThreshold: r.quality,
		Recipe:           recipeResolved,
		RecipeInputs:     recipeInputs,
	}
	orchCfg, err = buildRunServices(ctx, orchCfg, serviceInputs{
		llm:            r.newLLMClient(),
		resolved:       r,
		followUp:       followUpEnabled,
		archiveEnabled: archiveEnabled,
		profile:        profileInst,
		recipe:         recipeResolved,
		recipeInputs:   recipeInputs,
	})
	if err != nil {
		return nil, err
	}
	orch, err := orchestrator.New(orchCfg)
	if err != nil {
		return nil, err
	}
	return &runPlan{Manifest: m, Orch: orch, Store: store}, nil
}

// formatForResume returns the output format a resumed run uses: the value
// recorded in the run's config snapshot (run.format) when present, falling
// back to the format resolved from the current configuration.
//
// v0.5 requires that a resumed run behaves like the original one: the same
// services, the same stages, the same outputs. The snapshot value is what
// the run recorded when it started, so the writer (built from this value in
// buildRunServices) and the render stage (which resolves run.format from the
// snapshot in runRender) agree on the artifact shape even when the operator
// changes output_format — or passes a different --format — between the
// failure and the resume.
func formatForResume(m *run.Manifest, current string) string {
	if v, ok := m.Config.Get(orchestrator.SettingRunFormat); ok && v != "" {
		return v
	}
	return current
}

// planRunNew creates the manifest and wires the services for a new run.
//
// The validation order is deliberate and historical: sources, then profile,
// then workspace, then manifest, then services — configuration problems
// fail before anything is created.
func planRunNew(ctx context.Context, r *runResolved, intent runIntent, ovs map[string]config.Override, store *workspace.Store) (*runPlan, error) {
	urls := dedupeURLs(intent.URLs)
	searchConfigured := r.searchBaseURL != ""

	// Derive the source mode when the caller left it empty (historical CLI
	// behavior), then enforce the per-mode invariants.
	mode := intent.SourceMode
	if mode == "" {
		mode = run.SourceModeDiscovered
		if len(urls) > 0 && !searchConfigured {
			mode = run.SourceModeExplicit
		} else if len(urls) > 0 {
			mode = run.SourceModeSupplement
		}
	}
	if len(urls) == 0 && !searchConfigured {
		return nil, errors.New("no sources configured: provide --url (repeatable) and/or --search-base-url (or SEARXNG_BASE_URL / search.base_url)")
	}
	switch mode {
	case run.SourceModeExplicit:
		if len(urls) == 0 {
			return nil, errors.New("source mode explicit requires at least one --url")
		}
	case run.SourceModeSupplement:
		if len(urls) == 0 {
			return nil, errors.New("source mode supplement requires at least one --url")
		}
	case run.SourceModeDiscovered:
	default:
		return nil, fmt.Errorf("invalid source mode %q", mode)
	}
	if intent.FollowUp && !searchConfigured {
		return nil, errors.New("follow-up is enabled but no search provider is configured (--search-base-url, SEARXNG_BASE_URL, or search.base_url)")
	}

	// --- Editorial publication preset (external bundle) -------------------
	//
	// The preset contributes its language/renderer only where no higher
	// precedence layer (flag, environment, config file) provided a value.
	preset, publication, err := resolveEditorial(r)
	if err != nil {
		return nil, err
	}

	// --- Editorial profile ----------------------------------------------
	root, spec, hasProfile := selectProfile(preset, r, intent)
	var (
		profileRef  *run.Profile
		profileInst profiles.Injector
	)
	if hasProfile {
		profileRef, profileInst, err = loadRunProfile(ctx, root, spec, preset != nil)
		if err != nil {
			return nil, err
		}
	}

	// --- Editorial recipe (v0.6) -----------------------------------------
	//
	// The recipe is data owned by the profile layout: it is loaded (common
	// layer + author override), its inputs are resolved with the explicit
	// --input values winning over defaults, and the run's topic and format
	// are derived from it where the operator left them empty. It never
	// changes engine behavior beyond its declarative guidance.
	var (
		recipeRef           *run.Recipe
		serviceRecipe       *recipes.Resolved
		serviceRecipeInputs []recipes.ResolvedInput
	)
	if strings.TrimSpace(intent.RecipeID) != "" {
		recipeRef, serviceRecipe, serviceRecipeInputs, intent, err = resolveRecipe(r, preset, intent)
		if err != nil {
			return nil, err
		}
	}

	// --- Workspace ---------------------------------------------------------
	ws := intent.Workspace
	if err := ws.Validate(); err != nil {
		return nil, fmt.Errorf("invalid workspace name %q: %w", ws.Name, err)
	}
	// The store was created with the absolute root; record it on the
	// manifest workspace for traceability.
	ws.Path = intent.Workspace.Path

	// The config snapshot makes the run reproducible: every run-level
	// parameter the stages resolve (format, follow-up, language, thresholds,
	// explicit URLs, archive, source bound) is recorded so a resumed run
	// behaves exactly like the original one.
	snap, err := appConfig.Snapshot(ovs)
	if err != nil {
		return nil, fmt.Errorf("building config snapshot: %w", err)
	}
	format := intent.Format
	if format == "" {
		format = r.format
	}
	quality := intent.QualityThreshold
	if quality <= 0 {
		quality = r.quality
	}
	archiveEnabled := r.archiveSet && r.archiveDB != ""
	snapPairs := [][2]string{
		{orchestrator.SettingRunFormat, format},
		{orchestrator.SettingRunFollowUp, strconv.FormatBool(intent.FollowUp)},
		{orchestrator.SettingRunLanguage, r.language},
		{orchestrator.SettingRunQualityThresh, strconv.FormatFloat(quality, 'f', 1, 64)},
		{orchestrator.SettingRunArchiveTopK, strconv.Itoa(r.archiveTopK)},
		{orchestrator.SettingRunArchiveEnabled, strconv.FormatBool(archiveEnabled)},
		{orchestrator.SettingRunMaxSources, strconv.Itoa(r.maxSources)},
	}
	for _, p := range snapPairs {
		if err := snap.Set(p[0], p[1]); err != nil {
			return nil, fmt.Errorf("recording run parameter %s: %w", p[0], err)
		}
	}
	// The publication preset identity is recorded in the snapshot (the hash
	// and bundle path live on the manifest's Publication reference): the
	// preset's private content itself never lands in the run record.
	if publication != nil {
		if err := snap.Set(orchestrator.SettingRunPublication, publication.ID); err != nil {
			return nil, fmt.Errorf("recording publication %s: %w", orchestrator.SettingRunPublication, err)
		}
	}
	if len(urls) > 0 {
		data, err := json.Marshal(urls)
		if err != nil {
			return nil, fmt.Errorf("recording explicit URLs: %w", err)
		}
		if err := snap.Set(orchestrator.SettingRunExplicitURLs, string(data)); err != nil {
			return nil, fmt.Errorf("recording explicit URLs: %w", err)
		}
	}

	now := time.Now().UTC()
	cfg := &run.NewManifestParams{
		Workspace:   ws,
		Topic:       strings.TrimSpace(intent.Topic),
		SourceMode:  mode,
		Profile:     profileRef,
		Publication: publication,
		Model:       run.NewModelMetadata("omlx", r.llmModel, r.llmBaseURL),
		Config:      snap,
		Stages:      run.CanonicalStages(),
		StartedAt:   now,
		Recipe:      recipeRef,
	}
	m, err := run.NewManifest(*cfg)
	if err != nil {
		return nil, fmt.Errorf("creating run manifest: %w", err)
	}

	// --- Stage services ----------------------------------------------------
	orchCfg := orchestrator.Config{
		Store:            store,
		Format:           format,
		ExplicitURLs:     urls,
		MaxSources:       maxSourcesOrDefault(r.maxSources),
		ArchiveTopK:      r.archiveTopK,
		QualityThreshold: quality,
		Recipe:           serviceRecipe,
		RecipeInputs:     serviceRecipeInputs,
	}
	orchCfg, err = buildRunServices(ctx, orchCfg, serviceInputs{
		llm:            r.newLLMClient(),
		resolved:       r,
		followUp:       intent.FollowUp,
		archiveEnabled: archiveEnabled,
		profile:        profileInst,
		recipe:         serviceRecipe,
		recipeInputs:   serviceRecipeInputs,
	})
	if err != nil {
		return nil, err
	}
	orch, err := orchestrator.New(orchCfg)
	if err != nil {
		return nil, err
	}
	return &runPlan{Manifest: m, Orch: orch, Store: store}, nil
}

// resolveRunConfigOverrides resolves every run setting through the full
// precedence chain, from an explicit override map (CLI flags for the run
// command, an empty map for the TUI).
func resolveRunConfigOverrides(ovs map[string]config.Override) (*runResolved, error) {
	ov := func(name string) config.Override {
		if ovs == nil {
			return config.Override{}
		}
		o, ok := ovs[name]
		if !ok {
			return config.Override{}
		}
		return o
	}
	resolve := func(name string) (string, bool, error) {
		return appConfig.GetString(name, ov(name))
	}
	resolveInt := func(name string) (int, bool, error) {
		return appConfig.GetInt(name, ov(name))
	}
	resolveFloat := func(name string) (float64, bool, error) {
		return appConfig.GetFloat(name, ov(name))
	}

	r := &runResolved{}
	var err error
	if r.llmBaseURL, _, err = resolve(config.SettingLLMBaseURL); err != nil {
		return nil, err
	}
	if r.llmModel, _, err = resolve(config.SettingLLMModel); err != nil {
		return nil, err
	}
	if r.llmAPIKey, _, err = resolve(config.SettingLLMAPIKey); err != nil {
		return nil, err
	}
	if r.llmTimeout, _, err = resolveFloat(config.SettingLLMTimeout); err != nil {
		return nil, err
	}

	if r.searchBaseURL, _, err = resolve(config.SettingSearchBaseURL); err != nil {
		return nil, err
	}
	searchLang, searchLangSet, err := resolve(config.SettingSearchLanguage)
	if err != nil {
		return nil, err
	}
	r.searchLanguage = searchLang
	if r.searchTimeRange, _, err = resolve(config.SettingSearchTimeRange); err != nil {
		return nil, err
	}
	if r.searchMaxQueries, _, err = resolveInt(config.SettingSearchMaxQueries); err != nil {
		return nil, err
	}
	if r.searchResults, _, err = resolveInt(config.SettingSearchResults); err != nil {
		return nil, err
	}
	if r.maxSources, _, err = resolveInt(config.SettingSearchMaxSources); err != nil {
		return nil, err
	}

	if r.fetchTimeout, _, err = resolveFloat(config.SettingFetchTimeout); err != nil {
		return nil, err
	}
	if r.fetchMaxSize, _, err = resolveInt(config.SettingFetchMaxSize); err != nil {
		return nil, err
	}
	if r.fetchMaxWords, _, err = resolveInt(config.SettingFetchMaxWords); err != nil {
		return nil, err
	}

	if r.archiveDB, r.archiveSet, err = resolve(config.SettingArchiveDBPath); err != nil {
		return nil, err
	}
	if r.archiveTopK, _, err = resolveInt(config.SettingArchiveTopK); err != nil {
		return nil, err
	}

	if r.profilesRoot, _, err = resolve(config.SettingProfilesRoot); err != nil {
		return nil, err
	}
	// The profiles.name setting carries a built-in default ("default"), so a
	// resolved value alone cannot tell "the user chose a profile" from
	// "nothing was configured": only a non-default source counts. Otherwise
	// every run would require a profiles root just to read the default name.
	profilesName, nameSource, err := appConfig.Resolve(config.SettingProfilesName, config.Override{})
	if err != nil {
		return nil, err
	}
	r.profilesName = profilesName
	r.profilesNameSet = nameSource != "default" && nameSource != ""

	if r.bundleRoot, _, err = resolve(config.SettingEditorialBundle); err != nil {
		return nil, err
	}
	if r.publication, _, err = resolve(config.SettingEditorialPublication); err != nil {
		return nil, err
	}

	if r.language, _, err = resolve(config.SettingDefaultsLanguage); err != nil {
		return nil, err
	}
	// The source label is tracked: a publication preset contributes its own
	// language/renderer only where no higher precedence layer (flag, env,
	// config file) provided a value — i.e. where the resolution fell to the
	// built-in default or came back unset. The resolution must be repeated
	// with the CLI overrides so an explicit --language is not mislabeled.
	if _, r.languageSource, err = appConfig.Resolve(config.SettingDefaultsLanguage, ov(config.SettingDefaultsLanguage)); err != nil {
		return nil, err
	}
	if r.language == "" {
		// The search language doubles as the generation language when no
		// explicit language is configured.
		r.language = r.searchLanguage
		if r.language != "" {
			r.languageSource = "derived (search language)"
		}
	}
	if r.temperature, _, err = resolveFloat(config.SettingTemperature); err != nil {
		return nil, err
	}
	if r.temperature == 0 {
		r.temperature = 0.3
	}
	if r.maxTokens, _, err = resolveInt(config.SettingMaxTokens); err != nil {
		return nil, err
	}
	if r.maxTokens <= 0 {
		r.maxTokens = 50000
	}
	if r.tone, _, err = resolve(config.SettingTone); err != nil {
		return nil, err
	}
	if r.format, _, err = resolve(config.SettingOutputFormat); err != nil {
		return nil, err
	}
	if _, r.formatSource, err = appConfig.Resolve(config.SettingOutputFormat, ov(config.SettingOutputFormat)); err != nil {
		return nil, err
	}
	if r.format == "" {
		r.format = orchestrator.FormatArticle
		r.formatSource = "default"
	}
	_ = searchLangSet
	return r, nil
}

// resolveEditorial resolves the publication preset for a run from the
// resolved editorial settings.
//
// It returns nils (no error) when no publication is selected. A selected
// publication without a bundle root is a clean configuration error. On
// success it also applies the preset's own defaults to the run's resolved
// settings: the preset's language and renderer contribute only where no
// higher precedence layer (flag, environment, config file) provided a
// value — i.e. where the resolution fell to the built-in default or came
// back unset. The preset sits below the config file in the precedence
// chain, because it is data from the bundle, not operator configuration.
func resolveEditorial(r *runResolved) (*editorial.Preset, *run.Publication, error) {
	if r.publication == "" {
		return nil, nil, nil
	}
	if r.bundleRoot == "" {
		return nil, nil, fmt.Errorf(
			"publication %q requires an editorial bundle root: pass --bundle-root or set editorial.bundle_root (the flag, $NEWSROOM_EDITORIAL_BUNDLE, or the config file)",
			r.publication)
	}
	loader, err := editorial.NewLoader(editorial.Config{Root: r.bundleRoot})
	if err != nil {
		return nil, nil, fmt.Errorf("loading editorial bundle: %w", err)
	}
	preset, err := loader.LoadPreset(r.publication)
	if err != nil {
		return nil, nil, fmt.Errorf("publication %q: %w", r.publication, err)
	}
	if preset.Language != "" && (r.languageSource == "default" || r.languageSource == "unset") {
		r.language = preset.Language
	}
	if preset.Renderer != "" && (r.formatSource == "default" || r.formatSource == "unset") {
		r.format = preset.Renderer
	}
	pub := &run.Publication{
		ID:         preset.Name,
		BundlePath: loader.Root(),
		Hash:       preset.Hash,
	}
	return preset, pub, nil
}

// resumePublication re-derives the publication preset of a resumed run from
// the run's own manifest record, not from the current configuration: a
// resume must behave like the original run, so the recorded preset name and
// bundle path win over whatever the operator reconfigured since the failure.
//
// A bundle that changed on disk (preset hash mismatch) does not fail the
// resume; it is reported on stderr, because the run's own snapshot already
// pins the settings the stages resolve from the manifest.
func resumePublication(m *run.Manifest, r *runResolved) (*editorial.Preset, error) {
	pub := m.Publication
	if pub == nil {
		// The run recorded no publication: a resumed run keeps behaving
		// without one, even if the operator configured a publication in the
		// meantime — the manifest is the source of truth for what the run was.
		return nil, nil
	}
	bundle := pub.BundlePath
	if bundle == "" {
		bundle = r.bundleRoot
	}
	if bundle == "" {
		return nil, fmt.Errorf("run %s recorded publication %q but no editorial bundle root is available: pass --bundle-root or set editorial.bundle_root", m.ID, pub.ID)
	}
	loader, err := editorial.NewLoader(editorial.Config{Root: bundle})
	if err != nil {
		return nil, fmt.Errorf("loading editorial bundle: %w", err)
	}
	preset, err := loader.LoadPreset(pub.ID)
	if err != nil {
		return nil, fmt.Errorf("publication %q: %w", pub.ID, err)
	}
	if pub.Hash != "" && preset.Hash != pub.Hash {
		fmt.Fprintf(os.Stderr, "[newsroom] warning: publication %q content differs from what run %s recorded (the bundle changed); using the current bundle\n", pub.ID, m.ID)
	}
	if preset.Language != "" && (r.languageSource == "default" || r.languageSource == "unset") {
		r.language = preset.Language
	}
	if preset.Renderer != "" && (r.formatSource == "default" || r.formatSource == "unset") {
		r.format = preset.Renderer
	}
	return preset, nil
}

// selectProfile determines the profile source for a run.
//
// The spec precedence is: explicit --profile flag > profiles.name setting
// (when explicitly set, i.e. not the built-in default) > the publication
// preset's profile reference. The root follows the source: a selected
// publication preset reads the profile from the editorial bundle, otherwise
// from the profiles root (the historical behavior, unchanged).
//
// ok is false when no profile applies at all.
func selectProfile(preset *editorial.Preset, r *runResolved, intent runIntent) (root, spec string, ok bool) {
	spec = intent.ProfileSpec
	if spec == "" && r.profilesNameSet {
		spec = r.profilesName
	}
	if preset == nil {
		return r.profilesRoot, spec, spec != ""
	}
	// A selected publication reads the profile from the bundle; a missing
	// spec falls back to the preset's own profile reference, which may be
	// empty (the bundle's common profile, no author).
	if spec == "" {
		spec = preset.Profile
	}
	return r.bundleRoot, spec, true
}

// loadRunProfile loads the editorial profile for a run.
//
// spec is "<publication>/<author>", "<author>", or "" (the bundle's common
// profile without an author). fromBundle distinguishes the two root kinds
// for error messages and the empty-profile rule: an empty bundle (no common
// content and no author) yields "no profile" rather than a fake identity,
// while a named profile under the profiles root always yields a profile
// (the historical behavior).
func loadRunProfile(ctx context.Context, root, spec string, fromBundle bool) (*run.Profile, profiles.Injector, error) {
	base := root
	author := ""
	profileID := ""
	if spec != "" {
		if i := strings.LastIndex(spec, "/"); i >= 0 {
			author = spec[i+1:]
			profileID = spec[:i] + "." + author
			base = filepath.Join(root, spec[:i])
		} else {
			author = spec
			profileID = author
		}
		if author == "" {
			return nil, nil, fmt.Errorf("invalid profile %q: expected <publication>/<author> or <author>", spec)
		}
	}
	if root == "" {
		if fromBundle {
			return nil, nil, fmt.Errorf("profile needs an editorial bundle root: pass --bundle-root or set editorial.bundle_root (the flag, environment, or config file)")
		}
		return nil, nil, fmt.Errorf("profile needs a profiles root: pass --profile-root or set profiles.root (the flag, environment, or config file)")
	}
	if spec == "" && !fromBundle {
		// Historical contract: an empty spec means "no profile" outside a
		// bundle. The production callers gate on selectProfile, but the rule
		// is kept explicit here for direct callers.
		return nil, nil, nil
	}

	loader, err := profiles.NewLoader(profiles.Config{
		BasePath:       base,
		AuthorOverride: author,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("loading profile %q: %w", spec, err)
	}
	p, err := loader.Load(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("loading profile %q: %w", spec, err)
	}

	if spec == "" {
		// A publication with no profile reference loads the bundle's common
		// profile. If that content is empty, the run simply has no profile:
		// an empty bundle must not fake a profile identity.
		if profileEmpty(p) {
			return nil, nil, nil
		}
		profileID = "common"
	}
	inst := profiles.NewInjector(p)
	return &run.Profile{
		ID:         profileID,
		SourcePath: base,
		Hash:       p.ContentHash(),
	}, inst, nil
}

// loadRunProfileInstance loads only the profile instance (used by the resume
// path, which must not re-derive manifest metadata).
func loadRunProfileInstance(ctx context.Context, r *runResolved, preset *editorial.Preset, intent runIntent) (profiles.Injector, error) {
	root, spec, ok := selectProfile(preset, r, intent)
	if !ok {
		return nil, nil
	}
	_, inst, err := loadRunProfile(ctx, root, spec, preset != nil)
	return inst, err
}

// profileEmpty reports whether a loaded profile carries no editorial content
// (the engine's immutable core facts policy is always present and does not
// count as profile content).
func profileEmpty(p profiles.Profile) bool {
	return p.Style == "" && p.Vocabulary == "" && p.OutputFormat == "" && len(p.Examples) == 0
}
