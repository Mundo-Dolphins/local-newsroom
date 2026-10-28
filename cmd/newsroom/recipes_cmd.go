package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/recipes"
)

// recipesCmd lists and inspects the editorial recipes (v0.6) available to a
// run. Recipes are data: `list` shows what a run can select, `show` prints
// the resolved document (common layer + author override, merged) exactly as
// the engine would use it.
var recipesCmd = &cobra.Command{
	Use:     "recipes",
	Short:   "List and inspect editorial recipes",
	Long:    `List the editorial recipes available for newsroom run --recipe, and print the resolved (merged) document for one of them.`,
	Aliases: []string{"recipe"},
}

// recipesFlags are the recipes command flags.
type recipesFlags struct {
	author      string
	profileRoot string
	bundleRoot  string
	publication string
}

var rc recipesFlags

func init() {
	// Persistent: the subcommands (list / show) must see them too — a run of
	// `recipes list --profile-root X` parses them on the subcommand.
	f := recipesCmd.PersistentFlags()
	f.StringVar(&rc.author, "author", "", "author layer for recipe resolution (default: profiles.name from the configuration chain)")
	f.StringVar(&rc.profileRoot, "profile-root", "", "profiles root directory (default: profiles.root from the configuration chain)")
	f.StringVar(&rc.bundleRoot, "bundle-root", "", "editorial bundle directory (default: editorial.bundle_root from the configuration chain)")
	f.StringVar(&rc.publication, "publication", "", "publication preset in the editorial bundle (default: editorial.publication from the configuration chain)")

	recipesCmd.AddCommand(recipesListCmd, recipesShowCmd)
	rootCmd.AddCommand(recipesCmd)
}

// recipesRoot resolves the recipes base tree for the commands: the editorial
// bundle root when a publication is selected, otherwise the profiles root —
// the same rule a run uses.
func recipesRoot(cmd *cobra.Command) (string, error) {
	overrideFor := func(setting string) config.Override {
		f := cmd.Flags().Lookup(flagForSetting(setting))
		if f == nil || !f.Changed {
			return config.Override{}
		}
		return config.FromFlag(f.Value.String(), true)
	}
	get := func(setting string) (string, error) {
		v, _, err := appConfig.GetString(setting, overrideFor(setting))
		return v, err
	}
	publication, err := get(config.SettingEditorialPublication)
	if err != nil {
		return "", err
	}
	if publication != "" {
		root, err := get(config.SettingEditorialBundle)
		if err != nil {
			return "", err
		}
		if root == "" {
			return "", fmt.Errorf("publication %q is selected but no editorial bundle root is set (pass --bundle-root or set editorial.bundle_root)", publication)
		}
		return root, nil
	}
	return get(config.SettingProfilesRoot)
}

// flagForSetting maps the recipes command's flag names onto config settings
// (same pairs the run command uses for the same flags).
func flagForSetting(setting string) string {
	switch setting {
	case config.SettingEditorialPublication:
		return "publication"
	case config.SettingEditorialBundle:
		return "bundle-root"
	case config.SettingProfilesRoot:
		return "profile-root"
	}
	return ""
}

// recipesLoader builds the recipe loader from the resolved root. The author
// layer falls back to the profiles.name setting when --author is unset (the
// built-in default name is not a recipe author layer).
// recipesLoader builds the recipe loader from the resolved root. The author
// layer falls back to the profiles.name setting when --author is unset (the
// built-in default name is not a recipe author layer); it returns the
// effective author together with the loader.
func recipesLoader(root, author string) (string, *recipes.Loader, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", nil, fmt.Errorf("no recipes root: pass --profile-root or set profiles.root (or select a publication with a bundle root)")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", nil, fmt.Errorf("resolving recipes root: %w", err)
	}
	if author == "" {
		if v, _, err := appConfig.GetString(config.SettingProfilesName, config.Override{}); err == nil && v != "default" && !strings.ContainsRune(v, '/') {
			author = v
		}
	}
	loader, err := recipes.NewLoader(root)
	if err != nil {
		return "", nil, fmt.Errorf("recipes root %s: %w", root, err)
	}
	return author, loader, nil
}

var recipesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available editorial recipes",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := recipesRoot(cmd)
		if err != nil {
			return err
		}
		author, loader, err := recipesLoader(root, rc.author)
		if err != nil {
			return err
		}
		summaries, err := loader.List(author)
		if err != nil {
			return err
		}
		if len(summaries) == 0 {
			fmt.Printf("No recipes found under %s (author layer: %q). Recipes are YAML files in common/recipes/ or authors/<author>/recipes/.\n", root, rc.author)
			return nil
		}
		for i, s := range summaries {
			if i > 0 {
				fmt.Println()
			}
			title := s.Title
			if title == "" {
				title = s.ID
			}
			fmt.Printf("%s  (%s)\n", title, s.ID)
			if s.Version != "" {
				fmt.Printf("  version: %s\n", s.Version)
			}
			if s.OutputFormat != "" {
				fmt.Printf("  format:  %s\n", s.OutputFormat)
			}
			if s.Layer != "" {
				fmt.Printf("  layer:   %s\n", s.Layer)
			}
			if s.Description != "" {
				fmt.Printf("  %s\n", s.Description)
			}
			if len(s.Inputs) > 0 {
				var parts []string
				for _, in := range s.Inputs {
					label := in.Name
					if in.Required {
						label += "*"
					}
					if in.Default != "" {
						label += " (default " + in.Default + ")"
					}
					if in.Type != "" {
						label += " [" + in.Type + "]"
					}
					parts = append(parts, label)
				}
				fmt.Printf("  inputs:  %s%s\n", strings.Join(parts, ", "), "  (* required)")
			}
		}
		fmt.Println("\nRun one with: newsroom run --recipe <id> --input name=value")
		return nil
	},
}

var recipesShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Print the resolved (merged) document of one editorial recipe",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := recipesRoot(cmd)
		if err != nil {
			return err
		}
		author, loader, err := recipesLoader(root, rc.author)
		if err != nil {
			return err
		}
		resolved, err := loader.Resolve(args[0], author)
		if err != nil {
			return fmt.Errorf("recipe %q: %w", args[0], err)
		}
		fmt.Printf("# recipe %s (author layer: %q, hash: %s)\n", resolved.Recipe.ID, resolved.Author, shortHash(resolved.Hash))
		data, err := marshalRecipeYAML(resolved.Recipe)
		if err != nil {
			return fmt.Errorf("rendering recipe: %w", err)
		}
		fmt.Println(string(data))
		if len(resolved.Examples) > 0 {
			fmt.Println("# attached examples:")
			for _, ex := range resolved.Examples {
				fmt.Printf("  %s\n", ex.Path)
			}
		}
		return nil
	},
}

// marshalRecipeYAML renders a resolved recipe as indented YAML for display.
func marshalRecipeYAML(r *recipes.Recipe) ([]byte, error) {
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return []byte(strings.TrimRight(buf.String(), "\n")), nil
}
