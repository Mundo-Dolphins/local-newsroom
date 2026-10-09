package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/spf13/cobra"
)

// defaultModel is the default model identifier used in the pipeline.
const defaultModel = "v0.4"

// appConfig is the resolved application configuration for this process.
//
// It is populated (in rootCmd's PersistentPreRunE) from the config file (if
// one was found or requested), environment variables, and built-in defaults.
// Every setting resolves with the deterministic precedence chain:
//
//	CLI flags > environment variables > config file > built-in defaults
var appConfig = config.New(nil, "")

// configPathFlag holds the --config persistent flag value.
var configPathFlag string

// loadAppConfig (re)builds appConfig.
//
// When the --config flag is set, exactly that file is loaded and a missing
// file is an error. Otherwise the standard lookup locations are searched and
// an absent file simply means "environment variables + built-in defaults".
func loadAppConfig() error {
	var (
		f    *config.File
		path string
		err  error
	)
	if configPathFlag != "" {
		f, err = config.Load(configPathFlag)
		path = configPathFlag
	} else {
		f, path, err = config.FindAndLoad()
	}
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	appConfig = config.New(f, path)
	if path != "" {
		fmt.Fprintf(os.Stderr, "[newsroom] Config file: %s\n", path)
	}
	return nil
}

// flagOverride returns the (value, changed) override for the named flag on
// cmd. Only a user-provided, non-empty flag participates in precedence
// resolution; an unprovided (or explicitly empty) flag falls through to the
// environment, config file, and built-in default layers.
func flagOverride(cmd *cobra.Command, name string) config.Override {
	f := cmd.Flags().Lookup(name)
	if f == nil {
		return config.Override{}
	}
	return config.FromFlag(f.Value.String(), f.Changed)
}

// buildLLMClient builds an LLM client from the full precedence chain
// (--llm-* flags > OMLX_* environment variables > config file > built-in
// defaults) instead of from flags and environment only.
//
// The error messages keep the historical "LLM base URL required" /
// "LLM model required" prefixes.
func buildLLMClient(cmd *cobra.Command) (llm.Client, error) {
	baseURL, _, err := appConfig.GetString(config.SettingLLMBaseURL, flagOverride(cmd, "llm-base-url"))
	if err != nil {
		return nil, err
	}
	model, _, err := appConfig.GetString(config.SettingLLMModel, flagOverride(cmd, "llm-model"))
	if err != nil {
		return nil, err
	}
	apiKey, _, err := appConfig.GetString(config.SettingLLMAPIKey, flagOverride(cmd, "llm-api-key"))
	if err != nil {
		return nil, err
	}
	timeout, _, err := appConfig.GetFloat(config.SettingLLMTimeout, flagOverride(cmd, "llm-timeout"))
	if err != nil {
		return nil, err
	}

	if baseURL == "" {
		return nil, errors.New("LLM base URL required: set --llm-base-url, OMLX_BASE_URL, or llm.base_url in the config file")
	}
	if model == "" {
		return nil, errors.New("LLM model required: set --llm-model, OMLX_MODEL, or llm.model in the config file")
	}

	return llm.NewClient(llm.Config{
		BaseURL:         baseURL,
		Model:           model,
		APIKey:          apiKey,
		Timeout:         time.Duration(timeout * float64(time.Second)),
		Temperature:     0.3, // Overridden per-request by individual calls
		MaxOutputTokens: 50000,
	}), nil
}

// log writes a log message if the verbosity level is sufficient.
func log(verbosity int, level int, format string, args ...interface{}) {
	if level <= verbosity {
		fmt.Fprintf(os.Stderr, "[newsroom] "+format+"\n", args...)
	}
}
