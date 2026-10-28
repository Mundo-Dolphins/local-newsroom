package recipes

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
)

// Loader resolves recipes from a base tree (the profiles layout: <root>/common
// and <root>/authors/<author>). The base is usually the editorial bundle root
// (when a publication bundle is in use) or the profiles root.
//
// Load-time safety (all fail-closed, mirrored from the editorial bundle and
// profiles loaders):
//   - files are size-bounded ([MaxFileBytes]) and path-contained;
//   - YAML is parsed with KnownFields: unknown keys are rejected (typos and
//     stale sections are errors, not silently ignored);
//   - every YAML key is secret-scanned (config.ValidateSecretKeys): a recipe
//     may not contain literal credentials;
//   - the document schema version must match [SchemaVersion];
//   - every free-form text field passes the instruction-override scan.
type Loader struct {
	root string // absolute, clean base tree
}

// NewLoader validates the base tree and returns a loader. The root must
// exist; an empty/missing recipes directory simply yields "no recipes".
func NewLoader(root string) (*Loader, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("recipes: resolving base tree: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("recipes: base tree %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("recipes: base tree %s is not a directory", abs)
	}
	return &Loader{root: abs}, nil
}

// Root returns the absolute base tree.
func (l *Loader) Root() string { return l.root }

// layerDir returns the recipes directory of one layer.
func (l *Loader) layerDir(layer string) string {
	switch layer {
	case LayerCommon:
		return filepath.Join(l.root, CommonDir, RecipesDir)
	default: // "author:<name>"
		name := strings.TrimPrefix(layer, layerAuthorPrefix)
		return filepath.Join(l.root, AuthorsDir, name, RecipesDir)
	}
}

// recipeFileNames are the accepted recipe file extensions.
var recipeFileNames = map[string]bool{".yaml": true, ".yml": true}

// findRecipeFile locates <recipesDir>/**/<id>.yaml (yml) in one layer.
// Nested subdirectories are allowed (feature families); the file name
// (extension stripped) must equal id.
func (l *Loader) findRecipeFile(layer, id string) (string, error) {
	dir := l.layerDir(layer)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: layer %s has no recipes directory", ErrNotFound, layer)
		}
		return "", err
	}
	var found []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		ext := filepath.Ext(name)
		if !recipeFileNames[ext] {
			return nil
		}
		if strings.TrimSuffix(name, ext) != id {
			return nil
		}
		found = append(found, filepath.ToSlash(path))
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("recipes: walking %s: %w", dir, err)
	}
	if len(found) == 0 {
		return "", fmt.Errorf("%w: %s in %s", ErrNotFound, id, dir)
	}
	if len(found) > 1 {
		sort.Strings(found)
		return "", fmt.Errorf("%w: %s matches %d files (%s, %s...)", ErrDuplicateID, id, len(found), found[0], found[1])
	}
	return found[0], nil
}

// readRecipeFile reads and parses one recipe file (size-bounded,
// KnownFields, secret-scanned) and validates it.
func (l *Loader) readRecipeFile(absPath string) (*Recipe, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxFileBytes {
		rel, _ := filepath.Rel(l.root, absPath)
		return nil, fmt.Errorf("%w: %s (size %d, limit %d)", ErrTooLarge, rel, info.Size(), MaxFileBytes)
	}
	data, err := os.ReadFile(absPath) // #nosec G304 -- path is loader-built (validated containment in findRecipeFile)
	if err != nil {
		return nil, err
	}
	var r Recipe
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, filepath.Base(absPath), err)
	}
	// Literal credentials are forbidden in recipes (they are prompt
	// material and manifest material).
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, filepath.Base(absPath), err)
	}
	if err := config.ValidateSecretKeys(&doc); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, filepath.Base(absPath), err)
	}
	// The id must equal the file name (no extension).
	wantID := strings.TrimSuffix(filepath.Base(absPath), filepath.Ext(absPath))
	if r.ID != wantID {
		return nil, fmt.Errorf("%w: %s declares id %q but the file name is %q", ErrInvalid, filepath.Base(absPath), r.ID, wantID)
	}
	if err := r.ValidateLayer(); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(absPath), err)
	}
	return &r, nil
}

// Resolve loads and merges the recipe with the given id, applying the author
// layer when author != "". It returns the resolved recipe with its
// provenance (layers, root) and hash, plus the loaded example documents.
//
// Precedence: engine schema/safety → common layer → author layer. Explicit
// run inputs are NOT part of this merge: they supply values only and are
// resolved separately via [Recipe.ResolveInputs].
func (l *Loader) Resolve(id, author string) (*Resolved, error) {
	if !idRe.MatchString(id) {
		return nil, fmt.Errorf("%w: recipe id %q is not a valid identifier", ErrInvalid, id)
	}
	var common, authorRec *Recipe
	var layers []string

	if path, err := l.findRecipeFile(LayerCommon, id); err == nil {
		r, err := l.readRecipeFile(path)
		if err != nil {
			return nil, err
		}
		common = r
		rel, _ := filepath.Rel(l.root, path)
		layers = append(layers, filepath.ToSlash(rel))
	} else if !isNotFound(err) {
		return nil, err
	}

	if author != "" {
		layer := layerAuthorPrefix + author
		path, err := l.findRecipeFile(layer, id)
		switch {
		case err == nil:
			r, rerr := l.readRecipeFile(path)
			if rerr != nil {
				return nil, rerr
			}
			authorRec = r
			rel, _ := filepath.Rel(l.root, path)
			layers = append(layers, filepath.ToSlash(rel))
		case isNotFound(err):
			// author layer simply absent: fine
		default:
			return nil, err
		}
	}

	if common == nil && authorRec == nil {
		return nil, fmt.Errorf("%w: recipe %q not found (looked in %s/common/%s%s%s)",
			ErrNotFound, id, l.root, CommonDir, string(filepath.Separator), RecipesDir)
	}

	res := &Recipe{}
	*res = *merge(common, authorRec)
	// Re-validate the merged document (merge can combine half-declarations
	// into a whole that neither layer was alone guilty of).
	if err := res.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %s (merged)", ErrInvalid, id)
	}

	// Load examples (size/line bounded; content becomes prompt material).
	examples, err := l.loadExamples(res)
	if err != nil {
		return nil, err
	}

	return &Resolved{
		Recipe:   res,
		Layers:   layers,
		Root:     l.root,
		Author:   author,
		Hash:     res.Hash(),
		Examples: examples,
	}, nil
}

// List returns the available recipes for the given author ("" = common
// only), with the author layer winning for shared ids.
func (l *Loader) List(author string) ([]Summary, error) {
	byID := make(map[string]Summary)
	add := func(layer string) error {
		dir := l.layerDir(layer)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			ext := filepath.Ext(name)
			if !recipeFileNames[ext] {
				continue
			}
			id := strings.TrimSuffix(name, ext)
			if !idRe.MatchString(id) {
				continue // not a recipe (stray file)
			}
			r, err := l.readRecipeFile(filepath.Join(dir, name))
			if err != nil {
				return err // an invalid recipe file is an error, not skipped
			}
			s := Summary{
				ID:           id,
				Title:        r.Title,
				Description:  r.Description,
				Version:      r.Version,
				OutputFormat: r.OutputFormat,
				Layer:        layer,
				Inputs:       r.Inputs,
			}
			// Merge over the common entry when this is the author layer: the
			// author's SET values win, unset fields keep the common values,
			// and inputs are merged by name (additive, like Resolve).
			if prev, ok := byID[id]; ok && layer != LayerCommon {
				if s.Title == "" {
					s.Title = prev.Title
				}
				if s.Description == "" {
					s.Description = prev.Description
				}
				if s.Version == "" {
					s.Version = prev.Version
				}
				if s.OutputFormat == "" {
					s.OutputFormat = prev.OutputFormat
				}
				merged := make([]Input, 0, len(prev.Inputs)+len(s.Inputs))
				merged = append(merged, prev.Inputs...)
				for _, in := range s.Inputs {
					found := false
					for i := range merged {
						if merged[i].Name == in.Name {
							merged[i] = in
							found = true
						}
					}
					if !found {
						merged = append(merged, in)
					}
				}
				s.Inputs = merged
			}
			byID[id] = s
		}
		return nil
	}
	if err := add(LayerCommon); err != nil {
		return nil, err
	}
	if author != "" {
		if err := add(layerAuthorPrefix + author); err != nil {
			return nil, err
		}
	}
	out := make([]Summary, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// loadExamples reads the recipe's example documents with the profile
// example limits (per-file bytes + combined line count).
func (l *Loader) loadExamples(r *Recipe) ([]Example, error) {
	if len(r.Examples) == 0 {
		return nil, nil
	}
	out := make([]Example, 0, len(r.Examples))
	totalLines := 0
	for _, rel := range r.Examples {
		data, err := safeFile(l.root, rel, MaxExampleFileBytes)
		if err != nil {
			return nil, fmt.Errorf("recipes: example %s: %w", rel, err)
		}
		text := string(data)
		lines := strings.Count(text, "\n")
		if text != "" && !strings.HasSuffix(text, "\n") {
			lines++
		}
		totalLines += lines
		if totalLines > MaxExampleTotalLines {
			return nil, fmt.Errorf("%w: examples total %d lines (limit %d)", ErrTooLarge, totalLines, MaxExampleTotalLines)
		}
		out = append(out, Example{
			Path:     filepath.ToSlash(rel),
			Content:  text,
			Lines:    lines,
			Bytes:    len(data),
			HasImage: strings.Contains(text, "![") || strings.Contains(text, "<img"),
		})
	}
	return out, nil
}

// isNotFound reports whether err (or any wrapped error) is [ErrNotFound].
func isNotFound(err error) bool {
	for err != nil {
		if err == ErrNotFound {
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
