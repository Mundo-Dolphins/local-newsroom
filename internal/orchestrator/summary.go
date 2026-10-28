package orchestrator

import (
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contracts"
	"github.com/Mundo-Dolphins/local-newsroom/internal/followup"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/runner"
)

// Summary is the machine-readable outcome of an end-to-end run. The run
// command prints a concise rendering of it; tests assert on its fields.
type Summary struct {
	// RunID identifies the run.
	RunID run.RunID

	// Status is the run's final status (succeeded, failed, or cancelled).
	Status run.RunStatus

	// Reason is the run-level failure reason ("" for succeeded runs).
	Reason string

	// FailedStage is the stage that stopped the run ("" when none).
	FailedStage string

	// Topic is the run's editorial topic.
	Topic string

	// Recipe is the selected editorial recipe id (v0.6; "" for standard runs).
	Recipe string

	// SourceMode is how sources were obtained (explicit, discovered, supplement).
	SourceMode run.SourceMode

	// SourcesAttempted is the number of source URLs selected for fetching.
	SourcesAttempted int

	// SourcesFetched is the number of sources retrieved successfully.
	SourcesFetched int

	// DocumentsExtracted is the number of documents normalized for research.
	DocumentsExtracted int

	// ClaimsTotal is the number of dossier claims assessed.
	ClaimsTotal int

	// ClaimsSupported is the number of claims fully supported by evidence.
	ClaimsSupported int

	// ClaimsUnresolved is the number of claims not fully supported
	// (uncertain, insufficient evidence, or contradicted).
	ClaimsUnresolved int

	// QualityScore is the verification quality score (nil when not reached).
	QualityScore *float64

	// FinalCheck is the final factual check status: "pass", "fail", or
	// "review" ("" when the stage was not reached).
	FinalCheck string

	// Findings is the number of findings in the final check result.
	Findings int

	// OutputPaths lists the rendered output files, absolute.
	OutputPaths []string

	// WorkspaceDir is the absolute path of the run directory.
	WorkspaceDir string
}

// BuildSummary assembles a Summary from a persisted run: the manifest plus
// whatever stage artifacts exist. Stages that were never reached (a failed
// or cancelled run) simply contribute zero/empty values.
func BuildSummary(store runner.Store, m *run.Manifest) (*Summary, error) {
	if store == nil {
		return nil, errors.New("orchestrator: store is required")
	}
	if m == nil {
		return nil, errors.New("orchestrator: manifest is required")
	}
	runDir, err := store.RunDir(m.ID)
	if err != nil {
		return nil, err
	}

	s := &Summary{
		RunID:        m.ID,
		Status:       m.Status,
		Topic:        m.Topic,
		SourceMode:   m.SourceMode,
		WorkspaceDir: runDir,
	}
	if m.Recipe != nil {
		s.Recipe = m.Recipe.ID
	}
	if m.Result.Reason != "" {
		s.Reason = m.Result.Reason
	}
	if m.Result.FailedStage != nil {
		s.FailedStage = string(*m.Result.FailedStage)
	}

	read := func(path string, v any) bool {
		data, err := store.LoadArtifact(m.ID, path)
		if err != nil {
			return false
		}
		return jsonUnmarshal(data, v) == nil
	}

	var si sourcesIndex
	if read(sourcesIndexPath, &si) {
		s.SourcesAttempted = len(si.Selected)
	}
	var fi fetchIndex
	if read(fetchIndexPath, &fi) {
		s.SourcesFetched = fi.Successes
	}
	var di documentsIndex
	if read(documentsIndexPath, &di) {
		s.DocumentsExtracted = di.Count
	}
	var vres contracts.VerificationResult
	if read(verificationPath, &vres) {
		s.ClaimsTotal = len(vres.ExtractClaimIDs())
		s.ClaimsSupported = len(vres.GetSupportedClaims())
		s.ClaimsUnresolved = len(followup.GetUnresolvedClaimIDs(&vres))
		score := vres.CalculateQualityScore()
		s.QualityScore = &score
	}
	var cr contracts.FactualCheckResult
	if read(finalCheckPath, &cr) {
		s.FinalCheck = string(cr.OverallStatus)
		s.Findings = len(cr.FactualFindings)
	}

	for _, a := range m.Artifacts {
		if a.Kind == run.ArtifactKindRendered && a.Path != "" {
			s.OutputPaths = append(s.OutputPaths, filepath.Join(runDir, filepath.FromSlash(a.Path)))
		}
	}
	return s, nil
}

// String renders the concise human-facing summary the run command prints.
func (s Summary) String() string {
	var b strings.Builder
	b.WriteString("Run:         ")
	b.WriteString(string(s.RunID))
	b.WriteString("\n")
	b.WriteString("Status:      ")
	b.WriteString(string(s.Status))
	if s.Reason != "" {
		b.WriteString(" — ")
		b.WriteString(s.Reason)
	}
	if s.FailedStage != "" {
		b.WriteString(" (stopped at ")
		b.WriteString(s.FailedStage)
		b.WriteString(")")
	}
	b.WriteString("\n")
	b.WriteString("Topic:       ")
	b.WriteString(s.Topic)
	b.WriteString(" [")
	b.WriteString(string(s.SourceMode))
	b.WriteString("]\n")
	if s.Recipe != "" {
		b.WriteString("Recipe:      ")
		b.WriteString(s.Recipe)
		b.WriteString("\n")
	}
	b.WriteString("Sources:     ")
	b.WriteString(strconv.Itoa(s.SourcesFetched))
	b.WriteString(" of ")
	b.WriteString(strconv.Itoa(s.SourcesAttempted))
	b.WriteString(" fetched; ")
	b.WriteString(strconv.Itoa(s.DocumentsExtracted))
	b.WriteString(" documents extracted\n")
	if s.QualityScore != nil {
		b.WriteString("Claims:      ")
		b.WriteString(strconv.Itoa(s.ClaimsSupported))
		b.WriteString(" supported, ")
		b.WriteString(strconv.Itoa(s.ClaimsUnresolved))
		b.WriteString(" unresolved of ")
		b.WriteString(strconv.Itoa(s.ClaimsTotal))
		b.WriteString(" (quality ")
		b.WriteString(strings.TrimRight(strings.TrimRight(
			strconv.FormatFloat(*s.QualityScore, 'f', 1, 64), "0"), "."))
		b.WriteString(")\n")
	}
	if s.FinalCheck != "" {
		b.WriteString("Final check: ")
		b.WriteString(s.FinalCheck)
		if s.Findings > 0 {
			b.WriteString(" (")
			b.WriteString(strconv.Itoa(s.Findings))
			b.WriteString(" finding(s))")
		}
		b.WriteString("\n")
	}
	if len(s.OutputPaths) > 0 {
		b.WriteString("Output:      ")
		b.WriteString(s.OutputPaths[0])
		for _, p := range s.OutputPaths[1:] {
			b.WriteString(", ")
			b.WriteString(p)
		}
		b.WriteString("\n")
	}
	if s.WorkspaceDir != "" {
		b.WriteString("Workspace:   ")
		b.WriteString(s.WorkspaceDir)
		b.WriteString("\n")
	}
	return b.String()
}
