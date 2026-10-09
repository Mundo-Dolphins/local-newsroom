package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// showVersion prints the version. In the future this will be replaced by a
// real version command.
func printVersion(cmd *cobra.Command, args []string) error {
	fmt.Println(cmd.VersionTemplate())
	return nil
}

var rootCmd = &cobra.Command{
	Use:   "newsroom",
	Short: "newsroom CLI",
	Long: `newsroom is the local-newsroom CLI.

It provides the v0.4 editorial pipeline stages (research, verify, write,
final-check) and the local archive. Persistent configuration is supported
via a YAML config file; every setting resolves with the precedence:

  CLI flags > environment variables > config file > built-in defaults

See 'newsroom config --help' for configuration management.`,
	Version: "0.5.0",
	RunE:    printVersion,
}

var helpCmd = &cobra.Command{
	Use:   "help",
	Short: "print the newsroom pipeline stages",
	Long:  `Prints the pipeline stages the internal packages are reserved for. A placeholder until the orchestrator is implemented.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println(rootCmd.Long)
		return nil
	},
	// Help must work even when the config file is missing or invalid, so
	// break the persistent-pre-run inheritance from the root.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error { return nil },
}

func init() {
	flags := pflag.NewFlagSet("newsroom", pflag.ExitOnError)
	flags.BoolVar(&versionFlag, "version", false, "print the newsroom version and exit")
	rootCmd.PersistentFlags().AddFlagSet(flags)
	rootCmd.PersistentFlags().StringVar(&configPathFlag, "config", "",
		"Path to the newsroom config file (default: $NEWSROOM_CONFIG, ./.newsroom.yaml, $XDG_CONFIG_HOME/newsroom/config.yaml, ~/.newsroom.yaml)")
	// Resolve the persistent configuration (config file + environment +
	// built-in defaults) before every command runs. Version printing and
	// help bypass the load so they work even with a broken config file.
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if versionFlag {
			return nil
		}
		return loadAppConfig()
	}
	rootCmd.AddCommand(helpCmd)
}

var versionFlag bool

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", rootCmd.CommandPath(), err)
		os.Exit(1)
	}
}
