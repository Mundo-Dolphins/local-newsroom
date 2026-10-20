package publish

import (
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/publication"
	"github.com/Mundo-Dolphins/local-newsroom/internal/review"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// BuildGateEvidence gathers the pre-publication gate evidence for a run
// artifact:
//
//   - the run's status and terminal outcome (from the manifest),
//   - the final factual check verdict (from the review bundle),
//   - the review readiness decision — review.Readiness, the exact rule the
//     rendered review bundle shows a human, so the gate a publisher must
//     pass and the verdict a reviewer sees cannot diverge,
//   - the digest of the current artifact bytes (the content file the plan
//     is being baselined to).
//
// The evidence must be gathered from the exact bytes that will be
// published; NewPlan and SetGate refuse evidence whose digest does not
// match the plan's artifact baseline.
func BuildGateEvidence(m *run.Manifest, b *review.Bundle, content []byte, at time.Time) publication.GateEvidence {
	ready, reason := review.Readiness(b)
	return publication.GateEvidence{
		RunStatus:      b.Status,
		FinalCheck:     b.FinalCheckStatus,
		ReviewReady:    ready,
		ArtifactDigest: publication.DigestOf(content),
		CheckedAt:      run.NewTimestamp(at),
		Note:           reason,
	}
}

// GateReady reports whether the evidence passes, and (when not) the reason
// a human can act on: the same readiness verdict the review bundle renders.
func GateReady(gate publication.GateEvidence) (bool, string) {
	if gate.Passing() {
		return true, ""
	}
	if !gate.ReviewReady {
		return false, gate.Note
	}
	return false, "the recorded gate evidence does not pass (run status " + string(gate.RunStatus) + ", final check " + string(gate.FinalCheck) + ")"
}
