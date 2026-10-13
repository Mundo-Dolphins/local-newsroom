package config_test

import (
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
)

// TestSecretValues pins the bridge between configuration and the persistence
// boundary: every secret setting that resolves to a non-empty value is
// returned (deduplicated, table order) so a store can register them as exact
// redaction values.
func TestSecretValues(t *testing.T) {
	t.Setenv("OMLX_API_KEY", "omlx-live-key-1")
	t.Setenv("SEARXNG_API_KEY", "searxng-live-key-2")
	// The embedding key reuses the LLM key: deduplication must collapse it.
	t.Setenv("EMBEDDING_API_KEY", "omlx-live-key-1")

	f := &config.File{
		LLM:       config.LLMSection{APIKey: &config.APIKey{Env: "OMLX_API_KEY"}},
		Search:    config.SearchSection{APIKey: &config.APIKey{Env: "SEARXNG_API_KEY"}},
		Embedding: config.EmbeddingSection{APIKey: &config.APIKey{Env: "EMBEDDING_API_KEY"}},
	}
	c := config.New(f, "")

	got := c.SecretValues(nil)
	want := []string{"omlx-live-key-1", "searxng-live-key-2"}
	if len(got) != len(want) {
		t.Fatalf("SecretValues length = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SecretValues = %v, want %v", got, want)
		}
	}
}

// TestSecretValuesUnsetReportsNothing: absent secrets (no file reference and
// no environment value) yield an empty slice, never a placeholder or error.
func TestSecretValuesUnsetReportsNothing(t *testing.T) {
	for _, name := range []string{"OMLX_API_KEY", "SEARXNG_API_KEY", "EMBEDDING_API_KEY"} {
		t.Setenv(name, "")
	}
	// No file references at all.
	c := config.New(&config.File{}, "")
	if got := c.SecretValues(nil); len(got) != 0 {
		t.Fatalf("SecretValues = %v, want empty", got)
	}
}

// TestSecretValuesCLIOverrideWins: a CLI override supplies the live value
// even when no environment variable is set.
func TestSecretValuesCLIOverrideWins(t *testing.T) {
	t.Setenv("OMLX_API_KEY", "")
	c := config.New(&config.File{}, "")
	overrides := map[string]config.Override{
		config.SettingLLMAPIKey: config.Override{Value: "cli-live-key", Changed: true},
	}
	got := c.SecretValues(overrides)
	if len(got) != 1 || got[0] != "cli-live-key" {
		t.Fatalf("SecretValues = %v, want [cli-live-key]", got)
	}
}
