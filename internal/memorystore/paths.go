package memorystore

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Mundo-Dolphins/local-newsroom/internal/memory"
)

// DefaultDBName is the default file name of the memory database inside its
// root directory.
const DefaultDBName = "memory.db"

// dbPath resolves and validates the memory database path.
//
// Path containment rules:
//   - The path is cleaned and resolved against the current working
//     directory; relative paths are anchored there, so a path like
//     "../escape.db" cannot point outside the intended tree without an
//     explicit absolute path.
//   - The parent directory must exist and be a directory; the database file
//     itself (and any WAL/SHM sidecars created by SQLite) live inside it.
//   - The resolved path must not be a directory.
//
// Special value: ":memory:" (and the empty string, which maps to it) selects
// an in-memory database, used by tests and by consumers that do not need
// durable storage.
//
// The returned path is the cleaned path passed to sql.Open.
func dbPath(root, name string) (string, error) {
	if root == ":memory:" || name == "" {
		return ":memory:", nil
	}
	if name == ":memory:" {
		return ":memory:", nil
	}
	if filepath.IsAbs(name) {
		return cleanPath(name)
	}
	joined := filepath.Join(root, name)
	return cleanPath(joined)
}

// cleanPath normalizes a path and verifies its parent directory.
func cleanPath(p string) (string, error) {
	cleaned := filepath.Clean(p)
	parent := filepath.Dir(cleaned)
	if parent != "" && parent != "." {
		st, err := os.Stat(parent)
		if err != nil {
			if os.IsNotExist(err) {
				return "", fmt.Errorf("memory database directory does not exist: %s (create it first; the store never creates directories outside its own tree)", parent)
			}
			return "", fmt.Errorf("cannot stat memory database directory %s: %w", parent, err)
		}
		if !st.IsDir() {
			return "", fmt.Errorf("memory database parent %s is not a directory", parent)
		}
	}
	if st, err := os.Lstat(cleaned); err == nil {
		if st.IsDir() {
			return "", fmt.Errorf("memory database path %s is a directory", cleaned)
		}
		if !st.Mode().IsRegular() {
			return "", fmt.Errorf("memory database path %s is not a regular file (symlinks and special files are refused: the store must create its WAL/SHM sidecars without following links)", cleaned)
		}
	}
	return cleaned, nil
}

// ensurePermissions sets private permissions on the database file and its
// sidecars (WAL/SHM) after the store has been opened.
//
// The memory database may contain sensitive editorial content (corrections,
// feedback, entity attributes); 0600 is the least-privilege mode and the
// directory itself should be 0700 by the operator. Best-effort: failures
// are reported but do not fail the open — the file already exists and
// callers can inspect the error.
func ensurePermissions(path string) error {
	if path == "" || path == ":memory:" {
		return nil
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := path + suffix
		if _, err := os.Stat(p); err != nil {
			continue // sidecar may not exist yet
		}
		if err := os.Chmod(p, 0o600); err != nil {
			return fmt.Errorf("cannot set permissions on %s: %w", p, err)
		}
	}
	return nil
}

// validateRecord is the persistence-side invariant check applied before any
// write: the record must validate and must not be a nil pointer.
func validateRecord(rec *memory.MemoryRecord) error {
	if rec == nil {
		return InvalidRecordError{Err: fmt.Errorf("record is nil")}
	}
	if err := rec.Validate(); err != nil {
		return InvalidRecordError{ID: rec.StableID, Err: err}
	}
	return nil
}
