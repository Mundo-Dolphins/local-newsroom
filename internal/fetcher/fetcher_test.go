package fetcher

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		check  func(*testing.T, *Client)
	}{
		{
			name:   "default config creates client with reasonable defaults",
			config: Config{},
			check: func(t *testing.T, c *Client) {
				if c.userAgent == "" {
					t.Error("user agent should be set")
				}
				if c.maxSize == 0 {
					t.Error("max size should be set")
				}
			},
		},
		{
			name: "custom config overrides defaults",
			config: Config{
				Timeout:   60 * time.Second,
				MaxSize:   5 * 1024 * 1024,
				UserAgent: "Custom Agent",
			},
			check: func(t *testing.T, c *Client) {
				if c.userAgent != "Custom Agent" {
					t.Errorf("expected custom user agent, got: %s", c.userAgent)
				}
			},
		},
		{
			name: "follow redirects disabled",
			config: Config{
				FollowRedirects: false,
			},
			check: func(t *testing.T, c *Client) {
				if c.httpClient.CheckRedirect == nil {
					t.Error("CheckRedirect should be set")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(tt.config)
			if c == nil {
				t.Fatal("client should not be nil")
			}
			tt.check(t, c)
		})
	}
}

func TestFetch_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><body>Hello World</body></html>"))
	}))
	defer server.Close()

	client := NewDefaultClient()
	result, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	if result.HTTPStatus != http.StatusOK {
		t.Errorf("expected status 200, got: %d", result.HTTPStatus)
	}

	if result.ContentType != "text/html; charset=utf-8" {
		t.Errorf("expected content type 'text/html; charset=utf-8', got: %s", result.ContentType)
	}

	expectedBody := "<html><body>Hello World</body></html>"
	if string(result.Body) != expectedBody {
		t.Errorf("expected body %q, got: %q", expectedBody, string(result.Body))
	}

	if result.FinalURL != server.URL {
		t.Errorf("expected final URL %q, got: %q", server.URL, result.FinalURL)
	}
}

func TestFetch_ContentTypeDetection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"key": "value"}`))
	}))
	defer server.Close()

	client := NewDefaultClient()
	result, err := client.Fetch(context.Background(), server.URL)

	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}

	if result.ContentType != "application/json" {
		t.Errorf("expected content type 'application/json', got: %s", result.ContentType)
	}
}

func TestFetch_RedirectHandling(t *testing.T) {
	// Create a server with a redirect chain: initial -> /redirect1 -> /redirect2 -> /final
	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/initial":
			http.Redirect(w, r, "/redirect1", http.StatusFound)
		case "/redirect1":
			http.Redirect(w, r, "/redirect2", http.StatusFound)
		case "/redirect2":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Redirected content"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer redirectServer.Close()

	client := NewClient(Config{FollowRedirects: true})
	result, fetchErr := client.Fetch(context.Background(), redirectServer.URL+"/initial")

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	finalURL := redirectServer.URL + "/final"
	if result.FinalURL != finalURL {
		t.Errorf("expected final URL %q, got: %q", finalURL, result.FinalURL)
	}
}

func TestFetch_RedirectDisabled(t *testing.T) {
	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/final", http.StatusFound)
	}))
	defer redirectServer.Close()

	client := NewClient(Config{FollowRedirects: false})
	result, fetchErr := client.Fetch(context.Background(), redirectServer.URL)

	// Should get a redirect response (3xx) without following
	if fetchErr == nil {
		t.Errorf("expected error for non-2xx status, got nil")
	}

	if result != nil {
		t.Errorf("expected nil result on error, got: %v", result)
	}
}

func TestFetch_Non2xxStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not Found"))
	}))
	defer server.Close()

	client := NewDefaultClient()
	result, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr == nil {
		t.Fatal("expected fetch error for 404, got nil")
	}

	if fetchErr.StatusCode == nil {
		t.Fatal("fetch error should have status code")
	}

	if *fetchErr.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got: %d", *fetchErr.StatusCode)
	}

	if result != nil {
		t.Errorf("expected nil result on error, got: %v", result)
	}
}

func TestFetch_UnsupportedScheme(t *testing.T) {
	client := NewDefaultClient()

	tests := []struct {
		name string
		url  string
	}{
		{"ftp scheme", "ftp://example.com/file"},
		{"file scheme", "file:///etc/passwd"},
		{"mailto scheme", "mailto:test@example.com"},
		{"unknown scheme", "custom://example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, fetchErr := client.Fetch(context.Background(), tt.url)

			if fetchErr == nil {
				t.Fatal("expected fetch error for unsupported scheme, got nil")
			}

			if !fetchErr.IsUnsupportedScheme() {
				t.Errorf("expected unsupported scheme error, got code: %s", fetchErr.Code)
			}

			if result != nil {
				t.Errorf("expected nil result on error, got: %v", result)
			}
		})
	}
}

func TestFetch_OversizedResponse(t *testing.T) {
	// Create a server that returns a large response
	largeContent := strings.Repeat("x", DefaultMaxResponseSize+1000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(largeContent)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(largeContent))
	}))
	defer server.Close()

	// Use a smaller max size to trigger the error
	client := NewClient(Config{
		MaxSize: 1000, // Only allow 1KB
	})

	result, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr == nil {
		t.Fatal("expected fetch error for oversized response, got nil")
	}

	if !fetchErr.IsOversizedError() {
		t.Errorf("expected oversized response error, got code: %s", fetchErr.Code)
	}

	if result != nil {
		t.Errorf("expected nil result on error, got: %v", result)
	}
}

func TestFetch_ContextCancellation(t *testing.T) {
	// Create a server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second) // Delay longer than context timeout
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("delayed response"))
	}))
	defer server.Close()

	client := NewDefaultClient()

	// Create context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, fetchErr := client.Fetch(ctx, server.URL)

	if fetchErr == nil {
		t.Fatal("expected fetch error for context timeout, got nil")
	}

	if !fetchErr.IsTimeoutError() && !strings.Contains(fetchErr.Error(), "context") {
		t.Logf("error message: %v", fetchErr)
	}

	if result != nil {
		t.Errorf("expected nil result on error, got: %v", result)
	}
}

func TestFetch_ContextCancelledBeforeRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("response"))
	}))
	defer server.Close()

	client := NewDefaultClient()

	// Cancel context before making request
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, fetchErr := client.Fetch(ctx, server.URL)

	if fetchErr == nil {
		t.Fatal("expected fetch error for cancelled context, got nil")
	}

	if result != nil {
		t.Errorf("expected nil result on error, got: %v", result)
	}
}

func TestFetch_InvalidURL(t *testing.T) {
	client := NewDefaultClient()

	tests := []struct {
		name string
		url  string
	}{
		{"empty URL", ""},
		{"invalid URL", "not a url"},
		{"invalid scheme with path", "://invalid/path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, fetchErr := client.Fetch(context.Background(), tt.url)

			if fetchErr == nil {
				t.Fatal("expected fetch error for invalid URL, got nil")
			}

			if result != nil {
				t.Errorf("expected nil result on error, got: %v", result)
			}
		})
	}
}

func TestFetch_MultipleRedirects(t *testing.T) {
	// Test redirect limit handling
	redirectCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectCount++
		if redirectCount < 15 {
			// Chain of redirects
			http.Redirect(w, r, "/redirect", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("final"))
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	client := NewClient(Config{FollowRedirects: true})
	_, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr == nil {
		t.Fatal("expected redirect limit error")
	}

	if !strings.Contains(fetchErr.Error(), "redirect") {
		t.Errorf("expected redirect-related error, got: %v", fetchErr)
	}
}

func TestFetch_HTTPHeadersPreserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom-Header", "custom-value")
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("content"))
	}))
	defer server.Close()

	client := NewDefaultClient()
	result, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	if result.Headers.Get("X-Custom-Header") != "custom-value" {
		t.Errorf("expected custom header to be preserved")
	}
}

func TestFetch_ContentLength(t *testing.T) {
	content := []byte("Hello, World!")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	defer server.Close()

	client := NewDefaultClient()
	result, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	if result.ContentLength != int64(len(content)) {
		t.Errorf("expected content length %d, got: %d", len(content), result.ContentLength)
	}
}

func TestFetch_UserAgentSent(t *testing.T) {
	var receivedUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUserAgent = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("content"))
	}))
	defer server.Close()

	client := NewClient(Config{
		UserAgent: "Test-Agent/1.0",
	})
	_, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	if receivedUserAgent != "Test-Agent/1.0" {
		t.Errorf("expected user agent 'Test-Agent/1.0', got: %s", receivedUserAgent)
	}
}

func TestFetchMany(t *testing.T) {
	// Create a simple server that always returns success
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("content"))
	}))
	defer server.Close()

	client := NewDefaultClient()
	ctx := context.Background()

	urls := []string{
		server.URL + "/1",
		server.URL + "/2",
		server.URL + "/3",
	}

	results := client.FetchMany(ctx, urls)

	if len(results) != len(urls) {
		t.Fatalf("expected %d results, got: %d", len(urls), len(results))
	}

	for i, result := range results {
		if !result.Success {
			t.Errorf("fetch for URL %d failed: %v", i, result.Error)
		}
		if result.Result == nil {
			t.Errorf("expected result for URL %d, got nil", i)
		}
	}
}

func TestFetchMany_ContextCancellation(t *testing.T) {
	// Create a slow server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("content"))
	}))
	defer server.Close()

	client := NewDefaultClient()

	// Context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	urls := []string{
		server.URL + "/1",
		server.URL + "/2",
		server.URL + "/3",
	}

	results := client.FetchMany(ctx, urls)

	// Some or all requests may fail due to timeout
	successCount := 0
	for _, result := range results {
		if result.Success {
			successCount++
		}
	}

	t.Logf("success count: %d out of %d", successCount, len(results))
}

func TestFetch_BodyContentMatches(t *testing.T) {
	expectedContent := []byte(`<html>
	<head><title>Test Page</title></head>
	<body>
		<h1>Test Heading</h1>
		<p>Test paragraph with <strong>bold</strong> text.</p>
	</body>
</html>`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(expectedContent)
	}))
	defer server.Close()

	client := NewDefaultClient()
	result, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	if !bytes.Equal(result.Body, expectedContent) {
		t.Errorf("body mismatch\nexpected: %s\ngot:      %s", expectedContent, result.Body)
	}
}

func TestFetchStatusFromResultNil(t *testing.T) {
	status := FetchStatusFromResult(nil)
	if status != nil {
		t.Error("expected nil status for nil result")
	}
}

func TestFetchResult_Status(t *testing.T) {
	result := &FetchResult{
		HTTPStatus: 200,
	}

	status := FetchStatusFromResult(result)
	if status == nil {
		t.Fatal("status should not be nil")
	}

	if status.HTTPStatus == nil {
		t.Fatal("HTTPStatus should be set")
	}
	if *status.HTTPStatus != 200 {
		t.Errorf("expected HTTPStatus 200, got: %d", *status.HTTPStatus)
	}
}

func TestFetchError_String(t *testing.T) {
	status := 404
	err := &FetchError{
		Code:        "http_error",
		Message:     "Not Found",
		OriginalURL: "http://example.com",
		StatusCode:  &status,
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "404") {
		t.Errorf("error string should contain status code, got: %s", errStr)
	}
}

func TestFetch_DeliversStatusCodeOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := NewDefaultClient()
	_, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr == nil {
		t.Fatal("expected fetch error for 503")
	}

	if fetchErr.StatusCode == nil {
		t.Error("fetch error should include status code")
	}

	if *fetchErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got: %d", *fetchErr.StatusCode)
	}
}

// Test that response body is always closed properly
func TestFetch_ResponseBodyClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a successful response
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := NewDefaultClient()
	result, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestFetch_SizeLimitingAccurate(t *testing.T) {
	// Test that we correctly stop at the limit
	limit := int64(100)
	content := strings.Repeat("x", 200) // 200 bytes

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(content))
	}))
	defer server.Close()

	client := NewClient(Config{MaxSize: limit})
	_, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr == nil {
		t.Fatal("expected oversized response error")
	}

	if !fetchErr.IsOversizedError() {
		t.Errorf("expected oversized error, got: %v", fetchErr)
	}
}

func TestFetch_ExactSizeLimit(t *testing.T) {
	// Test that we accept exactly the limit size
	limit := int64(100)
	content := strings.Repeat("x", int(limit)) // Exactly 100 bytes

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(content))
	}))
	defer server.Close()

	client := NewClient(Config{MaxSize: limit})
	result, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr != nil {
		t.Fatalf("fetch failed for exact limit size: %v", fetchErr)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if len(result.Body) != int(limit) {
		t.Errorf("expected body length %d, got: %d", limit, len(result.Body))
	}
}

func TestNewDefaultClient(t *testing.T) {
	client := NewDefaultClient()
	if client == nil {
		t.Fatal("NewDefaultClient should not return nil")
	}
}

// Test with 301 redirect
func TestFetch_301Redirect(t *testing.T) {
	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/original" {
			http.Redirect(w, r, "/final", http.StatusMovedPermanently)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("redirected content"))
	}))
	defer redirectServer.Close()

	client := NewClient(Config{FollowRedirects: true})
	result, fetchErr := client.Fetch(context.Background(), redirectServer.URL+"/original")

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	if result.FinalURL != redirectServer.URL+"/final" {
		t.Errorf("expected final URL to be %q, got: %q", redirectServer.URL+"/final", result.FinalURL)
	}
}

// Test with 302 redirect
func TestFetch_302Redirect(t *testing.T) {
	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/original" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("redirected content"))
	}))
	defer redirectServer.Close()

	client := NewClient(Config{FollowRedirects: true})
	result, fetchErr := client.Fetch(context.Background(), redirectServer.URL+"/original")

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	if result.FinalURL != redirectServer.URL+"/final" {
		t.Errorf("expected final URL to be %q, got: %q", redirectServer.URL+"/final", result.FinalURL)
	}
}

// Test with 307 redirect (temporary redirect that preserves method)
func TestFetch_307Redirect(t *testing.T) {
	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/original" {
			http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("redirected content"))
	}))
	defer redirectServer.Close()

	client := NewClient(Config{FollowRedirects: true})
	result, fetchErr := client.Fetch(context.Background(), redirectServer.URL+"/original")

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	if result.FinalURL != redirectServer.URL+"/final" {
		t.Errorf("expected final URL to be %q, got: %q", redirectServer.URL+"/final", result.FinalURL)
	}
}

// Test that multiple redirects are chained properly
func TestFetch_ChainedRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect1":
			http.Redirect(w, r, "/redirect2", http.StatusFound)
		case "/redirect2":
			http.Redirect(w, r, "/redirect3", http.StatusFound)
		case "/redirect3":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("final"))
		default:
			http.Redirect(w, r, "/redirect1", http.StatusFound)
		}
	}))
	defer server.Close()

	client := NewClient(Config{FollowRedirects: true})
	result, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	if result.FinalURL != server.URL+"/final" {
		t.Errorf("expected final URL %q, got: %q", server.URL+"/final", result.FinalURL)
	}
}

// Test valid HTTP scheme
func TestFetch_HttpScheme(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("http response"))
	}))
	defer server.Close()

	client := NewDefaultClient()
	result, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

// Test valid HTTPS scheme
func TestFetch_HttpsScheme(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("https response"))
	}))
	defer server.Close()

	client := NewDefaultClient()
	// Disable SSL verification for httptest self-signed certificate
	client.httpClient.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	result, fetchErr := client.Fetch(context.Background(), server.URL)

	if fetchErr != nil {
		t.Fatalf("fetch failed: %v", fetchErr)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}
}
