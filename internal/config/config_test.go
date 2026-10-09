package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const exampleFile = `
schema_version: 1
llm:
  base_url: http://mac-studio:8000/v1
  model: llama3.1:8b
  timeout: 60
search:
  base_url: http://raspberrypi:8080
  language: es
  max_queries: 3
archive:
  db_path: /tmp/newsroom-test/archive.db
  top_k: 25
profiles:
  root: ./profiles
  name: Estilo A
defaults:
  language: es
  output_format: thread
  temperature: 0.5
`

func TestLoadFileOnly(t *testing.T) {
	f, err := LoadBytes([]byte(exampleFile), "test")
	require.NoError(t, err)
	cfg := New(f, "test")
	cfg.Env = fakeEnv(nil) // hermetic: ignore the ambient environment

	tests := []struct {
		name   string
		want   string
		source string
	}{
		{"llm.base_url", "http://mac-studio:8000/v1", "config"},
		{"llm.model", "llama3.1:8b", "config"},
		{"llm.timeout", "60", "config"},
		{"search.base_url", "http://raspberrypi:8080", "config"},
		{"search.language", "es", "config"},
		{"search.max_queries", "3", "config"},
		{"archive.db_path", "/tmp/newsroom-test/archive.db", "config"},
		{"archive.top_k", "25", "config"},
		{"profiles.root", "./profiles", "config"},
		{"profiles.name", "Estilo A", "config"},
		{"defaults.language", "es", "config"},
		{"defaults.output_format", "thread", "config"},
		{"defaults.temperature", "0.5", "config"},
		// Unset in the file: built-in defaults apply.
		{"archive.format", "text", "default"},
		{"search.api_header", "X-API-Key", "default"},
		{"fetch.timeout", "30", "default"},
		// Unset in the file with no built-in: unset.
		{"llm.api_key", "", "unset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, source, err := cfg.Resolve(tt.name, Override{})
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.source, source)
		})
	}
}

func TestEnvOverridesConfig(t *testing.T) {
	f, err := LoadBytes([]byte(exampleFile), "test")
	require.NoError(t, err)
	cfg := New(f, "test")
	cfg.Env = fakeEnv(map[string]string{
		"OMLX_MODEL":              "env-model",
		"OMLX_API_KEY":            "sk-env-secret-abcdefghijklmnopqrstuvwxyz123",
		"SEARXNG_SEARCH_LANGUAGE": "fr",
		"CHUNK_OVERLAP_RATIO":     "0.3",
	})

	tests := []struct {
		name   string
		want   string
		source string
	}{
		// Environment beats file.
		{"llm.model", "env-model", "env:OMLX_MODEL"},
		{"llm.api_key", "sk-env-secret-abcdefghijklmnopqrstuvwxyz123", "env:OMLX_API_KEY"},
		{"search.language", "fr", "env:SEARXNG_SEARCH_LANGUAGE"},
		// File still wins where env is absent.
		{"llm.base_url", "http://mac-studio:8000/v1", "config"},
		// Environment beats built-in default.
		{"chunk.overlap_ratio", "0.3", "env:CHUNK_OVERLAP_RATIO"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, source, err := cfg.Resolve(tt.name, Override{})
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.source, source)
		})
	}
}

func TestCLIOverridesEnv(t *testing.T) {
	f, err := LoadBytes([]byte(exampleFile), "test")
	require.NoError(t, err)
	cfg := New(f, "test")
	cfg.Env = fakeEnv(map[string]string{"OMLX_MODEL": "env-model"})

	// CLI beats environment, which beats file, which beats default.
	got, source, err := cfg.Resolve(SettingLLMModel, Override{Value: "cli-model", Changed: true})
	require.NoError(t, err)
	assert.Equal(t, "cli-model", got)
	assert.Equal(t, "cli", source)

	// An explicitly empty CLI value is "not provided" and falls through
	// to the environment layer.
	got, source, err = cfg.Resolve(SettingLLMModel, Override{Value: "", Changed: true})
	require.NoError(t, err)
	assert.Equal(t, "env-model", got)
	assert.Equal(t, "env:OMLX_MODEL", source)

	// Unchanged flag values never participate, even when non-empty.
	got, source, err = cfg.Resolve(SettingLLMModel, Override{Value: "flag-default", Changed: false})
	require.NoError(t, err)
	assert.Equal(t, "env-model", got)
	assert.Equal(t, "env:OMLX_MODEL", source)
}

func TestFullPrecedenceChain(t *testing.T) {
	f, err := LoadBytes([]byte("llm:\n  model: file-model\n"), "test")
	require.NoError(t, err)
	cfg := New(f, "test")
	cfg.Env = fakeEnv(map[string]string{"OMLX_MODEL": "env-model"})

	tests := []struct {
		name string
		ov   Override
		want string
	}{
		{"all layers", Override{Value: "cli-model", Changed: true}, "cli-model"},
		{"env + file", Override{}, "env-model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := cfg.Resolve(SettingLLMModel, tt.ov)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
	// File layer beats the built-in default when no env var is present.
	cfg.Env = fakeEnv(nil)
	got, _, err := cfg.Resolve(SettingLLMModel, Override{Value: "", Changed: true})
	require.NoError(t, err)
	assert.Equal(t, "file-model", got)

	// No file, no env, no CLI: built-in default (none for llm.model).
	cfg2 := New(&File{}, "")
	cfg2.Env = fakeEnv(nil)
	got, source, err := cfg2.Resolve(SettingLLMModel, Override{})
	require.NoError(t, err)
	assert.Equal(t, "", got)
	assert.Equal(t, "unset", source)
}

func TestMissingConfig(t *testing.T) {
	// Load: explicit missing path.
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	assert.ErrorIs(t, err, ErrConfigNotFound)

	// FindAndLoad with an empty environment: no file, no error.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(EnvConfigPath, "")
	cfg, path, err := FindAndLoad()
	require.NoError(t, err)
	assert.Equal(t, "", path)
	assert.NotNil(t, cfg)

	// A defaults-only Config resolves built-ins only.
	runCfg := New(cfg, path)
	runCfg.Env = fakeEnv(nil) // hermetic
	got, source, err := runCfg.Resolve(SettingArchiveDBPath, Override{})
	require.NoError(t, err)
	assert.Equal(t, "./archive.db", got)
	assert.Equal(t, "default", source)
	got, source, err = runCfg.Resolve(SettingLLMBaseURL, Override{})
	require.NoError(t, err)
	assert.Equal(t, "", got)
	assert.Equal(t, "unset", source)

	// An explicitly named missing file ($NEWSROOM_CONFIG) is an error.
	t.Setenv(EnvConfigPath, filepath.Join(home, "missing.yaml"))
	_, _, err = FindAndLoad()
	assert.ErrorIs(t, err, ErrConfigNotFound)
}

func TestFindAndLoadLocations(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg-config")
	require.NoError(t, os.MkdirAll(filepath.Join(xdg, "newsroom"), 0o755))
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv(EnvConfigPath, "")

	// Nothing yet: no file.
	_, path, err := FindAndLoad()
	require.NoError(t, err)
	assert.Equal(t, "", path)

	// Home file is found.
	require.NoError(t, os.MkdirAll(filepath.Join(home, "sub"), 0o755))
	homeFile := filepath.Join(home, ".newsroom.yaml")
	require.NoError(t, os.WriteFile(homeFile, []byte("llm:\n  model: home-model\n"), 0o644))
	cfg, path, err := FindAndLoad()
	require.NoError(t, err)
	assert.Equal(t, homeFile, path)
	rcfg := New(cfg, path)
	rcfg.Env = fakeEnv(nil) // hermetic
	got, _, err := rcfg.Resolve(SettingLLMModel, Override{})
	require.NoError(t, err)
	assert.Equal(t, "home-model", got)
	require.NoError(t, os.Remove(homeFile))

	// XDG file is found and beats home.
	xdgFile := filepath.Join(xdg, "newsroom", "config.yaml")
	require.NoError(t, os.WriteFile(xdgFile, []byte("llm:\n  model: xdg-model\n"), 0o644))
	require.NoError(t, os.WriteFile(homeFile, []byte("llm:\n  model: home-model\n"), 0o644))
	cfg, path, err = FindAndLoad()
	require.NoError(t, err)
	assert.Equal(t, xdgFile, path)
	rcfg = New(cfg, path)
	rcfg.Env = fakeEnv(nil) // hermetic
	got, _, err = rcfg.Resolve(SettingLLMModel, Override{})
	require.NoError(t, err)
	assert.Equal(t, "xdg-model", got)
	require.NoError(t, os.Remove(homeFile))

	// $NEWSROOM_CONFIG wins over everything.
	other := filepath.Join(home, "other.yaml")
	require.NoError(t, os.WriteFile(other, []byte("llm:\n  model: explicit-model\n"), 0o644))
	t.Setenv(EnvConfigPath, other)
	cfg, path, err = FindAndLoad()
	require.NoError(t, err)
	assert.Equal(t, other, path)
	rcfg = New(cfg, path)
	rcfg.Env = fakeEnv(nil) // hermetic
	got, _, err = rcfg.Resolve(SettingLLMModel, Override{})
	require.NoError(t, err)
	assert.Equal(t, "explicit-model", got)
}

func TestMalformedConfig(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantSub string
	}{
		{
			name:    "yaml syntax error",
			content: "llm: [unclosed",
			wantSub: "YAML syntax error",
		},
		{
			name:    "unknown top-level key",
			content: "lml:\n  model: x\n",
			wantSub: "not found",
		},
		{
			name:    "unknown section key",
			content: "llm:\n  baseurl: http://x\n",
			wantSub: "not found",
		},
		{
			name:    "wrong type",
			content: "llm:\n  timeout: 60\n  model: [not, a, string]\n",
			wantSub: "", // decode error message is implementation-defined
		},
		{
			name:    "unsupported schema version",
			content: "schema_version: 99\n",
			wantSub: "unsupported schema_version",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadBytes([]byte(tc.content), "test")
			require.Error(t, err)
			if tc.wantSub != "" {
				assert.Contains(t, err.Error(), tc.wantSub)
			}
			assert.NotContains(t, err.Error(), "supersecret")
		})
	}

	// An empty file is valid and means "all defaults".
	f, err := LoadBytes([]byte(""), "test")
	require.NoError(t, err)
	assert.Equal(t, 0, f.SchemaVersion)
	assert.NoError(t, f.Validate())
}

func TestInvalidValues(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantSub string
	}{
		{"negative timeout", "llm:\n  timeout: -5\n", "between"},
		{"timeout not a number", "llm:\n  timeout: fast\n", "cannot unmarshal"},
		{"zero max_queries", "search:\n  max_queries: 0\n", "between 1 and 10"},
		{"huge results per query", "search:\n  results_per_query: 999\n", "between 1 and 100"},
		{"bad URL", "llm:\n  base_url: not-a-url\n", "http(s) URL"},
		{"bad overlap ratio", "chunk:\n  overlap_ratio: 1.5\n", "between 0 and 0.99"},
		{"bad archive format", "archive:\n  format: html\n", "one of text, json"},
		{"bad output format", "defaults:\n  output_format: blog\n", "one of article, thread, hybrid"},
		{"temperature above 2", "defaults:\n  temperature: 2.5\n", "between 0 and 2"},
		{"bool not a bool", "rerank:\n  enabled: maybe\n", "cannot unmarshal"},
		{"parallel out of range", "fetch:\n  parallel: 256\n", "between 1 and 64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadBytes([]byte(tc.content), "test")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSub)
		})
	}
}

func TestSecretsMustBeEnvReferences(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"literal api_key string", "llm:\n  api_key: supersecretkey123\n"},
		{"literal api_key mapping with extra key", "llm:\n  api_key:\n      env: OMLX_API_KEY\n      mode: static\n"},
		{"literal api_key empty mapping", "llm:\n  api_key: {}\n"},
		{"search api_key literal", "search:\n  api_key: supersecretsearxng\n"},
		{"invalid env name", "llm:\n  api_key:\n      env: not a valid name\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadBytes([]byte(tc.content), "test")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSecretInConfig)
			// The raw secret must never be echoed back.
			assert.NotContains(t, err.Error(), "supersecret")
		})
	}

	// An undeclared credential-like key is rejected as an unknown field
	// (strict decoding) — it is never accepted, and its literal value is
	// never echoed.
	_, err := LoadBytes([]byte("llm:\n  token: supersecrettoken\n"), "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token")
	assert.NotContains(t, err.Error(), "supersecrettoken")

	// The env-reference form is accepted.
	f, err := LoadBytes([]byte("llm:\n  api_key:\n    env: OMLX_KEY_ALT\n  model: llama\n"), "test")
	require.NoError(t, err)
	assert.Equal(t, "OMLX_KEY_ALT", f.LLM.APIKey.Env)

	// A resolved secret honors the referenced variable (and only it).
	cfg := New(f, "test")
	cfg.Env = fakeEnv(map[string]string{"OMLX_KEY_ALT": "sk-referenced-secret-abcdefghijklmnopqrstuvwxyz123"})
	got, source, err := cfg.Resolve(SettingLLMAPIKey, Override{})
	require.NoError(t, err)
	assert.Equal(t, "sk-referenced-secret-abcdefghijklmnopqrstuvwxyz123", got)
	assert.Equal(t, "config (env:OMLX_KEY_ALT)", source)

	// The primary environment variable still wins over the file reference.
	cfg.Env = fakeEnv(map[string]string{
		"OMLX_API_KEY": "sk-primary-secret-abcdefghijklmnopqrstuvwxyz123",
		"OMLX_KEY_ALT": "sk-referenced-secret-abcdefghijklmnopqrstuvwxyz123",
	})
	got, source, err = cfg.Resolve(SettingLLMAPIKey, Override{})
	require.NoError(t, err)
	assert.Equal(t, "sk-primary-secret-abcdefghijklmnopqrstuvwxyz123", got)
	assert.Equal(t, "env:OMLX_API_KEY", source)
}

func TestSecretRedactionEffective(t *testing.T) {
	const secret = "sk-super-secret-abcdefghijklmnopqrstuvwxyz-1234567890"
	cfg := New(&File{}, "")
	cfg.Env = fakeEnv(map[string]string{
		"OMLX_API_KEY":      secret,
		"SEARXNG_API_KEY":   secret,
		"EMBEDDING_API_KEY": secret,
		"OMLX_BASE_URL":     "http://user:" + secret + "@mac-studio:8000/v1",
	})

	entries := cfg.Effective(nil)
	byKey := make(map[string]EffectiveEntry, len(entries))
	for _, e := range entries {
		byKey[e.Key] = e
	}

	for _, key := range []string{SettingLLMAPIKey, SettingSearchAPIKey, SettingEmbeddingAPIKey} {
		assert.Equal(t, run.RedactedPlaceholder, byKey[key].Value, "secret %s must be redacted", key)
	}
	assert.Equal(t, run.RedactedPlaceholder, byKey[SettingLLMAPIKey].Value)
	assert.Contains(t, byKey[SettingLLMBaseURL].Value, run.RedactedPlaceholder, "secret in URL must be redacted")
	assert.NotContains(t, byKey[SettingLLMBaseURL].Value, secret)

	// The raw secret must never appear anywhere in the report.
	report := cfg.TextReport(nil)
	assert.NotContains(t, report, secret)
	assert.Contains(t, report, run.RedactedPlaceholder)

	// Snapshot redaction via run.ConfigSnapshot.
	snap, err := cfg.Snapshot(nil)
	require.NoError(t, err)
	v, ok := snap.Get(SettingLLMAPIKey)
	assert.True(t, ok)
	assert.Equal(t, run.RedactedPlaceholder, v)
	for _, v := range snap.Values {
		assert.NotContains(t, v, secret)
	}

	// JSON-effective output is also clean.
	data, err := cfg.EffectiveJSON(nil)
	require.NoError(t, err)
	assert.NotContains(t, string(data), secret)
}

// The config file itself (on disk) never contains the secret either: the
// file stores only the env reference.
func TestFileStoresEnvReferenceOnly(t *testing.T) {
	f, err := LoadBytes([]byte("llm:\n  api_key:\n    env: OMLX_API_KEY\n  model: llama\n"), "test")
	require.NoError(t, err)
	cfg := New(f, "test")
	assert.Equal(t, "OMLX_API_KEY", cfg.File.LLM.APIKey.Env)
}

func TestSnapshotDefaultsOnly(t *testing.T) {
	cfg := New(&File{}, "")
	cfg.Env = fakeEnv(nil)
	snap, err := cfg.Snapshot(nil)
	require.NoError(t, err)
	// Built-ins present.
	v, ok := snap.Get(SettingArchiveDBPath)
	assert.True(t, ok)
	assert.Equal(t, "./archive.db", v)
	v, ok = snap.Get(SettingLLMTimeout)
	assert.True(t, ok)
	assert.Equal(t, "120", v)
	// Unset settings are omitted (empty values are dropped).
	_, ok = snap.Get(SettingLLMBaseURL)
	assert.False(t, ok)
	_, ok = snap.Get(SettingLLMModel)
	assert.False(t, ok)
}

func TestResolveUnknownSetting(t *testing.T) {
	cfg := New(&File{}, "")
	_, _, err := cfg.Resolve("llm.nope", Override{})
	assert.Error(t, err)
}

func TestTypedGetters(t *testing.T) {
	f, err := LoadBytes([]byte(`
llm:
  timeout: 60
search:
  max_queries: 7
rerank:
  enabled: true
`), "test")
	require.NoError(t, err)
	cfg := New(f, "test")
	cfg.Env = fakeEnv(nil)

	tm, ok, err := cfg.GetFloat(SettingLLMTimeout, Override{})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.InDelta(t, 60, tm, 0.001)

	n, ok, err := cfg.GetInt(SettingSearchMaxQueries, Override{})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, 7, n)

	b, ok, err := cfg.GetBool(SettingRerankEnabled, Override{})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.True(t, b)

	// Unset in every layer.
	_, ok, err = cfg.GetFloat(SettingTemperature, Override{})
	require.NoError(t, err)
	assert.False(t, ok)

	// CLI override with an invalid range fails.
	_, _, err = cfg.GetFloat(SettingChunkOverlapRatio, Override{Value: "5", Changed: true})
	assert.Error(t, err)
}

// fakeEnv builds an Env func from a fixed map.
func fakeEnv(m map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}
