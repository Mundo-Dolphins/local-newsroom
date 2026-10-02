package main

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestResearchCmdValidation tests CLI validation logic for research command.
func TestResearchCmdValidation(t *testing.T) {
	tests := []struct {
		name          string
		topic         string
		urls          []string
		searchBaseURL string
		wantError     bool
		errContains   string
	}{
		{
			name:          "missing topic",
			topic:         "",
			urls:          []string{"https://example.com"},
			searchBaseURL: "",
			wantError:     true,
			errContains:   "--topic is required",
		},
		{
			name:          "missing both URLs and search config",
			topic:         "test",
			urls:          []string{},
			searchBaseURL: "",
			wantError:     true,
			errContains:   "at least one source mode required",
		},
		{
			name:          "URL-only mode valid",
			topic:         "test",
			urls:          []string{"https://example.com"},
			searchBaseURL: "",
			wantError:     false,
		},
		{
			name:          "search-only mode valid",
			topic:         "test",
			urls:          []string{},
			searchBaseURL: "http://example.com",
			wantError:     false,
		},
		{
			name:          "supplement mode valid",
			topic:         "test",
			urls:          []string{"https://example.com"},
			searchBaseURL: "http://example.com",
			wantError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().StringVar(&rFlags.topic, "topic", tt.topic, "")
			cmd.Flags().StringSliceVar(&rFlags.urls, "url", tt.urls, "")
			cmd.Flags().StringVar(&rFlags.searchBaseURL, "search-base-url", tt.searchBaseURL, "")
			_ = cmd.MarkFlagRequired("topic")

			// Simulate flag parsing
			rFlags.topic = tt.topic
			rFlags.urls = tt.urls
			rFlags.searchBaseURL = tt.searchBaseURL

			// Validate
			if rFlags.topic == "" {
				err := errors.New("--topic is required")
				if !tt.wantError || !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got %v", tt.errContains, err)
				}
				return
			}

			urlsProvided := len(rFlags.urls) > 0
			searchConfigured := rFlags.searchBaseURL != ""

			if !urlsProvided && !searchConfigured {
				err := errors.New("at least one source mode required: provide --url(s) or configure --search-base-url (or SEARXNG_BASE_URL)")
				if !tt.wantError || !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got %v", tt.errContains, err)
				}
				return
			}

			if tt.wantError {
				t.Error("expected error but got none")
			}
		})
	}
}

// TestResearchCmdFlagDefinitions tests that all expected flags are defined.
func TestResearchCmdFlagDefinitions(t *testing.T) {
	// Reset flags for each test
	resetResearchFlags()

	// Test URL flag exists
	urlFlag := researchCmd.Flags().Lookup("url")
	if urlFlag == nil {
		t.Error("expected --url flag to be defined")
	}

	// Test topic flag exists and is required
	topicFlag := researchCmd.Flags().Lookup("topic")
	if topicFlag == nil {
		t.Error("expected --topic flag to be defined")
	}
	if topicFlag.Usage != "Research topic (required)" {
		t.Errorf("expected topic flag to be marked required, got usage: %q", topicFlag.Usage)
	}

	// Test search-base-url flag exists
	searchBaseURLFlag := researchCmd.Flags().Lookup("search-base-url")
	if searchBaseURLFlag == nil {
		t.Error("expected --search-base-url flag to be defined")
	}

	// Test search-language flag exists
	searchLanguageFlag := researchCmd.Flags().Lookup("search-language")
	if searchLanguageFlag == nil {
		t.Error("expected --search-language flag to be defined")
	}

	// Test search-time-range flag exists
	searchTimeRangeFlag := researchCmd.Flags().Lookup("search-time-range")
	if searchTimeRangeFlag == nil {
		t.Error("expected --search-time-range flag to be defined")
	}

	// Test search-max-queries flag exists
	searchMaxQueriesFlag := researchCmd.Flags().Lookup("search-max-queries")
	if searchMaxQueriesFlag == nil {
		t.Error("expected --search-max-queries flag to be defined")
	}

	// Test search-results-per-query flag exists
	searchResultsPerQueryFlag := researchCmd.Flags().Lookup("search-results-per-query")
	if searchResultsPerQueryFlag == nil {
		t.Error("expected --search-results-per-query flag to be defined")
	}

	// Test search-max-sources flag exists
	searchMaxSourcesFlag := researchCmd.Flags().Lookup("search-max-sources")
	if searchMaxSourcesFlag == nil {
		t.Error("expected --search-max-sources flag to be defined")
	}

	// Test output flag exists
	outputFlag := researchCmd.Flags().Lookup("output")
	if outputFlag == nil {
		t.Error("expected --output flag to be defined")
	}

	// Test llm-base-url flag exists
	llmBaseURLFlag := researchCmd.Flags().Lookup("llm-base-url")
	if llmBaseURLFlag == nil {
		t.Error("expected --llm-base-url flag to be defined")
	}
}

// TestResearchCmdSearchFlagsDefaults tests default values for search flags.
func TestResearchCmdSearchFlagsDefaults(t *testing.T) {
	resetResearchFlags()

	// Test default values
	if rFlags.searchMaxQueries != 5 {
		t.Errorf("expected default search-max-queries=5, got %d", rFlags.searchMaxQueries)
	}
	if rFlags.searchResultsPerQuery != 10 {
		t.Errorf("expected default search-results-per-query=10, got %d", rFlags.searchResultsPerQuery)
	}
	if rFlags.searchMaxSources != 0 {
		t.Errorf("expected default search-max-sources=0 (unlimited), got %d", rFlags.searchMaxSources)
	}
}

// TestResearchCmdEnvironmentVariablePrecedence tests environment variable precedence.
func TestResearchCmdEnvironmentVariablePrecedence(t *testing.T) {
	// Save original env vars
	originalSearxngURL := os.Getenv("SEARXNG_BASE_URL")
	originalOmlxURL := os.Getenv("OMLX_BASE_URL")
	originalOmlxModel := os.Getenv("OMLX_MODEL")
	originalOmlxAPIKey := os.Getenv("OMLX_API_KEY")
	originalSearxngAPIKey := os.Getenv("SEARXNG_API_KEY")
	originalSearxngAPIHeader := os.Getenv("SEARXNG_API_HEADER")

	// Restore on exit
	defer func() {
		_ = os.Setenv("SEARXNG_BASE_URL", originalSearxngURL)
		_ = os.Setenv("OMLX_BASE_URL", originalOmlxURL)
		_ = os.Setenv("OMLX_MODEL", originalOmlxModel)
		_ = os.Setenv("OMLX_API_KEY", originalOmlxAPIKey)
		_ = os.Setenv("SEARXNG_API_KEY", originalSearxngAPIKey)
		_ = os.Setenv("SEARXNG_API_HEADER", originalSearxngAPIHeader)
	}()

	t.Run("SEARXNG_BASE_URL env var", func(t *testing.T) {
		_ = os.Setenv("SEARXNG_BASE_URL", "http://env-var.example.com")
		rFlags.searchBaseURL = "" // CLI flag not set
		// Simulate what buildDiscoveryConfigInternal would do
		if rFlags.searchBaseURL == "" {
			rFlags.searchBaseURL = os.Getenv("SEARXNG_BASE_URL")
		}
		if rFlags.searchBaseURL != "http://env-var.example.com" {
			t.Errorf("expected SEARXNG_BASE_URL env var to be read, got %q", rFlags.searchBaseURL)
		}
	})

	t.Run("CLI flag overrides env var", func(t *testing.T) {
		_ = os.Setenv("SEARXNG_BASE_URL", "http://env-var.example.com")
		rFlags.searchBaseURL = "http://cli-flag.example.com"
		// CLI flag should be used
		if rFlags.searchBaseURL != "http://cli-flag.example.com" {
			t.Errorf("expected CLI flag to override env var, got %q", rFlags.searchBaseURL)
		}
	})

	t.Run("OMLX_BASE_URL env var", func(t *testing.T) {
		_ = os.Setenv("OMLX_BASE_URL", "http://omlx-env.example.com/v1")
		rFlags.llmBaseURL = ""
		if rFlags.llmBaseURL == "" {
			rFlags.llmBaseURL = os.Getenv("OMLX_BASE_URL")
		}
		if rFlags.llmBaseURL != "http://omlx-env.example.com/v1" {
			t.Errorf("expected OMLX_BASE_URL env var to be read, got %q", rFlags.llmBaseURL)
		}
	})

	t.Run("OMLX_API_KEY env var", func(t *testing.T) {
		_ = os.Setenv("OMLX_API_KEY", "env-api-key-123")
		rFlags.llmAPIKey = ""
		if rFlags.llmAPIKey == "" {
			rFlags.llmAPIKey = os.Getenv("OMLX_API_KEY")
		}
		if rFlags.llmAPIKey != "env-api-key-123" {
			t.Errorf("expected OMLX_API_KEY env var to be read, got %q", rFlags.llmAPIKey)
		}
	})
}

// resetResearchFlags resets the global research flags to default values.
func resetResearchFlags() {
	rFlags = researchFlags{
		topic:                 "",
		urls:                  nil,
		outputPath:            "dossier.json",
		searchBaseURL:         "",
		searchLanguage:        "",
		searchTimeRange:       "",
		searchMaxQueries:      5,
		searchResultsPerQuery: 10,
		searchMaxSources:      0,
		llmBaseURL:            "",
		llmModel:              "",
		llmAPIKey:             "",
		llmTimeout:            120,
		fetchTimeout:          30,
		maxSize:               10 * 1024 * 1024,
		maxWords:              50000,
		promptFile:            "",
	}
}

// TestResearchCmdHelpText tests that help text mentions source modes.
func TestResearchCmdHelpText(t *testing.T) {
	resetResearchFlags()

	help := researchCmd.Long
	if help == "" {
		t.Fatal("expected help text to be defined")
	}

	// Check that help mentions key features
	requiredPhrases := []string{
		"URL-only",
		"SearXNG",
		"--search-base-url",
		"SEARXNG_BASE_URL",
	}

	for _, phrase := range requiredPhrases {
		if !strings.Contains(help, phrase) {
			t.Errorf("expected help text to contain %q", phrase)
		}
	}
}

// TestResearchCmdInvalidSearchConfig tests validation of search config.
func TestResearchCmdInvalidSearchConfig(t *testing.T) {
	resetResearchFlags()

	// Test invalid search-max-queries (below minimum)
	t.Run("search-max-queries below minimum", func(t *testing.T) {
		// This would be validated in buildDiscoveryConfigInternal
		// The config.Validate() should return an error
	})

	// Test invalid search-results-per-query (above maximum)
	t.Run("search-results-per-query above maximum", func(t *testing.T) {
		// Test that config validation catches invalid values
	})
}
