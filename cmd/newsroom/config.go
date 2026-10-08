package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
)

// defaultModel is the default model identifier used in the pipeline.
const defaultModel = "v0.4"

// buildLLMClientFromFlags builds an LLM client from the given flags struct.
// This function is used by verify, write, and final-check commands.
func buildLLMClientFromFlags(baseURLFlag, modelFlag, apiKeyFlag string, timeout float64) llm.Client {
	// Determine configuration using precedence: CLI > env > defaults
	baseURL := baseURLFlag
	if baseURL == "" {
		baseURL = os.Getenv("OMLX_BASE_URL")
	}

	model := modelFlag
	if model == "" {
		model = os.Getenv("OMLX_MODEL")
	}

	apiKey := apiKeyFlag
	if apiKey == "" {
		apiKey = os.Getenv("OMLX_API_KEY")
	}

	if baseURL == "" {
		fmt.Fprintf(os.Stderr, "Error: LLM base URL required: set --llm-base-url or OMLX_BASE_URL\n")
		os.Exit(1)
	}

	if model == "" {
		fmt.Fprintf(os.Stderr, "Error: LLM model required: set --llm-model or OMLX_MODEL\n")
		os.Exit(1)
	}

	// Build LLM client
	return llm.NewClient(llm.Config{
		BaseURL:         baseURL,
		Model:           model,
		APIKey:          apiKey,
		Timeout:         time.Duration(timeout) * time.Second,
		Temperature:     0.3,
		MaxOutputTokens: 50000,
	})
}

// log writes a log message if the verbosity level is sufficient.
func log(verbosity int, level int, format string, args ...interface{}) {
	if level <= verbosity {
		fmt.Fprintf(os.Stderr, "[newsroom] "+format+"\n", args...)
	}
}
