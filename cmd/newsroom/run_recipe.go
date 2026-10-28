package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Mundo-Dolphins/local-newsroom/internal/contentplan"
	"github.com/Mundo-Dolphins/local-newsroom/internal/editorial"
	"github.com/Mundo-Dolphins/local-newsroom/internal/llm"
	"github.com/Mundo-Dolphins/local-newsroom/internal/recipes"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
	"github.com/Mundo-Dolphins/local-newsroom/internal/workspace"
)

// recipeRoot returns the base tree a recipe is read from for a run: the
// editorial bundle root when a publication preset is selected (recipes live
// with the bundle's profile layout), otherwise the profiles root (the
// historical profile layout).
func recipeRoot(r *runResolved, preset *editorial.Preset) string {
	if preset != nil {
		return r.bundleRoot
	}
	return r.profilesRoot
}

// recipeAuthor determines the author layer used to resolve a recipe: the
// explicit --recipe-author wins, then the run's profile author (the author
// segment of --profile), then the profiles.name setting when it names a
// simple author.
func recipeAuthor(r *runResolved, intent runIntent) string {
	if a := strings.TrimSpace(intent.RecipeAuthor); a != "" {
		return a
	}
	if spec := intent.ProfileSpec; spec != "" {
		if i := strings.LastIndex(spec, "/"); i >= 0 && i < len(spec)-1 {
			return spec[i+1:]
		}
		if !strings.Contains(spec, "/") {
			return spec
		}
	}
	if r.profilesNameSet && !strings.ContainsRune(r.profilesName, '/') {
		return r.profilesName
	}
	return ""
}

// resolveRecipe loads the run's editorial recipe (v0.6) and prepares
// everything a recipe run needs from it:
//
//   - the resolved recipe document (common layer + author override, merged
//     and validated),
//   - the resolved input values (explicit --input values win over defaults,
//     type and constraint checks included),
//   - the bounded manifest reference,
//   - the run's topic and output format, derived from the recipe where the
//     operator left them empty,
//   - the source policy's explicit_urls stance, enforced against the
//     operator's --url flags.
//
// The resolved document itself is written into the run directory by the
// discovery stage; nothing about it lands in the manifest beyond the bounded
// reference.
func resolveRecipe(r *runResolved, preset *editorial.Preset, intent runIntent) (*run.Recipe, *recipes.Resolved, []recipes.ResolvedInput, runIntent, error) {
	id := strings.TrimSpace(intent.RecipeID)
	if id == "" {
		return nil, nil, nil, intent, fmt.Errorf("an empty recipe id was selected")
	}
	root := strings.TrimSpace(recipeRoot(r, preset))
	if root == "" {
		return nil, nil, nil, intent, fmt.Errorf(
			"recipe %q needs a recipes root: pass --profile-root or set profiles.root (or select a publication whose bundle root is set)", id)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, nil, intent, fmt.Errorf("resolving recipes root: %w", err)
	}
	author := recipeAuthor(r, intent)

	loader, err := recipes.NewLoader(root)
	if err != nil {
		return nil, nil, nil, intent, fmt.Errorf("recipes root %s: %w", root, err)
	}
	resolved, err := loader.Resolve(id, author)
	if err != nil {
		return nil, nil, nil, intent, fmt.Errorf("recipe %q: %w", id, err)
	}

	inputs, err := resolved.Recipe.ResolveInputs(intent.RecipeInputs)
	if err != nil {
		return nil, nil, nil, intent, fmt.Errorf("recipe %q: %w", id, err)
	}

	// Topic: the operator's --topic wins; otherwise the recipe's topic
	// template, rendered with the resolved inputs.
	if strings.TrimSpace(intent.Topic) == "" {
		topic := resolved.Recipe.Topic(inputs)
		if strings.TrimSpace(topic) == "" {
			return nil, nil, nil, intent, fmt.Errorf("recipe %q has no topic and none was given (--topic)", id)
		}
		intent.Topic = topic
	}

	// Output format: the operator's --format wins; otherwise the recipe's
	// declared output format; otherwise the configured default (applied by
	// the caller).
	if strings.TrimSpace(intent.Format) == "" && resolved.Recipe.OutputFormat != "" {
		intent.Format = resolved.Recipe.OutputFormat
	}

	// Source policy explicit_urls stance.
	if sp := resolved.Recipe.Source; sp != nil && len(intent.URLs) > 0 {
		switch sp.ExplicitURLs {
		case "forbidden":
			return nil, nil, nil, intent, fmt.Errorf("recipe %q forbids explicit URLs (source policy) but %d were provided via --url", id, len(intent.URLs))
		}
	}

	inputValues := make(map[string]string, len(inputs))
	for _, in := range inputs {
		inputValues[in.Name] = in.Value
	}
	ref := &run.Recipe{
		ID:         id,
		Version:    resolved.Recipe.Version,
		Author:     resolved.Author,
		BundlePath: root,
		Hash:       resolved.Hash,
		Inputs:     inputValues,
	}
	if err := ref.Validate(); err != nil {
		return nil, nil, nil, intent, fmt.Errorf("recipe %q reference: %w", id, err)
	}
	return ref, resolved, inputs, intent, nil
}

// resumeRecipe recovers the recipe for a resumed run, in order of fidelity:
//
//  1. the run's own persisted resolved recipe (recipe/resolved.json), when
//     present — its hash must match the manifest record (a mismatch fails
//     closed; it would mean the run's own record drifted), and
//  2. otherwise a fresh re-resolution from the recorded bundle path and
//     author, whose hash must match the manifest record (a mismatch fails
//     closed: the recipe changed on disk since the run started, and a
//     resumed run must behave like the original one).
//
// The recorded resolved inputs (name → value) are re-validated against the
// recovered recipe so their types and constraints are honored on resume.
func resumeRecipe(m *run.Manifest, store *workspace.Store, r *runResolved) (*recipes.Resolved, []recipes.ResolvedInput, error) {
	ref := m.Recipe
	if ref == nil {
		return nil, nil, nil
	}

	var resolved *recipes.Resolved
	data, err := store.LoadArtifact(m.ID, run.ArtifactPathResolvedRecipe)
	if err == nil {
		var rec recipes.Resolved
		if err := json.Unmarshal(data, &rec); err != nil {
			return nil, nil, fmt.Errorf("run %s: unreadable persisted recipe: %w", m.ID, err)
		}
		if ref.Hash != "" && rec.Hash != "" && rec.Hash != ref.Hash {
			return nil, nil, fmt.Errorf("run %s: persisted recipe hash %s… does not match the manifest record %s… — the run record is inconsistent, refusing to resume",
				m.ID, shortHash(rec.Hash), shortHash(ref.Hash))
		}
		resolved = &rec
	} else {
		// No persisted recipe (older run or a partial first stage): re-derive
		// from the recorded root and anchor on the manifest hash.
		root := ref.BundlePath
		if root == "" {
			root = r.bundleRoot
		}
		if root == "" {
			root = r.profilesRoot
		}
		if root == "" {
			return nil, nil, fmt.Errorf("run %s recorded recipe %q but no recipes root is available to re-resolve it (pass --profile-root or --bundle-root)", m.ID, ref.ID)
		}
		root, err := filepath.Abs(root)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving recipes root: %w", err)
		}
		loader, err := recipes.NewLoader(root)
		if err != nil {
			return nil, nil, fmt.Errorf("recipes root %s: %w", root, err)
		}
		resolved, err = loader.Resolve(ref.ID, ref.Author)
		if err != nil {
			return nil, nil, fmt.Errorf("re-resolving recipe %q for run %s: %w", ref.ID, m.ID, err)
		}
		if ref.Hash != "" && resolved.Hash != ref.Hash {
			return nil, nil, fmt.Errorf("recipe %q on disk differs from what run %s recorded (recorded %s…, found %s…): the recipe files changed since the run started; restore them or start a new run",
				ref.ID, m.ID, shortHash(ref.Hash), shortHash(resolved.Hash))
		}
	}

	inputs, err := resolved.Recipe.ResolveInputs(ref.Inputs)
	if err != nil {
		return nil, nil, fmt.Errorf("run %s recipe %q inputs: %w", m.ID, ref.ID, err)
	}
	return resolved, inputs, nil
}

// recipeResearchInstructions renders the recipe's research guidance for the
// researcher prompt: the recipe's instructions plus its required facts,
// presented as advisory (the recipe is untrusted content).
func recipeResearchInstructions(res *recipes.Resolved) string {
	if res == nil {
		return ""
	}
	rs := res.Recipe.Research
	if rs == nil {
		return ""
	}
	var b strings.Builder
	if rs.Instructions != "" {
		b.WriteString(rs.Instructions)
	}
	if len(rs.RequiredFacts) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Facts to cover where evidence supports them:\n")
		for _, f := range rs.RequiredFacts {
			b.WriteString("- ")
			b.WriteString(f)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// recipeWriterInstructions renders the recipe's writing guidance for the
// writer prompt: the worked examples the recipe attaches (bounded at load).
func recipeWriterInstructions(res *recipes.Resolved) string {
	if res == nil || len(res.Examples) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Worked examples from the selected recipe (style references only; follow your factual rules):\n\n")
	for _, ex := range res.Examples {
		fmt.Fprintf(&b, "### Example: %s\n\n", ex.Path)
		b.WriteString(ex.Content)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

// buildContentPlanner builds the content planner for a recipe run (nil for
// standard runs). The planner turns the recipe's blueprint into the
// deterministic content plan with per-unit claim traceability.
func buildContentPlanner(llm *llm.OpenAICompatClient, model string, res *recipes.Resolved) (contentplan.Service, error) {
	if res == nil {
		return nil, nil
	}
	return contentplan.New(llm, model, contentplan.DefaultConfig())
}
