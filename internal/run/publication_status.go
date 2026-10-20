package run

import "regexp"

// This file mirrors the v0.6 publication plan status vocabulary
// (internal/publication) into the run manifest type system.
//
// The run manifest carries a summary of the run's latest publication plan
// (Manifest.PublicationState). internal/publication imports this package, so
// this package cannot import it back; the status strings are therefore
// re-declared here. internal/publish carries a drift-guard test that fails
// the build if the two vocabularies ever diverge — the mirrored values are
// the contract between the manifest summary and the plan document.

// Publication plan statuses recorded in the manifest summary.
//
// The values are the wire vocabulary of the v0.6 publication contract
// (docs/contracts-v0.6.md, §2) and MUST equal the publication.Status
// constants in internal/publication, byte for byte.
const (
	// PublicationStatusPlanned means the plan exists but has no approval.
	PublicationStatusPlanned = "planned"

	// PublicationStatusApproved means a human approved the plan, bound to
	// the exact artifact digest at approval time.
	PublicationStatusApproved = "approved"

	// PublicationStatusPublishing means a publication cycle is (or was
	// mid-crash) executing the plan's targets.
	PublicationStatusPublishing = "publishing"

	// PublicationStatusSucceeded means every target succeeded or was
	// explicitly skipped (a complete publication).
	PublicationStatusSucceeded = "succeeded"

	// PublicationStatusFailed means at least one target failed and none
	// succeeded.
	PublicationStatusFailed = "failed"

	// PublicationStatusPartiallySucceeded means some targets succeeded and
	// at least one failed.
	PublicationStatusPartiallySucceeded = "partially_succeeded"

	// PublicationStatusCancelled means a human cancelled the plan before it
	// (fully) completed.
	PublicationStatusCancelled = "cancelled"

	// PublicationStatusSkipped means the plan was never started (all of its
	// targets were explicitly skipped).
	PublicationStatusSkipped = "skipped"
)

// AllPublicationStatuses lists every known plan status.
var AllPublicationStatuses = []string{
	PublicationStatusPlanned,
	PublicationStatusApproved,
	PublicationStatusPublishing,
	PublicationStatusSucceeded,
	PublicationStatusFailed,
	PublicationStatusPartiallySucceeded,
	PublicationStatusCancelled,
	PublicationStatusSkipped,
}

// ValidPublicationStatus reports whether the value is a known publication
// plan status.
func ValidPublicationStatus(s string) bool {
	for _, v := range AllPublicationStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// PublicationStatusTerminal reports whether a plan status is terminal: a
// plan in one of these states never transitions again (re-publishing an
// artifact is a new plan, never a mutation of a finished one).
func PublicationStatusTerminal(s string) bool {
	switch s {
	case PublicationStatusSucceeded,
		PublicationStatusFailed,
		PublicationStatusPartiallySucceeded,
		PublicationStatusCancelled,
		PublicationStatusSkipped:
		return true
	}
	return false
}

// sha256DigestRe validates a canonical content digest.
var sha256DigestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ValidSHA256Digest reports whether d is a canonical "sha256:<64 hex>" digest.
func ValidSHA256Digest(d string) bool {
	return sha256DigestRe.MatchString(d)
}
