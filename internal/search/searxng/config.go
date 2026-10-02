package searxng

import (
	"errors"
	"net/http"
	"time"
)

// ErrInvalidConfig is returned when configuration is invalid.
var ErrInvalidConfig = errors.New("invalid searxng configuration")

// Config holds the configuration for the SearXNG provider.
type Config struct {
	// BaseURL is the base URL of the SearXNG instance.
	// Example: "https://searx.example.com"
	BaseURL string `json:"base_url"`

	// HTTPTimeout is the maximum duration for HTTP requests.
	// If zero, a default of 30 seconds is used.
	HTTPTimeout time.Duration `json:"http_timeout,omitempty"`

	// MaxResponseSize is the maximum size of the response body (in bytes).
	// If zero, a default of 1MB is used.
	MaxResponseSize int64 `json:"max_response_size,omitempty"`

	// UserAgent is the User-Agent header to send with requests.
	// If empty, a default user agent is used.
	UserAgent string `json:"user_agent,omitempty"`

	// APIKey is an optional API key for authentication.
	// If the SearXNG instance requires authentication via a custom header,
	// provide the value here.
	APIKey string `json:"api_key,omitempty"`

	// APIHeader is the name of the header to use for API key authentication.
	// If APIKey is set but APIHeader is empty, "X-API-Key" is used as default.
	APIHeader string `json:"api_header,omitempty"`

	// SafeSearch is the safe search setting (0=off, 1=moderate, 2=strict).
	// If negative, SearXNG uses its default setting.
	SafeSearch int `json:"safe_search,omitempty"`
}

// Validate validates the configuration and returns an error if it's invalid.
func (c Config) Validate() error {
	if c.BaseURL == "" {
		return &ValidationError{
			Field:   "base_url",
			Message: "base_url is required",
		}
	}

	// Validate BaseURL format by attempting to parse it
	if _, err := parseURL(c.BaseURL); err != nil {
		return &ValidationError{
			Field:   "base_url",
			Message: err.Error(),
		}
	}

	// Validate that URL has a scheme
	parsed, err := parseURL(c.BaseURL)
	if err != nil {
		return &ValidationError{
			Field:   "base_url",
			Message: "invalid URL format",
		}
	}
	if parsed.Scheme == "" {
		return &ValidationError{
			Field:   "base_url",
			Message: "base_url must include a scheme (e.g., http:// or https://)",
		}
	}

	// Validate safe search value if provided
	if c.SafeSearch != -1 && (c.SafeSearch < 0 || c.SafeSearch > 2) {
		return &ValidationError{
			Field:   "safe_search",
			Message: "safe_search must be -1 (default), 0 (off), 1 (moderate), or 2 (strict)",
		}
	}

	return nil
}

// BuildHTTPClient returns an http.Client configured with the settings from c.
func (c Config) BuildHTTPClient() *http.Client {
	timeout := c.HTTPTimeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	userAgent := c.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &transportWithUserAgent{
			userAgent: userAgent,
			base:      http.DefaultTransport,
		},
	}

	return client
}

// transportWithUserAgent wraps http.RoundTripper to add a User-Agent header.
type transportWithUserAgent struct {
	userAgent string
	base      http.RoundTripper
}

func (t *transportWithUserAgent) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", t.userAgent)
	return t.base.RoundTrip(req)
}

// ValidationError represents a configuration validation error.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return "validation error [" + e.Field + "]: " + e.Message
}

func (e *ValidationError) Unwrap() error {
	return nil
}

// constants for defaults
const (
	defaultTimeout         = 30 * time.Second
	defaultUserAgent       = "Mundo-Dolphins/1.0 (searxng-provider)"
	defaultMaxResponseSize = 1024 * 1024 // 1MB
	safeSearchDefault      = 0
	offTimeRange           = ""
)
