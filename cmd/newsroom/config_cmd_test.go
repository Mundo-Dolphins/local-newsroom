package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newsroomEnvVars lists every environment variable the CLI resolves. Tests
// must strip all of these (plus HOME/XDG_CONFIG_HOME) so that resolution
// starts from a known, hermetic state.
var newsroomEnvVars = []string{
	"OMLX_BASE_URL", "OMLX_MODEL", "OMLX_API_KEY", "OMLX_TIMEOUT",
	"SEARXNG_BASE_URL", "SEARXNG_SEARCH_LANGUAGE", "SEARXNG_SEARCH_TIME_RANGE",
	"SEARXNG_MAX_QUERIES", "SEARXNG_RESULTS_PER_QUERY", "SEARXNG_MAX_SOURCES",
	"SEARXNG_API_KEY", "SEARXNG_API_HEADER",
	"EMBEDDING_BASE_URL", "EMBEDDING_MODEL", "EMBEDDING_API_KEY", "EMBEDDING_TIMEOUT",
	"RERANK_ENABLED", "RERANK_MODEL",
	"ARCHIVE_DB_PATH", "ARCHIVE_TOP_K", "ARCHIVE_FORMAT",
	"FETCH_TIMEOUT", "FETCH_MAX_SIZE", "FETCH_MAX_WORDS", "FETCH_PARALLEL",
	"CHUNK_TARGET_SIZE", "CHUNK_MAX_SIZE", "CHUNK_OVERLAP_RATIO",
	"NEWSROOM_PROFILES_ROOT", "NEWSROOM_PROFILE_NAME",
	"NEWSROOM_LANGUAGE", "NEWSROOM_OUTPUT_FORMAT", "NEWSROOM_TEMPERATURE",
	"NEWSROOM_MAX_TOKENS", "NEWSROOM_TONE", "NEWSROOM_CONFIG",
}

// hermeticEnv returns the full environment with all newsroom-related
// variables removed, HOME pointed at an empty temp dir, and XDG_CONFIG_HOME
// unset, plus any extra variables (which may re-set HOME or any newsroom
// variable). This makes `go run` children resolve configuration from a known
// state regardless of the developer's machine.
func hermeticEnv(t *testing.T, extra map[string]string) []string {
	t.Helper()
	home := t.TempDir()

	strip := make(map[string]bool, len(newsroomEnvVars)+2)
	for _, k := range newsroomEnvVars {
		strip[k] = true
	}
	strip["HOME"] = true
	strip["XDG_CONFIG_HOME"] = true

	values := make(map[string]string)
	for _, kv := range os.Environ() {
		key, val, _ := strings.Cut(kv, "=")
		if strip[key] {
			continue
		}
		values[key] = val
	}
	values["HOME"] = home
	// Pin the module/build caches to the real user locations (outside the
	// temp HOME) so children use the warm cache and never write into the
	// temp dir that t.TempDir() cleanup would then have to remove.
	realHome, err := os.UserHomeDir()
	if err == nil {
		values["GOPATH"] = filepath.Join(realHome, "go")
	}
	values["GOMODCACHE"] = filepath.Join(values["GOPATH"], "pkg", "mod")
	for k, v := range extra {
		values[k] = v
	}

	env := make([]string, 0, len(values))
	for k, v := range values {
		env = append(env, k+"="+v)
	}
	return env
}

// runCLI executes the newsroom CLI (via `go run`) with the given environment
// and arguments. It returns stdout, stderr, and whether the command exited 0.
//
// The exec.Cmd is built by hand (argv slice + struct literal) because the
// Go 1.27.1 toolchain in use here mis-compiles exec.Command(spread) calls
// with a dynamically-sized argument list.
func runCLI(t *testing.T, env []string, args ...string) (string, string, bool) {
	t.Helper()
	full := append([]string{"go", "run", "github.com/Mundo-Dolphins/local-newsroom/cmd/newsroom"}, args...)
	path, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go not found in PATH: %v", err)
	}
	full[0] = path
	cmd := &exec.Cmd{Path: path, Args: full, Env: env}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	return outBuf.String(), errBuf.String(), runErr == nil
}

// output is a helper combining stdout and stderr for assertions on commands
// that report through either stream.
func output(stdout, stderr string) string { return stdout + "\n" + stderr }

// writeConfigFile writes YAML content to a temp file and returns its path.
func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "newsroom.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing config file: %v", err)
	}
	return path
}

// TestConfigShowNoFile verifies "config show" with no config file anywhere:
// everything resolves from built-in defaults.
func TestConfigShowNoFile(t *testing.T) {
	stdout, stderr, ok := runCLI(t, hermeticEnv(t, nil), "config", "show")
	if !ok {
		t.Fatalf("config show failed: %s", output(stdout, stderr))
	}
	for _, want := range []string{
		"Config file: (none; environment variables and built-in defaults)",
		"llm.base_url",
		"(unset)",
		"llm.timeout",
		"120",
		"(default)",
		"defaults.language",
		"en",
		"search.max_queries",
		"5",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q\noutput:\n%s", want, stdout)
		}
	}
}

// TestConfigShowPrecedence verifies file < env precedence and redaction
// through the real process boundary.
func TestConfigShowPrecedence(t *testing.T) {
	cfg := writeConfigFile(t, `
schema_version: 1

llm:
  base_url: http://file-host:8080/v1
  model: file-model
  api_key:
    env: TEST_LLM_KEY

search:
  max_queries: 7
`)
	env := hermeticEnv(t, map[string]string{
		// Env beats the config file for model.
		"OMLX_MODEL":   "env-model",
		"TEST_LLM_KEY": "supersecret-credential",
	})

	stdout, stderr, ok := runCLI(t, env, "--config", cfg, "config", "show", "--format", "json")
	if !ok {
		t.Fatalf("config show failed: %s", output(stdout, stderr))
	}

	var report struct {
		ConfigFile string `json:"config_file"`
		Entries    []struct {
			Key    string `json:"key"`
			Value  string `json:"value"`
			Source string `json:"source"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout)
	}
	if report.ConfigFile != cfg {
		t.Errorf("config_file = %q, want %q", report.ConfigFile, cfg)
	}

	type got struct{ value, source string }
	byKey := map[string]got{}
	for _, e := range report.Entries {
		byKey[e.Key] = got{e.Value, e.Source}
	}

	cases := []struct {
		key, wantValue, wantSource string
	}{
		{"llm.base_url", "http://file-host:8080/v1", "config"},
		{"llm.model", "env-model", "env:OMLX_MODEL"},
		{"llm.api_key", "[REDACTED]", "config (env:TEST_LLM_KEY)"},
		{"search.max_queries", "7", "config"},
		{"llm.timeout", "120", "default"},
	}
	for _, tc := range cases {
		g, okk := byKey[tc.key]
		if !okk {
			t.Errorf("entry %s missing from report", tc.key)
			continue
		}
		if g.value != tc.wantValue {
			t.Errorf("%s value = %q, want %q (source %s)", tc.key, g.value, tc.wantValue, g.source)
		}
		if g.source != tc.wantSource {
			t.Errorf("%s source = %q, want %q", tc.key, g.source, tc.wantSource)
		}
	}

	// The raw credential must never appear anywhere in the output.
	if strings.Contains(output(stdout, stderr), "supersecret-credential") {
		t.Errorf("raw secret leaked into output:\n%s", output(stdout, stderr))
	}
}

// TestConfigValidate covers the validate subcommand for all three states.
func TestConfigValidate(t *testing.T) {
	// Valid file.
	valid := writeConfigFile(t, "llm:\n  model: m\n  timeout: 60\n")
	stdout, stderr, ok := runCLI(t, hermeticEnv(t, nil), "--config", valid, "config", "validate")
	if !ok {
		t.Fatalf("validate of valid file failed: %s", output(stdout, stderr))
	}
	if !strings.Contains(stdout, "is valid") {
		t.Errorf("expected 'is valid' in output:\n%s", output(stdout, stderr))
	}

	// Invalid value.
	invalid := writeConfigFile(t, "search:\n  max_queries: 999\n")
	stdout, stderr, ok = runCLI(t, hermeticEnv(t, nil), "--config", invalid, "config", "validate")
	if ok {
		t.Errorf("validate of invalid file succeeded: %s", output(stdout, stderr))
	}
	if !strings.Contains(output(stdout, stderr), "search.max_queries") {
		t.Errorf("expected setting name in error:\n%s", output(stdout, stderr))
	}

	// Malformed YAML.
	broken := writeConfigFile(t, "llm:\n  model: [unclosed")
	stdout, stderr, ok = runCLI(t, hermeticEnv(t, nil), "--config", broken, "config", "show")
	if ok {
		t.Errorf("expected failure for malformed YAML: %s", output(stdout, stderr))
	}
	if !strings.Contains(output(stdout, stderr), "YAML syntax error") {
		t.Errorf("expected a YAML syntax error:\n%s", output(stdout, stderr))
	}

	// No file anywhere.
	stdout, stderr, ok = runCLI(t, hermeticEnv(t, nil), "config", "validate")
	if !ok {
		t.Fatalf("validate without file failed: %s", output(stdout, stderr))
	}
	if !strings.Contains(stdout, "No config file found") {
		t.Errorf("expected 'No config file found' in output:\n%s", stdout)
	}
}

// TestConfigMissingFile verifies --config with a missing path is an error.
func TestConfigMissingFile(t *testing.T) {
	stdout, stderr, ok := runCLI(t, hermeticEnv(t, nil), "--config", "/nonexistent/newsroom.yaml", "config", "show")
	if ok {
		t.Errorf("expected failure, got success: %s", output(stdout, stderr))
	}
	if !strings.Contains(output(stdout, stderr), "config file not found") {
		t.Errorf("expected 'config file not found' in error:\n%s", output(stdout, stderr))
	}
}

// TestWriteUsesConfigFileLLM verifies that a config file can supply the LLM
// endpoint that CLI flags and environment used to be the only source of.
// The endpoint is a dead local port: the command must fail with a
// connection error, never with "LLM base URL required".
func TestWriteUsesConfigFileLLM(t *testing.T) {
	cfg := writeConfigFile(t, `
schema_version: 1
llm:
  base_url: http://127.0.0.1:9/v1
  model: fake-model
`)
	verification := map[string]interface{}{
		"stable_id":        "ver-001",
		"input_dossier_id": "dossier-001",
		"verified_at":      "2024-01-15T13:00:00Z",
		"verification_statuses": map[string]string{
			"claim-001": "supported",
		},
		"claim_details": map[string]interface{}{
			"claim-001": map[string]interface{}{
				"claim_id":            "claim-001",
				"statement":           "Test claim",
				"verification_status": "supported",
				"confidence_level":    "high",
				"supporting_evidence": []map[string]interface{}{
					{
						"source_id":        "source-001",
						"excerpt":          "Evidence text",
						"evidence_context": "Context",
					},
				},
				"conflicting_evidence": []map[string]interface{}{},
				"verification_notes":   "Notes",
			},
		},
		"contradictions":       []map[string]interface{}{},
		"verification_notes":   []map[string]interface{}{},
		"verification_summary": "Summary",
		"quality_score":        100.0,
	}
	vb, err := json.MarshalIndent(verification, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dossier := filepath.Join(t.TempDir(), "verification.json")
	if err := os.WriteFile(dossier, vb, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, ok := runCLI(t, hermeticEnv(t, nil), "--config", cfg, "write", "--dossier", dossier)
	if ok {
		t.Fatalf("expected a network failure, got success: %s", output(stdout, stderr))
	}
	combined := output(stdout, stderr)
	if strings.Contains(combined, "LLM base URL required") || strings.Contains(combined, "LLM model required") {
		t.Errorf("config file was not used for LLM resolution:\n%s", combined)
	}
	if !strings.Contains(combined, "127.0.0.1:9") {
		t.Errorf("expected the connection failure to reference the configured endpoint:\n%s", combined)
	}
}

// TestArchiveUsesConfigFile verifies the archive pipeline reads its settings
// from the config file when no flags are given, that environment variables
// override the file, and that CLI flags win over both. The observable is
// the stats report: text mode prints "Archive Statistics", JSON mode prints
// a document with "database_path".
func TestArchiveUsesConfigFile(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test-archive.db")
	baseEnv := hermeticEnv(t, map[string]string{"ARCHIVE_DB_PATH": dbPath})

	// 1. No flags: db path from env, format from built-in default (text).
	cfg := writeConfigFile(t, "archive:\n  top_k: 5\n")
	stdout, stderr, ok := runCLI(t, baseEnv, "--config", cfg, "archive", "stats")
	if !ok {
		t.Fatalf("archive stats failed: %s", output(stdout, stderr))
	}
	if !strings.Contains(stdout, "Archive Statistics") || !strings.Contains(stdout, "Database: "+dbPath) {
		t.Errorf("expected text stats for the env db path:\n%s", output(stdout, stderr))
	}

	// 2. Config file supplies the format (json).
	cfgJSON := writeConfigFile(t, "archive:\n  top_k: 5\n  format: json\n")
	stdout, stderr, ok = runCLI(t, baseEnv, "--config", cfgJSON, "archive", "stats")
	if !ok {
		t.Fatalf("archive stats failed: %s", output(stdout, stderr))
	}
	if !strings.Contains(stdout, "database_path") {
		t.Errorf("config file format was not applied (expected JSON stats):\n%s", output(stdout, stderr))
	}

	// 3. Env overrides the file (back to text).
	envText := hermeticEnv(t, map[string]string{"ARCHIVE_DB_PATH": dbPath, "ARCHIVE_FORMAT": "text"})
	stdout, stderr, ok = runCLI(t, envText, "--config", cfgJSON, "archive", "stats")
	if !ok {
		t.Fatalf("archive stats failed: %s", output(stdout, stderr))
	}
	if !strings.Contains(stdout, "Archive Statistics") {
		t.Errorf("env should override the config file format (expected text stats):\n%s", output(stdout, stderr))
	}
	if strings.Contains(stdout, "database_path") {
		t.Errorf("env format should have won over the file (got JSON stats):\n%s", output(stdout, stderr))
	}

	// 4. CLI overrides env and file (back to json).
	stdout, stderr, ok = runCLI(t, envText, "--config", cfgJSON, "archive", "stats", "--format", "json")
	if !ok {
		t.Fatalf("archive stats failed: %s", output(stdout, stderr))
	}
	if !strings.Contains(stdout, "database_path") {
		t.Errorf("CLI flag should win over env and file (expected JSON stats):\n%s", output(stdout, stderr))
	}
}

// TestHelpAndVersionWorkWithoutConfig ensures help and --version do not
// depend on a valid config file.
func TestHelpAndVersionWorkWithoutConfig(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".newsroom.yaml"),
		[]byte("llm:\n  model: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := hermeticEnv(t, map[string]string{"HOME": home})

	stdout, stderr, ok := runCLI(t, env, "--version")
	if !ok {
		t.Fatalf("--version failed: %s", output(stdout, stderr))
	}
	if !strings.Contains(stdout, "0.5.0") {
		t.Errorf("expected version 0.5.0:\n%s", output(stdout, stderr))
	}

	stdout, stderr, ok = runCLI(t, env, "help")
	if !ok {
		t.Fatalf("help failed: %s", output(stdout, stderr))
	}
	if !strings.Contains(output(stdout, stderr), "newsroom") {
		t.Errorf("expected help text:\n%s", output(stdout, stderr))
	}
}
