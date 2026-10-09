// Package config implements persistent configuration for the newsroom CLI
// (v0.5).
//
// # Precedence
//
// Every setting resolves with a single deterministic precedence chain:
//
//	CLI flags  >  environment variables  >  config file  >  built-in defaults
//
// A CLI flag only participates when the user explicitly provided it (an
// explicitly empty flag value is treated as "not provided"). Environment
// variables are only consulted when the corresponding variable is set to a
// non-empty value. Config file fields that are absent fall through; fields
// that are present (including explicit zero values) are honored.
//
// # Config file
//
// The config file is a small YAML document with one section per service:
//
//	llm, search, embedding, rerank, archive, fetch, chunk, profiles, defaults
//
// Lookup order when no --config flag is given:
//
//  1. $NEWSROOM_CONFIG (when set; an existing file is required)
//  2. ./.newsroom.yaml and ./.newsroom.yml (current directory)
//  3. $XDG_CONFIG_HOME/newsroom/config.yaml (or ~/.config/newsroom/config.yaml)
//  4. ~/.newsroom.yaml
//
// The first existing file wins; when no file exists the CLI runs on built-in
// defaults and environment variables only.
//
// # Secrets
//
// Credentials are never stored in the config file. Secret fields (api_key)
// only accept an environment reference, e.g. `api_key: {env: OMLX_API_KEY}`;
// a literal secret is a load-time error. Any key whose name looks like a
// credential must use the same env-reference form. Resolved/effective output
// (newsroom config show, run-manifest snapshots) always redacts secret
// values using the internal/run redaction rules.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the config file schema version understood by this release.
const SchemaVersion = 1

// EnvConfigPath names the environment variable that points at a specific
// config file. It is checked first during default lookup.
const EnvConfigPath = "NEWSROOM_CONFIG"

// EnvXDGConfigHome is the XDG base directory for user configuration.
const EnvXDGConfigHome = "XDG_CONFIG_HOME"

// Errors returned by config loading.
var (
	// ErrConfigNotFound indicates that an explicitly requested config file
	// (the --config flag or $NEWSROOM_CONFIG) does not exist.
	ErrConfigNotFound = errors.New("config file not found")

	// ErrSecretInConfig indicates that a credential was stored literally in
	// the config file. Secrets must come from environment variables.
	ErrSecretInConfig = errors.New("secrets must not be stored in the config file")
)

// ---------------------------------------------------------------------------
// File schema
// ---------------------------------------------------------------------------

// File is the on-disk YAML configuration schema.
//
// Every leaf field is a pointer so that "absent" can be distinguished from an
// explicit zero value: an absent field falls through to the next precedence
// layer, while an explicit value (even 0) is honored.
type File struct {
	// SchemaVersion is optional; when set it must equal SchemaVersion.
	SchemaVersion int `yaml:"schema_version"`

	// LLM configures the OpenAI-compatible LLM endpoint (oMLX and friends).
	LLM LLMSection `yaml:"llm"`
	// Search configures the SearXNG discovery endpoint.
	Search SearchSection `yaml:"search"`
	// Embedding configures the embedding endpoint used by the archive.
	Embedding EmbeddingSection `yaml:"embedding"`
	// Rerank configures optional search reranking.
	Rerank RerankSection `yaml:"rerank"`
	// Archive configures the local archive database.
	Archive ArchiveSection `yaml:"archive"`
	// Fetch configures HTTP fetching (research and archive add).
	Fetch FetchSection `yaml:"fetch"`
	// Chunk configures document chunking (archive).
	Chunk ChunkSection `yaml:"chunk"`
	// Profiles configures the editorial profile directory.
	Profiles ProfilesSection `yaml:"profiles"`
	// Defaults holds global fallbacks for generation parameters shared by
	// the write, verify, final-check, and research stages.
	Defaults DefaultsSection `yaml:"defaults"`
}

// LLMSection configures the LLM endpoint.
type LLMSection struct {
	// BaseURL is the API base URL (e.g. "http://mac-studio:8000/v1").
	BaseURL *string `yaml:"base_url"`
	// Model is the model name (e.g. "llama3.1:8b").
	Model *string `yaml:"model"`
	// APIKey references the environment variable holding the key.
	APIKey *APIKey `yaml:"api_key"`
	// Timeout is the request timeout in seconds.
	Timeout *float64 `yaml:"timeout"`
}

// SearchSection configures SearXNG discovery.
type SearchSection struct {
	// BaseURL is the SearXNG instance URL (e.g. "http://raspberrypi:8080").
	BaseURL *string `yaml:"base_url"`
	// Language is the preferred result language (RFC 5646 tag).
	Language *string `yaml:"language"`
	// TimeRange is the SearXNG time range (e.g. "last_week").
	TimeRange *string `yaml:"time_range"`
	// MaxQueries bounds the number of search queries (1-10).
	MaxQueries *int `yaml:"max_queries"`
	// ResultsPerQuery bounds results consumed per query (1-100).
	ResultsPerQuery *int `yaml:"results_per_query"`
	// MaxSources bounds returned candidates (0 = unlimited).
	MaxSources *int `yaml:"max_sources"`
	// APIKey references the environment variable holding the key.
	APIKey *APIKey `yaml:"api_key"`
	// APIHeader is the header name used to send the API key.
	APIHeader *string `yaml:"api_header"`
}

// EmbeddingSection configures the embedding endpoint.
type EmbeddingSection struct {
	// BaseURL is the embedding API base URL.
	BaseURL *string `yaml:"base_url"`
	// Model is the embedding model name.
	Model *string `yaml:"model"`
	// APIKey references the environment variable holding the key.
	APIKey *APIKey `yaml:"api_key"`
	// Timeout is the request timeout in seconds.
	Timeout *float64 `yaml:"timeout"`
}

// RerankSection configures optional reranking.
type RerankSection struct {
	// Enabled turns reranking on.
	Enabled *bool `yaml:"enabled"`
	// Model is the reranker model name.
	Model *string `yaml:"model"`
}

// ArchiveSection configures the local archive.
type ArchiveSection struct {
	// DBPath is the archive SQLite database path.
	DBPath *string `yaml:"db_path"`
	// TopK is the default number of search results.
	TopK *int `yaml:"top_k"`
	// Format is the archive output format ("text" or "json").
	Format *string `yaml:"format"`
}

// FetchSection configures HTTP fetching.
type FetchSection struct {
	// Timeout is the fetch timeout in seconds.
	Timeout *float64 `yaml:"timeout"`
	// MaxSize is the maximum response size in bytes.
	MaxSize *int `yaml:"max_size"`
	// MaxWords is the maximum words per document.
	MaxWords *int `yaml:"max_words"`
	// Parallel is the number of parallel fetch operations (1-64).
	Parallel *int `yaml:"parallel"`
}

// ChunkSection configures document chunking.
type ChunkSection struct {
	// TargetSize is the target chunk size in characters.
	TargetSize *int `yaml:"target_size"`
	// MaxSize is the maximum chunk size in characters.
	MaxSize *int `yaml:"max_size"`
	// OverlapRatio is the fraction of chunk overlap (0.0-0.99).
	OverlapRatio *float64 `yaml:"overlap_ratio"`
}

// ProfilesSection configures the editorial profile directory.
type ProfilesSection struct {
	// Root is the profile root directory (e.g. "./profiles").
	Root *string `yaml:"root"`
	// Name is the profile name within the root directory.
	Name *string `yaml:"name"`
}

// DefaultsSection holds global fallbacks for generation parameters.
//
// A value here overrides every stage's built-in default (but never an
// explicit CLI flag or environment variable), e.g. `temperature: 0.5` is
// used by write, verify, final-check, and research alike.
type DefaultsSection struct {
	// Language is the output language (e.g. "en", "es").
	Language *string `yaml:"language"`
	// OutputFormat is the default write format ("article", "thread", "hybrid").
	OutputFormat *string `yaml:"output_format"`
	// Temperature is the default generation temperature (0.0-2.0).
	Temperature *float64 `yaml:"temperature"`
	// MaxTokens is the default maximum output tokens.
	MaxTokens *int `yaml:"max_tokens"`
	// Tone is the default writing tone.
	Tone *string `yaml:"tone"`
}

// APIKey is a credential reference for a secret setting.
//
// Secrets are never stored in the config file: the mapping may hold exactly
// one key, `env`, naming the environment variable that supplies the value at
// runtime. A literal string is rejected with ErrSecretInConfig.
type APIKey struct {
	// Env is the environment variable name supplying the credential.
	Env string `yaml:"env"`
}

// envNameRe validates environment variable names.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// UnmarshalYAML enforces the env-reference-only rule for secret fields.
func (a *APIKey) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.MappingNode:
		keys := make([]string, 0, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			keys = append(keys, node.Content[i].Value)
		}
		if len(keys) != 1 || keys[0] != "env" {
			return fmt.Errorf("%w: api_key must be exactly {env: NAME}, got %v", ErrSecretInConfig, keys)
		}
		var ref struct {
			Env string `yaml:"env"`
		}
		if err := node.Decode(&ref); err != nil {
			return fmt.Errorf("api_key: %w", err)
		}
		if !envNameRe.MatchString(ref.Env) {
			return fmt.Errorf("%w: invalid environment variable name %q in api_key (want e.g. OMLX_API_KEY)", ErrSecretInConfig, ref.Env)
		}
		a.Env = ref.Env
		return nil
	case yaml.ScalarNode:
		return fmt.Errorf("%w: api_key must reference an environment variable (e.g. `env: OMLX_API_KEY`), a literal secret is not allowed", ErrSecretInConfig)
	default:
		return fmt.Errorf("%w: api_key must be `env: NAME`", ErrSecretInConfig)
	}
}

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------

// Load reads, parses, and validates the config file at path.
//
// A missing file returns ErrConfigNotFound (wrapped); a file that exists but
// fails to parse or validate returns a descriptive error.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrConfigNotFound, path)
		}
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}
	return LoadBytes(data, path)
}

// LoadBytes parses and validates a config document.
//
// source names the document in error messages (a file path or "<bytes>").
func LoadBytes(data []byte, source string) (*File, error) {
	name := source
	if name == "" {
		name = "<bytes>"
	}

	// Keep the raw document for the secret-key walk below.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid config %s: YAML syntax error: %w", name, err)
	}

	var f File
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // reject unknown keys at any level (typos, stale sections)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("invalid config %s: %w", name, err)
	}

	if f.SchemaVersion != 0 && f.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("invalid config %s: unsupported schema_version %d (this release understands %d)", name, f.SchemaVersion, SchemaVersion)
	}

	if err := walkSecretKeys(&doc, ""); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", name, err)
	}
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", name, err)
	}
	return &f, nil
}

// walkSecretKeys walks the raw document and rejects any key whose name looks
// like a credential unless its value is an env-reference mapping. This
// catches secret keys that are not part of the declared schema as well as
// literal secrets in fields that are.
func walkSecretKeys(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			val := node.Content[i+1]
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if looksLikeSecretKey(key) && !isEnvReference(val) {
				return fmt.Errorf("%w: %q must reference an environment variable (e.g. `env: OMLX_API_KEY`)", ErrSecretInConfig, childPath)
			}
			if err := walkSecretKeys(val, childPath); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, item := range node.Content {
			if err := walkSecretKeys(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// looksLikeSecretKey reports whether a YAML key name may hold a credential.
// Separators ('_', '-', '.') and camelCase boundaries are treated as word
// boundaries so that "api_key", "api-key", "apiKey" and "api key" all match.
func looksLikeSecretKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		switch r {
		case '_', '-', '.':
			return ' '
		default:
			return r
		}
	}, key)
	normalized = camelCaseRe.ReplaceAllString(normalized, "$1 $2")
	return secretNameRe.MatchString(normalized)
}

// isEnvReference reports whether node is a mapping of exactly one `env` key
// with a scalar value.
func isEnvReference(node *yaml.Node) bool {
	if node.Kind != yaml.MappingNode || len(node.Content) != 2 {
		return false
	}
	return node.Content[0].Kind == yaml.ScalarNode && node.Content[0].Value == "env" && node.Content[1].Kind == yaml.ScalarNode
}

// secretNameRe matches credential-like key names in their normalized form.
var secretNameRe = regexp.MustCompile(`(?i)\b(api ?keys?|access ?keys?|auth ?tokens?|tokens?|secrets?|passwords?|passwds?|credentials?|private ?keys?|auths?)\b`)

// camelCaseRe splits camelCase boundaries ("apiKey" -> "api Key").
var camelCaseRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// Validate checks every value present in the file.
//
// Absent values are skipped; every present value must satisfy its setting's
// range/enum/URL rules.
func (f File) Validate() error {
	var errs []error
	for i := range Settings {
		s := &Settings[i]
		v, ok := f.fileValue(s)
		if !ok || v == "" {
			continue
		}
		if s.validate == nil {
			continue
		}
		if err := s.validate(v); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
		}
	}
	return errors.Join(errs...)
}

// fileValue returns the raw string form of setting s from the file, if set.
//
// For secret settings the returned string is the referenced environment
// variable name, never a credential.
func (f File) fileValue(s *Setting) (string, bool) {
	if s.file == nil {
		return "", false
	}
	v, ok := s.file(f)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// ---------------------------------------------------------------------------
// Default lookup
// ---------------------------------------------------------------------------

// DefaultLocations returns the config file lookup order used when no
// --config flag is given.
func DefaultLocations() []string {
	var locations []string
	if p := os.Getenv(EnvConfigPath); p != "" {
		locations = append(locations, p)
	}
	locations = append(locations, "./.newsroom.yaml", "./.newsroom.yml")
	if home, err := os.UserHomeDir(); err == nil {
		xdg := os.Getenv(EnvXDGConfigHome)
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		locations = append(locations,
			filepath.Join(xdg, "newsroom", "config.yaml"),
			filepath.Join(home, ".newsroom.yaml"),
		)
	}
	return locations
}

// FindAndLoad walks the default lookup order and returns the first existing
// file together with its path.
//
// When no file exists it returns an empty File, an empty path, and a nil
// error: the caller then operates on environment variables and built-in
// defaults only. A missing file named by $NEWSROOM_CONFIG is an error,
// because that variable is an explicit pointer.
func FindAndLoad() (*File, string, error) {
	explicit := os.Getenv(EnvConfigPath) != ""
	for i, loc := range DefaultLocations() {
		info, err := os.Stat(loc)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if i == 0 && explicit {
					return nil, "", fmt.Errorf("%w: %s (set by $%s)", ErrConfigNotFound, loc, EnvConfigPath)
				}
				continue
			}
			return nil, "", fmt.Errorf("accessing config file %s: %w", loc, err)
		}
		if info.IsDir() {
			return nil, "", fmt.Errorf("%w: %s is a directory", ErrConfigNotFound, loc)
		}
		f, err := Load(loc)
		if err != nil {
			return nil, "", err
		}
		return f, loc, nil
	}
	return &File{}, "", nil
}

// ---------------------------------------------------------------------------
// Typed scalar helpers (pointer leaf -> string)
// ---------------------------------------------------------------------------

// str converts a *string leaf to its string form.
func str(p *string) (string, bool) {
	if p == nil {
		return "", false
	}
	return *p, true
}

// inum converts an *int leaf to its string form.
func inum(p *int) (string, bool) {
	if p == nil {
		return "", false
	}
	return strconv.Itoa(*p), true
}

// fnum converts a *float64 leaf to its shortest string form.
func fnum(p *float64) (string, bool) {
	if p == nil {
		return "", false
	}
	return strconv.FormatFloat(*p, 'f', -1, 64), true
}

// bstr converts a *bool leaf to its string form.
func bstr(p *bool) (string, bool) {
	if p == nil {
		return "", false
	}
	return strconv.FormatBool(*p), true
}
