package hugo

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	texttmpl "text/template"
	"time"
)

// dirPerm and filePerm are applied explicitly (via chmod) after creation so
// that resulting permissions do not depend on the process umask. A Hugo
// content tree is operator-private material until the site deploys it, so
// it uses the same safe permissions as the run workspace.
const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// pathContext is the template context of the section and filename
// templates. Only plain fields and the standard time.Time methods are
// exposed; templates are operator configuration (trusted), but the rendered
// output is still validated for path safety below.
type pathContext struct {
	Slug  string
	Title string
	Date  time.Time
}

// template is a pre-parsed path template (parsed once in New).
type template struct {
	name string
	t    *texttmpl.Template
}

// parsePathTemplate parses one section or filename template. An empty
// section template is allowed (it means "no section"); an empty filename
// template is rejected (the default is applied by Config.Validate).
func parsePathTemplate(name, src string) (*template, error) {
	if strings.TrimSpace(src) == "" && name == "filename" {
		return nil, fmt.Errorf("%s template is empty", name)
	}
	t, err := texttmpl.New(name).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s template %q: %v", name, src, err)
	}
	return &template{name: name, t: t}, nil
}

// execute renders the template with the given context.
func (p *template) execute(ctx pathContext) (string, error) {
	var sb strings.Builder
	if err := p.t.Execute(&sb, ctx); err != nil {
		return "", fmt.Errorf("%s template: %v", p.name, err)
	}
	return sb.String(), nil
}

// validateRelPath validates and normalizes a path relative to the content
// directory: it must be relative, contain no NUL or backslash, and clean to
// a form with no "." or ".." segments. The result uses forward slashes.
func validateRelPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path is empty")
	}
	if strings.ContainsRune(p, '\x00') {
		return "", fmt.Errorf("path contains a NUL byte")
	}
	if strings.ContainsRune(p, '\\') {
		return "", fmt.Errorf("path %q must not contain backslashes", p)
	}
	if path.IsAbs(p) || filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q must be relative to the content directory", p)
	}
	cleaned := path.Clean(p)
	if cleaned == "." || path.IsAbs(cleaned) {
		return "", fmt.Errorf("path %q resolves to the content root itself", p)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path %q traverses outside the content directory", p)
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("path %q contains an empty or dot segment", p)
		}
	}
	return cleaned, nil
}

// assertInsideRoot proves that p, after resolving symbolic links, is the
// Hugo root or a sub-path of it. This is what stops a symlinked directory
// inside the project (content -> /etc, or a crafted section directory) from
// steering the write outside the configured tree.
func assertInsideRoot(root, p string) error {
	resolved, err := resolveSymlinks(p)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve %s: %v", ErrPathEscape, p, err)
	}
	rootResolved, err := resolveSymlinks(root)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve Hugo root %s: %v", ErrRoot, root, err)
	}
	if resolved != rootResolved && !strings.HasPrefix(resolved, rootResolved+string(os.PathSeparator)) {
		return fmt.Errorf("%w: %s resolves outside the Hugo root %s", ErrPathEscape, resolved, rootResolved)
	}
	return nil
}

// resolveSymlinks resolves the symbolic links of the deepest existing
// ancestor of p and re-appends the not-yet-existing suffix, so paths that
// can still be created are resolvable as well. (Same algorithm as the run
// workspace store.)
func resolveSymlinks(p string) (string, error) {
	p = filepath.Clean(p)
	target := p
	for {
		resolved, err := filepath.EvalSymlinks(target)
		if err == nil {
			suffix := strings.TrimPrefix(p, target)
			return filepath.Clean(resolved + suffix), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(target)
		if parent == target {
			// Reached the filesystem root without finding an existing
			// ancestor; nothing to resolve.
			return p, nil
		}
		target = parent
	}
}

// ensureDir creates dir (and any missing parents down to the deepest
// existing ancestor) with dirPerm. Pre-existing directories are never
// chmodded: the chain above the deepest existing ancestor belongs to the
// operator's project and keeps whatever permissions it has.
func ensureDir(root, dir string) error {
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%w: %s exists and is not a directory", ErrPathEscape, dir)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspecting directory %s: %w", dir, err)
	}

	// Find the deepest existing ancestor; everything between it and dir is
	// what we are about to create.
	probe := dir
	for {
		if _, err := os.Stat(probe); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspecting directory %s: %w", probe, err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return fmt.Errorf("%w: no existing ancestor for %s (the Hugo root should exist)", ErrRoot, dir)
		}
		probe = parent
	}

	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}
	// Set permissions on exactly the directories we created (dir up to, but
	// not including, the pre-existing probe ancestor) so the result does not
	// depend on the process umask. The probe itself already existed and
	// keeps whatever permissions it had.
	d := dir
	for d != probe {
		if err := os.Chmod(d, dirPerm); err != nil {
			return fmt.Errorf("setting permissions on %s: %w", d, err)
		}
		d = filepath.Dir(d)
	}
	return nil
}

// atomicWrite writes data to dir/name atomically: it creates a temporary
// file in the same directory, writes and syncs it, sets the final
// permissions, and renames it over the destination. Readers therefore only
// ever observe a complete file — either the previous version or the new
// one — and a failure at any step removes the temporary, leaving whatever
// was already on disk untouched.
func atomicWrite(dir, name string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup: close and, if the temp file was never
		// renamed into place, remove it.
		_ = tmp.Close()
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("setting permissions on temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	// #nosec:G301 -- dir is the validated target directory (inside the Hugo
	// root) and name the validated file name; the destination exists or is
	// a plain file.
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("renaming temp file into place: %w", err)
	}
	tmpName = "" // renamed: the defer's remove must not run
	return nil
}
