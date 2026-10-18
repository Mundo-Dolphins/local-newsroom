package editorial

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
	"github.com/Mundo-Dolphins/local-newsroom/internal/profiles"
)

// Preset is the resolved, validated configuration of one publication within
// an editorial bundle.
//
// Every preset field is a *reference* to other bundle content: Profile names
// a profile (author) inside the bundle, and the Hugo/Bluesky fields hold the
// destination config files the preset references. The resolved values
// (language, renderer) are the non-secret snapshot metadata a run records;
// the destination content itself never leaves the bundle (see the package
// documentation).
type Preset struct {
	// Name is the publication name (the directory under publications/).
	Name string

	// Profile is the profile spec within the bundle: "<author>" for the
	// flat bundle layout, "<dir>/<author>" for a nested one, or "" to load
	// only the bundle's common profile.
	Profile string

	// Language is the output language (e.g. "es"), applied when no higher
	// precedence layer (flag, environment, config file) set one.
	Language string

	// Renderer is the output format: "article", "thread", or "hybrid".
	// Applied when no higher precedence layer set one.
	Renderer string

	// Hugo is the Hugo destination configuration (hugo.yaml), when the
	// preset references one.
	Hugo *HugoDestination

	// Bluesky is the Bluesky destination configuration (bluesky.yaml), when
	// the preset references one.
	Bluesky *BlueskyDestination

	// Hash is the digest of the resolved non-secret preset content
	// (lowercase hex). Secret fields contribute their environment variable
	// name, never a credential.
	Hash string

	// Files lists the bundle-relative config files read for this preset, in
	// load order (the preset file first).
	Files []string
}

// HugoDestination is the destination configuration for a Hugo site.
//
// All fields are non-secret: a local project path, a public URL, a layout
// name, and a drafts flag.
type HugoDestination struct {
	// Destination is the local Hugo project directory. A relative path is
	// resolved against the bundle root; an absolute path is used as-is.
	Destination string `yaml:"destination"`

	// BaseURL is the public site URL (e.g. "https://www.example.com").
	BaseURL string `yaml:"base_url"`

	// Layout is the optional content layout or template name.
	Layout string `yaml:"layout"`

	// Drafts marks published content as Hugo draft by default.
	Drafts bool `yaml:"drafts"`
}

// DestinationPath returns the Hugo project directory as an absolute path.
func (h *HugoDestination) DestinationPath(bundleRoot string) string {
	if filepath.IsAbs(h.Destination) {
		return filepath.Clean(h.Destination)
	}
	return filepath.Join(bundleRoot, filepath.Clean(h.Destination))
}

// BlueskyDestination is the destination configuration for a Bluesky
// account.
//
// The handle and endpoint are public facts; the posting credential is an
// environment reference and never a literal value.
type BlueskyDestination struct {
	// Handle is the public Bluesky handle (e.g. "@mundodolphins").
	Handle string `yaml:"handle"`

	// APIEndpoint is the optional AT Protocol API endpoint.
	APIEndpoint string `yaml:"api_endpoint"`

	// APIKey references the environment variable holding the posting
	// credential (e.g. {env: BLUESKY_API_KEY}).
	APIKey config.APIKey `yaml:"api_key"`
}

// presetFile is the on-disk schema of publications/<name>/publication.yaml.
//
// Pointer leaves distinguish "absent" from "zero": an absent field means
// "the engine default applies". The hugo/bluesky fields name the
// destination config file (a single file name, relative to the preset's
// directory), not an arbitrary path.
type presetFile struct {
	SchemaVersion int     `yaml:"schema_version"`
	Profile       *string `yaml:"profile"`
	Language      *string `yaml:"language"`
	Renderer      *string `yaml:"renderer"`
	Hugo          *string `yaml:"hugo"`
	Bluesky       *string `yaml:"bluesky"`
}

// languageRe allows conservative language tags ("es", "pt-BR", "zh-Hans").
var languageRe = regexp.MustCompile(`^[a-zA-Z]{2,8}(-[a-zA-Z0-9]{1,8})?$`)

// renderersRe allows the known output formats.
var renderersRe = regexp.MustCompile(`^(article|thread|hybrid)$`)

// fileRefRe allows a single file name (no separators, no leading dot).
var fileRefRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`)

// LoadPreset loads one publication preset: publications/<name>/
// publication.yaml plus the destination config files it references.
//
// The preset name must be a conservative identifier. A referenced
// destination config file that is missing or invalid is an error; a file
// that is not referenced may be absent.
func (l *Loader) LoadPreset(name string) (*Preset, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("%w: %q (use letters, digits, '.', '-', '_' — 1-64 chars, must not start with a separator)",
			ErrInvalidName, name)
	}
	presetPath := filepath.Join(PublicationsDir, name, PresetFileName)
	_, data, err := l.readBundleFile(presetPath)
	if err != nil {
		if errors.Is(err, ErrFileNotFound) {
			available, _ := l.Publications()
			return nil, fmt.Errorf("%w: %s (available: %s)", ErrFileNotFound, presetPath,
				strings.Join(available, ", "))
		}
		return nil, fmt.Errorf("loading %s: %w", presetPath, err)
	}

	var pf presetFile
	if err := l.parseYAML(data, presetPath, &pf); err != nil {
		return nil, err
	}
	if pf.SchemaVersion != 0 && pf.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("%w in %s: unsupported schema_version %d (this release understands %d)",
			ErrInvalidYAML, presetPath, pf.SchemaVersion, SchemaVersion)
	}

	p := &Preset{Name: name, Files: []string{presetPath}}

	if pf.Profile != nil {
		spec := strings.TrimSpace(*pf.Profile)
		if err := validateProfileSpec(spec); err != nil {
			return nil, fmt.Errorf("invalid config %s: %w", presetPath, err)
		}
		p.Profile = spec
	}
	if pf.Language != nil {
		lang := strings.TrimSpace(*pf.Language)
		if lang == "" {
			return nil, fmt.Errorf("%w in %s: language must not be empty", ErrInvalidYAML, presetPath)
		}
		if !languageRe.MatchString(lang) {
			return nil, fmt.Errorf("%w in %s: invalid language %q (use e.g. \"en\", \"es\", \"pt-BR\")", ErrInvalidYAML, presetPath, lang)
		}
		p.Language = lang
	}
	if pf.Renderer != nil {
		renderer := strings.TrimSpace(*pf.Renderer)
		if renderer != "" && !renderersRe.MatchString(renderer) {
			return nil, fmt.Errorf("%w in %s: invalid renderer %q (use article, thread, or hybrid)", ErrInvalidYAML, presetPath, renderer)
		}
		p.Renderer = renderer
	}
	if pf.Hugo != nil {
		h, err := l.loadHugoDestination(name, *pf.Hugo)
		if err != nil {
			return nil, err
		}
		p.Hugo = h
		p.Files = append(p.Files, filepath.Join(PublicationsDir, name, strings.TrimSpace(*pf.Hugo)))
	}
	if pf.Bluesky != nil {
		b, err := l.loadBlueskyDestination(name, *pf.Bluesky)
		if err != nil {
			return nil, err
		}
		p.Bluesky = b
		p.Files = append(p.Files, filepath.Join(PublicationsDir, name, strings.TrimSpace(*pf.Bluesky)))
	}

	p.Hash = presetHash(p)
	return p, nil
}

// loadHugoDestination reads and validates the referenced Hugo destination
// config file. The reference must be a single file name inside the preset's
// directory; a missing file is an error (a dangling reference).
func (l *Loader) loadHugoDestination(presetName, ref string) (*HugoDestination, error) {
	ref = strings.TrimSpace(ref)
	if !fileRefRe.MatchString(ref) {
		return nil, fmt.Errorf("%w: %q in %s/%s (a single file name, e.g. %q)",
			ErrInvalidName, ref, PublicationsDir, presetName, HugoConfigFileName)
	}
	rel := filepath.Join(PublicationsDir, presetName, ref)
	_, data, err := l.readBundleFile(rel)
	if err != nil {
		if errors.Is(err, ErrFileNotFound) {
			return nil, fmt.Errorf("%w: %s (referenced by %s/%s)", ErrFileNotFound, rel, PublicationsDir, presetName)
		}
		return nil, fmt.Errorf("loading %s: %w", rel, err)
	}
	var d HugoDestination
	if err := l.parseYAML(data, rel, &d); err != nil {
		return nil, err
	}
	if d.Destination != "" && containsDotDot(d.Destination) {
		return nil, fmt.Errorf("%w in %s: destination %q must not contain '..' segments", ErrInvalidYAML, rel, d.Destination)
	}
	if d.BaseURL != "" && !isAbsoluteURL(d.BaseURL) {
		return nil, fmt.Errorf("%w in %s: base_url %q must be an absolute http(s) URL", ErrInvalidYAML, rel, d.BaseURL)
	}
	d.Destination = strings.TrimSpace(d.Destination)
	d.BaseURL = strings.TrimSpace(d.BaseURL)
	d.Layout = strings.TrimSpace(d.Layout)
	p := &HugoDestination{Destination: d.Destination, BaseURL: d.BaseURL, Layout: d.Layout, Drafts: d.Drafts}
	return p, nil
}

// loadBlueskyDestination reads and validates the referenced Bluesky
// destination config file (same reference rules as loadHugoDestination).
func (l *Loader) loadBlueskyDestination(presetName, ref string) (*BlueskyDestination, error) {
	ref = strings.TrimSpace(ref)
	if !fileRefRe.MatchString(ref) {
		return nil, fmt.Errorf("%w: %q in %s/%s (a single file name, e.g. %q)",
			ErrInvalidName, ref, PublicationsDir, presetName, BlueskyConfigFileName)
	}
	rel := filepath.Join(PublicationsDir, presetName, ref)
	_, data, err := l.readBundleFile(rel)
	if err != nil {
		if errors.Is(err, ErrFileNotFound) {
			return nil, fmt.Errorf("%w: %s (referenced by %s/%s)", ErrFileNotFound, rel, PublicationsDir, presetName)
		}
		return nil, fmt.Errorf("loading %s: %w", rel, err)
	}
	var d BlueskyDestination
	if err := l.parseYAML(data, rel, &d); err != nil {
		return nil, err
	}
	d.Handle = strings.TrimSpace(d.Handle)
	if len(d.Handle) > 200 {
		return nil, fmt.Errorf("%w in %s: handle exceeds 200 characters", ErrInvalidYAML, rel)
	}
	d.APIEndpoint = strings.TrimSpace(d.APIEndpoint)
	if d.APIEndpoint != "" && !isAbsoluteURL(d.APIEndpoint) {
		return nil, fmt.Errorf("%w in %s: api_endpoint %q must be an absolute http(s) URL", ErrInvalidYAML, rel, d.APIEndpoint)
	}
	return &d, nil
}

// isAbsoluteURL reports whether s is an absolute http(s) URL.
func isAbsoluteURL(s string) bool {
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return false
	}
	return !strings.ContainsAny(s, "\t\n\r ")
}

// validateProfileSpec checks a profile spec ("<author>" or
// "<dir>/<author>") against the conservative identifier rules, so every
// referenced profile name can also be recorded on a run manifest.
//
// The bundle layout is flat (common/, authors/, publications/ are
// siblings), so the spec is one segment (an author) or two (a subdirectory
// plus an author); anything deeper has no meaning in the profiles loader.
func validateProfileSpec(spec string) error {
	if spec == "" {
		return nil
	}
	if len(spec) > 100 {
		return fmt.Errorf("%w: profile spec %q exceeds 100 characters", ErrInvalidName, spec)
	}
	segments := strings.Split(spec, "/")
	if len(segments) > 2 {
		return fmt.Errorf("%w: profile spec %q must be <author> or <dir>/<author>", ErrInvalidName, spec)
	}
	for _, segment := range segments {
		if err := profiles.ValidateName(segment); err != nil {
			return fmt.Errorf("%w: profile spec %q: %v", ErrInvalidName, spec, err)
		}
	}
	return nil
}

// parseYAML decodes one bundle YAML document into out, enforcing:
//
//   - a syntax error is reported with the file name;
//   - every credential-like key holds an environment reference
//     ({env: NAME}) — a literal credential is rejected before decoding;
//   - unknown keys are rejected (typos and stale fields).
//
// The file name in errors is the bundle-relative path; the raw document is
// never echoed, so a literal secret never appears in an error message.
func (l *Loader) parseYAML(data []byte, file string, out any) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%w in %s: YAML syntax error: %v", ErrInvalidYAML, file, err)
	}
	if err := config.ValidateSecretKeys(&doc); err != nil {
		return fmt.Errorf("invalid config %s: %w", file, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w in %s: %v", ErrInvalidYAML, file, err)
	}
	return nil
}

// presetHashForm is the canonical hash input of a preset: every resolved
// non-secret value. The credential contributes its environment variable
// name — a reference, never a credential.
type presetHashForm struct {
	Name     string        `json:"name"`
	Profile  string        `json:"profile"`
	Language string        `json:"language"`
	Renderer string        `json:"renderer"`
	Hugo     *hugoHashForm `json:"hugo,omitempty"`
	Bluesky  *blueskyForm  `json:"bluesky,omitempty"`
}

type hugoHashForm struct {
	Destination string `json:"destination"`
	BaseURL     string `json:"base_url"`
	Layout      string `json:"layout"`
	Drafts      bool   `json:"drafts"`
}

type blueskyForm struct {
	Handle      string `json:"handle"`
	APIEndpoint string `json:"api_endpoint"`
	APIKeyEnv   string `json:"api_key_env,omitempty"`
}

// presetHash returns the digest of the resolved non-secret preset content.
//
// The hash is over the canonical form, so field ordering and formatting in
// the YAML files never affect it: the same resolved preset always hashes the
// same.
func presetHash(p *Preset) string {
	form := presetHashForm{
		Name:     p.Name,
		Profile:  p.Profile,
		Language: p.Language,
		Renderer: p.Renderer,
	}
	if p.Hugo != nil {
		form.Hugo = &hugoHashForm{
			Destination: p.Hugo.Destination,
			BaseURL:     p.Hugo.BaseURL,
			Layout:      p.Hugo.Layout,
			Drafts:      p.Hugo.Drafts,
		}
	}
	if p.Bluesky != nil {
		form.Bluesky = &blueskyForm{
			Handle:      p.Bluesky.Handle,
			APIEndpoint: p.Bluesky.APIEndpoint,
			APIKeyEnv:   p.Bluesky.APIKey.Env,
		}
	}
	data, err := json.Marshal(form)
	if err != nil {
		// The form holds only plain strings and a bool: marshaling cannot
		// fail. The panic is unreachable in practice; a corrupted in-memory
		// preset is a programming error, not operator input.
		panic(fmt.Sprintf("internal error: marshaling preset hash form: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
