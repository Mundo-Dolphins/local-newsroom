// Package review builds the deterministic human review bundle for a newsroom
// run: a compact, human-readable projection of the run manifest and the
// stage artifacts persisted in the run directory, rendered as a single
// Markdown file (review.md) that a person can inspect before using the run's
// output.
//
// The bundle is strictly a *reader*: it performs no LLM calls, re-derives
// nothing, and summarizes only what the run persisted (plus what the run
// record says). Stages that were never reached (failed or cancelled runs)
// contribute "not reached" sections rather than errors, so every terminal
// run — succeeded, failed, or cancelled — can produce a review bundle.
//
// Security: the bundle renders content that crossed the workspace store's
// sanitization boundary on the way in, and every free-text value it prints
// passes run.RedactSecrets on the way out (defence in depth; redaction is
// idempotent). When the bundle is saved, the rendered document is passed
// through the store's content sanitizer before it is written to the run
// directory, so the file on disk is guaranteed secret-free.
package review

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/discovery"
	"github.com/Mundo-Dolphins/local-newsroom/internal/followup"
	"github.com/Mundo-Dolphins/local-newsroom/internal/planner"
	"github.com/Mundo-Dolphins/local-newsroom/internal/researcher"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
)

// Bundle is the review data for one run: the manifest projection plus the
// stage artifacts that exist in the run directory. All slices are in
// deterministic order (sorted or as persisted), and Render produces
// byte-identical output for a given bundle.
type Bundle struct {
	// --- Run identity and lifecycle (from the manifest) ----------------
	RunID         run.RunID
	WorkspaceName string
	WorkspacePath string
	Status        run.RunStatus
	Topic         string
	SourceMode    run.SourceMode
	StartedAt     run.Timestamp
	UpdatedAt     run.Timestamp
	FinishedAt    *run.Timestamp
	// RunDir is the absolute path of the run directory ("" when the store
	// could not resolve it).
	RunDir string
	// FailedStage names the stage that stopped a failed run ("" otherwise).
	FailedStage string
	// Reason is the run-level failure/cancellation reason ("" when none).
	Reason string

	// --- Profile and model metadata (from the manifest) ----------------
	ProfileID     string
	ProfilePath   string
	ProfileHash   string
	ModelProvider string
	ModelName     string
	ModelEndpoint string

	// Config is the run's configuration snapshot (values already redacted at
	// manifest construction).
	Config map[string]string

	// --- Sources --------------------------------------------------------
	// SourcesSelected are the URLs the run worked from, in selection order.
	SourcesSelected []string
	// FetchResults carries the per-URL retrieval outcome (parallel to the
	// attempted URLs, as persisted by the fetch stage).
	FetchResults []SourceFetch
	// FetchSuccesses / FetchFailed are the fetch stage's own counters.
	FetchSuccesses int
	FetchFailed    int
	// Queries are the planned search queries (discovered/supplement runs).
	Queries []string
	// Candidates is the number of discovery candidates before selection.
	Candidates int

	// DocumentsCount is the number of normalized documents the extract stage
	// produced (0 when not reached).
	DocumentsCount int

	// --- Research dossier ------------------------------------------------
	// DossierPresent is true when the dossier artifact was read.
	DossierPresent bool
	// DossierClaims is the number of claims the research stage produced.
	DossierClaims int
	// DossierSources are the sources the dossier records (research input).
	DossierSources []researcher.SourceReference

	// --- Verification -----------------------------------------------------
	// Verified is true when the verification artifact was read.
	Verified bool
	// ClaimsTotal counts every assessed claim.
	ClaimsTotal int
	// Claims by verification status.
	ClaimsSupported    int
	ClaimsContradicted int
	ClaimsUncertain    int
	ClaimsInsufficient int
	// QualityScore is the verification quality score (0-100).
	QualityScore float64
	// QualityScoreSet distinguishes a legitimate 0 score from "not reached".
	QualityScoreSet bool
	// Contradicted / Unresolved claims, sorted by claim ID. Each carries the
	// claim statement so a reviewer sees the issue without opening the
	// verification file.
	Contradicted []ClaimInfo
	Unresolved   []ClaimInfo
	// ContradictionCount is the number of documented contradictions.
	ContradictionCount int
	// VerificationSummary is the verifier's own summary text, when recorded.
	VerificationSummary string

	// --- Follow-up research ----------------------------------------------
	// FollowUpPresent is true when the follow-up result was read.
	FollowUpPresent bool
	// FollowUpSkippedReason is the stage's skip reason (when the stage did
	// not run).
	FollowUpSkippedReason string
	// FollowUpRounds is the number of follow-up rounds executed.
	FollowUpRounds int
	// FollowUpQueries / FollowUpSources bound the research performed.
	FollowUpQueries int
	FollowUpSources int
	// FollowUpResolved are the claim IDs resolved during follow-up.
	FollowUpResolved []string
	// FollowUpUnresolved are the claim IDs still unresolved afterwards.
	FollowUpUnresolved []string
	// FollowUpWarnings / FollowUpErrors are the loop's own notes.
	FollowUpWarnings []string
	FollowUpErrors   []string

	// --- Final factual check ----------------------------------------------
	// FinalCheckPresent is true when the final-check result file was read
	// (it is persisted even when the gate fails).
	FinalCheckPresent bool
	// FinalCheckStatus is pass / fail / review ("" when not reached).
	FinalCheckStatus contracts.FactualCheckStatus
	// FinalCheckConfidence is the checker's confidence level.
	FinalCheckConfidence contracts.ConfidenceLevel
	// FinalCheckSummary is the checker's summary text.
	FinalCheckSummary string
	// Findings are the actionable findings (order as persisted).
	Findings []contracts.FactualFinding
	// Unsupported / Altered / OutOfContext are the raw finding categories
	// (order as persisted).
	Unsupported  []contracts.UnsupportedStatement
	Altered      []contracts.AlteredDetail
	OutOfContext []contracts.OutOfContextStatement
	// Recommendations are the checker's recommended actions.
	Recommendations []contracts.Recommendation

	// --- Editorial artifact ------------------------------------------------
	// ArtifactPresent is true when the editorial artifact was read.
	ArtifactPresent bool
	// ArtifactTitle / ArtifactType identify the written story.
	ArtifactTitle string
	ArtifactType  contracts.ArtifactType
	// ArtifactClaims is the number of claim references in the artifact.
	ArtifactClaims int
	// ArtifactWarnings are the artifact-level warnings (e.g. low confidence).
	ArtifactWarnings []contracts.Warning

	// --- Output ------------------------------------------------------------
	// OutputPaths are the absolute paths of the rendered output files
	// recorded on the manifest, in manifest order.
	OutputPaths []string
	// OutputMissing lists the rendered paths that do not exist on disk.
	OutputMissing []string

	// --- Stage overview ------------------------------------------------------
	// Stages is the run's stage plan with per-stage status (manifest order).
	Stages []StageInfo

	// --- Run logs ------------------------------------------------------------
	// Warnings / Errors are the run-level logs (manifest order, already
	// redacted at the door).
	Warnings []run.Warning
	Errors   []run.ErrorSummary

	// BuildNotes are non-fatal problems found while assembling the bundle
	// (for example: a stage succeeded but its artifact file is missing).
	// They are surfaced prominently in the rendered document.
	BuildNotes []string
}

// SourceFetch is one attempted URL retrieval, as recorded by the fetch
// stage's index (the raw body is never part of the review).
type SourceFetch struct {
	URL         string    `json:"url"`
	Title       string    `json:"title,omitempty"`
	Status      int       `json:"status"`
	ContentType string    `json:"content_type,omitempty"`
	OK          bool      `json:"ok"`
	Error       string    `json:"error,omitempty"`
	RetrievedAt time.Time `json:"retrieved_at"`
}

// ClaimInfo is one claim with its verification status, for the bundle's
// claim listings.
type ClaimInfo struct {
	ID           string
	Statement    string
	Status       contracts.VerificationStatus
	Confidence   contracts.ConfidenceLevel
	Limitations  string
	HasConflicts bool // conflicting evidence was recorded
}

// StageInfo is one planned stage with its lifecycle state.
type StageInfo struct {
	Name       run.StageName
	Status     run.StageStatus
	SkipReason string
	Attempts   int
	// Error is the summary of the failed attempt, when the stage failed.
	Error string
}

// artifactView types are the minimal projections of the orchestrator's stage
// index files. They mirror the JSON the stages persist (same field names) so
// the review reads back exactly what was written.

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

type fetchIndex struct {
	Attempted []SourceFetch `json:"attempted"`
	Successes int           `json:"successes"`
	Failed    int           `json:"failed"`
}

type documentsIndex struct {
	Count     int      `json:"count"`
	SourceIDs []string `json:"source_ids"`
	Paths     []string `json:"paths"`
}

// readJSON loads one stage artifact by run-relative path and unmarshals it.
// A missing or unparseable file is not an error: it is reported through the
// bundle (BuildNotes) because a failed run simply never produced the file.
// It returns false when the file is absent (the section renders as "not
// reached") and true when it was present (parse problems become notes).
func readJSON(store runner.Store, id run.RunID, path string, v any, present func(), notes *[]string) bool {
	data, err := store.LoadArtifact(id, path)
	if err != nil {
		return false
	}
	if jsonErr := unmarshal(data, v); jsonErr != nil {
		*notes = append(*notes, fmt.Sprintf("artifact %s exists but could not be parsed (%v)", path, jsonErr))
	}
	if present != nil {
		present()
	}
	return true
}

// unmarshal is a small indirection so tests can substitute a counting parser.
var unmarshal = func(data []byte, v any) error { return jsonUnmarshal(data, v) }

// Build assembles the review bundle for a run from the store and the final
// manifest. It never executes pipeline work and never calls an LLM.
//
// The returned bundle is complete for any manifest state: stages the run
// never reached yield zero/empty sections, and integrity problems (a
// succeeded stage whose artifact file is missing) become BuildNotes rather
// than errors, so a bundle can always be produced for a terminal run.
func Build(store runner.Store, m *run.Manifest) (*Bundle, error) {
	if store == nil {
		return nil, errors.New("review: store is required")
	}
	if m == nil {
		return nil, errors.New("review: manifest is required")
	}

	b := &Bundle{
		RunID:         m.ID,
		WorkspaceName: m.Workspace.Name,
		WorkspacePath: m.Workspace.Path,
		Status:        m.Status,
		Topic:         m.Topic,
		SourceMode:    m.SourceMode,
		StartedAt:     m.StartedAt,
		UpdatedAt:     m.UpdatedAt,
		FinishedAt:    m.FinishedAt,
		Reason:        m.Result.Reason,
		Config:        copyConfig(m.Config.Values),
	}
	if m.Result.FailedStage != nil {
		b.FailedStage = string(*m.Result.FailedStage)
	}
	if m.Profile != nil {
		b.ProfileID = m.Profile.ID
		b.ProfilePath = m.Profile.SourcePath
		b.ProfileHash = m.Profile.Hash
	}
	b.ModelProvider = m.Model.Provider
	b.ModelName = m.Model.Model
	b.ModelEndpoint = m.Model.Endpoint

	if dir, err := store.RunDir(m.ID); err == nil {
		b.RunDir = dir
	}

	for _, s := range m.Stages {
		info := StageInfo{Name: s.Name, Status: s.Status, SkipReason: s.SkipReason, Attempts: len(s.Attempts)}
		if len(s.Attempts) > 0 {
			last := s.Attempts[len(s.Attempts)-1]
			if last.Status == run.StageStatusFailed && last.Error != nil {
				info.Error = last.Error.Message
			}
		}
		b.Stages = append(b.Stages, info)
	}
	b.Warnings = append(b.Warnings, m.Warnings...)
	b.Errors = append(b.Errors, m.Errors...)

	// Manifest-recorded rendered output becomes the bundle's output paths.
	// (The render stage only succeeds after a passing final check, so a
	// recorded rendered path is always a file the run legitimately produced
	// in its current cycle; stale references from invalidated stages are
	// dropped by the manifest itself.)
	for _, a := range m.Artifacts {
		if a.Kind != run.ArtifactKindRendered || a.Path == "" {
			continue
		}
		full := a.Path
		if b.RunDir != "" {
			full = filepath.Join(b.RunDir, filepath.FromSlash(a.Path))
		}
		b.OutputPaths = append(b.OutputPaths, full)
	}
	for _, p := range b.OutputPaths {
		if !exists(p) {
			b.OutputMissing = append(b.OutputMissing, p)
		}
	}

	// --- Stage artifacts, by canonical path --------------------------------
	readOpt := func(path string, v any, present func()) bool {
		return readJSON(store, m.ID, path, v, present, &b.BuildNotes)
	}

	var si sourcesIndex
	if readOpt(run.ArtifactPathSourcesIndex, &si, nil) {
		b.SourcesSelected = si.Selected
		b.Candidates = len(si.Candidates)
		for _, q := range si.Queries {
			b.Queries = append(b.Queries, q.Query)
		}
	}

	var fi fetchIndex
	if readOpt(run.ArtifactPathFetchIndex, &fi, nil) {
		b.FetchResults = fi.Attempted
		b.FetchSuccesses = fi.Successes
		b.FetchFailed = fi.Failed
	}

	var di documentsIndex
	if readOpt(run.ArtifactPathDocumentsIndex, &di, nil) {
		b.DocumentsCount = di.Count
	}

	var dossier researcher.ResearchDossier
	if readOpt(run.ArtifactPathDossier, &dossier, func() { b.DossierPresent = true }) {
		b.DossierClaims = len(dossier.Claims)
		b.DossierSources = append(b.DossierSources, dossier.Sources...)
	}

	var vres contracts.VerificationResult
	if readOpt(run.ArtifactPathVerification, &vres, func() { b.Verified = true }) {
		assessClaims(b, &vres)
	}

	var fu followup.FollowUpResult
	if readOpt(run.ArtifactPathFollowUp, &fu, func() { b.FollowUpPresent = true }) {
		fillFollowUp(b, &fu)
	}
	for _, s := range m.Stages {
		if s.Name == run.StageFollowUp && s.Status == run.StageStatusSkipped {
			b.FollowUpSkippedReason = s.SkipReason
		}
	}

	var cr contracts.FactualCheckResult
	if readOpt(run.ArtifactPathFinalCheck, &cr, func() { b.FinalCheckPresent = true }) {
		b.FinalCheckStatus = cr.OverallStatus
		b.FinalCheckConfidence = cr.ConfidenceLevel
		b.FinalCheckSummary = cr.Summary
		b.Findings = append(b.Findings, cr.FactualFindings...)
		b.Unsupported = append(b.Unsupported, cr.UnsupportedStatements...)
		b.Altered = append(b.Altered, cr.AlteredDetails...)
		b.OutOfContext = append(b.OutOfContext, cr.OutOfContextStatements...)
		b.Recommendations = append(b.Recommendations, cr.Recommendations...)
	}

	var art contracts.EditorialArtifact
	if readOpt(run.ArtifactPathArtifact, &art, func() { b.ArtifactPresent = true }) {
		b.ArtifactTitle = art.Title
		b.ArtifactType = art.ArtifactType
		b.ArtifactClaims = len(art.ClaimReferences)
		b.ArtifactWarnings = append(b.ArtifactWarnings, art.Warnings...)
	}

	// --- Integrity: succeeded stages must have their canonical files --------
	for _, s := range m.Stages {
		if s.Status != run.StageStatusSucceeded {
			continue
		}
		if path, ok := canonicalArtifactPath(s.Name); ok {
			if _, err := store.LoadArtifact(m.ID, path); err != nil {
				b.BuildNotes = append(b.BuildNotes, fmt.Sprintf(
					"stage %s is recorded as succeeded but its artifact %s is missing from the run directory", s.Name, path))
			}
		}
	}

	return b, nil
}

// assessClaims folds the verification result into the bundle: per-status
// counts, the quality score, and sorted listings of the claims a reviewer
// must look at (contradicted first, then unresolved).
func assessClaims(b *Bundle, vres *contracts.VerificationResult) {
	b.ClaimsTotal = len(vres.VerificationStatuses)
	b.ContradictionCount = len(vres.Contradictions)
	b.QualityScore = vres.QualityScore
	b.QualityScoreSet = true
	if vres.QualityScore == 0 {
		// The persisted score field is authoritative; recompute only when
		// the stage never stored one (a zero in the file means zero).
		b.QualityScore = vres.CalculateQualityScore()
	}
	b.VerificationSummary = vres.VerificationSummary

	detail := func(claimID string) (statement, limitations string, confidence contracts.ConfidenceLevel, conflicts bool) {
		d, ok := vres.ClaimDetails[claimID]
		if !ok {
			return "", "", "", false
		}
		return d.Statement, d.Limitations, d.ConfidenceLevel, len(d.ConflictingEvidence) > 0
	}
	for claimID, status := range vres.VerificationStatuses {
		st, lim, conf, conflicts := detail(claimID)
		switch status {
		case contracts.VerificationStatusSupported:
			b.ClaimsSupported++
		case contracts.VerificationStatusContradicted:
			b.ClaimsContradicted++
			b.Contradicted = append(b.Contradicted, ClaimInfo{ID: claimID, Statement: st, Status: status, Confidence: conf, Limitations: lim, HasConflicts: conflicts})
		case contracts.VerificationStatusUncertain:
			b.ClaimsUncertain++
			b.Unresolved = append(b.Unresolved, ClaimInfo{ID: claimID, Statement: st, Status: status, Confidence: conf, Limitations: lim, HasConflicts: conflicts})
		case contracts.VerificationStatusInsufficientEvidence:
			b.ClaimsInsufficient++
			b.Unresolved = append(b.Unresolved, ClaimInfo{ID: claimID, Statement: st, Status: status, Confidence: conf, Limitations: lim, HasConflicts: conflicts})
		}
	}
	sortClaims(b.Contradicted)
	sortClaims(b.Unresolved)
}

// fillFollowUp copies the follow-up result into the bundle in stable order.
func fillFollowUp(b *Bundle, fu *followup.FollowUpResult) {
	b.FollowUpRounds = fu.RoundNumber
	b.FollowUpQueries = fu.QueryCount
	b.FollowUpSources = fu.FetchedCount
	b.FollowUpResolved = sortedCopy(fu.ResolvedClaims)
	b.FollowUpUnresolved = sortedCopy(fu.UnresolvedClaimsAfter)
	b.FollowUpWarnings = append(b.FollowUpWarnings, fu.Warnings...)
	b.FollowUpErrors = append(b.FollowUpErrors, fu.Errors...)
}

// canonicalArtifactPath maps a stage to the one canonical file whose
// presence proves the stage's work persisted (the stage's primary result
// artifact). Stages without a single primary result (render) are not
// checked here; their output files are listed in the bundle instead.
func canonicalArtifactPath(name run.StageName) (string, bool) {
	switch name {
	case run.StageDiscovery:
		return run.ArtifactPathSourcesIndex, true
	case run.StageFetch:
		return run.ArtifactPathFetchIndex, true
	case run.StageExtract:
		return run.ArtifactPathDocumentsIndex, true
	case run.StageResearch:
		return run.ArtifactPathDossier, true
	case run.StageVerify:
		return run.ArtifactPathVerification, true
	case run.StageFollowUp:
		return run.ArtifactPathFollowUp, true
	case run.StageWrite:
		return run.ArtifactPathArtifact, true
	case run.StageFinalCheck:
		return run.ArtifactPathFinalCheck, true
	default:
		return "", false
	}
}

// sortClaims orders claim listings by ID for deterministic rendering.
func sortClaims(cs []ClaimInfo) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].ID < cs[j].ID })
}

// sortedCopy returns a sorted, deduplicated copy of in.
func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// copyConfig returns a copy of the manifest's config snapshot values so the
// bundle never aliases (and mutates) the live manifest's map.
func copyConfig(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// exists reports whether the path is a regular file.
func exists(path string) bool {
	if path == "" {
		return false
	}
	info, err := osStat(path)
	return err == nil && !info.IsDir()
}
