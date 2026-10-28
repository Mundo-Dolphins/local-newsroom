package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mundo-Dolphins/local-newsroom/internal/orchestrator"
	"github.com/Mundo-Dolphins/local-newsroom/internal/recipes"
	"github.com/Mundo-Dolphins/local-newsroom/internal/run"
)

// recipeFixtureTree writes a two-layer recipe tree under root:
//
//	root/common/recipes/afc-east-weekly.yaml   (base document)
//	root/authors/javi/recipes/afc-east-weekly.yaml (author override)
//
// and returns the absolute root path.
func recipeFixtureTree(t *testing.T, root string) string {
	t.Helper()

	base := `schema: "0.6.0"
id: afc-east-weekly
title: AFC East Weekly
version: "1.0.0"
output_format: article
description: Weekly recap of the AFC East.
inputs:
  - name: week
    type: int
    required: true
    min: 1
    max: 17
    description: The NFL week number.
research:
  instructions: "Cover the Week {week} results for each AFC East team."
blueprint:
  missing_policy: warn
  units:
    - role: intro
      title: "Week {week} recap"
      required: true
`
	override := `schema: "0.6.0"
id: afc-east-weekly
version: "1.1.0"
description: Authorized weekly recap of the AFC East, with author notes.
research:
  required_facts:
    - "Each team's Week {week} result"
`
	writeFile := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	writeFile("common/recipes/afc-east-weekly.yaml", base)
	writeFile("authors/javi/recipes/afc-east-weekly.yaml", override)

	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// loadManifestForDir reads the manifest of a specific run directory.
func loadManifestForDir(t *testing.T, dir string) *run.Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("reading manifest in %s: %v", dir, err)
	}
	m, err := run.UnmarshalManifest(data)
	if err != nil {
		t.Fatalf("unmarshaling manifest: %v", err)
	}
	return m
}

// TestRecipesList shows the resolved recipe with its merged summary fields.
func TestRecipesList(t *testing.T) {
	setHermeticEnv(t)
	root := recipeFixtureTree(t, t.TempDir())

	out := captureStdout(t, func() {
		if err := runCommand(t, "recipes", "list", "--profile-root", root, "--author", "javi"); err != nil {
			t.Fatalf("recipes list: %v", err)
		}
	})
	for _, want := range []string{
		"afc-east-weekly",
		"AFC East Weekly",
		"version: 1.1.0", // author layer version wins
		"format:  article",
		"layer:   author:javi",
		"inputs:  week*",
		"newsroom run --recipe",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("recipes list output lacks %q:\n%s", want, out)
		}
	}
}

// TestRecipesShow prints the merged document: author values override common,
// and additive fields (inputs, units) survive the merge.
func TestRecipesShow(t *testing.T) {
	setHermeticEnv(t)
	root := recipeFixtureTree(t, t.TempDir())

	out := captureStdout(t, func() {
		if err := runCommand(t, "recipes", "show", "afc-east-weekly", "--profile-root", root, "--author", "javi"); err != nil {
			t.Fatalf("recipes show: %v", err)
		}
	})
	for _, want := range []string{
		"description: Authorized weekly recap of the AFC East, with author notes.",
		"name: week",
		"role: intro",     // common-layer unit survives
		"required_facts:", // author-layer research field is additive
		"hash:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("recipes show output lacks %q:\n%s", want, out)
		}
	}
	// The common description must be overridden, not duplicated.
	if strings.Count(out, "description: Authorized weekly recap") != 1 || strings.Contains(out, "description: Weekly recap") {
		t.Errorf("merged document must carry the author description, not the common one:\n%s", out)
	}
	// The common inputs must survive the merge.
	if !strings.Contains(out, "min: 1") {
		t.Errorf("merged document must keep the common input constraints:\n%s", out)
	}
}

// TestRecipesListNoRoot fails when no recipes root is configured.
func TestRecipesListNoRoot(t *testing.T) {
	setHermeticEnv(t)
	err := runCommand(t, "recipes", "list")
	if err == nil {
		t.Fatal("recipes list without a root must fail")
	}
	if !strings.Contains(err.Error(), "no recipes root") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRunRecipeMissingRequiredInput: a recipe declaring a required input
// without a value fails before the run starts.
func TestRunRecipeMissingRequiredInput(t *testing.T) {
	setHermeticEnv(t)
	root := recipeFixtureTree(t, t.TempDir())
	h := newRunHarness(t, "pass")

	err := runCommand(t, append(h.baseArgs(),
		"--recipe", "afc-east-weekly",
		"--profile-root", root,
		"--url", h.articleURLs[0],
		"--url", h.articleURLs[1],
	)...)
	if err == nil {
		t.Fatal("run with a recipe that has an unsatisfied required input must fail")
	}
	if !strings.Contains(err.Error(), "week") {
		t.Errorf("error should name the missing input: %v", err)
	}
}

// TestRunRecipeForbidsExplicitURLs: a recipe whose source policy forbids
// explicit URLs rejects --url flags (the policy is engine-enforced).
func TestRunRecipeForbidsExplicitURLs(t *testing.T) {
	setHermeticEnv(t)
	root := t.TempDir()
	recipe := `schema: "0.6.0"
id: strict-recipe
title: Strict
version: "1.0.0"
inputs:
  - name: week
    type: int
    required: true
source_policy:
  explicit_urls: forbidden
`
	p := filepath.Join(root, "common", "recipes", "strict-recipe.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(recipe), 0o644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(root)
	h := newRunHarness(t, "pass")

	err := runCommand(t, append(h.baseArgs(),
		"--recipe", "strict-recipe",
		"--profile-root", abs,
		"--input", "week=7",
		"--url", h.articleURLs[0],
	)...)
	if err == nil {
		t.Fatal("a recipe that forbids explicit URLs must reject --url")
	}
	if !strings.Contains(err.Error(), "forbids explicit URLs") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestRunRecipe runs a full recipe run (explicit sources) and verifies the
// bounded manifest reference, the persisted resolved recipe, and the derived
// topic — while the run itself behaves like any other.
func TestRunRecipe(t *testing.T) {
	setHermeticEnv(t)
	root := recipeFixtureTree(t, t.TempDir())
	h := newRunHarness(t, "pass")

	args := append(h.baseArgs(),
		"--recipe", "afc-east-weekly",
		"--recipe-author", "javi",
		"--profile-root", root,
		"--input", "week=7",
		"--url", h.articleURLs[0],
		"--url", h.articleURLs[1],
	)
	// baseArgs sets --topic; a recipe run derives its own, so drop it.
	keep := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--topic" {
			i++
			continue
		}
		keep = append(keep, args[i])
	}
	if err := runCommand(t, keep...); err != nil {
		t.Fatalf("recipe run should succeed: %v", err)
	}

	m := h.loadManifest(t)
	if m.Status != run.RunStatusSucceeded {
		t.Fatalf("manifest status = %s, want succeeded", m.Status)
	}
	if m.Recipe == nil {
		t.Fatal("manifest must record the selected recipe")
	}
	if m.Recipe.ID != "afc-east-weekly" {
		t.Errorf("recipe id = %q", m.Recipe.ID)
	}
	if m.Recipe.Version != "1.1.0" {
		t.Errorf("recipe version = %q, want 1.1.0 (merged author layer)", m.Recipe.Version)
	}
	if m.Recipe.Author != "javi" {
		t.Errorf("recipe author = %q, want javi (from --recipe-author? no: from profiles name? check)", m.Recipe.Author)
	}
	if m.Recipe.Inputs["week"] != "7" {
		t.Errorf("recipe inputs = %v, want week=7", m.Recipe.Inputs)
	}
	if len(m.Recipe.Hash) != 64 {
		t.Errorf("recipe hash = %q, want 64 hex chars", m.Recipe.Hash)
	}
	// The topic derives from the recipe (title + inputs).
	if !strings.Contains(m.Topic, "AFC East Weekly") {
		t.Errorf("derived topic = %q, want the recipe title", m.Topic)
	}
	if v, ok := m.Config.Get(orchestrator.SettingRunFormat); !ok || v != "article" {
		t.Errorf("run.format = %q (present=%v), want article (recipe-declared)", v, ok)
	}

	// The resolved recipe is persisted in the run directory.
	recipePath := filepath.Join(h.findRunDir(t), "recipe", "resolved.json")
	data, err := os.ReadFile(recipePath)
	if err != nil {
		t.Fatalf("missing persisted resolved recipe: %v", err)
	}
	var res recipes.Resolved
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("unmarshaling persisted recipe: %v", err)
	}
	if res.Hash != m.Recipe.Hash {
		t.Errorf("persisted recipe hash %s does not match the manifest record %s", res.Hash, m.Recipe.Hash)
	}
	// And the content plan was persisted by the write stage.
	if !fileExistsPath(filepath.Join(h.findRunDir(t), "write", "content-plan.json")) {
		t.Error("missing persisted content plan")
	}
}

// TestRunRecipeResumeTampered covers the resume hash contract in both
// directions: a tampered persisted resolved recipe (the run's own record)
// and a tampered on-disk recipe (re-resolution anchored on the manifest).
func TestRunRecipeResumeTampered(t *testing.T) {
	setHermeticEnv(t)
	root := recipeFixtureTree(t, t.TempDir())
	h := newRunHarness(t, "fail") // the run fails at the final check → resumable

	recipeArgs := []string{
		"--recipe", "afc-east-weekly",
		"--recipe-author", "javi",
		"--profile-root", root,
		"--input", "week=7",
	}
	explicitArgs := append(append([]string{}, h.baseArgs()...),
		"--url", h.articleURLs[0],
		"--url", h.articleURLs[1],
	)
	// Drop the --topic the harness sets: a recipe run derives its topic.
	strip := func(args []string) []string {
		out := make([]string, 0, len(args))
		for i := 0; i < len(args); i++ {
			if args[i] == "--topic" {
				i++
				continue
			}
			out = append(out, args[i])
		}
		return out
	}

	// The first run (explicit sources) fails at the final check: the recipe
	// resolved fine, discovery persisted the resolved recipe, and the
	// manifest records the bounded reference.
	args := append(append([]string{}, explicitArgs...), recipeArgs...)
	if err := runCommand(t, strip(args)...); err == nil {
		t.Fatal("first run should fail at the final check")
	}
	m := h.loadManifest(t)
	if m.Status != run.RunStatusFailed {
		t.Fatalf("status = %s, want failed", m.Status)
	}
	if m.Recipe == nil || m.Recipe.Hash == "" {
		t.Fatal("failed recipe run must still record the recipe reference")
	}
	runDir := h.findRunDir(t)
	resumeArgs := func(id, searchURL string) []string {
		args := []string{"run", "--resume", id, "--workspace-root", h.workspace,
			"--llm-base-url", h.llmSrv.URL, "--llm-api-key", "test-key", "--llm-model", "test-model"}
		if searchURL != "" {
			args = append(args, "--search-base-url", searchURL)
		}
		return args
	}

	t.Run("persisted recipe altered", func(t *testing.T) {
		p := filepath.Join(runDir, "recipe", "resolved.json")
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read persisted recipe: %v", err)
		}
		// Corrupt the recorded hash so the run record is inconsistent.
		corrupted := strings.Replace(string(data), m.Recipe.Hash, strings.Repeat("0", 64), 1)
		if corrupted == string(data) {
			t.Fatalf("could not corrupt the persisted hash")
		}
		if err := os.WriteFile(p, []byte(corrupted), 0o644); err != nil {
			t.Fatal(err)
		}
		h.setCheckVerdict("pass")
		err = runCommand(t, resumeArgs(string(m.ID), "")...)
		if err == nil {
			t.Fatal("resuming with a tampered persisted recipe must fail")
		}
		if !strings.Contains(err.Error(), "refusing to resume") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("on-disk recipe altered", func(t *testing.T) {
		src := filepath.Join(root, "common", "recipes", "afc-east-weekly.yaml")
		orig, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.WriteFile(src, orig, 0o644); err != nil {
				t.Errorf("restoring recipe fixture: %v", err)
			}
		})

		// A discovered run whose recipe requires a domain no candidate
		// satisfies fails INSIDE discovery — before the resolved recipe is
		// persisted — so a resume must re-resolve from disk (the hash
		// anchor is the manifest record).
		withPolicy := strings.Replace(string(orig), "blueprint:", "source_policy:\n  required_domains:\n    - \".nomatch.example\"\nblueprint:", 1)
		if err := os.WriteFile(src, []byte(withPolicy), 0o644); err != nil {
			t.Fatal(err)
		}
		searxng := newFakeSearxngServer(t, map[string][]fakeSearxngResult{
			"Miami": {
				{url: h.articleURLs[0], title: "Alpha", snippet: "s1", rank: 1},
				{url: h.articleURLs[1], title: "Beta", snippet: "s2", rank: 2},
			},
		})
		discovered := append(append([]string{}, h.baseArgs()...), "--search-base-url", searxng.URL)
		discovered = append(discovered, recipeArgs...)
		if err := runCommand(t, strip(discovered)...); err == nil {
			t.Fatal("discovery must fail: the required domain is satisfied by no candidate")
		}
		// Locate the discovered run (the newest one).
		matches, _ := filepath.Glob(filepath.Join(h.workspace, "runs", "*", "manifest.json"))
		if len(matches) != 2 {
			t.Fatalf("expected 2 runs, found %d", len(matches))
		}
		var newDir string
		for _, path := range matches {
			if dir := filepath.Dir(path); dir != runDir {
				newDir = dir
			}
		}
		nm := loadManifestForDir(t, newDir)

		// A failed stage persists no files, so no resolved recipe exists on
		// disk: the resume re-resolves from the recipe files, anchored on
		// the manifest record. Change the COMMON layer's title (the author
		// file sets no title, so the merge keeps it) and the hash must
		// move.
		tampered := strings.Replace(withPolicy, "title: AFC East Weekly", "title: Tampered AFC East Weekly", 1)
		if err := os.WriteFile(src, []byte(tampered), 0o644); err != nil {
			t.Fatal(err)
		}
		err = runCommand(t, resumeArgs(string(nm.ID), searxng.URL)...)
		if err == nil {
			t.Fatal("resuming after the on-disk recipe changed must fail")
		}
		if !strings.Contains(err.Error(), "differs from") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
