package config_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Mundo-Dolphins/local-newsroom/internal/config"
)

// TestEditorialSectionRoundTrip: the editorial section of the config file
// deserializes (and stays absent when unset).
func TestEditorialSectionRoundTrip(t *testing.T) {
	y := `
llm:
  base_url: https://example.com
editorial:
  bundle_root: /srv/editorial
  publication: mundodolphins
`
	f, err := config.LoadBytes([]byte(y), "test")
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if f.Editorial.BundleRoot == nil || *f.Editorial.BundleRoot != "/srv/editorial" {
		t.Errorf("bundle_root = %v", f.Editorial.BundleRoot)
	}
	if f.Editorial.Publication == nil || *f.Editorial.Publication != "mundodolphins" {
		t.Errorf("publication = %v", f.Editorial.Publication)
	}
	if _, _, err := config.New(f, "test").Resolve(config.SettingEditorialBundle, config.Override{}); err != nil {
		t.Errorf("Resolve bundle: %v", err)
	}
}

// TestEditorialSectionUnknownFields: unknown keys in the editorial section
// are rejected (KnownFields).
func TestEditorialSectionUnknownFields(t *testing.T) {
	y := `
editorial:
  bundle_root: /srv/editorial
  typo: x
`
	if _, err := config.LoadBytes([]byte(y), "test"); err == nil {
		t.Fatal("unknown editorial key must be rejected")
	}
}

// TestValidateSecretKeysUnwrapsDocument: a DocumentNode (as produced by
// yaml.Unmarshal into a yaml.Node) is unwrapped and its content walked —
// the guard the old private walkSecretKeys call used to miss.
func TestValidateSecretKeysUnwrapsDocument(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte("api:\n  api_key: literal-secret\n"), &doc); err != nil {
		t.Fatal(err)
	}
	err := config.ValidateSecretKeys(&doc)
	if err == nil {
		t.Fatal("a literal credential inside a document node must be rejected")
	}
	if strings.Contains(err.Error(), "literal-secret") {
		t.Errorf("error must not echo the secret: %v", err)
	}

	var ok yaml.Node
	if err := yaml.Unmarshal([]byte("api:\n  api_key:\n    env: OMLX_API_KEY\n"), &ok); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateSecretKeys(&ok); err != nil {
		t.Errorf("env reference must be accepted: %v", err)
	}
}

// TestValidateSecretKeysPlainNodeAndEmpty.
func TestValidateSecretKeysPlainNodeAndEmpty(t *testing.T) {
	// A mapping node directly (not wrapped in a document node).
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("token: abc\n"), &node); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateSecretKeys(&node); err == nil {
		t.Fatal("literal credential in a plain mapping must be rejected")
	}

	// An empty (zero) node is valid.
	var zero yaml.Node
	if err := config.ValidateSecretKeys(&zero); err != nil {
		t.Errorf("zero node must be accepted: %v", err)
	}

	// A document node without content (empty input) is valid.
	var emptyDoc yaml.Node
	if err := yaml.Unmarshal([]byte(""), &emptyDoc); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateSecretKeys(&emptyDoc); err != nil {
		t.Errorf("empty document must be accepted: %v", err)
	}
}

// TestLoadBytesSecretGuard: the full load path rejects literal credentials.
func TestLoadBytesSecretGuard(t *testing.T) {
	y := `
editorial:
  bundle_root: /x
llm:
  api_key: supersecret
`
	_, err := config.LoadBytes([]byte(y), "test")
	if err == nil {
		t.Fatal("literal api_key in the config file must be rejected")
	}
	if strings.Contains(err.Error(), "supersecret") {
		t.Errorf("error must not echo the secret: %v", err)
	}
}

// TestResolveEditorialSettings: the editorial settings resolve through the
// standard precedence chain and validate their values.
func TestResolveEditorialSettings(t *testing.T) {
	y := `
llm:
  base_url: https://example.com
editorial:
  bundle_root: /srv/editorial
  publication: mundodolphins
`
	f, err := config.LoadBytes([]byte(y), "test")
	if err != nil {
		t.Fatal(err)
	}
	c := config.New(f, "test")

	if v, src, err := c.Resolve(config.SettingEditorialBundle, config.Override{}); err != nil || v != "/srv/editorial" || src != "config" {
		t.Errorf("editorial.bundle_root = %q (%q), %v", v, src, err)
	}
	if v, _, err := c.Resolve(config.SettingEditorialPublication, config.Override{}); err != nil || v != "mundodolphins" {
		t.Errorf("editorial.publication = %q, %v", v, err)
	}

	// Environment overrides the file.
	t.Setenv("NEWSROOM_EDITORIAL_BUNDLE", "/tmp/bundle")
	if v, src, err := c.Resolve(config.SettingEditorialBundle, config.Override{}); err != nil || v != "/tmp/bundle" || src != "env:NEWSROOM_EDITORIAL_BUNDLE" {
		t.Errorf("env override: got %q (%q), %v", v, src, err)
	}

	// A traversal root is rejected.
	t.Setenv("NEWSROOM_EDITORIAL_BUNDLE", "../outside")
	if _, _, err := c.Resolve(config.SettingEditorialBundle, config.Override{}); err == nil {
		t.Error("a '..' bundle root must be rejected")
	}

	// An invalid publication name is rejected.
	t.Setenv("NEWSROOM_PUBLICATION", "bad name")
	if _, _, err := c.Resolve(config.SettingEditorialPublication, config.Override{}); err == nil {
		t.Error("a publication name with a space must be rejected")
	}
}
