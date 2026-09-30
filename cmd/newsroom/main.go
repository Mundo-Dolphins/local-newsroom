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
	Use:     "newsroom",
	Short:   "newsroom CLI",
	Long:    `newsroom is the local-newsroom CLI. The pipeline stages are not implemented yet.`,
	Version: "0.0.0",
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
}

// Add more commands here as the internal packages get implemented:
//
//	var researchCmd = &cobra.Command{
//	    Use:   "research",
//	    Short: "research collected material",
//	    RunE: doResearch,
//	}
//	rootCmd.AddCommand(researchCmd)

func init() {
	flags := pflag.NewFlagSet("newsroom", pflag.ExitOnError)
	flags.BoolVar(&versionFlag, "version", false, "print the newsroom version and exit")
	rootCmd.PersistentFlags().AddFlagSet(flags)
	rootCmd.AddCommand(helpCmd)
}

var versionFlag bool

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", rootCmd.CommandPath(), err)
		os.Exit(1)
	}
}
