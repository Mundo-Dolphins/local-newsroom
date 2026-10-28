package tui

import (
	"context"
	"time"
)

// Service is the application capability the TUI may use.
//
// It is the only bridge between this package and the rest of the system: the
// TUI calls these methods and renders their results, and nothing else. The
// implementation (in the application layer) wires the real run, workspace,
// and orchestrator services behind this interface. This package therefore
// contains no pipeline business logic — no stage rules, no LLM calls, no
// persistence of its own — only presentation:
//
//   - the interface below (what the UI can ask for);
//   - plain data-transfer types (what the UI displays);
//   - the Model state machine and its rendering (how the UI behaves).
//
// Every value crossing this boundary is display-safe: credentials are never
// part of the contract, and free-text fields (topics, warnings, errors) come
// from the already-redacted run manifest, not from raw LLM responses.
type Service interface {
	// Info describes the application environment the UI shows up front:
	// which remote endpoints are configured and which editorial profiles
	// are available. It never contains secrets.
	Info() (Info, error)

	// ListRuns returns the workspace's runs, most recently updated first.
	ListRuns(ctx context.Context) ([]RunInfo, error)

	// RunDetail returns the full display state of one run: stages,
	// warnings, errors, artifacts, and the run summary.
	RunDetail(ctx context.Context, id string) (RunDetail, error)

	// StartNewRun validates the intent, creates and starts a new run, and
	// returns the run ID as soon as the run is claimed and persisted.
	// Execution continues in the background; the UI observes progress by
	// polling RunDetail and Done.
	StartNewRun(ctx context.Context, intent NewRunIntent) (string, error)

	// ResumeRun continues an interrupted or failed run. Like StartNewRun it
	// returns when the cycle is underway; Done and RunDetail track it.
	ResumeRun(ctx context.Context, id string) error

	// RetryStage re-runs one stage of a run (invalidating stale downstream
	// stages) and continues the run.
	RetryStage(ctx context.Context, id, stage string) error

	// Cancel stops the in-flight run (if any). The run is recorded as
	// cancelled and remains resumable.
	Cancel()

	// Done is closed when the in-flight run/resume/retry cycle reaches its
	// terminal state. It may be nil when nothing is in flight.
	Done() <-chan struct{}

	// InFlight reports the ID of the run currently executing in this
	// session ("" when none).
	InFlight() string
}

// Info is the environment summary shown to the user.
type Info struct {
	// WorkspaceRoot is the absolute directory where runs are stored.
	WorkspaceRoot string

	// LLMConfigured reports whether an LLM endpoint is fully configured.
	LLMConfigured bool
	// LLMBaseURL is the configured LLM endpoint (not a secret).
	LLMBaseURL string
	// LLMModel is the configured model name.
	LLMModel string

	// SearchConfigured reports whether a search endpoint is configured.
	SearchConfigured bool
	// SearchBaseURL is the configured search endpoint (not a secret).
	SearchBaseURL string

	// OutputFormat is the configured default output format
	// ("article", "thread", or "hybrid"; "" when unset).
	OutputFormat string

	// Profiles lists the available editorial profiles, in the form the
	// profile spec uses (<publication>/<author> or <author>).
	Profiles []ProfileOption

	// ProfilesDefault is the profiles.name value from the configuration
	// chain ("" when none is configured).
	ProfilesDefault string

	// Recipes lists the available editorial recipes (v0.6) for the run
	// wizard ("" when no recipes root is configured).
	Recipes []RecipeOption
}

// ProfileOption is one selectable editorial profile.
type ProfileOption struct {
	// Spec is the profile spec to pass in NewRunIntent.Profile.
	Spec string
	// Detail is a short display note (e.g. the directory it was found in).
	Detail string
}

// RecipeOption is one selectable editorial recipe (v0.6).
type RecipeOption struct {
	// ID is the recipe identifier to pass in NewRunIntent.Recipe.
	ID string
	// Title is the human name ("" falls back to the ID for display).
	Title string
	// Detail is a short display note (e.g. the layer it came from).
	Detail string
}

// NewRunIntent is the user's specification of a new run. Values mirror the
// `newsroom run` flags; empty strings mean "use the configured default".
type NewRunIntent struct {
	// Topic is the editorial topic (required).
	Topic string

	// Profile is the editorial profile spec ("" = profiles.name default).
	Profile string

	// Format is the output format: "article", "thread", or "hybrid"
	// ("" = configured default).
	Format string

	// SourceMode selects how sources are obtained: "auto" (search only),
	// "explicit" (URLs only), or "supplement" (URLs plus search).
	SourceMode string

	// URLs are the explicit source URLs (explicit/supplement modes).
	URLs []string

	// FollowUp enables bounded follow-up research for unresolved claims.
	FollowUp bool

	// Recipe selects an editorial recipe (v0.6) by ID ("" = no recipe; the
	// wizard offers "(none)"). A selected recipe may derive the topic and
	// the output format.
	Recipe string

	// RecipeInputs are the recipe's input values, name → value, as typed in
	// the wizard ("name=value" pairs).
	RecipeInputs map[string]string
}

// Valid checks the intent's source-mode specific fields.
func (i NewRunIntent) Valid() errorKind {
	if i.Topic == "" && i.Recipe == "" {
		return errKindTopic
	}
	switch i.SourceMode {
	case ModeExplicit, ModeSupplement:
		if len(i.URLs) == 0 {
			return errKindURLs
		}
	case ModeAuto:
	default:
		return errKindMode
	}
	return ""
}

// errKind classifies intent validation failures for display.
type errorKind string

const (
	errKindTopic errorKind = "topic"
	errKindURLs  errorKind = "urls"
	errKindMode  errorKind = "mode"
)

// Source mode values accepted in NewRunIntent.SourceMode.
const (
	// ModeAuto discovers sources through search only.
	ModeAuto = "auto"
	// ModeExplicit uses the provided URLs only.
	ModeExplicit = "explicit"
	// ModeSupplement uses the provided URLs plus search discovery.
	ModeSupplement = "supplement"
)

// RunInfo is the one-line display state of a run (list view).
type RunInfo struct {
	// ID is the run identifier.
	ID string
	// Topic is the run's editorial topic.
	Topic string
	// Status is the run status (pending, running, succeeded, failed, cancelled).
	Status string
	// SourceMode is how sources were obtained (explicit, discovered, supplement).
	SourceMode string
	// Profile is the selected profile ID ("" when none).
	Profile string
	// Recipe is the selected editorial recipe ID (v0.6; "" for standard runs).
	Recipe string
	// Model is the LLM model name ("" when unknown).
	Model string
	// StartedAt is when the run was created.
	StartedAt time.Time
	// UpdatedAt is when the run was last persisted.
	UpdatedAt time.Time
	// Warnings counts the run-level warnings recorded so far.
	Warnings int
	// Errors counts the run-level error entries recorded so far.
	Errors int
	// Artifacts counts the artifacts recorded so far.
	Artifacts int
}

// StageInfo is the display state of one stage.
type StageInfo struct {
	// Name is the stage name.
	Name string
	// Status is the stage status (pending, skipped, running, succeeded, failed, cancelled).
	Status string
	// SkipReason explains a skipped stage.
	SkipReason string
	// Error summarizes the stage's last failed attempt ("" when none).
	Error string
	// Attempts is the number of recorded attempts.
	Attempts int
	// StartedAt is when the current or last attempt started (zero when none).
	StartedAt time.Time
	// FinishedAt is when the last attempt finished (zero while running).
	FinishedAt time.Time
}

// ArtifactInfo is the display state of one run artifact.
type ArtifactInfo struct {
	// Kind classifies the artifact (e.g. rendered, sources, dossier).
	Kind string
	// Path is the absolute path of the artifact file.
	Path string
	// Size is the file size in bytes (-1 when unknown, 0 when readable).
	Size int64
}

// RunDetail is the full display state of one run (detail/run view).
type RunDetail struct {
	// RunInfo carries the one-line fields.
	RunInfo

	// Reason is the run-level failure or cancellation reason.
	Reason string
	// FailedStage names the stage that stopped a failed run ("" when none).
	FailedStage string

	// Stages is the stage plan in execution order.
	Stages []StageInfo
	// Warnings are the run-level warning messages, oldest first.
	Warnings []string
	// Errors are the run-level error messages, oldest first.
	Errors []string
	// Artifacts are the recorded artifacts (rendered outputs last).
	Artifacts []ArtifactInfo

	// Resumable reports whether a resume would be accepted.
	Resumable bool
	// ResumeReason explains why a resume would be refused ("" when resumable).
	ResumeReason string

	// Summary numbers (zero when the corresponding stage was not reached).
	SourcesAttempted int
	SourcesFetched   int
	Documents        int
	ClaimsTotal      int
	ClaimsSupported  int
	ClaimsUnresolved int
	// QualityScore is the verification quality score (nil when not reached).
	QualityScore *float64
	// FinalCheck is the final factual check status ("" when not reached).
	FinalCheck string
	// Findings is the number of final-check findings.
	Findings int

	// PublicationStatus mirrors the manifest's publication_state summary
	// (the v0.6 plan status; "" when the run has no plans), alongside its
	// target counts. The run view renders these as a single line.
	PublicationStatus    string
	PublicationTargets   int
	PublicationSucceeded int
	PublicationFailed    int
}
