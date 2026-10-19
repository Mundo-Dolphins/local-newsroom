package bluesky

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// DefaultAPIEndpoint is the public Bluesky PDS API endpoint, used when the
// configuration does not name one (e.g. for an account on a custom PDS).
const DefaultAPIEndpoint = "https://bsky.social"

// provider is the contract provider tag for this adapter.
const provider = "bluesky"

// clientVersion is the implementation version recorded on receipts.
const clientVersion = "bluesky/0.6"

// Config is the bluesky publisher's non-secret configuration.
//
// SECURITY: the posting credential is an *environment reference* — the name
// of the variable that holds the account's app password — never a value.
// New reads the variable into the in-memory session provider; the Config
// itself (and therefore anything that persists or logs it) holds no
// credential.
type Config struct {
	// Handle is the account's public handle, without the leading "@"
	// (e.g. "mundodolphins.bsky.social"). It is the identifier in the
	// createSession call and the profile used in canonical post URLs.
	// Required.
	Handle string

	// APIEndpoint is the AT Protocol xrpc endpoint (e.g.
	// "https://bsky.social" for Bluesky Social, or a custom PDS URL).
	// Empty means DefaultAPIEndpoint. Must be an absolute http(s) URL
	// without userinfo.
	APIEndpoint string

	// APIKeyEnv is the name of the environment variable holding the
	// account's app password (e.g. "BLUESKY_APP_PASSWORD"). Required;
	// the variable must be set.
	APIKeyEnv string

	// DefaultLangs are the language tags applied to posts that do not
	// name their own (0..3 conservative tags, e.g. ["en"]).
	DefaultLangs []string

	// Client overrides the HTTP client (tests inject one pointed at a
	// fake xrpc server). Nil means a safe default.
	Client *http.Client
}

// handleRe is the AT Protocol handle identifier syntax
// (atproto.com/specs/handle): dot-separated labels, each starting and
// ending with a letter or digit, 63 labels at most, 255 characters total.
var handleRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)+$`)

// envNameRe allows conservative environment variable names.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// resolvedConfig is the validated, non-secret configuration (endpoint
// defaulted, handle trimmed).
//
// It retains the credential *variable name* (not the credential) so the
// session provider can read it at construction; the value never travels
// through this type.
type resolvedConfig struct {
	handle       string
	endpoint     string
	apiKeyEnv    string
	defaultLangs []string
}

// normalize validates c and returns its resolved form.
//
// The password is deliberately not part of this type: it is read from the
// environment by New into the session provider and held in memory only.
func (c Config) normalize() (resolvedConfig, error) {
	handle := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.Handle), "@"))
	if handle == "" {
		return resolvedConfig{}, errors.New("bluesky: handle is required")
	}
	if len(handle) > 255 || !handleRe.MatchString(handle) {
		return resolvedConfig{}, fmt.Errorf("bluesky: handle %q is not a valid AT Protocol handle", handle)
	}

	endpoint := strings.TrimSpace(c.APIEndpoint)
	if endpoint == "" {
		endpoint = DefaultAPIEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return resolvedConfig{}, fmt.Errorf("bluesky: API endpoint %q must be an absolute http(s) URL", endpoint)
	}
	if u.User != nil {
		return resolvedConfig{}, fmt.Errorf("bluesky: API endpoint must not embed credentials")
	}

	envName := strings.TrimSpace(c.APIKeyEnv)
	if !envNameRe.MatchString(envName) {
		return resolvedConfig{}, fmt.Errorf("bluesky: API key environment variable name %q is invalid", envName)
	}

	if pw := os.Getenv(envName); strings.TrimSpace(pw) == "" {
		return resolvedConfig{}, fmt.Errorf("bluesky: environment variable %s (posting credential) is not set or empty", envName)
	}

	return resolvedConfig{
		handle:       handle,
		endpoint:     strings.TrimRight(endpoint, "/"),
		apiKeyEnv:    envName,
		defaultLangs: append([]string(nil), c.DefaultLangs...),
	}, nil
}
