// Package fetcher provides transport-only HTTP/HTTPS fetching capabilities.
package fetcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Mundo-Dolphins/local-newsroom/internal/types"
)

// Default maximum response size (10MB).
const DefaultMaxResponseSize = 10 * 1024 * 1024

// Default timeout for HTTP requests (30 seconds).
const DefaultTimeout = 30 * time.Second

// Default user agent.
const DefaultUserAgent = "local-newsroom-fetcher/1.0"

// Client wraps HTTP client configuration for fetcher operations.
type Client struct {
	httpClient *http.Client
	maxSize    int64
	userAgent  string
}

// Config holds fetcher configuration options.
type Config struct {
	// Timeout for HTTP requests. Defaults to 30 seconds.
	Timeout time.Duration

	// Maximum response body size in bytes. Defaults to 10MB.
	MaxSize int64

	// User-Agent header value. Defaults to "local-newsroom-fetcher/1.0".
	UserAgent string

	// FollowRedirects determines if redirects should be followed.
	// Defaults to true.
	FollowRedirects bool
}

// FetchResult contains the result of a successful fetch operation.
type FetchResult struct {
	// FinalURL is the URL after all redirects have been followed.
	FinalURL string

	// ContentType is the Content-Type header from the response.
	ContentType string

	// ContentLength is the actual content length of the response body.
	ContentLength int64

	// Body contains the raw response bytes.
	Body []byte

	// HTTPStatus is the final HTTP status code.
	HTTPStatus int

	// Headers contains relevant response headers.
	Headers http.Header
}

// FetchError represents a fetch operation failure.
type FetchError struct {
	Code        string
	Message     string
	OriginalURL string
	StatusCode  *int // populated if we received an HTTP response
}

// Error implements the error interface.
func (e *FetchError) Error() string {
	if e.StatusCode != nil {
		return fmt.Sprintf("%s: %s (status %d)", e.Code, e.Message, *e.StatusCode)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// IsUnsupportedScheme returns true if the error is due to an unsupported URL scheme.
func (e *FetchError) IsUnsupportedScheme() bool {
	return e.Code == "unsupported_scheme"
}

// IsTimeoutError returns true if the error is a timeout.
func (e *FetchError) IsTimeoutError() bool {
	return e.Code == "timeout" || e.Code == "context_deadline_exceeded"
}

// IsOversizedError returns true if the error is due to response exceeding size limit.
func (e *FetchError) IsOversizedError() bool {
	return e.Code == "oversized_response"
}

// NewClient creates a new fetcher client with the given configuration.
func NewClient(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = DefaultMaxResponseSize
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}
	if !cfg.FollowRedirects {
		cfg.FollowRedirects = true
	}

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}

	return &Client{
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if !cfg.FollowRedirects {
					return http.ErrUseLastResponse
				}
				// Limit redirect count to prevent abuse
				if len(via) >= 10 {
					return errors.New("stopped after 10 redirects")
				}
				return nil
			},
		},
		maxSize:   cfg.MaxSize,
		userAgent: cfg.UserAgent,
	}
}

// NewDefaultClient creates a client with default configuration.
func NewDefaultClient() *Client {
	return NewClient(Config{})
}

// Fetch retrieves content from the given URL.
// It returns a FetchResult on success, or a FetchError on failure.
// The context can be used to cancel the request or set a timeout.
func (c *Client) Fetch(ctx context.Context, baseURL string) (*FetchResult, *FetchError) {
	// Parse and validate URL
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		return nil, &FetchError{
			Code:        "invalid_url",
			Message:     fmt.Sprintf("failed to parse URL: %v", err),
			OriginalURL: baseURL,
		}
	}

	// Validate scheme
	scheme := strings.ToLower(parsedURL.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, &FetchError{
			Code:        "unsupported_scheme",
			Message:     fmt.Sprintf("unsupported URL scheme: %s", scheme),
			OriginalURL: baseURL,
		}
	}

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return nil, &FetchError{
			Code:        "request_build",
			Message:     fmt.Sprintf("failed to build request: %v", err),
			OriginalURL: baseURL,
		}
	}

	// Set headers
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &FetchError{
				Code:        "context_deadline_exceeded",
				Message:     fmt.Sprintf("request cancelled: %v", ctx.Err()),
				OriginalURL: baseURL,
			}
		}

		return nil, &FetchError{
			Code:        "request_failed",
			Message:     fmt.Sprintf("request failed: %v", err),
			OriginalURL: baseURL,
		}
	}
	defer resp.Body.Close()

	// Check status code
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &FetchError{
			Code:        "http_error",
			Message:     fmt.Sprintf("HTTP error: %s", resp.Status),
			OriginalURL: baseURL,
			StatusCode:  &resp.StatusCode,
		}
	}

	// Read and limit response body
	body, contentLength, err := c.readLimitedBody(resp.Body)
	if err != nil {
		if errors.Is(err, errOversized) {
			return nil, &FetchError{
				Code:        "oversized_response",
				Message:     fmt.Sprintf("response exceeds maximum size limit (%d bytes)", c.maxSize),
				OriginalURL: baseURL,
			}
		}
		return nil, &FetchError{
			Code:        "read_failed",
			Message:     fmt.Sprintf("failed to read response body: %v", err),
			OriginalURL: baseURL,
			StatusCode:  &resp.StatusCode,
		}
	}

	// Get final URL (after redirects)
	finalURL := resp.Request.URL.String()

	// Get content type
	contentType := resp.Header.Get("Content-Type")

	return &FetchResult{
		FinalURL:      finalURL,
		ContentType:   contentType,
		ContentLength: contentLength,
		Body:          body,
		HTTPStatus:    resp.StatusCode,
		Headers:       resp.Header,
	}, nil
}

// errOversized is returned when response exceeds size limit.
var errOversized = errors.New("response body exceeds maximum size")

// readLimitedBody reads the response body with size limiting.
func (c *Client) readLimitedBody(r io.Reader) ([]byte, int64, error) {
	var buf bytes.Buffer
	lr := io.LimitReader(r, c.maxSize+1) // +1 to detect overflow

	n, err := io.Copy(&buf, lr)
	if err != nil && err != io.EOF {
		return nil, n, err
	}

	// Check if we hit the limit
	if n > c.maxSize {
		return nil, n, errOversized
	}

	return buf.Bytes(), n, nil
}

// FetchStatusFromResult converts a FetchResult to a types.FetchStatus for the types package.
func FetchStatusFromResult(result *FetchResult) *types.FetchStatus {
	if result == nil {
		return nil
	}

	contentType := result.ContentType
	contentLength := result.ContentLength
	httpStatus := result.HTTPStatus

	return &types.FetchStatus{
		HTTPStatus:    &httpStatus,
		ContentType:   &contentType,
		ContentLength: &contentLength,
	}
}

// FetchMany fetches multiple URLs concurrently, respecting the context.
func (c *Client) FetchMany(ctx context.Context, urls []string) []FetchItem {
	results := make([]FetchItem, len(urls))
	var wg sync.WaitGroup
	wg.Add(len(urls))

	for i, u := range urls {
		go func(idx int, urlStr string) {
			defer wg.Done()
			result, err := c.Fetch(ctx, urlStr)
			results[idx] = FetchItem{
				URL:     urlStr,
				Result:  result,
				Error:   err,
				Success: err == nil,
			}
		}(i, u)
	}

	wg.Wait()
	return results
}

// FetchItem holds the result for a single URL in a FetchMany operation.
type FetchItem struct {
	URL     string
	Result  *FetchResult
	Error   *FetchError
	Success bool
}
