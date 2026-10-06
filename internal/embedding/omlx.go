package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Config holds the configuration for the oMLX embedding client.
type Config struct {
	// BaseURL is the base URL of the oMLX API, e.g., "http://localhost:8000/v1"
	BaseURL string
	// EmbeddingModel is the model name to use for embeddings
	EmbeddingModel string
	// Timeout is the maximum duration for a single request
	Timeout time.Duration
	// APIKey is an optional API key for authentication
	APIKey string
	// Dimensions is an optional dimension specification when supported by the provider
	// Set to a positive value to request embeddings of specific dimension
	Dimensions int
}

// NewClient creates a new oMLX embedding client with the given configuration.
// The client uses a default HTTP timeout if config.Timeout is zero.
func NewClient(config Config) *Client {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &Client{
		config: config,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// Client is an oMLX embedding client that uses the OpenAI-compatible
// /v1/embeddings endpoint.
type Client struct {
	config Config
	client *http.Client
}

// Embed generates embeddings for the given input texts.
//
// It sends a request to the /v1/embeddings endpoint and returns the embedded
// vectors with validation. Context cancellation is supported.
//
// Errors are returned as typed embedding.Error values:
//   - ContextCanceled: if the context is canceled or times out
//   - RateLimited: if the provider returns a 429 status
//   - InvalidResponse: if the provider returns a response that cannot be parsed
//     or doesn't match expected structure
//   - Internal: for other HTTP errors or JSON parsing failures
//
// The returned slice has the same length as the inputs.
func (c *Client) Embed(ctx context.Context, req Request) ([]Embedding, error) {
	// Validate input
	if len(req.Inputs) == 0 {
		return nil, InvalidResponse("empty inputs")
	}

	// Prepare the request body
	embeddingReq := embeddingRequest{
		Model:      c.resolveModel(req.Model),
		Inputs:     req.Inputs,
		Dimensions: c.resolveDimensions(req.Dimensions),
	}

	jsonBody, err := json.Marshal(embeddingReq)
	if err != nil {
		return nil, Internal("failed to marshal request: " + err.Error())
	}

	// Create the HTTP request
	url := c.config.BaseURL + "/embeddings"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, Internal("failed to create request: " + err.Error())
	}

	// Set headers
	httpReq.Header.Set("Content-Type", "application/json")
	if c.config.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}

	// Execute the request
	resp, err := c.client.Do(httpReq)
	if err != nil {
		// Check for context cancellation
		if ctx.Err() != nil {
			return nil, ContextCanceled("request canceled: " + ctx.Err().Error())
		}
		return nil, Internal("connection failed: " + err.Error())
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// Handle non-2xx responses
	if resp.StatusCode != http.StatusOK {
		return nil, c.handleHTTPError(resp)
	}

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, Internal("failed to read response: " + err.Error())
	}

	// Parse response
	var embedResp embeddingResponse
	if err := json.Unmarshal(respBody, &embedResp); err != nil {
		return nil, InvalidResponse("failed to parse response: " + err.Error())
	}

	// Validate response structure
	if len(embedResp.Data) == 0 {
		return nil, InvalidResponse("no embeddings returned")
	}

	// Convert response to Embedding structs with validation
	embeddings := make([]Embedding, len(embedResp.Data))
	for _, data := range embedResp.Data {
		if data.Index >= len(embedResp.Data) {
			return nil, InvalidResponse("invalid embedding index in response")
		}

		if err := ValidateVector(data.Vector, data.Index); err != nil {
			return nil, InvalidResponse(err.Error())
		}

		embeddings[data.Index] = Embedding{
			Index:     data.Index,
			Vector:    data.Vector,
			ModelUsed: data.Model,
		}
	}

	// Validate batch dimension consistency
	if err := ValidateBatch(embeddings); err != nil {
		return nil, InvalidResponse(err.Error())
	}

	return embeddings, nil
}

// resolveModel returns the model to use, preferring the request model
// if specified, otherwise the configured default.
func (c *Client) resolveModel(reqModel string) string {
	if reqModel != "" {
		return reqModel
	}
	return c.config.EmbeddingModel
}

// resolveDimensions returns the dimensions to use, preferring the request
// dimensions if set, otherwise the configured default.
func (c *Client) resolveDimensions(reqDims int) *int {
	dim := reqDims
	if dim <= 0 {
		dim = c.config.Dimensions
	}
	if dim > 0 {
		return &dim
	}
	return nil
}

// handleHTTPError parses the HTTP error response and returns an appropriate error.
func (c *Client) handleHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)

	// Try to parse provider-specific error
	var apiErr struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
		msg := apiErr.Error.Message
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			return RateLimited(msg)
		case http.StatusInternalServerError:
			return Internal("provider error: " + msg)
		case http.StatusUnauthorized:
			return Internal("unauthorized: " + msg)
		case http.StatusNotFound:
			return Internal("endpoint not found: " + msg)
		default:
			return Internal(fmt.Sprintf("provider error (%d): %s", resp.StatusCode, msg))
		}
	}

	// Fallback to generic errors based on status code
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return RateLimited("rate limit exceeded")
	case http.StatusInternalServerError:
		return Internal("internal server error")
	case http.StatusUnauthorized:
		return Internal("unauthorized")
	case http.StatusNotFound:
		return Internal("endpoint not found")
	default:
		errMsg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if len(body) > 0 {
			errMsg += ": " + string(body)
		}
		return Internal(errMsg)
	}
}

// embeddingRequest represents the request body for /embeddings.
// Based on the OpenAI embeddings API format.
type embeddingRequest struct {
	Model      string   `json:"model"`
	Inputs     []string `json:"input"`
	Dimensions *int     `json:"dimensions,omitempty"`
}

// embeddingResponse represents the response body from /embeddings.
// Based on the OpenAI embeddings API format.
type embeddingResponse struct {
	Object string          `json:"object"`
	Model  string          `json:"model"`
	Data   []embeddingData `json:"data"`
	Usage  usage           `json:"usage,omitempty"`
}

// embeddingData represents a single embedding in the response.
type embeddingData struct {
	Object string    `json:"object"`
	Index  int       `json:"index"`
	Vector []float32 `json:"embedding"`
	Model  string    `json:"-"` // Not in response, set from parent response
}

// usage represents token usage information.
type usage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// WithDimensions returns a new config with dimensions set.
func (c Config) WithDimensions(dimensions int) Config {
	c.Dimensions = dimensions
	return c
}

// errors for embedding-specific cases
var (
	ErrEmptyInputs = errors.New("embedding request has no inputs")
)
