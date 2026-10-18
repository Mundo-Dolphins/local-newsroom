package editorial

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadBundleFileContainment (internal test: white-box) verifies the
// reader's path defenses directly: absolute paths and ".." escapes are
// refused, and legitimate reads inside the root succeed.
func TestReadBundleFileContainment(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "sensitive.yaml"), []byte("handle: @x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := NewLoader(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := l.readBundleFile("/etc/passwd"); !errors.Is(err, ErrFileTraversal) {
		t.Errorf("absolute path: got %v, want ErrFileTraversal", err)
	}
	if _, _, err := l.readBundleFile(filepath.Join("..", "x.yaml")); !errors.Is(err, ErrFileTraversal) {
		t.Errorf("dotdot path: got %v, want ErrFileTraversal", err)
	}
	// A name that would resolve outside the root must be refused.
	if _, _, err := l.readBundleFile(filepath.Join("..", filepath.Base(outside), "sensitive.yaml")); !errors.Is(err, ErrFileTraversal) {
		t.Errorf("escape path: got %v, want ErrFileTraversal", err)
	}

	// A legitimate read succeeds (bundle-relative path).
	if err := os.MkdirAll(filepath.Join(root, "sites"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sites", "ok.yaml"), []byte("a: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, data, err := l.readBundleFile("sites/ok.yaml")
	if err != nil {
		t.Fatalf("legit read: %v", err)
	}
	if !strings.Contains(path, filepath.Join(root, "sites")) {
		t.Errorf("resolved path = %q, want under %q", path, root)
	}
	if !strings.Contains(string(data), "a: 1") {
		t.Errorf("read content = %q", data)
	}
}

// TestReadBundleFileSizeCap: files over the cap fail with ErrFileTooLarge.
func TestReadBundleFileSizeCap(t *testing.T) {
	root := t.TempDir()
	l, _ := NewLoader(Config{Root: root})
	l.maxConfigFileBytes = 32
	big := strings.Repeat("x", 64) + "\n"
	path := filepath.Join(root, "big.yaml")
	if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.readBundleFile("big.yaml"); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("oversized file: got %v, want ErrFileTooLarge", err)
	}
}
