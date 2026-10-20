package publish

import (
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// Store is the persistence boundary the publish engine needs from the
// workspace. workspace.Store (internal/workspace) implements it, and it is
// the single sanitization boundary: every byte the engine persists passes
// the workspace sanitizer (configured exact secrets replaced, pattern
// detectors applied, clean-byte re-check, atomic write) exactly as every
// other run file does.
//
// The methods mirror runner.Store, plus SaveManifest (the engine updates
// the manifest's publication_state summary) and ListArtifacts (plan history
// discovery).
type Store interface {
	// RunDir returns the run directory for an ID.
	RunDir(id run.RunID) (string, error)

	// Sanitizer is the store's byte-level sanitization policy.
	Sanitizer() *run.Sanitizer

	// SaveArtifact persists one file inside the run directory (sanitized,
	// atomic) and returns its canonical run-relative path.
	SaveArtifact(id run.RunID, ref run.ArtifactRef, data []byte) (string, error)

	// LoadArtifact reads a persisted run file back.
	LoadArtifact(id run.RunID, relPath string) ([]byte, error)

	// SaveManifest persists the run manifest (validated, sanitized,
	// atomic).
	SaveManifest(m *run.Manifest) (string, error)

	// LoadManifest loads the persisted run manifest and its run directory.
	LoadManifest(id run.RunID) (*run.Manifest, string, error)

	// ListArtifacts lists the regular files directly inside one
	// run-relative directory (run-relative paths, sorted).
	ListArtifacts(id run.RunID, relDir string) ([]string, error)
}

// planFileName is the file name of one plan document.
func planFileName(planID publication.PublicationID) string {
	return string(planID) + ".json"
}

// PlanPath is the run-relative path of one plan document
// (publication/plans/<plan-id>.json).
func PlanPath(planID publication.PublicationID) string {
	return path.Join(run.PublicationPlansDir, planFileName(planID))
}

// planRef builds the artifact reference used to persist a plan document.
//
// The plan file is a run-directory document (like review.md): it is
// persisted through the same artifact pipeline (sanitization, atomic write,
// redaction sidecar) and referenced from the manifest's publication_state
// summary — but deliberately NOT added to Manifest.Artifacts: the manifest's
// artifact list tracks pipeline artifacts, and publication documents live in
// their own section so v0.5 manifests stay untouched.
func planRef(planID publication.PublicationID, at time.Time) run.ArtifactRef {
	return run.ArtifactRef{
		Kind:       run.ArtifactKindCustom,
		ID:         "publication-plan-" + shortID(string(planID)),
		Stage:      "",
		Path:       PlanPath(planID),
		ProducedAt: run.NewTimestamp(at),
	}
}

// shortID keeps the ref ID within its 128-character conservative bound:
// the plan ID is "pub-<15 chars>-<12 hex>" (~30) and the ref ID reuses the
// tail of it.
func shortID(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:40]
}

// savePlan persists a plan document and returns the digest of the persisted
// bytes (the store sanitizes on write, so the hash must be computed from
// what actually landed on disk, not from the in-memory marshaling).
func (e *Engine) savePlan(plan *publication.Plan) (string, error) {
	data, err := publication.MarshalPlan(plan)
	if err != nil {
		return "", fmt.Errorf("marshaling plan %s: %w", plan.ID, err)
	}
	if _, err := e.store.SaveArtifact(plan.RunID, planRef(plan.ID, e.now()), data); err != nil {
		return "", fmt.Errorf("persisting plan %s: %w", plan.ID, err)
	}
	persisted, err := e.store.LoadArtifact(plan.RunID, PlanPath(plan.ID))
	if err != nil {
		// The file was just written and cannot be read back: the run
		// directory is unreadable (or was raced away). Fail closed — a
		// plan that is on disk but unreadable is a plan the resume path
		// cannot trust.
		return "", fmt.Errorf("%w: plan %s was written but cannot be read back: %v", ErrPlanInvalid, plan.ID, err)
	}
	return string(publication.DigestOf(persisted)), nil
}

// loadPlan loads and validates one plan document from the run directory.
//
// Fail-closed rules: a missing file reports ErrPlanNotFound; a present file
// that fails to parse, fails UnmarshalPlan's schema gate, or fails
// Plan.Validate reports ErrPlanInvalid — a corrupted receipt or a tampered
// plan is never executed, never "recovered", and its targets are never
// re-pushed.
func (e *Engine) loadPlan(runID run.RunID, planID publication.PublicationID) (*publication.Plan, error) {
	data, err := e.store.LoadArtifact(runID, PlanPath(planID))
	if err != nil {
		if isNotExist(err) {
			return nil, fmt.Errorf("%w: %s (run %s)", ErrPlanNotFound, PlanPath(planID), runID)
		}
		return nil, fmt.Errorf("%w: cannot read plan %s: %v", ErrPlanInvalid, planID, err)
	}
	plan, err := publication.UnmarshalPlan(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPlanInvalid, err)
	}
	return plan, nil
}

// isNotExist reports whether a store read error is a missing file (as
// opposed to an unreadable or permission-failed read, which must not be
// conflated with absence).
func isNotExist(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "not exist"))
}

// listPlanIDs lists the plan documents of a run in file-name (i.e.
// derivation-time) order.
func (e *Engine) listPlanIDs(runID run.RunID) ([]publication.PublicationID, error) {
	files, err := e.store.ListArtifacts(runID, run.PublicationPlansDir)
	if err != nil {
		// An absent plans directory means "no plans yet", not an error.
		if isNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing publication plans of run %s: %w", runID, err)
	}
	ids := make([]publication.PublicationID, 0, len(files))
	for _, f := range files {
		name := path.Base(f)
		name = strings.TrimSuffix(name, ".json")
		id := publication.PublicationID(name)
		if err := id.Validate(); err != nil {
			// A file that is not a well-formed plan document name is
			// ignored (it may be unrelated content someone dropped
			// there); it is never interpreted as a plan.
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// resolvePlanID selects the plan a Publish/inspection call targets: the
// explicit PlanID when given, otherwise the run's latest plan (the manifest
// summary when present, else the newest plan document on disk).
func (e *Engine) resolvePlanID(m *run.Manifest, explicit publication.PublicationID) (publication.PublicationID, error) {
	if explicit != "" {
		if err := explicit.Validate(); err != nil {
			return "", err
		}
		return explicit, nil
	}
	if m.PublicationState != nil && m.PublicationState.PlanID != "" {
		return publication.PublicationID(m.PublicationState.PlanID), nil
	}
	ids, err := e.listPlanIDs(m.ID)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("%w: run %s has no publication plans (create one with CreatePlan)", ErrPlanNotFound, m.ID)
	}
	return ids[len(ids)-1], nil // file names sort by derivation time: the last is the latest
}

// syncManifest rewrites the manifest's publication_state summary from the
// plan and persists the manifest.
//
// The summary is a roll-up only (plan reference, persisted-bytes digest,
// status, target counts): the full plan stays in its own document. The
// manifest's updated_at advances with the plan's last transition, so the
// manifest timeline and the plan timeline cannot drift apart.
func (e *Engine) syncManifest(m *run.Manifest, plan *publication.Plan, planHash string) error {
	succeeded, failed := 0, 0
	for _, t := range plan.Targets {
		switch t.Status {
		case publication.StatusSucceeded:
			succeeded++
		case publication.StatusFailed:
			failed++
		}
	}
	m.PublicationState = &run.PublicationState{
		PlanID:    string(plan.ID),
		PlanPath:  PlanPath(plan.ID),
		PlanHash:  planHash,
		Status:    string(plan.Status),
		Targets:   len(plan.Targets),
		Succeeded: succeeded,
		Failed:    failed,
		UpdatedAt: plan.UpdatedAt,
	}
	m.UpdatedAt = run.NewTimestamp(e.now())
	if _, err := e.store.SaveManifest(m); err != nil {
		return fmt.Errorf("persisting manifest %s (publication state of %s): %w", m.ID, plan.ID, err)
	}
	return nil
}

// persistPlan is the single "record a plan change" step: persist the plan
// document, then the manifest summary. Both writes are atomic. A crash
// between them leaves the manifest's plan_hash stale relative to the plan
// document on disk; the resume path detects that exact situation and fails
// closed (ErrPlanInvalid) rather than acting on a document it cannot prove
// is the one it last persisted — re-planning is the recovery.
func (e *Engine) persistPlan(m *run.Manifest, plan *publication.Plan) error {
	planHash, err := e.savePlan(plan)
	if err != nil {
		return err
	}
	return e.syncManifest(m, plan, planHash)
}

// planContext bundles the manifest and plan for one engine operation, so
// the persist-then-continue flow has one place to load both.
type planContext struct {
	manifest *run.Manifest
	plan     *publication.Plan
}

// loadPlanContext loads the manifest and the target plan, failing closed on
// any inconsistency the resume path cannot safely interpret.
func (e *Engine) loadPlanContext(runID run.RunID, planID publication.PublicationID) (*planContext, error) {
	m, _, err := e.store.LoadManifest(runID)
	if err != nil {
		return nil, fmt.Errorf("loading manifest for run %s: %w", runID, err)
	}
	id, err := e.resolvePlanID(m, planID)
	if err != nil {
		return nil, err
	}
	plan, err := e.loadPlan(runID, id)
	if err != nil {
		return nil, err
	}

	// Tamper detection: when the manifest summary references this plan, its
	// recorded plan_hash must still digest the persisted document bytes.
	// A plan file that was altered after the manifest was written is not
	// the plan the operator last saw: it fails closed (its targets are
	// never executed) rather than being silently trusted.
	if m.PublicationState != nil && m.PublicationState.PlanID == string(id) && m.PublicationState.PlanHash != "" {
		data, err := e.store.LoadArtifact(runID, PlanPath(id))
		if err != nil {
			return nil, fmt.Errorf("%w: plan %s cannot be re-read for verification: %v", ErrPlanInvalid, id, err)
		}
		if got := string(publication.DigestOf(data)); got != m.PublicationState.PlanHash {
			return nil, fmt.Errorf("%w: plan %s on disk no longer matches the manifest's recorded plan hash (document was modified after the manifest was written)", ErrPlanInvalid, id)
		}
	}

	// Cross-check: the manifest summary (when present) must point at the
	// plan we are acting on; a mismatch means the manifest and the plan
	// history diverged (hand-editing, a partial write). The plan document
	// is authoritative, so this is a warning, not a failure — but only
	// when the summary actually references a different plan.
	if m.PublicationState != nil && m.PublicationState.PlanID != string(id) {
		if werr := plan.AddWarning("manifest-summary-mismatch",
			fmt.Sprintf("manifest publication_state references plan %s but %s was requested", m.PublicationState.PlanID, id), e.now()); werr != nil {
			return nil, fmt.Errorf("recording plan warning: %w", werr)
		}
	}
	return &planContext{manifest: m, plan: plan}, nil
}
