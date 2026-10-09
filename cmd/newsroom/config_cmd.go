package main

import (
	"fmt"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/spf13/cobra"
)

// configCmd is the "newsroom config" subcommand: it shows and validates the
// persistent configuration.
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show or validate the newsroom configuration",
	Long: `Show or validate the newsroom persistent configuration.

Settings resolve with the deterministic precedence:

  CLI flags > environment variables > config file > built-in defaults

The config file is searched in this order when --config is not given:

  1. $NEWSROOM_CONFIG
  2. ./.newsroom.yaml or ./.newsroom.yml
  3. $XDG_CONFIG_HOME/newsroom/config.yaml (or ~/.config/newsroom/config.yaml)
  4. ~/.newsroom.yaml

Secrets are never stored in the config file: api_key fields only reference
the environment variable that supplies them, and every secret value is
redacted in all output of this command.`,
	RunE: doConfigShow,
}

// configShowCmd is "newsroom config show".
var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the resolved (effective) configuration",
	Long: `Show every setting with its resolved value and the precedence layer
that supplied it (CLI flag, environment variable, config file, or built-in
default).

Secret values are always redacted; this output is safe to share.`,
	RunE: doConfigShow,
}

// configValidateCmd is "newsroom config validate".
var configValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate the config file and exit non-zero on problems",
	Long: `Validate the config file and exit non-zero on problems.

File parsing, schema (unknown keys), and value range validation all run
during configuration loading, so an invalid file fails with a precise
message before this command reports.`,
	RunE: doConfigValidate,
}

// configOutputFormat selects the output format for "config show".
var configOutputFormat string

func init() {
	configShowCmd.Flags().StringVar(&configOutputFormat, "format", "text", "Output format: 'text' or 'json'")
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configValidateCmd)
	rootCmd.AddCommand(configCmd)
}

// doConfigShow prints the resolved effective configuration.
func doConfigShow(cmd *cobra.Command, args []string) error {
	switch configOutputFormat {
	case "json":
		data, err := appConfig.EffectiveJSON(nil)
		if err != nil {
			return fmt.Errorf("rendering config: %w", err)
		}
		fmt.Println(string(data))
		return nil
	case "text":
		fmt.Print(appConfig.TextReport(nil))
		return nil
	default:
		return fmt.Errorf("invalid format %q: must be 'text' or 'json'", configOutputFormat)
	}
}

// doConfigValidate reports the validation status of the active configuration.
func doConfigValidate(cmd *cobra.Command, args []string) error {
	// Reaching this point means the config file (if any) loaded, parsed,
	// and validated successfully: an invalid file would already have failed
	// during PersistentPreRunE.
	if appConfig.FilePath != "" {
		fmt.Printf("Config file %s is valid\n", appConfig.FilePath)
	} else {
		// --config pointing at a missing file would already have failed
		// during PersistentPreRunE; this is the "no file anywhere" case.
		fmt.Println("No config file found; running on environment variables and built-in defaults.")
		fmt.Printf("Searched: %v\n", config.DefaultLocations())
	}
	return nil
}
