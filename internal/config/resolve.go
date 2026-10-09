package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// ---------------------------------------------------------------------------
// Setting table
// ---------------------------------------------------------------------------

// Setting name constants.
const (
	SettingLLMBaseURL        = "llm.base_url"
	SettingLLMModel          = "llm.model"
	SettingLLMAPIKey         = "llm.api_key"
	SettingLLMTimeout        = "llm.timeout"
	SettingSearchBaseURL     = "search.base_url"
	SettingSearchLanguage    = "search.language"
	SettingSearchTimeRange   = "search.time_range"
	SettingSearchMaxQueries  = "search.max_queries"
	SettingSearchResults     = "search.results_per_query"
	SettingSearchMaxSources  = "search.max_sources"
	SettingSearchAPIKey      = "search.api_key"
	SettingSearchAPIHeader   = "search.api_header"
	SettingEmbeddingBaseURL  = "embedding.base_url"
	SettingEmbeddingModel    = "embedding.model"
	SettingEmbeddingAPIKey   = "embedding.api_key"
	SettingEmbeddingTimeout  = "embedding.timeout"
	SettingRerankEnabled     = "rerank.enabled"
	SettingRerankModel       = "rerank.model"
	SettingArchiveDBPath     = "archive.db_path"
	SettingArchiveTopK       = "archive.top_k"
	SettingArchiveFormat     = "archive.format"
	SettingFetchTimeout      = "fetch.timeout"
	SettingFetchMaxSize      = "fetch.max_size"
	SettingFetchMaxWords     = "fetch.max_words"
	SettingFetchParallel     = "fetch.parallel"
	SettingChunkTargetSize   = "chunk.target_size"
	SettingChunkMaxSize      = "chunk.max_size"
	SettingChunkOverlapRatio = "chunk.overlap_ratio"
	SettingProfilesRoot      = "profiles.root"
	SettingProfilesName      = "profiles.name"
	SettingDefaultsLanguage  = "defaults.language"
	SettingOutputFormat      = "defaults.output_format"
	SettingTemperature       = "defaults.temperature"
	SettingMaxTokens         = "defaults.max_tokens"
	SettingTone              = "defaults.tone"
)

// Setting classifies one configuration value: its canonical name, the
// environment variable that supplies it, its built-in default, and the
// validation rules applied to the resolved value.
type Setting struct {
	// Name is the canonical dot-separated key (e.g. "llm.base_url").
	Name string
	// EnvVar is the environment variable consulted in the environment
	// layer ("" when the setting has no environment variable).
	EnvVar string
	// Default is the built-in default ("": unset, the consuming stage
	// applies its own fallback).
	Default string
	// Secret marks credential settings. Their resolved values are always
	// redacted in effective output and run-manifest snapshots, and their
	// config-file value is an environment reference, not a credential.
	Secret bool

	kind     valueKind
	validate validator
	file     func(File) (string, bool)
}

// valueKind classifies the scalar type of a setting.
type valueKind int

// Setting scalar kinds.
const (
	kindString valueKind = iota
	kindInt
	kindFloat
	kindBool
)

// validator checks a resolved string value. Validators must accept ""
// (unset); presence requirements are enforced by the consuming command.
type validator func(raw string) error

// validateAny accepts any value.
func validateAny(string) error { return nil }

// validateURL accepts only absolute http(s) URLs.
func validateURL(raw string) error {
	if raw == "" {
		return nil
	}
	if !urlRe.MatchString(raw) {
		return fmt.Errorf("%q must be an absolute http(s) URL", raw)
	}
	return nil
}

// urlRe is a conservative absolute http(s) URL check.
var urlRe = regexp.MustCompile(`^https?://[^\s]+$`)

// validateSeconds accepts positive durations up to one day.
func validateSeconds(raw string) error {
	return validateFloatRange(0, 86400, "seconds")(raw)
}

// validateFloatRange accepts floats within [min, max].
func validateFloatRange(min, max float64, unit string) validator {
	return func(raw string) error {
		if raw == "" {
			return nil
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("%q is not a number", raw)
		}
		if v < min || v > max {
			return fmt.Errorf("must be between %g and %g %s, got %s", min, max, unit, raw)
		}
		return nil
	}
}

// validateIntRange accepts integers within [min, max].
func validateIntRange(min, max int) validator {
	return func(raw string) error {
		if raw == "" {
			return nil
		}
		v, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("%q is not an integer", raw)
		}
		if v < min || v > max {
			return fmt.Errorf("must be between %d and %d, got %d", min, max, v)
		}
		return nil
	}
}

// validatePositiveInt accepts integers >= 1.
func validatePositiveInt(raw string) error {
	return validateIntRange(1, 1<<30)(raw)
}

// validateEnum accepts one of the allowed values.
func validateEnum(allowed ...string) validator {
	return func(raw string) error {
		if raw == "" {
			return nil
		}
		for _, a := range allowed {
			if raw == a {
				return nil
			}
		}
		return fmt.Errorf("must be one of %s, got %q", strings.Join(allowed, ", "), raw)
	}
}

// Settings is the canonical, ordered table of every resolvable setting.
//
// The order is the display order of `newsroom config show` and is grouped by
// section. Every environment variable name referenced here is documented in
// the README; none of the built-in defaults contains a host name or a
// credential.
var Settings = []Setting{
	{
		Name:     SettingLLMBaseURL,
		EnvVar:   "OMLX_BASE_URL",
		kind:     kindString,
		validate: validateURL,
		file:     func(f File) (string, bool) { return str(f.LLM.BaseURL) },
	},
	{
		Name:     SettingLLMModel,
		EnvVar:   "OMLX_MODEL",
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return str(f.LLM.Model) },
	},
	{
		Name:     SettingLLMAPIKey,
		EnvVar:   "OMLX_API_KEY",
		Secret:   true,
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return apiKeyEnv(f.LLM.APIKey) },
	},
	{
		Name:     SettingLLMTimeout,
		EnvVar:   "OMLX_TIMEOUT",
		Default:  "120",
		kind:     kindFloat,
		validate: validateSeconds,
		file:     func(f File) (string, bool) { return fnum(f.LLM.Timeout) },
	},
	{
		Name:     SettingSearchBaseURL,
		EnvVar:   "SEARXNG_BASE_URL",
		kind:     kindString,
		validate: validateURL,
		file:     func(f File) (string, bool) { return str(f.Search.BaseURL) },
	},
	{
		Name:     SettingSearchLanguage,
		EnvVar:   "SEARXNG_SEARCH_LANGUAGE",
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return str(f.Search.Language) },
	},
	{
		Name:     SettingSearchTimeRange,
		EnvVar:   "SEARXNG_SEARCH_TIME_RANGE",
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return str(f.Search.TimeRange) },
	},
	{
		Name:     SettingSearchMaxQueries,
		EnvVar:   "SEARXNG_MAX_QUERIES",
		Default:  "5",
		kind:     kindInt,
		validate: validateIntRange(1, 10),
		file:     func(f File) (string, bool) { return inum(f.Search.MaxQueries) },
	},
	{
		Name:     SettingSearchResults,
		EnvVar:   "SEARXNG_RESULTS_PER_QUERY",
		Default:  "10",
		kind:     kindInt,
		validate: validateIntRange(1, 100),
		file:     func(f File) (string, bool) { return inum(f.Search.ResultsPerQuery) },
	},
	{
		Name:     SettingSearchMaxSources,
		EnvVar:   "SEARXNG_MAX_SOURCES",
		Default:  "0",
		kind:     kindInt,
		validate: validateIntRange(0, 100000),
		file:     func(f File) (string, bool) { return inum(f.Search.MaxSources) },
	},
	{
		Name:     SettingSearchAPIKey,
		EnvVar:   "SEARXNG_API_KEY",
		Secret:   true,
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return apiKeyEnv(f.Search.APIKey) },
	},
	{
		Name:     SettingSearchAPIHeader,
		EnvVar:   "SEARXNG_API_HEADER",
		Default:  "X-API-Key",
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return str(f.Search.APIHeader) },
	},
	{
		Name:     SettingEmbeddingBaseURL,
		EnvVar:   "EMBEDDING_BASE_URL",
		Default:  "http://localhost:8000/v1",
		kind:     kindString,
		validate: validateURL,
		file:     func(f File) (string, bool) { return str(f.Embedding.BaseURL) },
	},
	{
		Name:     SettingEmbeddingModel,
		EnvVar:   "EMBEDDING_MODEL",
		Default:  "all-MiniLM-L6-v2",
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return str(f.Embedding.Model) },
	},
	{
		Name:     SettingEmbeddingAPIKey,
		EnvVar:   "EMBEDDING_API_KEY",
		Secret:   true,
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return apiKeyEnv(f.Embedding.APIKey) },
	},
	{
		Name:     SettingEmbeddingTimeout,
		EnvVar:   "EMBEDDING_TIMEOUT",
		Default:  "30",
		kind:     kindFloat,
		validate: validateSeconds,
		file:     func(f File) (string, bool) { return fnum(f.Embedding.Timeout) },
	},
	{
		Name:     SettingRerankEnabled,
		EnvVar:   "RERANK_ENABLED",
		Default:  "false",
		kind:     kindBool,
		validate: validateBool,
		file:     func(f File) (string, bool) { return bstr(f.Rerank.Enabled) },
	},
	{
		Name:     SettingRerankModel,
		EnvVar:   "RERANK_MODEL",
		Default:  "bge-reranker",
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return str(f.Rerank.Model) },
	},
	{
		Name:     SettingArchiveDBPath,
		EnvVar:   "ARCHIVE_DB_PATH",
		Default:  "./archive.db",
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return str(f.Archive.DBPath) },
	},
	{
		Name:     SettingArchiveTopK,
		EnvVar:   "ARCHIVE_TOP_K",
		Default:  "10",
		kind:     kindInt,
		validate: validateIntRange(1, 1000),
		file:     func(f File) (string, bool) { return inum(f.Archive.TopK) },
	},
	{
		Name:     SettingArchiveFormat,
		EnvVar:   "ARCHIVE_FORMAT",
		Default:  "text",
		kind:     kindString,
		validate: validateEnum("text", "json"),
		file:     func(f File) (string, bool) { return str(f.Archive.Format) },
	},
	{
		Name:     SettingFetchTimeout,
		EnvVar:   "FETCH_TIMEOUT",
		Default:  "30",
		kind:     kindFloat,
		validate: validateSeconds,
		file:     func(f File) (string, bool) { return fnum(f.Fetch.Timeout) },
	},
	{
		Name:     SettingFetchMaxSize,
		EnvVar:   "FETCH_MAX_SIZE",
		Default:  "10485760",
		kind:     kindInt,
		validate: validatePositiveInt,
		file:     func(f File) (string, bool) { return inum(f.Fetch.MaxSize) },
	},
	{
		Name:     SettingFetchMaxWords,
		EnvVar:   "FETCH_MAX_WORDS",
		Default:  "50000",
		kind:     kindInt,
		validate: validatePositiveInt,
		file:     func(f File) (string, bool) { return inum(f.Fetch.MaxWords) },
	},
	{
		Name:     SettingFetchParallel,
		EnvVar:   "FETCH_PARALLEL",
		Default:  "5",
		kind:     kindInt,
		validate: validateIntRange(1, 64),
		file:     func(f File) (string, bool) { return inum(f.Fetch.Parallel) },
	},
	{
		Name:     SettingChunkTargetSize,
		EnvVar:   "CHUNK_TARGET_SIZE",
		Default:  "300",
		kind:     kindInt,
		validate: validatePositiveInt,
		file:     func(f File) (string, bool) { return inum(f.Chunk.TargetSize) },
	},
	{
		Name:     SettingChunkMaxSize,
		EnvVar:   "CHUNK_MAX_SIZE",
		Default:  "500",
		kind:     kindInt,
		validate: validatePositiveInt,
		file:     func(f File) (string, bool) { return inum(f.Chunk.MaxSize) },
	},
	{
		Name:     SettingChunkOverlapRatio,
		EnvVar:   "CHUNK_OVERLAP_RATIO",
		Default:  "0.1",
		kind:     kindFloat,
		validate: validateFloatRange(0, 0.99, ""),
		file:     func(f File) (string, bool) { return fnum(f.Chunk.OverlapRatio) },
	},
	{
		Name:     SettingProfilesRoot,
		EnvVar:   "NEWSROOM_PROFILES_ROOT",
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return str(f.Profiles.Root) },
	},
	{
		Name:     SettingProfilesName,
		EnvVar:   "NEWSROOM_PROFILE_NAME",
		Default:  "default",
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return str(f.Profiles.Name) },
	},
	{
		Name:     SettingDefaultsLanguage,
		EnvVar:   "NEWSROOM_LANGUAGE",
		Default:  "en",
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return str(f.Defaults.Language) },
	},
	{
		Name:     SettingOutputFormat,
		EnvVar:   "NEWSROOM_OUTPUT_FORMAT",
		Default:  "article",
		kind:     kindString,
		validate: validateEnum("article", "thread", "hybrid"),
		file:     func(f File) (string, bool) { return str(f.Defaults.OutputFormat) },
	},
	{
		// Built-in default is intentionally unset (""): each stage keeps its
		// own built-in temperature (write 0.3, final-check 0.1, ...) unless
		// the user sets a global preference via flag, env, or config file.
		Name:     SettingTemperature,
		EnvVar:   "NEWSROOM_TEMPERATURE",
		kind:     kindFloat,
		validate: validateFloatRange(0, 2, ""),
		file:     func(f File) (string, bool) { return fnum(f.Defaults.Temperature) },
	},
	{
		// Built-in default is intentionally unset (""): each stage keeps its
		// own built-in token budget (write 20000, final-check 5000, ...)
		// unless the user sets a global preference.
		Name:     SettingMaxTokens,
		EnvVar:   "NEWSROOM_MAX_TOKENS",
		kind:     kindInt,
		validate: validateIntRange(1, 1000000),
		file:     func(f File) (string, bool) { return inum(f.Defaults.MaxTokens) },
	},
	{
		Name:     SettingTone,
		EnvVar:   "NEWSROOM_TONE",
		Default:  "neutral",
		kind:     kindString,
		validate: validateAny,
		file:     func(f File) (string, bool) { return str(f.Defaults.Tone) },
	},
}

// settingIndex maps canonical names to Settings positions.
var settingIndex = func() map[string]int {
	m := make(map[string]int, len(Settings))
	for i := range Settings {
		m[Settings[i].Name] = i
	}
	return m
}()

// apiKeyEnv returns the referenced environment variable name of an APIKey
// leaf, if present.
func apiKeyEnv(a *APIKey) (string, bool) {
	if a == nil || a.Env == "" {
		return "", false
	}
	return a.Env, true
}

// validateBool accepts true/false spellings.
func validateBool(raw string) error {
	if raw == "" {
		return nil
	}
	if _, err := strconv.ParseBool(raw); err != nil {
		return fmt.Errorf("%q is not a boolean (use true or false)", raw)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

// Config is the in-memory configuration for one CLI process: the parsed
// config file (when one was found) plus the environment lookup.
//
// Config is immutable after construction; resolution methods are safe for
// concurrent use.
type Config struct {
	// FilePath is the config file that was loaded, or "" when running on
	// environment variables and built-in defaults only.
	FilePath string
	// File is the parsed config file (zero value when none was found).
	File File
	// Env reads an environment variable. Defaults to os.LookupEnv; tests
	// may substitute a fake.
	Env func(key string) (string, bool)

	fileValues map[string]string
}

// New builds a Config from a loaded file.
//
// A nil file is allowed and yields a defaults-only configuration.
func New(f *File, path string) *Config {
	if f == nil {
		f = &File{}
	}
	c := &Config{
		FilePath:   path,
		File:       *f,
		Env:        os.LookupEnv,
		fileValues: make(map[string]string, len(Settings)),
	}
	for i := range Settings {
		if v, ok := f.fileValue(&Settings[i]); ok {
			c.fileValues[Settings[i].Name] = v
		}
	}
	return c
}

// Override is a CLI flag value together with whether the user provided it.
type Override struct {
	// Value is the flag value as given (may be "").
	Value string
	// Changed reports whether the user explicitly provided the flag.
	Changed bool
}

// FromFlag builds an Override from a (value, changed) flag pair.
func FromFlag(value string, changed bool) Override {
	return Override{Value: value, Changed: changed}
}

// Resolve applies the precedence CLI > environment > config file > built-in
// default for the named setting.
//
// It returns the winning value, a human-readable source label ("cli",
// "env:NAME", "config", "config (env:NAME)", "default", or "unset"), and an
// error when the winning value fails validation.
//
// An explicitly empty CLI value is treated as "not provided" and falls
// through to the next layer.
func (c *Config) Resolve(name string, ov Override) (string, string, error) {
	i, ok := settingIndex[name]
	if !ok {
		return "", "", fmt.Errorf("unknown setting %q", name)
	}
	s := &Settings[i]

	if ov.Changed && ov.Value != "" {
		if err := s.validateResolved(ov.Value); err != nil {
			return "", "cli", fmt.Errorf("setting %s: %w", s.Name, err)
		}
		return ov.Value, "cli", nil
	}
	if s.EnvVar != "" {
		if v, ok := c.Env(s.EnvVar); ok && v != "" {
			if err := s.validateResolved(v); err != nil {
				return "", "env:" + s.EnvVar, fmt.Errorf("setting %s: %w", s.Name, err)
			}
			return v, "env:" + s.EnvVar, nil
		}
	}
	if v, ok := c.fileValues[name]; ok && v != "" {
		if s.Secret {
			// The file value is the name of an environment variable to
			// consult; a missing referenced variable falls through.
			if rv, ok := c.Env(v); ok && rv != "" {
				return rv, "config (env:" + v + ")", nil
			}
		} else {
			if err := s.validateResolved(v); err != nil {
				return "", "config", fmt.Errorf("setting %s: %w", s.Name, err)
			}
			return v, "config", nil
		}
	}
	if s.Default == "" {
		return "", "unset", nil
	}
	if err := s.validateResolved(s.Default); err != nil {
		return "", "default", fmt.Errorf("setting %s: %w", s.Name, err)
	}
	return s.Default, "default", nil
}

// validateResolved validates a resolved string against the setting's kind
// and range/enum rules.
func (s *Setting) validateResolved(raw string) error {
	if s.validate != nil {
		if err := s.validate(raw); err != nil {
			return err
		}
	}
	switch s.kind {
	case kindInt:
		if _, err := strconv.Atoi(raw); err != nil {
			return fmt.Errorf("%q is not an integer", raw)
		}
	case kindFloat:
		if _, err := strconv.ParseFloat(raw, 64); err != nil {
			return fmt.Errorf("%q is not a number", raw)
		}
	case kindBool:
		if _, err := strconv.ParseBool(raw); err != nil {
			return fmt.Errorf("%q is not a boolean", raw)
		}
	}
	return nil
}

// Names returns the canonical setting names in display order.
func (c *Config) Names() []string {
	names := make([]string, len(Settings))
	for i := range Settings {
		names[i] = Settings[i].Name
	}
	return names
}

// GetString resolves a string setting.
//
// ok is false when the setting is unset in every layer (the returned value
// is "").
func (c *Config) GetString(name string, ov Override) (string, bool, error) {
	v, _, err := c.Resolve(name, ov)
	return v, v != "", err
}

// GetInt resolves an integer setting.
//
// ok is false when the setting is unset in every layer.
func (c *Config) GetInt(name string, ov Override) (int, bool, error) {
	v, _, err := c.Resolve(name, ov)
	if err != nil {
		return 0, false, err
	}
	if v == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(v)
	return n, true, err
}

// GetFloat resolves a float setting.
//
// ok is false when the setting is unset in every layer.
func (c *Config) GetFloat(name string, ov Override) (float64, bool, error) {
	v, _, err := c.Resolve(name, ov)
	if err != nil {
		return 0, false, err
	}
	if v == "" {
		return 0, false, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	return f, true, err
}

// GetBool resolves a boolean setting.
//
// ok is false when the setting is unset in every layer.
func (c *Config) GetBool(name string, ov Override) (bool, bool, error) {
	v, _, err := c.Resolve(name, ov)
	if err != nil {
		return false, false, err
	}
	if v == "" {
		return false, false, nil
	}
	b, err := strconv.ParseBool(v)
	return b, true, err
}

// ---------------------------------------------------------------------------
// Effective output and snapshots
// ---------------------------------------------------------------------------

// EffectiveEntry is one resolved setting for display.
type EffectiveEntry struct {
	// Key is the canonical setting name.
	Key string `json:"key"`
	// Value is the resolved value; always redacted for secret-like content.
	Value string `json:"value"`
	// Source names the precedence layer that won ("cli", "env:NAME",
	// "config", "config (env:NAME)", "default", or "unset").
	Source string `json:"source"`
}

// Effective resolves every known setting with the given per-setting CLI
// overrides and returns display-safe entries in a stable order.
//
// Security: secret settings always render as run.RedactedPlaceholder when a
// value was resolved, and every non-secret value is passed through
// run.RedactSecrets as defence in depth, so this output is safe for debug
// logging, terminals, and manifest snapshots.
func (c *Config) Effective(overrides map[string]Override) []EffectiveEntry {
	entries := make([]EffectiveEntry, 0, len(Settings))
	for i := range Settings {
		s := &Settings[i]
		v, source, err := c.Resolve(s.Name, overrides[s.Name])
		if err != nil {
			entries = append(entries, EffectiveEntry{Key: s.Name, Value: "invalid: " + err.Error(), Source: source})
			continue
		}
		display := ""
		if v != "" {
			if s.Secret {
				display = run.RedactedPlaceholder
			} else {
				display = run.RedactSecrets(v)
			}
		}
		entries = append(entries, EffectiveEntry{Key: s.Name, Value: display, Source: source})
	}
	return entries
}

// Snapshot renders the resolved configuration (with the given per-setting CLI
// overrides) as a run.ConfigSnapshot for run-manifest records.
//
// All secret-like values are redacted by the run package, and the snapshot
// is validated before being returned, so the result is guaranteed to be
// credential-free.
func (c *Config) Snapshot(overrides map[string]Override) (*run.ConfigSnapshot, error) {
	snap := run.ConfigSnapshot{}
	for i := range Settings {
		s := &Settings[i]
		v, _, err := c.Resolve(s.Name, overrides[s.Name])
		if err != nil {
			return nil, fmt.Errorf("setting %s: %w", s.Name, err)
		}
		if err := snap.Set(s.Name, v); err != nil {
			return nil, fmt.Errorf("setting %s: %w", s.Name, err)
		}
	}
	if err := snap.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config snapshot: %w", err)
	}
	return &snap, nil
}

// EffectiveJSON renders the effective configuration as an indented JSON
// document with all secrets redacted. Safe for debug output and CI logs.
func (c *Config) EffectiveJSON(overrides map[string]Override) ([]byte, error) {
	type report struct {
		ConfigFile string           `json:"config_file"`
		Entries    []EffectiveEntry `json:"entries"`
	}
	return json.MarshalIndent(report{ConfigFile: c.FilePath, Entries: c.Effective(overrides)}, "", "  ")
}

// TextReport renders the effective configuration as an indented, sectioned,
// human-readable report with all secrets redacted.
func (c *Config) TextReport(overrides map[string]Override) string {
	var b strings.Builder
	if c.FilePath != "" {
		fmt.Fprintf(&b, "Config file: %s\n", c.FilePath)
	} else {
		b.WriteString("Config file: (none; environment variables and built-in defaults)\n")
	}

	currentSection := ""
	for _, e := range c.Effective(overrides) {
		section := strings.SplitN(e.Key, ".", 2)[0]
		if section != currentSection {
			currentSection = section
			fmt.Fprintf(&b, "\n  %s\n", section)
		}
		display := e.Value
		if display == "" {
			display = "(unset)"
		}
		fmt.Fprintf(&b, "    %-22s %-44s (%s)\n", e.Key, display, e.Source)
	}
	return b.String()
}
