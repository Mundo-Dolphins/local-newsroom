package recipes

import "errors"

// Load errors. Each is a sentinel so callers (CLI, plan layer) can shape
// user-facing messages without string matching.
var (
	// ErrNotFound is a recipe (or example) file that does not exist.
	ErrNotFound = errors.New("recipe file not found")

	// ErrTraversal is a path that would escape the recipe base tree.
	ErrTraversal = errors.New("recipe path escapes the base tree")

	// ErrTooLarge is a file exceeding its size bound.
	ErrTooLarge = errors.New("recipe file too large")

	// ErrDuplicateID is two files in one layer declaring the same id.
	ErrDuplicateID = errors.New("duplicate recipe id in one layer")

	// ErrInvalid is a structurally or safety-invalid recipe document.
	ErrInvalid = errors.New("invalid recipe")
)

// IsError reports whether err is a recipes load error (for CLI messaging).
func IsError(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrTraversal) ||
		errors.Is(err, ErrTooLarge) || errors.Is(err, ErrDuplicateID) || errors.Is(err, ErrInvalid)
}
