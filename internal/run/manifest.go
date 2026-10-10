// Package run defines the v0.5 persistence model for newsroom runs.
//
// A Manifest is a machine-readable, versioned record of one editorial job:
// from its topic and effective configuration, through every stage attempt,
// to its artifacts and final completion state. A single manifest represents
// partial, failed, and successful runs alike, so every execution is
// reproducible, inspectable, and resumable by future tooling.
//
// Design principles:
//   - Versioned: the schema_version field enables future migrations.
//   - Deterministic: stable RunIDs, explicit enums, UTC RFC 3339 timestamps.
//   - Secret-free: model names and endpoints are recorded; credentials are
//     never part of the contract, and free-text values are redacted before
//     recording.
//   - JSON-first: every contract is JSON-serializable.
//   - Stage-agnostic: a run does NOT execute every standard stage; stages
//     may be absent from the plan, skipped, retried, or run in any
//     combination.
//
// This package is a contract layer: it performs no filesystem I/O, no LLM
// calls, and implements no resume or CLI/TUI behavior.
package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SchemaVersion is the version of the run manifest schema implemented by this
// package.
//
// The version is semantic (MAJOR.MINOR.PATCH). Consumers must reject
// manifests whose MAJOR version they do not understand; MINOR/PATCH changes
// are backward compatible.
const SchemaVersion = "0.5.0"

// SchemaMajorCurrent is the schema major version understood by this package.
const SchemaMajorCurrent = 0

// ErrUnsupportedSchemaVersion indicates that a manifest was written with a
// schema major version this package cannot read.
var ErrUnsupportedSchemaVersion = errors.New("unsupported manifest schema version")

// ---------------------------------------------------------------------------
// RunID
// ---------------------------------------------------------------------------

// RunID is the stable identifier of a newsroom run.
//
// A run ID is fixed for the lifetime of the run: it never changes as the
// run progresses, is filesystem-safe, and is deterministic — the same
// workspace, topic, and start time always derive the same ID, so IDs can be
// re-derived (e.g. during resume) without storing them.
type RunID string

// RunIDPrefix is the required prefix of every run ID.
const RunIDPrefix = "run-"

// maxRunIDLen bounds the length of a run ID body (after the prefix).
const maxRunIDLen = 128

// NewRunID derives a stable run ID from the workspace name, the run topic,
// and its start time.
//
// Format: "run-<start-time>-<12 hex chars>", where the hash is the SHA-256
// of the workspace name, topic, and start time. The explicit timestamp
// component keeps IDs human-inspectable; the hash keeps them unique and
// unguessable.
func NewRunID(workspace, topic string, startedAt time.Time) RunID {
	at := startedAt.UTC()
	sum := sha256.Sum256([]byte(strings.Join([]string{workspace, topic, at.Format(time.RFC3339Nano)}, "\x00")))
	return RunID(RunIDPrefix + at.Format("20060102T150405Z") + "-" + hex.EncodeToString(sum[:6]))
}

// runIDBodyRe validates the conservative filesystem-safe character set of a
// run ID body: letters, digits, dot, dash, underscore.
var runIDBodyRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)

// Validate checks that the run ID is well-formed.
func (id RunID) Validate() error {
	s := string(id)
	if !strings.HasPrefix(s, RunIDPrefix) {
		return fmt.Errorf("run id %q must start with %q", s, RunIDPrefix)
	}
	body := s[len(RunIDPrefix):]
	if body == "" || len(body) > maxRunIDLen {
		return fmt.Errorf("run id %q: body must be 1-%d characters", s, maxRunIDLen)
	}
	if !runIDBodyRe.MatchString(body) {
		return fmt.Errorf("run id %q: contains invalid characters (allowed: letters, digits, '.', '-', '_')", s)
	}
	return nil
}

// String returns the string representation of the run ID.
func (id RunID) String() string {
	return string(id)
}

// ---------------------------------------------------------------------------
// Workspace
// ---------------------------------------------------------------------------

// Workspace identifies the workspace that owns a run.
//
// A workspace is the named context in which runs are executed (its
// publications, profiles, and archives). Only its identity is recorded here;
// v0.5 performs no filesystem persistence.
type Workspace struct {
	// Name is the workspace name (required).
	Name string `json:"name"`

	// Path is an optional reference to the workspace directory.
	// It is recorded for traceability only; v0.5 does not read or write it.
	Path string `json:"path,omitempty"`
}

// workspaceNameRe allows a conservative filesystem-safe name.
var workspaceNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Validate checks the workspace identity.
func (w Workspace) Validate() error {
	if w.Name == "" {
		return errors.New("workspace name is required")
	}
	if !workspaceNameRe.MatchString(w.Name) {
		return fmt.Errorf("workspace name %q is invalid: use letters, digits, '.', '-', '_' (1-64 chars, must not start with a separator)", w.Name)
	}
	if w.Path != "" && containsTraversal(w.Path) {
		return fmt.Errorf("workspace path %q must not contain '..' path segments", w.Path)
	}
	return nil
}

// containsTraversal reports whether p contains a ".." path segment.
func containsTraversal(p string) bool {
	for _, segment := range strings.FieldsFunc(p, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if segment == ".." {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// SourceMode
// ---------------------------------------------------------------------------

// SourceMode records how the sources for a run were selected.
type SourceMode string

// Source mode values.
const (
	// SourceModeExplicit means sources came from user-provided URLs only.
	SourceModeExplicit SourceMode = "explicit"

	// SourceModeDiscovered means sources were discovered from the topic.
	SourceModeDiscovered SourceMode = "discovered"

	// SourceModeSupplement means explicit URLs were supplemented with
	// discovered sources.
	SourceModeSupplement SourceMode = "supplement"

	// SourceModeUnknown means the source mode could not be determined
	// (e.g. the manifest was rehydrated from a partial record).
	SourceModeUnknown SourceMode = "unknown"
)

// AllSourceModes lists every valid source mode.
var AllSourceModes = []SourceMode{
	SourceModeExplicit,
	SourceModeDiscovered,
	SourceModeSupplement,
	SourceModeUnknown,
}

// Valid reports whether the source mode is a known value.
func (m SourceMode) Valid() bool {
	for _, v := range AllSourceModes {
		if m == v {
			return true
		}
	}
	return false
}

// String returns the string representation of the source mode.
func (m SourceMode) String() string {
	return string(m)
}

// ---------------------------------------------------------------------------
// Profile
// ---------------------------------------------------------------------------

// Profile identifies the editorial profile selected for a run (style, facts
// policy, vocabulary, examples).
//
// Only identity and provenance are recorded — profile *content* is never
// part of the manifest. The optional content hash makes profile-sensitive
// runs reproducible.
type Profile struct {
	// ID is the selected profile name (e.g. an author profile name).
	ID string `json:"id"`

	// SourcePath is the optional directory the profile was loaded from.
	SourcePath string `json:"source_path,omitempty"`

	// Hash is an optional digest of the merged profile content, for
	// reproducibility (lowercase hex).
	Hash string `json:"hash,omitempty"`
}

// profileIDRe allows a conservative filesystem-safe profile identifier.
var profileIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`)

// profileHashRe allows lowercase hex digests of 8-128 characters.
var profileHashRe = regexp.MustCompile(`^[0-9a-f]{8,128}$`)

// Validate checks the profile reference.
func (p Profile) Validate() error {
	if p.ID == "" {
		return errors.New("profile id is required")
	}
	if !profileIDRe.MatchString(p.ID) {
		return fmt.Errorf("profile id %q is invalid: use letters, digits, '.', '-', '_' (1-100 chars)", p.ID)
	}
	if p.Hash != "" && !profileHashRe.MatchString(p.Hash) {
		return fmt.Errorf("profile hash %q is invalid: use lowercase hex (8-128 chars)", p.Hash)
	}
	if p.SourcePath != "" && containsTraversal(p.SourcePath) {
		return fmt.Errorf("profile source path %q must not contain '..' path segments", p.SourcePath)
	}
	return nil
}

// ---------------------------------------------------------------------------
// ModelMetadata
// ---------------------------------------------------------------------------

// ModelMetadata records the LLM model and endpoint used by a run.
//
// SECURITY: credentials are deliberately NOT part of this contract. The
// struct has no field for API keys, tokens, or other secrets, and endpoints
// are validated to be credential-free. This makes it structurally impossible
// for a manifest to record an API key in this field.
type ModelMetadata struct {
	// Provider is the provider identifier (e.g. "omlx", "openai-compat").
	Provider string `json:"provider,omitempty"`

	// Model is the model name (e.g. "llama3.1:8b").
	Model string `json:"model,omitempty"`

	// Endpoint is the API base URL (e.g. "http://localhost:8000/v1").
	Endpoint string `json:"endpoint,omitempty"`
}

// maxModelFieldLen bounds provider/model/endpoint field length.
const maxModelFieldLen = 128

// NewModelMetadata builds model metadata, redacting anything secret-looking
// in the endpoint (defence in depth; the contract stores no credentials).
func NewModelMetadata(provider, model, endpoint string) ModelMetadata {
	return ModelMetadata{
		Provider: provider,
		Model:    model,
		Endpoint: RedactSecrets(endpoint),
	}
}

// Validate checks the model metadata.
func (m ModelMetadata) Validate() error {
	if m.Provider != "" && len(m.Provider) > maxModelFieldLen {
		return fmt.Errorf("model provider %q exceeds %d characters", m.Provider, maxModelFieldLen)
	}
	if m.Model != "" && len(m.Model) > maxModelFieldLen {
		return fmt.Errorf("model name %q exceeds %d characters", m.Model, maxModelFieldLen)
	}
	if m.Provider != "" && LooksLikeSecret(m.Provider) {
		return fmt.Errorf("model provider %q contains secret-looking material", m.Provider)
	}
	if m.Model != "" && LooksLikeSecret(m.Model) {
		return fmt.Errorf("model name %q contains secret-looking material", m.Model)
	}
	if m.Endpoint != "" {
		if len(m.Endpoint) > maxModelFieldLen {
			return fmt.Errorf("model endpoint %q exceeds %d characters", m.Endpoint, maxModelFieldLen)
		}
		// url.Parse rejects the "[REDACTED]" placeholder in userinfo (square
		// brackets are not valid userinfo syntax), so redacted endpoints are
		// re-parsed with the placeholder swapped for a parse-safe marker.
		redacted := strings.Contains(m.Endpoint, RedactedPlaceholder)
		toParse := m.Endpoint
		if redacted {
			toParse = strings.ReplaceAll(m.Endpoint, RedactedPlaceholder, "redacted")
		}
		u, err := url.Parse(toParse)
		if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("model endpoint %q is not a valid http(s) URL", m.Endpoint)
		}
		if u.User != nil {
			password, hasPassword := u.User.Password()
			// Any real password is a credential. The parse marker is only
			// accepted when it stands for a redacted password.
			if hasPassword && (!redacted || password != "redacted") {
				return fmt.Errorf("model endpoint must not contain credentials")
			}
		}
		if LooksLikeSecret(m.Endpoint) {
			return fmt.Errorf("model endpoint contains secret-looking material; redact it before recording")
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// ConfigSnapshot
// ---------------------------------------------------------------------------

// ConfigSnapshot records the effective configuration in force when the run
// executed — the values after all precedence (CLI, environment, defaults)
// was resolved.
//
// The snapshot is a generic name/value map so the manifest never hard-codes
// specific configuration keys. SECURITY: values for secret-like keys are
// stored as RedactedPlaceholder, and every stored value is checked for
// secret-looking material on validation.
type ConfigSnapshot struct {
	// Values maps effective configuration keys to their (redacted) string
	// values.
	Values map[string]string `json:"values,omitempty"`
}

// maxConfigKeyLen bounds configuration key length.
const maxConfigKeyLen = 128

// maxConfigValueLen bounds configuration value length.
const maxConfigValueLen = 8192

// configKeyRe allows conservative configuration key names.
var configKeyRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// Set records an effective configuration value.
//
// The value is always stored redacted: values under secret-like keys are
// replaced with RedactedPlaceholder, and every other value is passed through
// RedactSecrets. An empty value removes the key.
func (s *ConfigSnapshot) Set(key, value string) error {
	if key == "" || len(key) > maxConfigKeyLen || !configKeyRe.MatchString(key) {
		return fmt.Errorf("invalid config key %q: use letters, digits, '.', '-', '_' (1-%d chars)", key, maxConfigKeyLen)
	}
	if s.Values == nil {
		s.Values = make(map[string]string)
	}
	if value == "" {
		delete(s.Values, key)
		return nil
	}
	if IsSecretKey(key) {
		s.Values[key] = RedactedPlaceholder
		return nil
	}
	s.Values[key] = RedactSecrets(value)
	return nil
}

// Get returns the recorded value for a key.
func (s ConfigSnapshot) Get(key string) (string, bool) {
	v, ok := s.Values[key]
	return v, ok
}

// Keys returns the configuration keys in sorted order (deterministic).
func (s ConfigSnapshot) Keys() []string {
	keys := make([]string, 0, len(s.Values))
	for k := range s.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Validate checks the snapshot, rejecting any value that appears to be a raw
// credential.
func (s ConfigSnapshot) Validate() error {
	var errs []error
	for _, key := range s.Keys() {
		value := s.Values[key]
		if key == "" || len(key) > maxConfigKeyLen || !configKeyRe.MatchString(key) {
			errs = append(errs, fmt.Errorf("invalid config key %q", key))
			continue
		}
		if len(value) > maxConfigValueLen {
			errs = append(errs, fmt.Errorf("config value for %q exceeds %d characters", key, maxConfigValueLen))
			continue
		}
		if IsSecretKey(key) {
			if value != RedactedPlaceholder {
				errs = append(errs, fmt.Errorf("config value for secret-like key %q must be redacted", key))
			}
			continue
		}
		if LooksLikeSecret(value) {
			errs = append(errs, fmt.Errorf("config value for %q contains secret-looking material", key))
		}
	}
	return errors.Join(errs...)
}

// ---------------------------------------------------------------------------
// Warning / ErrorSummary
// ---------------------------------------------------------------------------

// Warning is a non-fatal note recorded against a run.
type Warning struct {
	// Code is an optional machine-readable category (e.g. "partial_fetch").
	Code string `json:"code,omitempty"`

	// Message is the human-readable warning text. Always redacted.
	Message string `json:"message"`

	// OccurredAt is when the warning was recorded (UTC).
	OccurredAt Timestamp `json:"occurred_at"`
}

// NewWarning builds a warning with its message redacted.
func NewWarning(code, message string, at time.Time) Warning {
	return Warning{
		Code:       code,
		Message:    RedactSecrets(message),
		OccurredAt: NewTimestamp(at),
	}
}

// codeRe allows conservative machine-readable warning/error codes.
var codeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// maxNoteLen bounds warning/error message length.
const maxNoteLen = 8192

// validateNote performs the checks shared by warnings and error summaries.
func validateNote(kind, code, message string, at Timestamp) error {
	if message == "" {
		return fmt.Errorf("%s message is required", kind)
	}
	if len(message) > maxNoteLen {
		return fmt.Errorf("%s message exceeds %d characters", kind, maxNoteLen)
	}
	if code != "" && !codeRe.MatchString(code) {
		return fmt.Errorf("%s code %q is invalid: use lowercase letters, digits, '.', '-', '_' (1-64 chars)", kind, code)
	}
	if at.IsZero() {
		return fmt.Errorf("%s timestamp is required", kind)
	}
	if LooksLikeSecret(message) {
		return fmt.Errorf("%s message contains secret-looking material; redact it before recording", kind)
	}
	return nil
}

// Validate checks the warning.
func (w Warning) Validate() error {
	return validateNote("warning", w.Code, w.Message, w.OccurredAt)
}

// ErrorSummary is a recorded, secret-free summary of a failure.
//
// Only a short summary is persisted: enough to understand what failed and
// where, without raw provider responses, request bodies, or credentials.
type ErrorSummary struct {
	// Code is an optional machine-readable category (e.g. "llm_config").
	Code string `json:"code,omitempty"`

	// Message is the human-readable failure summary. Always redacted.
	Message string `json:"message"`

	// OccurredAt is when the error was recorded (UTC).
	OccurredAt Timestamp `json:"occurred_at"`
}

// NewErrorSummary builds an error summary, redacting the message.
func NewErrorSummary(code, message string, at time.Time) ErrorSummary {
	return ErrorSummary{
		Code:       code,
		Message:    RedactSecrets(message),
		OccurredAt: NewTimestamp(at),
	}
}

// ErrorSummaryFrom builds an error summary from an error value, redacting its
// text.
func ErrorSummaryFrom(code string, err error, at time.Time) ErrorSummary {
	if err == nil {
		return ErrorSummary{}
	}
	return NewErrorSummary(code, err.Error(), at)
}

// Validate checks the error summary.
func (e ErrorSummary) Validate() error {
	return validateNote("error", e.Code, e.Message, e.OccurredAt)
}

// ---------------------------------------------------------------------------
// ArtifactRef
// ---------------------------------------------------------------------------

// ArtifactKind enumerates the kinds of artifacts a run can reference.
type ArtifactKind string

// Artifact kind values.
const (
	// ArtifactKindSource references fetched source material (fetch output).
	ArtifactKindSource ArtifactKind = "source"

	// ArtifactKindDocument references a normalized document (extract output).
	ArtifactKindDocument ArtifactKind = "document"

	// ArtifactKindDossier references the research dossier (research output).
	ArtifactKindDossier ArtifactKind = "dossier"

	// ArtifactKindVerification references the verification result.
	ArtifactKindVerification ArtifactKind = "verification"

	// ArtifactKindEditorial references the editorial artifact (write output).
	ArtifactKindEditorial ArtifactKind = "editorial"

	// ArtifactKindFactualCheck references the final factual check result.
	ArtifactKindFactualCheck ArtifactKind = "factual_check"

	// ArtifactKindRendered references a rendered output (render output).
	ArtifactKindRendered ArtifactKind = "rendered"

	// ArtifactKindCustom references an extension artifact kind.
	ArtifactKindCustom ArtifactKind = "custom"
)

// AllArtifactKinds lists every valid artifact kind.
var AllArtifactKinds = []ArtifactKind{
	ArtifactKindSource,
	ArtifactKindDocument,
	ArtifactKindDossier,
	ArtifactKindVerification,
	ArtifactKindEditorial,
	ArtifactKindFactualCheck,
	ArtifactKindRendered,
	ArtifactKindCustom,
}

// Valid reports whether the artifact kind is a known value.
func (k ArtifactKind) Valid() bool {
	for _, v := range AllArtifactKinds {
		if k == v {
			return true
		}
	}
	return false
}

// String returns the string representation of the artifact kind.
func (k ArtifactKind) String() string {
	return string(k)
}

// ArtifactRef references an artifact produced (or consumed) by a run.
//
// References, not content: the manifest points at artifacts by stable ID and
// optional storage path so a run can be re-attached to its outputs without
// embedding them.
type ArtifactRef struct {
	// Kind classifies the referenced artifact.
	Kind ArtifactKind `json:"kind"`

	// ID is the stable identifier of the artifact.
	ID string `json:"id"`

	// Stage is the stage that produced the artifact, when known.
	// Must refer to a stage planned in the run.
	Stage StageName `json:"stage,omitempty"`

	// Path is an optional reference to where the artifact is stored.
	// Recorded for traceability; v0.5 performs no filesystem persistence.
	Path string `json:"path,omitempty"`

	// ProducedAt is when the artifact was produced (UTC).
	ProducedAt Timestamp `json:"produced_at"`
}

// artifactIDRe allows conservative artifact identifiers.
var artifactIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// Validate checks the artifact reference.
func (a ArtifactRef) Validate() error {
	if !a.Kind.Valid() {
		return fmt.Errorf("artifact %q: unknown kind %q", a.ID, a.Kind)
	}
	if a.ID == "" || !artifactIDRe.MatchString(a.ID) {
		return fmt.Errorf("artifact id is required and must use letters, digits, '.', '-', '_' (1-128 chars)")
	}
	if a.Stage != "" && !a.Stage.Valid() {
		return fmt.Errorf("artifact %q: references unknown stage %q", a.ID, a.Stage)
	}
	if a.Path != "" && containsTraversal(a.Path) {
		return fmt.Errorf("artifact %q: path must not contain '..' path segments", a.ID)
	}
	if a.ProducedAt.IsZero() {
		return fmt.Errorf("artifact %q: produced_at is required", a.ID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------

// Outcome enumerates the terminal outcomes of a run.
//
// The zero value "" means the run has not reached a terminal state; it is a
// valid in-progress value, not an error.
type Outcome string

// Outcome values.
const (
	// OutcomeSucceeded means the run completed its editorial job.
	OutcomeSucceeded Outcome = "succeeded"

	// OutcomeFailed means the run stopped on a blocking failure.
	OutcomeFailed Outcome = "failed"

	// OutcomeCancelled means the run was stopped on request.
	OutcomeCancelled Outcome = "cancelled"
)

// AllOutcomes lists every terminal outcome.
var AllOutcomes = []Outcome{
	OutcomeSucceeded,
	OutcomeFailed,
	OutcomeCancelled,
}

// Valid reports whether the outcome is known, including the in-progress
// zero value.
func (o Outcome) Valid() bool {
	if o == "" {
		return true
	}
	for _, v := range AllOutcomes {
		if o == v {
			return true
		}
	}
	return false
}

// Terminal reports whether the outcome is a terminal one.
func (o Outcome) Terminal() bool {
	return o != ""
}

// String returns the string representation of the outcome.
func (o Outcome) String() string {
	return string(o)
}

// Result records the completion/failure state of a run.
type Result struct {
	// Outcome is the terminal outcome, or "" while the run is in progress.
	Outcome Outcome `json:"outcome,omitempty"`

	// Reason explains the failure or cancellation (redacted).
	Reason string `json:"reason,omitempty"`

	// FailedStage names the stage whose failure stopped the run, when
	// applicable. Only set for failed runs, and must refer to a stage that
	// actually failed.
	FailedStage *StageName `json:"failed_stage,omitempty"`
}

// maxReasonLen bounds the result reason length.
const maxReasonLen = 8192

// Validate checks the result.
func (r Result) Validate() error {
	if !r.Outcome.Valid() {
		return fmt.Errorf("unknown outcome %q", r.Outcome)
	}
	if len(r.Reason) > maxReasonLen {
		return fmt.Errorf("result reason exceeds %d characters", maxReasonLen)
	}
	if r.Reason != "" && LooksLikeSecret(r.Reason) {
		return fmt.Errorf("result reason contains secret-looking material; redact it before recording")
	}
	if r.Outcome == OutcomeFailed && r.Reason == "" {
		return fmt.Errorf("failed result requires a reason")
	}
	if r.FailedStage != nil {
		if !r.FailedStage.Valid() {
			return fmt.Errorf("failed_stage %q is unknown", *r.FailedStage)
		}
		if r.Outcome != OutcomeFailed {
			return fmt.Errorf("failed_stage is only valid on failed results")
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Manifest
// ---------------------------------------------------------------------------

// Manifest is the machine-readable, versioned record of one newsroom run.
//
// It is the unit of persistence: one editorial job from topic/configuration
// through final rendered artifacts. Manifests are JSON documents whose
// schema is versioned by SchemaVersion.
type Manifest struct {
	// SchemaVersion is the manifest schema version (see SchemaVersion).
	SchemaVersion string `json:"schema_version"`

	// ID uniquely and stably identifies this run.
	ID RunID `json:"id"`

	// Workspace is the workspace that owns this run.
	Workspace Workspace `json:"workspace"`

	// Status is the run lifecycle status.
	Status RunStatus `json:"status"`

	// Topic is the editorial topic of the run (required).
	Topic string `json:"topic"`

	// SourceMode records how sources were selected for this run.
	SourceMode SourceMode `json:"source_mode"`

	// Profile is the selected editorial profile, when one was selected.
	Profile *Profile `json:"profile,omitempty"`

	// Model records the LLM model and endpoint used by the run.
	// Credentials are never recorded (see ModelMetadata).
	Model ModelMetadata `json:"model"`

	// Config is the effective configuration snapshot.
	Config ConfigSnapshot `json:"config"`

	// Stages is the run's stage plan with per-stage status and attempt
	// history. A run may plan any subset of the standard stages, and every
	// planned stage may be skipped.
	Stages []StageRecord `json:"stages"`

	// Artifacts references the artifacts produced by the run.
	Artifacts []ArtifactRef `json:"artifacts,omitempty"`

	// Warnings is the run-level warning log (redacted).
	Warnings []Warning `json:"warnings,omitempty"`

	// Errors is the run-level error summary log (redacted).
	Errors []ErrorSummary `json:"errors,omitempty"`

	// Result is the completion/failure state of the run.
	Result Result `json:"result"`

	// StartedAt is when the run was created (UTC).
	StartedAt Timestamp `json:"started_at"`

	// UpdatedAt is when the manifest was last modified (UTC).
	UpdatedAt Timestamp `json:"updated_at"`

	// FinishedAt is when the run reached a terminal state.
	// Nil while the run is in progress.
	FinishedAt *Timestamp `json:"finished_at,omitempty"`
}

// maxTopicLen bounds the topic length.
const maxTopicLen = 1000

// NewManifestParams configures NewManifest.
type NewManifestParams struct {
	// ID is optional; derived via NewRunID when empty.
	ID RunID

	// Workspace identifies the owning workspace (name required).
	Workspace Workspace

	// Topic is the editorial topic (required).
	Topic string

	// SourceMode is optional; defaults to SourceModeUnknown.
	SourceMode SourceMode

	// Profile is optional.
	Profile *Profile

	// Model is optional; credentials must not be included (see ModelMetadata).
	Model ModelMetadata

	// Config is optional; when nil, an empty snapshot is used.
	Config *ConfigSnapshot

	// Stages is the planned stage list. When empty, the canonical pipeline
	// (CanonicalStages) is planned. Every planned stage starts pending;
	// none is required to run.
	Stages []StageName

	// StartedAt is optional; defaults to the current time.
	StartedAt time.Time
}

// NewManifest creates a new, fully valid manifest for a run that has not
// started yet: the run is pending, every planned stage is pending, and no
// attempts, artifacts, or results exist.
//
// It returns an error if the parameters would produce an invalid manifest.
func NewManifest(p NewManifestParams) (*Manifest, error) {
	if p.SourceMode == "" {
		p.SourceMode = SourceModeUnknown
	}
	if !p.SourceMode.Valid() {
		return nil, fmt.Errorf("invalid source mode %q", p.SourceMode)
	}
	if p.Topic == "" {
		return nil, errors.New("topic is required")
	}
	if len(p.Topic) > maxTopicLen {
		return nil, fmt.Errorf("topic exceeds %d characters", maxTopicLen)
	}
	// Redact the topic at the door, exactly like every other free-text value
	// (config values, error messages, skip reasons): a detected secret in
	// the topic must never be recorded. The run ID derives from the redacted
	// form so re-derivation stays stable.
	p.Topic = RedactSecrets(p.Topic)
	if len(p.Stages) == 0 {
		p.Stages = CanonicalStages()
	}
	plan := make([]StageRecord, 0, len(p.Stages))
	seen := make(map[StageName]bool, len(p.Stages))
	for _, name := range p.Stages {
		if !name.Valid() {
			return nil, fmt.Errorf("invalid stage name %q in run plan", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate stage %q in run plan", name)
		}
		seen[name] = true
		plan = append(plan, StageRecord{
			Name:   name,
			Status: StageStatusPending,
		})
	}

	startedAt := p.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	id := p.ID
	if id == "" {
		id = NewRunID(p.Workspace.Name, p.Topic, startedAt)
	}

	ts := NewTimestamp(startedAt)
	m := &Manifest{
		SchemaVersion: SchemaVersion,
		ID:            id,
		Workspace:     p.Workspace,
		Status:        RunStatusPending,
		Topic:         p.Topic,
		SourceMode:    p.SourceMode,
		Profile:       p.Profile,
		Model:         p.Model,
		Stages:        plan,
		StartedAt:     ts,
		UpdatedAt:     ts,
	}
	if p.Config != nil {
		m.Config = *p.Config
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	return m, nil
}

// stageIndex returns the index of the record for name, or -1.
func (m *Manifest) stageIndex(name StageName) int {
	for i := range m.Stages {
		if m.Stages[i].Name == name {
			return i
		}
	}
	return -1
}

// touch validates the timestamp against the manifest clock and advances
// updated_at. It rejects timestamps earlier than the last update so manifests
// can never record a backwards-moving history.
func (m *Manifest) touch(at time.Time) error {
	ts := NewTimestamp(at)
	if ts.IsZero() {
		return errors.New("timestamp is required")
	}
	if ts.Before(m.UpdatedAt) {
		return fmt.Errorf("timestamp regression: %s is earlier than last update %s", ts, m.UpdatedAt)
	}
	m.UpdatedAt = ts
	return nil
}

// assertActive rejects mutation of terminal runs: a finished manifest is a
// historical record and must not be rewritten (re-running a job is a new run).
func (m *Manifest) assertActive() error {
	if m.Status.Terminal() {
		return fmt.Errorf("run %s is %s; terminal runs are immutable", m.ID, m.Status)
	}
	return nil
}

// Validate performs full structural validation of the manifest, including all
// cross-field invariants:
//
//   - schema version, IDs, and every sub-contract
//   - run status <-> stage status consistency
//   - run status <-> result outcome consistency
//   - result failed_stage refers to an actually failed stage
//   - artifact references refer to planned stages
//   - timestamp ordering (started <= updated, terminal runs finished)
func (m *Manifest) Validate() error {
	if m == nil {
		return errors.New("manifest is nil")
	}
	var errs []error
	addf := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if m.SchemaVersion == "" {
		addf("schema_version is required")
	} else {
		major, err := SchemaMajor(m.SchemaVersion)
		if err != nil {
			addf("schema_version %q is not a semantic version: %v", m.SchemaVersion, err)
		} else if major != SchemaMajorCurrent {
			addf("%w: manifest is %s, this version understands major %d", ErrUnsupportedSchemaVersion, m.SchemaVersion, SchemaMajorCurrent)
		}
	}

	if err := m.ID.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := m.Workspace.Validate(); err != nil {
		errs = append(errs, err)
	}
	if !m.Status.Valid() {
		addf("invalid run status %q", m.Status)
	}
	if m.Topic == "" {
		addf("topic is required")
	}
	if len(m.Topic) > maxTopicLen {
		addf("topic exceeds %d characters", maxTopicLen)
	}
	if m.Topic != "" && LooksLikeSecret(m.Topic) {
		addf("topic contains secret-looking material; redact it before recording")
	}
	if !m.SourceMode.Valid() {
		addf("invalid source mode %q", m.SourceMode)
	}
	if m.Profile != nil {
		if err := m.Profile.Validate(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := m.Model.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := m.Config.Validate(); err != nil {
		errs = append(errs, err)
	}

	// Stages: names, records, duplicates.
	planned := make(map[StageName]bool, len(m.Stages))
	for _, s := range m.Stages {
		if !s.Name.Valid() {
			addf("stage %q: unknown stage name", s.Name)
			continue
		}
		if planned[s.Name] {
			addf("duplicate stage %q in manifest", s.Name)
			continue
		}
		planned[s.Name] = true
		if err := s.Validate(); err != nil {
			errs = append(errs, err)
		}
	}

	// Run status <-> stage status consistency.
	switch m.Status {
	case RunStatusPending:
		for _, s := range m.Stages {
			if s.Status != StageStatusPending && s.Status != StageStatusSkipped {
				addf("run is pending but stage %q is %q", s.Name, s.Status)
			}
		}
	case RunStatusRunning:
		anyActive := false
		for _, s := range m.Stages {
			if s.Status == StageStatusPending || s.Status == StageStatusRunning {
				anyActive = true
				break
			}
		}
		if !anyActive && len(m.Stages) > 0 {
			addf("run is running but no stage is pending or running")
		}
	case RunStatusSucceeded:
		for _, s := range m.Stages {
			if s.Status != StageStatusSucceeded && s.Status != StageStatusSkipped {
				addf("run is succeeded but stage %q is %q", s.Name, s.Status)
			}
		}
	case RunStatusFailed, RunStatusCancelled:
		for _, s := range m.Stages {
			if s.Status == StageStatusRunning {
				addf("run is %s but stage %q is still running", m.Status, s.Name)
			}
		}
	}

	// Artifacts.
	artifactIDs := make(map[string]bool, len(m.Artifacts))
	for _, a := range m.Artifacts {
		if err := a.Validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		if artifactIDs[a.ID] {
			addf("duplicate artifact id %q", a.ID)
			continue
		}
		artifactIDs[a.ID] = true
		if a.Stage != "" && !planned[a.Stage] {
			addf("artifact %q references stage %q which is not part of this run", a.ID, a.Stage)
		}
	}

	// Warnings and errors.
	for i, w := range m.Warnings {
		if err := w.Validate(); err != nil {
			addf("warnings[%d]: %v", i, err)
		}
	}
	for i, e := range m.Errors {
		if err := e.Validate(); err != nil {
			addf("errors[%d]: %v", i, err)
		}
	}

	// Result.
	if err := m.Result.Validate(); err != nil {
		errs = append(errs, err)
	}
	switch m.Status {
	case RunStatusPending, RunStatusRunning:
		if m.Result.Outcome != "" {
			addf("run is %s but result outcome is %q; terminal outcomes are only valid on terminal runs", m.Status, m.Result.Outcome)
		}
	case RunStatusSucceeded:
		if m.Result.Outcome != OutcomeSucceeded {
			addf("run is succeeded but result outcome is %q", m.Result.Outcome)
		}
	case RunStatusFailed:
		if m.Result.Outcome != OutcomeFailed {
			addf("run is failed but result outcome is %q", m.Result.Outcome)
		}
	case RunStatusCancelled:
		if m.Result.Outcome != OutcomeCancelled {
			addf("run is cancelled but result outcome is %q", m.Result.Outcome)
		}
	}
	if m.Result.FailedStage != nil {
		found := false
		for _, s := range m.Stages {
			if s.Name == *m.Result.FailedStage && s.Status == StageStatusFailed {
				found = true
				break
			}
		}
		if !found {
			addf("result failed_stage %q does not refer to a failed stage", *m.Result.FailedStage)
		}
	}

	// Timestamps.
	if m.StartedAt.IsZero() {
		addf("started_at is required")
	}
	if m.UpdatedAt.IsZero() {
		addf("updated_at is required")
	} else if !m.StartedAt.IsZero() && m.UpdatedAt.Before(m.StartedAt) {
		addf("updated_at is earlier than started_at")
	}
	if m.FinishedAt != nil {
		if m.FinishedAt.IsZero() {
			addf("finished_at must not be zero when set")
		} else if !m.Status.Terminal() {
			addf("finished_at is set but run is still %s", m.Status)
		} else if !m.StartedAt.IsZero() && m.FinishedAt.Before(m.StartedAt) {
			addf("finished_at is earlier than started_at")
		}
	} else if m.Status.Terminal() {
		addf("terminal run requires finished_at")
	}

	return errors.Join(errs...)
}

// ---------------------------------------------------------------------------
// Manifest mutators (state machine)
// ---------------------------------------------------------------------------

// transitionRun moves the run to a new status when the transition is valid,
// advancing updated_at and, for terminal states, recording finished_at.
//
// Terminal result fields (outcome, reason, failed_stage) are set by the
// specific mutators (Succeed/Fail/Cancel).
func (m *Manifest) transitionRun(to RunStatus, at time.Time) error {
	if !ValidRunTransition(m.Status, to) {
		return fmt.Errorf("invalid run transition: %q -> %q", m.Status, to)
	}
	if err := m.touch(at); err != nil {
		return err
	}
	m.Status = to
	if to.Terminal() {
		ts := NewTimestamp(at)
		m.FinishedAt = &ts
	}
	return nil
}

// Start marks the run as running.
//
// Valid only from the pending state.
func (m *Manifest) Start(at time.Time) error {
	return m.transitionRun(RunStatusRunning, at)
}

// Succeed completes the run successfully.
//
// Valid only from the running state, and only when every planned stage is
// succeeded or skipped. Stages that are still pending (planned but never
// started or skipped) block success: a successful run has fully executed its
// plan.
func (m *Manifest) Succeed(at time.Time) error {
	for _, s := range m.Stages {
		if s.Status != StageStatusSucceeded && s.Status != StageStatusSkipped {
			return fmt.Errorf("cannot succeed run: stage %q is %q; every planned stage must be succeeded or skipped", s.Name, s.Status)
		}
	}
	if err := m.transitionRun(RunStatusSucceeded, at); err != nil {
		return err
	}
	m.Result.Outcome = OutcomeSucceeded
	return nil
}

// Fail terminates the run with a blocking failure.
//
// Valid only from the running state. Stages still running are failed with the
// same summary (the run failure terminated them); stages that never started
// remain pending. The result records the reason and, when a stage failed,
// the last failed stage as the failure origin.
func (m *Manifest) Fail(at time.Time, code, message string) error {
	if message == "" {
		return errors.New("failure message is required")
	}
	if err := m.transitionRun(RunStatusFailed, at); err != nil {
		return err
	}
	ts := NewTimestamp(at)
	summary := NewErrorSummary(code, message, at)
	if err := summary.Validate(); err != nil {
		return fmt.Errorf("invalid failure summary: %w", err)
	}
	for i := range m.Stages {
		s := &m.Stages[i]
		if s.Status != StageStatusRunning {
			continue
		}
		if len(s.Attempts) == 0 {
			continue
		}
		last := &s.Attempts[len(s.Attempts)-1]
		last.Status = StageStatusFailed
		last.FinishedAt = &ts
		last.Error = &summary
		s.Status = StageStatusFailed
		m.Errors = append(m.Errors, summary)
	}
	m.Result.Outcome = OutcomeFailed
	m.Result.Reason = summary.Message
	for i := len(m.Stages) - 1; i >= 0; i-- {
		if m.Stages[i].Status == StageStatusFailed {
			name := m.Stages[i].Name
			m.Result.FailedStage = &name
			break
		}
	}
	return nil
}

// Cancel stops the run on request.
//
// Valid from pending or running. Stages still running are cancelled; stages
// that never started remain pending.
func (m *Manifest) Cancel(at time.Time, reason string) error {
	if err := m.transitionRun(RunStatusCancelled, at); err != nil {
		return err
	}
	ts := NewTimestamp(at)
	for i := range m.Stages {
		s := &m.Stages[i]
		if s.Status != StageStatusRunning {
			continue
		}
		if len(s.Attempts) == 0 {
			continue
		}
		last := &s.Attempts[len(s.Attempts)-1]
		last.Status = StageStatusCancelled
		last.FinishedAt = &ts
		s.Status = StageStatusCancelled
	}
	m.Result.Outcome = OutcomeCancelled
	m.Result.Reason = RedactSecrets(reason)
	return nil
}

// StartStage starts (or restarts after failure) a stage, appending a new
// attempt.
//
// Valid from pending, failed (retry), or — for an already-running stage —
// never (restarts must wait for the attempt to end). Starting the first
// stage of a pending run also moves the run to running.
func (m *Manifest) StartStage(name StageName, at time.Time) error {
	if err := m.assertActive(); err != nil {
		return err
	}
	idx := m.stageIndex(name)
	if idx < 0 {
		return fmt.Errorf("stage %q is not part of this run", name)
	}
	rec := &m.Stages[idx]
	if !ValidStageTransition(rec.Status, StageStatusRunning) {
		return fmt.Errorf("invalid stage transition for %q: %q -> %q", name, rec.Status, StageStatusRunning)
	}
	if err := m.touch(at); err != nil {
		return err
	}
	ts := NewTimestamp(at)
	rec.Status = StageStatusRunning
	rec.Attempts = append(rec.Attempts, StageAttempt{
		Number:    len(rec.Attempts) + 1,
		Status:    StageStatusRunning,
		StartedAt: ts,
	})
	if m.Status == RunStatusPending {
		m.Status = RunStatusRunning
	}
	return nil
}

// SkipStage marks a stage as skipped.
//
// Valid only from pending: skipping is a planning decision made before the
// stage runs. A skip reason is required.
func (m *Manifest) SkipStage(name StageName, at time.Time, reason string) error {
	if reason == "" {
		return fmt.Errorf("skip reason is required for stage %q", name)
	}
	if err := m.assertActive(); err != nil {
		return err
	}
	idx := m.stageIndex(name)
	if idx < 0 {
		return fmt.Errorf("stage %q is not part of this run", name)
	}
	rec := &m.Stages[idx]
	if !ValidStageTransition(rec.Status, StageStatusSkipped) {
		return fmt.Errorf("invalid stage transition for %q: %q -> %q", name, rec.Status, StageStatusSkipped)
	}
	if err := m.touch(at); err != nil {
		return err
	}
	rec.Status = StageStatusSkipped
	rec.SkipReason = RedactSecrets(reason)
	return nil
}

// SucceedStage completes the stage's current attempt successfully.
//
// Valid only while the stage is running.
func (m *Manifest) SucceedStage(name StageName, at time.Time) error {
	return m.finishStageAttempt(name, StageStatusSucceeded, at, nil)
}

// FailStage fails the stage's current attempt.
//
// Valid only while the stage is running. The failure is recorded as a new
// attempt summary, on the stage, and in the run-level error log (redacted).
// The stage status becomes failed; a retry via StartStage is still possible.
func (m *Manifest) FailStage(name StageName, at time.Time, code, message string) error {
	if message == "" {
		return fmt.Errorf("failure message is required for stage %q", name)
	}
	summary := NewErrorSummary(code, message, at)
	if err := summary.Validate(); err != nil {
		return fmt.Errorf("invalid failure summary for stage %q: %w", name, err)
	}
	if err := m.finishStageAttempt(name, StageStatusFailed, at, &summary); err != nil {
		return err
	}
	m.Errors = append(m.Errors, summary)
	return nil
}

// CancelStage cancels the stage's current attempt (e.g. the run was stopped).
//
// Valid only while the stage is running.
func (m *Manifest) CancelStage(name StageName, at time.Time) error {
	return m.finishStageAttempt(name, StageStatusCancelled, at, nil)
}

// finishStageAttempt moves the stage's current running attempt to a terminal
// status and syncs the stage status. Shared by SucceedStage/FailStage/CancelStage.
func (m *Manifest) finishStageAttempt(name StageName, to StageStatus, at time.Time, summary *ErrorSummary) error {
	if err := m.assertActive(); err != nil {
		return err
	}
	idx := m.stageIndex(name)
	if idx < 0 {
		return fmt.Errorf("stage %q is not part of this run", name)
	}
	rec := &m.Stages[idx]
	if !ValidStageTransition(rec.Status, to) {
		return fmt.Errorf("invalid stage transition for %q: %q -> %q", name, rec.Status, to)
	}
	if len(rec.Attempts) == 0 {
		return fmt.Errorf("stage %q has no running attempt", name)
	}
	if err := m.touch(at); err != nil {
		return err
	}
	last := &rec.Attempts[len(rec.Attempts)-1]
	if last.Status != StageStatusRunning {
		return fmt.Errorf("stage %q: current attempt is %q, not running", name, last.Status)
	}
	ts := NewTimestamp(at)
	last.Status = to
	last.FinishedAt = &ts
	if summary != nil {
		last.Error = summary
	}
	rec.Status = to
	return nil
}

// AddArtifact references an artifact produced by the run.
//
// The artifact ID must be unique within the run; its stage (when set) must
// be a planned stage that is not pending or skipped; and the reference must
// be internally valid.
func (m *Manifest) AddArtifact(ref ArtifactRef) error {
	if err := m.assertActive(); err != nil {
		return err
	}
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("invalid artifact: %w", err)
	}
	for _, a := range m.Artifacts {
		if a.ID == ref.ID {
			return fmt.Errorf("duplicate artifact id %q", ref.ID)
		}
	}
	if ref.Stage != "" {
		idx := m.stageIndex(ref.Stage)
		if idx < 0 {
			return fmt.Errorf("artifact %q references stage %q which is not part of this run", ref.ID, ref.Stage)
		}
		status := m.Stages[idx].Status
		if status == StageStatusPending || status == StageStatusSkipped {
			return fmt.Errorf("artifact %q references stage %q which is %q; stages that did not run produce no artifacts", ref.ID, ref.Stage, status)
		}
	}
	if err := m.touch(ref.ProducedAt.Time()); err != nil {
		return err
	}
	m.Artifacts = append(m.Artifacts, ref)
	return nil
}

// AddWarning records a run-level warning (redacted).
func (m *Manifest) AddWarning(code, message string, at time.Time) error {
	if err := m.assertActive(); err != nil {
		return err
	}
	w := NewWarning(code, message, at)
	if err := w.Validate(); err != nil {
		return fmt.Errorf("invalid warning: %w", err)
	}
	if err := m.touch(at); err != nil {
		return err
	}
	m.Warnings = append(m.Warnings, w)
	return nil
}

// AddError records a run-level error summary (redacted).
func (m *Manifest) AddError(code, message string, at time.Time) error {
	if err := m.assertActive(); err != nil {
		return err
	}
	e := NewErrorSummary(code, message, at)
	if err := e.Validate(); err != nil {
		return fmt.Errorf("invalid error summary: %w", err)
	}
	if err := m.touch(at); err != nil {
		return err
	}
	m.Errors = append(m.Errors, e)
	return nil
}

// StageStatusOf returns the current status of a planned stage, or an error if
// the stage is not part of the run.
func (m *Manifest) StageStatusOf(name StageName) (StageStatus, error) {
	idx := m.stageIndex(name)
	if idx < 0 {
		return "", fmt.Errorf("stage %q is not part of this run", name)
	}
	return m.Stages[idx].Status, nil
}

// ---------------------------------------------------------------------------
// Schema version helpers and JSON I/O
// ---------------------------------------------------------------------------

// SchemaMajor extracts the major version from a semantic version string.
//
// A valid version has exactly three non-negative integer components,
// e.g. "0.5.0".
func SchemaMajor(version string) (int, error) {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("%q must have MAJOR.MINOR.PATCH form", version)
	}
	major := 0
	for i, part := range parts {
		if part == "" {
			return 0, fmt.Errorf("version %q has an empty component", version)
		}
		var n int
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, fmt.Errorf("version %q has a non-numeric component", version)
			}
			n = n*10 + int(c-'0')
		}
		if i == 0 {
			major = n
		}
	}
	return major, nil
}

// MarshalManifest serializes a manifest to indented JSON.
func MarshalManifest(m *Manifest) ([]byte, error) {
	if m == nil {
		return nil, errors.New("manifest is nil")
	}
	return json.MarshalIndent(m, "", "  ")
}

// UnmarshalManifest parses a manifest from JSON and validates it.
//
// It rejects documents whose schema major version is not understood
// (ErrUnsupportedSchemaVersion) and any document that fails structural
// validation, including unknown enum values. Fields this version does not
// know about are ignored (forward compatibility with future 0.x schemas);
// all fields it does know must be present and valid.
func UnmarshalManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid manifest JSON: %w", err)
	}
	if m.SchemaVersion == "" {
		return nil, fmt.Errorf("%w: schema_version is required", ErrUnsupportedSchemaVersion)
	}
	major, err := SchemaMajor(m.SchemaVersion)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedSchemaVersion, err)
	}
	if major != SchemaMajorCurrent {
		return nil, fmt.Errorf("%w: manifest is %s, this version understands major %d", ErrUnsupportedSchemaVersion, m.SchemaVersion, SchemaMajorCurrent)
	}
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	return &m, nil
}
