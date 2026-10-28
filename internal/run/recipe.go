package run

import (
	"errors"
	"fmt"
	"regexp"
)

// Recipe identifies the editorial recipe selected for a run (v0.6 editorial
// recipes) and carries the resolved, non-secret input values.
//
// Only identity and provenance are recorded — recipe *content* is never part
// of the manifest: the full resolved recipe is persisted in the run directory
// (recipe/resolved.json, written by the discovery stage) so a resumed run
// executes the exact recipe it started with. The hash anchors that document;
// the inputs are the values the run resolved (bounded, redacted) so a resume
// never re-derives user data.
type Recipe struct {
	// ID is the recipe identifier (kebab-case, matches the recipe file).
	ID string `json:"id"`

	// Version is the resolved recipe's version string.
	Version string `json:"version,omitempty"`

	// Author is the author layer the run resolved the recipe with ("" when
	// only the common layer applied); a resume re-derives the same
	// resolution with it when the persisted resolved recipe is missing.
	Author string `json:"author,omitempty"`

	// BundlePath is the base tree the recipe was loaded from.
	BundlePath string `json:"bundle_path,omitempty"`

	// Hash is the SHA-256 digest of the resolved (merged) recipe.
	Hash string `json:"hash"`

	// Inputs are the resolved, redacted input values (name → value).
	Inputs map[string]string `json:"inputs,omitempty"`
}

// recipeIDRe allows a conservative recipe identifier (kebab-case).
var recipeIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// recipeInputNameRe is the config key character set (snapshot-compatible).
var recipeInputNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// recipeHashRe allows lowercase hex digests of 8-128 characters.
var recipeHashRe = regexp.MustCompile(`^[0-9a-f]{8,128}$`)

// maxRecipeInputValue bounds one resolved input value on the manifest.
const maxRecipeInputValue = 8192

// maxRecipeInputs bounds the number of recorded resolved inputs.
const maxRecipeInputs = 32

// Validate checks the recipe reference.
func (r Recipe) Validate() error {
	if r.ID == "" {
		return errors.New("recipe id is required")
	}
	if !recipeIDRe.MatchString(r.ID) {
		return fmt.Errorf("recipe id %q is invalid: kebab-case, max 64 chars", r.ID)
	}
	if len(r.Version) > 64 {
		return fmt.Errorf("recipe version exceeds 64 characters")
	}
	if r.BundlePath != "" && containsTraversal(r.BundlePath) {
		return fmt.Errorf("recipe bundle path %q must not contain '..' segments", r.BundlePath)
	}
	if r.Hash == "" {
		return errors.New("recipe hash is required (the resume anchor)")
	}
	if !recipeHashRe.MatchString(r.Hash) {
		return fmt.Errorf("recipe hash %q is invalid: use lowercase hex (8-128 chars)", r.Hash)
	}
	if len(r.Inputs) > maxRecipeInputs {
		return fmt.Errorf("recipe inputs: at most %d entries, got %d", maxRecipeInputs, len(r.Inputs))
	}
	for k, v := range r.Inputs {
		if !recipeInputNameRe.MatchString(k) {
			return fmt.Errorf("recipe input name %q is invalid", k)
		}
		if len(v) > maxRecipeInputValue {
			return fmt.Errorf("recipe input %q exceeds %d characters", k, maxRecipeInputValue)
		}
		if LooksLikeSecret(v) {
			return fmt.Errorf("recipe input %q contains secret-looking material", k)
		}
	}
	return nil
}
