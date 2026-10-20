package publication

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// TargetID is the stable identifier of a publication target within a plan.
//
// Target IDs are operator-assigned slugs (e.g. "bluesky-thread",
// "hugo-article"): they are stable within the plan document and are the
// keys for dependencies, approval scopes, and audit references. Unlike
// derived IDs they carry no timestamp: a plan is one document, and its
// targets are addressable by name.
type TargetID string

// targetIDRe allows conservative, filesystem-safe target identifiers.
var targetIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// Validate checks that the target ID is well-formed.
func (id TargetID) Validate() error {
	if id == "" {
		return errors.New("target ID is required")
	}
	if !targetIDRe.MatchString(string(id)) {
		return fmt.Errorf("target ID %q is invalid: use letters, digits, '.', '-', '_' (1-128 chars, must not start with a separator)", id)
	}
	return nil
}

// String returns the string representation of the target ID.
func (id TargetID) String() string {
	return string(id)
}

// TargetType categorizes a publication target in format-neutral terms.
//
// The contract deliberately does not know about Bluesky, Hugo, or Git: it
// categorizes the *kind* of external effect a target represents, and the
// provider field names the concrete system. Publisher implementations
// (v0.6) are where provider specifics live.
type TargetType string

// Target type values.
const (
	// TargetTypePost is a short-form external post (one or more linked
	// posts on a social platform).
	TargetTypePost TargetType = "post"

	// TargetTypePage is a web page or CMS entry (e.g. an article on a
	// site).
	TargetTypePage TargetType = "page"

	// TargetTypeRepository is a file or commit in a repository
	// (e.g. a site deployed through git).
	TargetTypeRepository TargetType = "repository"

	// TargetTypeCustom marks a non-standard target category.
	TargetTypeCustom TargetType = "custom"
)

// AllTargetTypes lists every valid target type.
var AllTargetTypes = []TargetType{
	TargetTypePost,
	TargetTypePage,
	TargetTypeRepository,
	TargetTypeCustom,
}

// Valid reports whether the target type is a known value.
func (t TargetType) Valid() bool {
	for _, v := range AllTargetTypes {
		if t == v {
			return true
		}
	}
	return false
}

// String returns the string representation of the target type.
func (t TargetType) String() string {
	return string(t)
}

// providerRe allows conservative, lowercase provider identifiers
// ("bluesky", "hugo", "git", "fs"). The contract treats the value as an
// opaque tag; publishers map it to real systems.
var providerRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Destination constants.
const (
	maxDestinationLen = 1024
	maxTargetNameLen  = 200
)

// DryRunOutcome enumerates the outcome of a target dry run.
//
// A dry run is the pre-publication rehearsal: it validates that a target
// can receive the content (configuration, reachability, format constraints)
// and produces a result with ZERO external side effects.
type DryRunOutcome string

// Dry run outcome values.
const (
	// DryRunOutcomeOK means the rehearsal found no blocking issues.
	DryRunOutcomeOK DryRunOutcome = "ok"

	// DryRunOutcomeFailed means the rehearsal found blocking issues; the
	// target should not be published until they are resolved.
	DryRunOutcomeFailed DryRunOutcome = "failed"
)

// AllDryRunOutcomes lists every valid dry run outcome.
var AllDryRunOutcomes = []DryRunOutcome{
	DryRunOutcomeOK,
	DryRunOutcomeFailed,
}

// Valid reports whether the dry run outcome is a known value.
func (o DryRunOutcome) Valid() bool {
	for _, v := range AllDryRunOutcomes {
		if o == v {
			return true
		}
	}
	return false
}

// String returns the string representation of the dry run outcome.
func (o DryRunOutcome) String() string {
	return string(o)
}

// DryRunIssue is one problem found by a dry run.
type DryRunIssue struct {
	// Code is an optional machine-readable category (e.g. "unreachable",
	// "over_char_limit").
	Code string `json:"code,omitempty"`

	// Message is the human-readable issue description (redacted).
	Message string `json:"message"`
}

// NewDryRunIssue builds a dry run issue with its message redacted.
func NewDryRunIssue(code, message string) DryRunIssue {
	return DryRunIssue{Code: code, Message: run.RedactSecrets(message)}
}

// Validate checks the issue: a non-empty, redacted message and a
// conservative code.
func (i DryRunIssue) Validate() error {
	if i.Message == "" {
		return errors.New("dry run issue message is required")
	}
	if len(i.Message) > maxNoteLen {
		return fmt.Errorf("dry run issue message exceeds %d characters", maxNoteLen)
	}
	if i.Code != "" && !codeRe.MatchString(i.Code) {
		return fmt.Errorf("dry run issue code %q is invalid: use lowercase letters, digits, '.', '-', '_' (1-64 chars)", i.Code)
	}
	if run.LooksLikeSecret(i.Message) {
		return fmt.Errorf("dry run issue message contains secret-looking material; redact it before recording")
	}
	return nil
}

// DryRunResult is the side-effect-free rehearsal outcome for one target.
//
// The result records WHAT was validated (the content digest) so a
// subsequently changed artifact can be detected: a dry run for digest A
// proves nothing about digest B.
type DryRunResult struct {
	// PerformedAt is when the rehearsal ran (UTC).
	PerformedAt run.Timestamp `json:"performed_at"`

	// Outcome is ok or failed.
	Outcome DryRunOutcome `json:"outcome"`

	// ContentDigest is the digest of the content the rehearsal validated.
	ContentDigest Digest `json:"content_digest"`

	// Details is a redacted human-readable summary.
	Details string `json:"details,omitempty"`

	// Issues lists the problems found (required when outcome is failed).
	Issues []DryRunIssue `json:"issues,omitempty"`
}

// maxDryRunDetails bounds the dry run summary length.
const maxDryRunDetails = 4096

// NewDryRunResult builds a dry run result, redacting every free-text value.
func NewDryRunResult(at time.Time, outcome DryRunOutcome, contentDigest Digest, details string, issues []DryRunIssue) *DryRunResult {
	out := make([]DryRunIssue, len(issues))
	copy(out, issues)
	for i := range out {
		out[i] = NewDryRunIssue(out[i].Code, out[i].Message)
	}
	return &DryRunResult{
		PerformedAt:   run.NewTimestamp(at),
		Outcome:       outcome,
		ContentDigest: contentDigest,
		Details:       run.RedactSecrets(details),
		Issues:        out,
	}
}

// Validate checks the dry run result: timestamp, outcome, digest, the
// outcome<->issues consistency, and redaction.
func (r *DryRunResult) Validate() error {
	var errs []error
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if r == nil {
		return errors.New("dry run result is nil")
	}
	if r.PerformedAt.IsZero() {
		errs = append(errs, errors.New("performed_at is required"))
	}
	if !r.Outcome.Valid() {
		addf("unknown dry run outcome %q", r.Outcome)
	}
	if !r.ContentDigest.Valid() {
		addf("content digest %q is not a valid %s digest", r.ContentDigest, DigestAlgorithm)
	}
	if len(r.Details) > maxDryRunDetails {
		addf("dry run details exceed %d characters", maxDryRunDetails)
	}
	if run.LooksLikeSecret(r.Details) {
		addf("dry run details contain secret-looking material; redact them before recording")
	}
	if len(r.Issues) > MaxIssuesPerDryRun {
		addf("dry run holds %d issues, exceeding the bound of %d", len(r.Issues), MaxIssuesPerDryRun)
	}
	for i, issue := range r.Issues {
		if err := issue.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("issues[%d]: %w", i, err))
		}
	}
	switch r.Outcome {
	case DryRunOutcomeOK:
		if len(r.Issues) > 0 {
			addf("dry run outcome is ok but %d issue(s) are recorded", len(r.Issues))
		}
	case DryRunOutcomeFailed:
		if len(r.Issues) == 0 {
			errs = append(errs, errors.New("failed dry run requires at least one issue"))
		}
	}
	return errors.Join(errs...)
}

// PublicationTarget is one destination a publication plan can reach.
//
// A target is deliberately dumb: it names WHERE content goes (type,
// provider, destination) and records WHAT happened there (status, attempt
// history, receipt, dry run). It carries no content and no credentials —
// the content is always addressed by the plan-level artifact digest, and
// the destination is validated to be credential-free.
//
// Targets may declare dependencies on other targets of the same plan
// (DependsOn), which the publisher must respect: a dependent target is not
// started before its dependencies have finished. Dependencies form a
// directed acyclic graph within the plan.
type PublicationTarget struct {
	// ID is the stable target identifier (unique within the plan).
	ID TargetID `json:"id"`

	// Name is an optional human-readable label (e.g. "Dolphins thread").
	Name string `json:"name,omitempty"`

	// Type categorizes the target in format-neutral terms.
	Type TargetType `json:"type"`

	// Provider names the concrete system the target publishes to
	// (e.g. "bluesky", "hugo", "git"). Opaque to this contract.
	Provider string `json:"provider"`

	// Destination is the target-specific location (e.g. a handle, a site
	// URL, a repository path). Credential-free.
	Destination string `json:"destination"`

	// DependsOn lists target IDs that must finish before this one starts.
	// References must exist within the plan and must not form a cycle.
	DependsOn []TargetID `json:"depends_on,omitempty"`

	// Status is the target lifecycle state.
	Status PublicationStatus `json:"status"`

	// SkipReason is required when the target is skipped (redacted).
	SkipReason string `json:"skip_reason,omitempty"`

	// DryRun is the most recent rehearsal result, when one was run.
	DryRun *DryRunResult `json:"dry_run,omitempty"`

	// Receipt is the current external proof of publication, set when the
	// target last succeeded (or partially succeeded).
	Receipt *PublicationReceipt `json:"receipt,omitempty"`

	// Error is the last failure summary, set when the target last failed
	// (redacted).
	Error *run.ErrorSummary `json:"error,omitempty"`

	// Attempts is the bounded per-target attempt history (most recent
	// MaxAttemptsPerTarget attempts, numbered 1..N).
	Attempts []PublicationAttempt `json:"attempts,omitempty"`
}

// NewTarget builds a target in its initial (planned) state.
//
// The destination is redacted on the way in; Validate then rejects
// anything that still looks like a credential, so a secret can never be
// persisted as a destination.
func NewTarget(id, name string, typ TargetType, provider, destination string, dependsOn []TargetID) PublicationTarget {
	return PublicationTarget{
		ID:          TargetID(id),
		Name:        run.RedactSecrets(name),
		Type:        typ,
		Provider:    provider,
		Destination: run.RedactSecrets(destination),
		DependsOn:   append([]TargetID(nil), dependsOn...),
		Status:      StatusPlanned,
	}
}

// Dependencies returns the target's dependency list.
func (t *PublicationTarget) Dependencies() []TargetID {
	return t.DependsOn
}

// Validate checks the target: identifiers, type, provider, destination,
// status and the status<->evidence invariants (a skipped target records why,
// a failed target carries its error, a succeeded target carries its receipt,
// and the attempt history matches the status).
func (t *PublicationTarget) Validate() error {
	if t == nil {
		return errors.New("publication target is nil")
	}
	var errs []error
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if err := t.ID.Validate(); err != nil {
		errs = append(errs, err)
	}
	if len(t.Name) > maxTargetNameLen {
		addf("target name exceeds %d characters", maxTargetNameLen)
	}
	if run.LooksLikeSecret(t.Name) {
		addf("target name contains secret-looking material; redact it before recording")
	}
	if !t.Type.Valid() {
		addf("target %q: unknown type %q", t.ID, t.Type)
	}
	if !providerRe.MatchString(t.Provider) {
		addf("target %q: provider %q is invalid: use lowercase letters, digits, '.', '-', '_' (1-64 chars)", t.ID, t.Provider)
	}
	if t.Destination == "" {
		addf("target %q: destination is required", t.ID)
	} else if err := validateCredentialFree("destination", t.Destination, maxDestinationLen); err != nil {
		errs = append(errs, fmt.Errorf("target %q: %w", t.ID, err))
	}
	if !t.Status.Valid() {
		addf("target %q: unknown status %q", t.ID, t.Status)
	}

	if len(t.SkipReason) > maxNoteLen {
		addf("target %q: skip reason exceeds %d characters", t.ID, maxNoteLen)
	}
	if run.LooksLikeSecret(t.SkipReason) {
		addf("target %q: skip reason contains secret-looking material; redact it before recording", t.ID)
	}
	switch t.Status {
	case StatusSkipped:
		if t.SkipReason == "" {
			addf("target %q is skipped but has no skip reason", t.ID)
		}
	case StatusFailed:
		if t.Error == nil {
			addf("target %q is failed but has no error summary", t.ID)
		} else if err := t.Error.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("target %q: invalid error summary: %w", t.ID, err))
		}
	case StatusSucceeded:
		if t.Receipt == nil {
			addf("target %q is succeeded but has no receipt", t.ID)
		} else if err := t.Receipt.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("target %q: invalid receipt: %w", t.ID, err))
		}
	}
	// A receipt is external proof of an attempt, so a target that never
	// attempted (planned) cannot legitimately carry one: a receipt there
	// marks a corrupted or forged document.
	//
	// Deliberately, the receipt persists across the states that follow the
	// attempt (publishing again on a resumed partial, failed again, or
	// aborted mid-resume): for a partially posted thread, that receipt is
	// the resume state the next cycle's publisher deduplicates against.
	// Dropping it would lose the "already posted" proof and the next resume
	// would repost those posts. A receipt that persists must still be a
	// well-formed one.
	if t.Receipt != nil && t.Status != StatusSucceeded {
		if t.Status == StatusPlanned {
			addf("target %q is %s but carries a receipt; only an attempted target may hold external proof", t.ID, t.Status)
		} else if err := t.Receipt.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("target %q: invalid receipt: %w", t.ID, err))
		}
	}
	if t.DryRun != nil {
		if err := t.DryRun.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("target %q: invalid dry run: %w", t.ID, err))
		}
	}
	if err := validateAttemptHistory(t.ID, t.Status, t.Attempts); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// validateTargetGraph checks the cross-target invariants of a plan: unique
// IDs, well-formed dependency references, duplicate dependencies, and
// absence of dependency cycles.
func validateTargetGraph(targets []PublicationTarget) error {
	ids := make(map[TargetID]int, len(targets))
	for i, t := range targets {
		if prev, dup := ids[t.ID]; dup {
			return fmt.Errorf("duplicate target ID %q (targets %d and %d)", t.ID, prev, i)
		}
		ids[t.ID] = i
	}
	for _, t := range targets {
		seen := make(map[TargetID]bool, len(t.DependsOn))
		for _, dep := range t.DependsOn {
			if dep == t.ID {
				return fmt.Errorf("target %q depends on itself", t.ID)
			}
			if _, ok := ids[dep]; !ok {
				return fmt.Errorf("target %q depends on unknown target %q", t.ID, dep)
			}
			if seen[dep] {
				return fmt.Errorf("target %q lists dependency %q twice", t.ID, dep)
			}
			seen[dep] = true
		}
	}
	return detectDependencyCycle(targets)
}

// cycleColor marks a node's position in the dependency traversal.
type cycleColor int

const (
	cycleWhite cycleColor = iota // not visited
	cycleGray                    // on the current path
	cycleBlack                   // fully visited, cycle-free
)

// detectDependencyCycle runs an iterative depth-first search over the
// dependency edges and reports an error naming one cycle if any exists.
func detectDependencyCycle(targets []PublicationTarget) error {
	index := make(map[TargetID]int, len(targets))
	for i, t := range targets {
		index[t.ID] = i
	}
	colors := make([]cycleColor, len(targets))
	var stack []TargetID
	var cycle []TargetID

	var visit func(id TargetID) bool
	visit = func(id TargetID) bool {
		colors[index[id]] = cycleGray
		stack = append(stack, id)
		for _, dep := range targets[index[id]].DependsOn {
			switch colors[index[dep]] {
			case cycleWhite:
				if visit(dep) {
					return true
				}
			case cycleGray:
				// Found a back edge: the cycle is the stack suffix from dep.
				start := 0
				for i, s := range stack {
					if s == dep {
						start = i
						break
					}
				}
				cycle = append(append([]TargetID{}, stack[start:]...), dep)
				return true
			}
		}
		stack = stack[:len(stack)-1]
		colors[index[id]] = cycleBlack
		return false
	}

	for i := range targets {
		if colors[i] == cycleWhite {
			if visit(TargetID(targets[i].ID)) {
				return fmt.Errorf("target dependency cycle: %s", strings.Join(mapFunc(cycle, func(s TargetID) string { return string(s) }), " -> "))
			}
		}
	}
	return nil
}

// mapFunc applies fn to every element of s, in order.
func mapFunc[S ~[]E, E, T any](s S, fn func(E) T) []T {
	out := make([]T, len(s))
	for i, e := range s {
		out[i] = fn(e)
	}
	return out
}
