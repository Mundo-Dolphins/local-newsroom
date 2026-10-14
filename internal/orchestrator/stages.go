package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Mundo-Dolphins/local-newsroom/internal/archiverev"
	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/discovery"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
	"github.com/Mundo-Dolphins/local-newsroom/internal/verifier"
)

// DefaultMaxSources bounds the number of source URLs selected from discovery.
const DefaultMaxSources = 5

// Stage artifact file names (relative to the run directory).
const (
	sourcesIndexPath   = "sources/index.json"
	fetchIndexPath     = "fetch/index.json"
	documentsIndexPath = "documents/index.json"
	researchIndexPath  = "research/index.json"
	dossierPath        = "research/dossier.json"
	verificationPath   = "verify/verification.json"
	followUpResultPath = "follow_up/result.json"
	artifactPath       = "write/artifact.json"
	finalCheckPath     = "final_check/result.json"
)

// sourcesIndex is the first persisted artifact of a run: the source URL list
// every later stage works from. It is written by the discovery stage in
// every source mode, so fetch (and any resume of the run) never re-derives
// the selection.
type sourcesIndex struct {
	Mode         run.SourceMode        `json:"mode"`
	Topic        string                `json:"topic"`
	Language     string                `json:"language,omitempty"`
	ExplicitURLs []string              `json:"explicit_urls,omitempty"`
	Queries      []planner.SearchQuery `json:"queries,omitempty"`
	Candidates   []discovery.Candidate `json:"candidates,omitempty"`
	Selected     []string              `json:"selected"`
	SelectedAt   time.Time             `json:"selected_at"`
}

// fetchIndex records the outcome of every attempted URL retrieval.
type fetchIndex struct {
	Attempted []FetchedSource `json:"attempted"`
	Successes int             `json:"successes"`
	Failed    int             `json:"failed"`
}

// documentsIndex lists the normalized documents produced by extract. The
// Paths are recorded (parallel to SourceIDs) because the extract stage names
// each file after the document's canonical host, which a later stage cannot
// re-derive from the source ID alone.
type documentsIndex struct {
	Count     int      `json:"count"`
	SourceIDs []string `json:"source_ids"`
	Paths     []string `json:"paths"`
}

// researchIndex records what the research stage synthesized from.
type researchIndex struct {
	Documents   int      `json:"documents"`
	FromArchive int      `json:"from_archive"`
	ClaimIDs    []string `json:"claim_ids"`
	SourceIDs   []string `json:"source_ids"`
}

// ---------------------------------------------------------------------------
// Run parameter resolution
//
// Run-level parameters come from the manifest's config snapshot when present
// (so a resumed run is deterministic) and fall back to the orchestrator's
// creation-time configuration.
// ---------------------------------------------------------------------------

// runParam returns a string run parameter from the manifest snapshot with a
// fallback to the given default.
func (o *Orchestrator) runParam(in *runner.StageInput, name, fallback string) string {
	if v, ok := in.Manifest.Config.Get(name); ok && v != "" {
		return v
	}
	return fallback
}

// languageFromRun returns the run language: manifest snapshot (run.language,
// then the defaults.language the command resolved) else empty.
func (o *Orchestrator) languageFromRun(in *runner.StageInput) string {
	if v, ok := in.Manifest.Config.Get(SettingRunLanguage); ok && v != "" {
		return v
	}
	if v, ok := in.Manifest.Config.Get(config.SettingDefaultsLanguage); ok && v != "" {
		return v
	}
	return ""
}

// explicitURLsFromRun returns the explicit source URLs recorded in the
// manifest snapshot (JSON array), falling back to the orchestrator config.
func (o *Orchestrator) explicitURLsFromRun(in *runner.StageInput) []string {
	if v, ok := in.Manifest.Config.Get(SettingRunExplicitURLs); ok && v != "" {
		var urls []string
		if jsonUnmarshal([]byte(v), &urls) == nil {
			return urls
		}
	}
	return o.cfg.ExplicitURLs
}

// archiveTopKFromRun returns the archived-document bound for the run.
func (o *Orchestrator) archiveTopK(in *runner.StageInput) int {
	if v, ok := in.Manifest.Config.Get(SettingRunArchiveTopK); ok {
		if n, err := parseInt(v); err == nil {
			return n
		}
	}
	return o.cfg.ArchiveTopK
}

// archiveEnabledForRun reports whether archive retrieval is enabled for the
// run. The manifest snapshot wins: a resumed run must behave like the
// original, and a service that exists in the orchestrator but was disabled
// for the run must not quietly change the research stage's inputs.
func (o *Orchestrator) archiveEnabledForRun(in *runner.StageInput) bool {
	if v, ok := in.Manifest.Config.Get(SettingRunArchiveEnabled); ok {
		if b, err := parseBool(v); err == nil {
			return b
		}
	}
	return o.cfg.Archive != nil
}

// maxSourcesFromRun returns the source bound for the run (0 = default).
func (o *Orchestrator) maxSourcesFromRun(in *runner.StageInput) int {
	if v, ok := in.Manifest.Config.Get(SettingRunMaxSources); ok {
		if n, err := parseInt(v); err == nil {
			return n
		}
	}
	return o.cfg.MaxSources
}

// ---------------------------------------------------------------------------
// Stage executors
// ---------------------------------------------------------------------------

// runDiscovery materializes the source URL list. In explicit mode it records
// the provided URLs; in discovered/supplement mode it runs the discovery
// service and selects candidate URLs.
func (o *Orchestrator) runDiscovery(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
	explicit := dedupeStrings(o.explicitURLsFromRun(in))
	mode := in.Manifest.SourceMode
	idx := sourcesIndex{
		Mode:         mode,
		Topic:        in.Manifest.Topic,
		Language:     o.languageFromRun(in),
		ExplicitURLs: explicit,
		SelectedAt:   o.cfg.Clock(),
	}
	out := &runner.StageOutput{}

	switch mode {
	case run.SourceModeExplicit:
		if len(explicit) == 0 {
			return nil, errors.New("discovery: run is in explicit source mode but no explicit URLs are recorded")
		}
		idx.Selected = explicit

	case run.SourceModeDiscovered, run.SourceModeSupplement:
		if o.cfg.Discovery == nil {
			return nil, errors.New("discovery: source mode requires discovery, but no Discovery service is configured (pass --url flags or configure search)")
		}
		res, err := o.cfg.Discovery.Discover(ctx, in.Manifest.Topic, idx.Language, nil)
		if err != nil {
			return nil, fmt.Errorf("discovery: %w", err)
		}
		for _, d := range res.Diagnostics {
			if d.Level == discovery.DiagnosticLevelWarning || d.Level == discovery.DiagnosticLevelError {
				out.Warnings = append(out.Warnings, runner.Warning{Code: "discovery." + d.Code, Message: d.Message})
			}
		}
		idx.Queries = res.PlannedQueries
		idx.Candidates = res.Candidates
		selected := selectCandidateURLs(res.Candidates, o.maxSourcesFromRun(in))
		if mode == run.SourceModeSupplement {
			selected = mergeURLs(explicit, selected)
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("discovery: no source URLs selected for topic %q (search returned no usable candidates)", in.Manifest.Topic)
		}
		idx.Selected = selected
	default:
		return nil, fmt.Errorf("discovery: unsupported source mode %q", mode)
	}

	f, err := o.stageFile(run.ArtifactKindCustom, sourcesIndexPath, idx)
	if err != nil {
		return nil, err
	}
	out.Files = append(out.Files, f)
	return out, nil
}

// selectCandidateURLs picks source URLs from the ranked discovery candidates:
// deduplicated, valid http(s) URLs only, in the order discovery ranked them,
// bounded by max (0 or negative means DefaultMaxSources).
func selectCandidateURLs(cands []discovery.Candidate, max int) []string {
	if max <= 0 {
		max = DefaultMaxSources
	}
	selected := make([]string, 0, max)
	seen := make(map[string]bool)
	for _, c := range cands {
		u := strings.TrimSpace(c.CandidateURL)
		if u == "" || seen[u] || !isValidHTTPURL(u) {
			continue
		}
		seen[u] = true
		selected = append(selected, u)
		if len(selected) >= max {
			break
		}
	}
	return selected
}

// runFetch retrieves raw content for every selected source URL.
func (o *Orchestrator) runFetch(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
	idx, err := loadStageJSON[sourcesIndex](in, sourcesIndexPath)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	if len(idx.Selected) == 0 {
		return nil, errors.New("fetch: no source URLs to fetch (sources index is empty)")
	}
	if o.cfg.Fetcher == nil {
		return nil, errors.New("fetch: no SourceFetcher is configured")
	}

	results := o.cfg.Fetcher.FetchMany(ctx, idx.Selected)
	out := &runner.StageOutput{}
	fidx := fetchIndex{Attempted: results}
	for i, src := range results {
		if !src.OK {
			fidx.Failed++
			out.Warnings = append(out.Warnings, runner.Warning{
				Code:    "source.failed",
				Message: fmt.Sprintf("fetching %s: %s", src.URL, src.Error),
			})
			continue
		}
		fidx.Successes++
		f, err := o.stageFile(run.ArtifactKindSource, rawFetchPath(i, src), src.RawBody)
		if err != nil {
			return nil, err
		}
		out.Files = append(out.Files, f)
	}
	if fidx.Successes == 0 {
		return nil, fmt.Errorf("fetch: all %d source URLs failed to retrieve; no documents to research", len(idx.Selected))
	}
	f, err := o.stageFile(run.ArtifactKindCustom, fetchIndexPath, fidx)
	if err != nil {
		return nil, err
	}
	out.Files = append(out.Files, f)
	return out, nil
}

// runExtract normalizes every successfully fetched source into a document.
func (o *Orchestrator) runExtract(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
	if o.cfg.Extractor == nil {
		return nil, errors.New("extract: no SourceExtractor is configured")
	}
	fidx, err := loadStageJSON[fetchIndex](in, fetchIndexPath)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	out := &runner.StageOutput{}
	didx := documentsIndex{SourceIDs: []string{}, Paths: []string{}}
	for i, src := range fidx.Attempted {
		if !src.OK {
			continue
		}
		// The fetch index deliberately excludes raw bodies (json:"-"); each
		// body was persisted as its own artifact file, so load it from there.
		// This also works on resume, where the in-memory body is gone.
		raw, err := in.Store.LoadArtifact(in.RunID, rawFetchPath(i, src))
		if err != nil {
			return nil, fmt.Errorf("extract: loading fetched body for %s: %w", src.URL, err)
		}
		src.RawBody = raw
		doc, err := o.cfg.Extractor.Extract(ctx, src)
		if err != nil {
			out.Warnings = append(out.Warnings, runner.Warning{
				Code:    "extract.failed",
				Message: fmt.Sprintf("extracting %s: %s", src.URL, err),
			})
			continue
		}
		path := documentPath(i, doc)
		f, err := o.stageFile(run.ArtifactKindDocument, path, doc)
		if err != nil {
			return nil, err
		}
		out.Files = append(out.Files, f)
		didx.SourceIDs = append(didx.SourceIDs, doc.SourceID)
		didx.Paths = append(didx.Paths, path)
	}
	didx.Count = len(didx.SourceIDs)
	if didx.Count == 0 {
		return nil, errors.New("extract: no documents could be extracted from the fetched sources")
	}
	f, err := o.stageFile(run.ArtifactKindCustom, documentsIndexPath, didx)
	if err != nil {
		return nil, err
	}
	out.Files = append(out.Files, f)
	return out, nil
}

// runResearch synthesizes the research dossier from the extracted documents,
// augmented with archived documents when an archive is configured.
func (o *Orchestrator) runResearch(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
	if o.cfg.Research == nil {
		return nil, errors.New("research: no Research service is configured")
	}
	didx, err := loadStageJSON[documentsIndex](in, documentsIndexPath)
	if err != nil {
		return nil, fmt.Errorf("research: %w", err)
	}
	docs := make([]types.Document, 0, len(didx.SourceIDs))
	for i, sourceID := range didx.SourceIDs {
		// The path comes from the index: the file name embeds the document's
		// canonical host, which is not derivable from the source ID.
		path := documentPath(i, &types.Document{SourceID: sourceID})
		if i < len(didx.Paths) && didx.Paths[i] != "" {
			path = didx.Paths[i]
		}
		doc, err := loadStageJSON[types.Document](in, path)
		if err != nil {
			return nil, fmt.Errorf("research: %w", err)
		}
		docs = append(docs, *doc)
	}
	if len(docs) == 0 {
		return nil, errors.New("research: no documents to research")
	}

	out := &runner.StageOutput{}
	ri := researchIndex{Documents: len(docs)}

	// Optional archive augmentation: a failed or empty retrieval never
	// fails the run (live sources carry the research).
	if o.cfg.Archive != nil && o.archiveEnabledForRun(in) {
		liveURLs := make([]string, 0, len(docs))
		for _, d := range docs {
			if d.CanonicalURL != nil {
				liveURLs = append(liveURLs, *d.CanonicalURL)
			}
		}
		ares, err := o.cfg.Archive.Retrieve(ctx, in.Manifest.Topic, liveURLs)
		if err != nil {
			out.Warnings = append(out.Warnings, runner.Warning{Code: "archive.failed", Message: "archive retrieval failed: " + err.Error()})
		} else if ares != nil {
			archived := boundDocuments(aresToDocuments(ares), o.archiveTopK(in))
			for i, doc := range archived {
				f, err := o.stageFile(run.ArtifactKindDocument, archiveDocumentPath(i, doc), doc)
				if err != nil {
					return nil, err
				}
				out.Files = append(out.Files, f)
				docs = append(docs, doc)
				didx.SourceIDs = append(didx.SourceIDs, doc.SourceID)
			}
			ri.FromArchive = len(archived)
		}
	}

	dossier, err := o.cfg.Research.Generate(ctx, in.Manifest.Topic, docs)
	if err != nil {
		return nil, fmt.Errorf("research: %w", err)
	}
	ri.ClaimIDs = claimIDs(dossier.Claims)
	ri.SourceIDs = make([]string, len(dossier.Sources))
	for i, s := range dossier.Sources {
		ri.SourceIDs[i] = s.StableID
	}

	f, err := o.stageFile(run.ArtifactKindDossier, dossierPath, dossier)
	if err != nil {
		return nil, err
	}
	out.Files = append(out.Files, f)
	f, err = o.stageFile(run.ArtifactKindCustom, researchIndexPath, ri)
	if err != nil {
		return nil, err
	}
	out.Files = append(out.Files, f)
	return out, nil
}

// runVerify verifies the dossier's claims.
func (o *Orchestrator) runVerify(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
	if o.cfg.Verify == nil {
		return nil, errors.New("verify: no Verify service is configured")
	}
	dossier, err := loadStageJSON[researcher.ResearchDossier](in, dossierPath)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	vres, err := o.cfg.Verify.Verify(ctx, verifier.Input{
		Dossier: dossier,
		Parameters: verifier.VerificationParameters{
			VerifierID:       o.cfg.VerifierID,
			VerificationDate: o.cfg.Clock(),
			QualityThreshold: o.cfg.QualityThreshold,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	out := &runner.StageOutput{}

	if score := vres.CalculateQualityScore(); score < o.cfg.QualityThreshold {
		out.Warnings = append(out.Warnings, runner.Warning{
			Code:    "quality.low",
			Message: fmt.Sprintf("verification quality score %.1f is below the threshold %.1f", score, o.cfg.QualityThreshold),
		})
	}
	if n := len(vres.GetInsufficientEvidenceClaims()) + len(vres.GetUncertainClaims()) + len(vres.GetContradictedClaims()); n > 0 {
		out.Warnings = append(out.Warnings, runner.Warning{
			Code:    "verification.unresolved",
			Message: fmt.Sprintf("%d claim(s) are not fully supported by evidence", n),
		})
	}

	f, err := o.stageFile(run.ArtifactKindVerification, verificationPath, vres)
	if err != nil {
		return nil, err
	}
	out.Files = append(out.Files, f)
	return out, nil
}

// runFollowUp performs bounded follow-up research for the verification's
// unresolved claims. The stage is skipped at plan time (BuildStages) when
// follow-up is disabled for the run.
func (o *Orchestrator) runFollowUp(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
	if o.cfg.FollowUp == nil {
		return nil, errors.New("follow_up: no FollowUp service is configured")
	}
	dossier, err := loadStageJSON[researcher.ResearchDossier](in, dossierPath)
	if err != nil {
		return nil, fmt.Errorf("follow_up: %w", err)
	}
	vres, err := loadStageJSON[contracts.VerificationResult](in, verificationPath)
	if err != nil {
		return nil, fmt.Errorf("follow_up: %w", err)
	}
	result, err := o.cfg.FollowUp.Run(ctx, dossier, vres)
	if err != nil {
		return nil, fmt.Errorf("follow_up: %w", err)
	}
	if result == nil {
		return nil, errors.New("follow_up: service returned no result")
	}
	out := &runner.StageOutput{}
	for _, w := range result.Warnings {
		out.Warnings = append(out.Warnings, runner.Warning{Code: "follow_up.warning", Message: w})
	}
	f, err := o.stageFile(run.ArtifactKindCustom, followUpResultPath, result)
	if err != nil {
		return nil, err
	}
	out.Files = append(out.Files, f)
	return out, nil
}

// runWrite writes the editorial artifact from the verified claims.
func (o *Orchestrator) runWrite(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
	if o.cfg.Write == nil {
		return nil, errors.New("write: no Write service is required")
	}
	vres, err := loadStageJSON[contracts.VerificationResult](in, verificationPath)
	if err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	artifact, err := o.cfg.Write.Generate(ctx, vres)
	if err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	f, err := o.stageFile(run.ArtifactKindEditorial, artifactPath, artifact)
	if err != nil {
		return nil, err
	}
	return &runner.StageOutput{Files: []runner.StageFile{f}}, nil
}

// runFinalCheck runs the final factual check. The result is always written
// to the run directory (even when the gate fails, so a failed run keeps its
// check record for inspection), and a non-pass status fails the run: the
// render stage is never reached and no usable output is produced.
//
// The result file is only RECORDED on the manifest when the check passes:
// the manifest model forbids artifacts on a stage that did not succeed, and
// the resume layer enforces that invariant (a failed stage holding recorded
// artifacts would make the run unresumable). The file itself remains on disk
// either way — the render gate and the summary read it by path, not by
// manifest reference.
func (o *Orchestrator) runFinalCheck(ctx context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
	if o.cfg.Check == nil {
		return nil, errors.New("final_check: no Check service is configured")
	}
	artifact, err := loadStageJSON[contracts.EditorialArtifact](in, artifactPath)
	if err != nil {
		return nil, fmt.Errorf("final_check: %w", err)
	}
	vres, err := loadStageJSON[contracts.VerificationResult](in, verificationPath)
	if err != nil {
		return nil, fmt.Errorf("final_check: %w", err)
	}
	result, err := o.cfg.Check.Check(ctx, artifact, vres)
	if err != nil {
		return nil, fmt.Errorf("final_check: %w", err)
	}

	// Persist the result before applying the gate so a failed run keeps its
	// check record on disk (the runner does not persist the output of a
	// failed stage).
	data, err := jsonMarshalIndent(result)
	if err != nil {
		return nil, fmt.Errorf("final_check: marshaling result: %w", err)
	}
	ref := run.ArtifactRef{
		Kind:       run.ArtifactKindFactualCheck,
		ID:         uuid.NewString(),
		Stage:      run.StageFinalCheck,
		Path:       finalCheckPath,
		ProducedAt: run.NewTimestamp(o.cfg.Clock()),
	}
	rel, err := in.Store.SaveArtifact(in.RunID, ref, data)
	if err != nil {
		return nil, fmt.Errorf("final_check: persisting result: %w", err)
	}

	if result.OverallStatus != contracts.FactualCheckStatusPass {
		// Gate: fail the run. The result file is kept on disk for inspection
		// and for the render gate, but deliberately NOT recorded on the
		// manifest: the manifest model forbids artifacts on a stage that did
		// not succeed, and the resume layer enforces that invariant.
		return nil, fmt.Errorf("final factual check did not pass (status: %s, %d finding(s)); the run fails and no output is rendered — fix the issue and resume with `newsroom run --resume %s`",
			result.OverallStatus, len(result.FactualFindings), in.RunID)
	}

	// The check passed: record the result on the manifest (the stage
	// succeeded, so the reference is consistent) — the run's durable record
	// names its own final check.
	ref.Path = rel
	if err := in.Manifest.AddArtifact(ref); err != nil {
		return nil, fmt.Errorf("final_check: recording result: %w", err)
	}
	return &runner.StageOutput{}, nil
}

// runRender renders the final output. It refuses to run unless the persisted
// final check result is a pass (a second, independent gate: a run that
// reached render with a non-pass check is inconsistent and must not produce
// usable output).
func (o *Orchestrator) runRender(_ context.Context, in *runner.StageInput) (*runner.StageOutput, error) {
	result, err := loadStageJSON[contracts.FactualCheckResult](in, finalCheckPath)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	if result.OverallStatus != contracts.FactualCheckStatusPass {
		return nil, fmt.Errorf("render: refused — the final factual check did not pass (status: %s)", result.OverallStatus)
	}
	artifact, err := loadStageJSON[contracts.EditorialArtifact](in, artifactPath)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}

	format := o.runParam(in, SettingRunFormat, o.cfg.Format)
	out := &runner.StageOutput{}

	if format == FormatArticle || format == FormatHybrid {
		text, err := o.cfg.Markdown.Render(artifact)
		if err != nil {
			return nil, fmt.Errorf("render: markdown: %w", err)
		}
		f, err := o.renderFile("output/article.md", []byte(text))
		if err != nil {
			return nil, err
		}
		out.Files = append(out.Files, f)
	}
	if format == FormatThread || format == FormatHybrid {
		posts, err := o.cfg.Thread.Render(artifact)
		if err != nil {
			return nil, fmt.Errorf("render: thread: %w", err)
		}
		f, err := o.renderFile("output/thread.txt", []byte(renderThreadPosts(posts)))
		if err != nil {
			return nil, err
		}
		out.Files = append(out.Files, f)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Path and serialization helpers
// ---------------------------------------------------------------------------

// rawFetchPath returns the artifact path for one raw fetched body.
func rawFetchPath(i int, src FetchedSource) string {
	return fmt.Sprintf("fetch/raw-%03d-%s%s", i, safeFilePart(hostOf(src.URL)), extensionForContentType(src.ContentType))
}

// documentPath returns the artifact path for one normalized document.
func documentPath(i int, doc *types.Document) string {
	host := "doc"
	if doc != nil && doc.CanonicalURL != nil {
		host = hostOf(*doc.CanonicalURL)
	}
	return fmt.Sprintf("documents/doc-%03d-%s.json", i, safeFilePart(host))
}

// archiveDocumentPath returns the artifact path for one archived document.
func archiveDocumentPath(i int, doc types.Document) string {
	host := hostOf(doc.SourceID)
	if h := safeFilePart(host); h != "source" {
		return fmt.Sprintf("research/archive-%03d-%s.json", i, h)
	}
	if doc.CanonicalURL != nil {
		return fmt.Sprintf("research/archive-%03d-%s.json", i, safeFilePart(hostOf(*doc.CanonicalURL)))
	}
	return fmt.Sprintf("research/archive-%03d.json", i)
}

// renderFile builds a stage file for rendered output content, ensuring a
// trailing newline.
func (o *Orchestrator) renderFile(path string, data []byte) (runner.StageFile, error) {
	out := append([]byte(nil), data...)
	if len(out) == 0 || out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return runner.StageFile{Kind: run.ArtifactKindRendered, ID: uuid.NewString(), Path: path, Data: out}, nil
}

// renderThreadPosts joins a post thread into one text file: posts separated
// by a single blank line, first post first.
func renderThreadPosts(posts []string) string {
	var sb strings.Builder
	for i, p := range posts {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(strings.TrimRight(p, "\n"))
		sb.WriteString("\n")
	}
	return sb.String()
}

// aresToDocuments copies the archived documents from a retrieval result.
func aresToDocuments(res *archiverev.RetrieveResult) []types.Document {
	if res == nil {
		return nil
	}
	docs := make([]types.Document, 0, len(res.Documents))
	docs = append(docs, res.Documents...)
	return docs
}

// boundDocuments limits a document list to n entries (n <= 0 keeps all).
func boundDocuments(docs []types.Document, n int) []types.Document {
	if n > 0 && len(docs) > n {
		return docs[:n]
	}
	return docs
}

// claimIDs extracts the claim IDs from a dossier's claims.
func claimIDs(claims []researcher.Claim) []string {
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}
	return ids
}

// hostOf extracts the URL host for file naming.
func hostOf(u string) string {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		return "source"
	}
	return parsed.Host
}

// safeFilePart reduces a string to a safe file name fragment.
func safeFilePart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}
	out := strings.Trim(sb.String(), "-.")
	if out == "" {
		return "source"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

// extensionForContentType maps a content type to a file extension.
func extensionForContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	switch {
	case strings.Contains(ct, "json"):
		return ".json"
	case strings.Contains(ct, "html"):
		return ".html"
	case strings.Contains(ct, "xml"):
		return ".xml"
	case strings.Contains(ct, "text"):
		return ".txt"
	default:
		return ".bin"
	}
}

// dedupeStrings removes duplicate (and empty) strings, preserving order.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// mergeURLs joins two URL lists, preserving the first list's order and
// appending the second's unseen entries.
func mergeURLs(first, second []string) []string {
	out := dedupeStrings(first)
	seen := make(map[string]bool, len(out))
	for _, u := range out {
		seen[u] = true
	}
	for _, u := range dedupeStrings(second) {
		if !seen[u] {
			out = append(out, u)
		}
	}
	return out
}

// isValidHTTPURL reports whether s is an absolute http(s) URL.
func isValidHTTPURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// parseInt parses a snapshot integer.
func parseInt(v string) (int, error) {
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}
