package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/orchestrator"
	"github.com/Mundo-Dolphins/local-newsroom/internal/profiles"
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
	profileInst, err := loadRunProfileInstance(ctx, r.profilesRoot, r.profilesName, r.profilesNameSet, intent.ProfileSpec)
	if err != nil {
		return nil, fmt.Errorf("loading editorial profile for resume: %w", err)
	}

	orchCfg := orchestrator.Config{
		Store:            store,
		Format:           format,
		MaxSources:       maxSourcesOrDefault(r.maxSources),
		ArchiveTopK:      r.archiveTopK,
		QualityThreshold: r.quality,
	}
	orchCfg, err = buildRunServices(ctx, orchCfg, serviceInputs{
		llm:            r.newLLMClient(),
		resolved:       r,
		followUp:       followUpEnabled,
		archiveEnabled: archiveEnabled,
		profile:        profileInst,
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

	// --- Editorial profile ----------------------------------------------
	profileRef, profileInst, err := loadRunProfile(ctx, r.profilesRoot, r.profilesName, r.profilesNameSet, intent.ProfileSpec)
	if err != nil {
		return nil, err
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
	m, err := run.NewManifest(run.NewManifestParams{
		Workspace:  ws,
		Topic:      strings.TrimSpace(intent.Topic),
		SourceMode: mode,
		Profile:    profileRef,
		Model:      run.NewModelMetadata("omlx", r.llmModel, r.llmBaseURL),
		Config:     snap,
		Stages:     run.CanonicalStages(),
		StartedAt:  now,
	})
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
	}
	orchCfg, err = buildRunServices(ctx, orchCfg, serviceInputs{
		llm:            r.newLLMClient(),
		resolved:       r,
		followUp:       intent.FollowUp,
		archiveEnabled: archiveEnabled,
		profile:        profileInst,
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

	if r.language, _, err = resolve(config.SettingDefaultsLanguage); err != nil {
		return nil, err
	}
	if r.language == "" {
		// The search language doubles as the generation language when no
		// explicit language is configured.
		r.language = r.searchLanguage
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
	if r.format == "" {
		r.format = orchestrator.FormatArticle
	}
	_ = searchLangSet
	return r, nil
}

// loadRunProfile resolves and loads the editorial profile for a run.
//
// spec takes precedence over the profiles.name setting; a two-segment value
// (<publication>/<author>) selects a publication directory under the
// profiles root, a one-segment value selects an author directly under the
// root. When neither is specified, (nil, nil, nil) is returned.
func loadRunProfile(ctx context.Context, root, name string, nameSet bool, spec string) (*run.Profile, profiles.Injector, error) {
	if spec == "" && nameSet {
		spec = name
	}
	if spec == "" {
		return nil, nil, nil
	}
	if root == "" {
		return nil, nil, fmt.Errorf("profile %q needs a profiles root: pass --profile-root or set profiles.root (the flag, environment, or config file)", spec)
	}

	base := root
	author := ""
	if i := strings.LastIndex(spec, "/"); i >= 0 {
		base = filepath.Join(root, spec[:i])
		author = spec[i+1:]
	} else {
		author = spec
	}
	if author == "" {
		return nil, nil, fmt.Errorf("invalid profile %q: expected <publication>/<author> or <author>", spec)
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

	inst := profiles.NewInjector(p)
	profileID := author
	if i := strings.LastIndex(spec, "/"); i >= 0 {
		profileID = spec[:i] + "." + author
	}
	return &run.Profile{
		ID:         profileID,
		SourcePath: base,
		Hash:       profileHash(p),
	}, inst, nil
}

// loadRunProfileInstance loads only the profile instance (used by the resume
// path, which must not re-derive manifest metadata).
func loadRunProfileInstance(ctx context.Context, root, name string, nameSet bool, spec string) (profiles.Injector, error) {
	_, inst, err := loadRunProfile(ctx, root, name, nameSet, spec)
	return inst, err
}
