package llm

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

// Config holds the configuration for the OpenAI-compatible client.
type Config struct {
	// BaseURL is the base URL of the API, e.g., "http://mac-studio:8000/v1"
	BaseURL string
	// Model is the model name to use for completions
	Model string
	// Timeout is the maximum duration for a single request
	Timeout time.Duration
	// APIKey is an optional API key for authentication
	APIKey string
	// Temperature controls randomness in generation (0.0 to 1.0+)
	Temperature float64
	// MaxOutputTokens is the maximum number of tokens to generate
	// Zero means no limit (provider default)
	MaxOutputTokens int
}

// OpenAICompatClient is an OpenAI-compatible LLM client.
// It implements the Client interface by communicating with
// an OpenAI-compatible API endpoint (e.g., oMLX).
type OpenAICompatClient struct {
	config Config
	client *http.Client
}

// NewClient creates a new OpenAI-compatible client with the given configuration.
// The client uses a default HTTP timeout if config.Timeout is zero.
func NewClient(config Config) *OpenAICompatClient {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &OpenAICompatClient{
		config: config,
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// Complete performs a chat completion request to the OpenAI-compatible API.
//
// It sends a request with the given prompt and returns the generated content.
// The context can be used to cancel the request asynchronously.
//
// Errors are returned as typed llm.Error values:
//   - ContextCanceled: if the context is canceled or times out
//   - RateLimited: if the provider returns a 429 status
//   - Internal: for other HTTP errors or JSON parsing failures
//   - EmptyResponse: if the model returns no content (returned as a standard error)
func (c *OpenAICompatClient) Complete(ctx context.Context, req Request) (Response, error) {
	// Prepare the request body
	body := chatCompletionRequest{
		Model:       req.Model,
		Messages:    buildMessages(req),
		Temperature: c.buildTemperature(req.Temperature),
		MaxTokens:   c.buildMaxTokens(req.MaxOutputTokens),
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return Response{}, Internal("failed to marshal request: " + err.Error())
	}

	// Create the HTTP request
	url := c.config.BaseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonBody))
	if err != nil {
		return Response{}, Internal("failed to create request: " + err.Error())
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
			return Response{}, ContextCanceled("request canceled: " + ctx.Err().Error())
		}
		return Response{}, Internal("connection failed: " + err.Error())
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// Handle non-2xx responses
	if resp.StatusCode != http.StatusOK {
		return Response{}, c.handleHTTPError(resp)
	}

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, Internal("failed to read response: " + err.Error())
	}

	// Parse response
	var chatResp chatCompletionResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return Response{}, Internal("failed to parse response: " + err.Error())
	}

	// Validate response content
	if len(chatResp.Choices) == 0 {
		return Response{}, errors.New("empty model response: no choices in response")
	}

	content := chatResp.Choices[0].Message.Content
	if content == "" {
		return Response{}, errors.New("empty model response: message content is empty")
	}

	return Response{Content: content}, nil
}

// buildMessages constructs the messages array from the request.
// System prompt becomes a system message, user prompt becomes a user message.
func buildMessages(req Request) []message {
	msgs := make([]message, 0, 2)

	if req.SystemPrompt != "" {
		msgs = append(msgs, message{
			Role:    "system",
			Content: req.SystemPrompt,
		})
	}

	if req.UserPrompt != "" {
		msgs = append(msgs, message{
			Role:    "user",
			Content: req.UserPrompt,
		})
	}

	// If both are empty, still send at least a user message to satisfy API requirements
	if len(msgs) == 0 {
		msgs = append(msgs, message{
			Role:    "user",
			Content: " ",
		})
	}

	return msgs
}

// buildTemperature returns the temperature value, clamped to [0, 2] range.
func (c *OpenAICompatClient) buildTemperature(temp float64) float64 {
	// OpenAI-compatible APIs typically expect temperature in [0, 2]
	// Default to 0.7 if zero or negative
	if temp <= 0 {
		return 0.7
	}
	// Clamp to reasonable range
	if temp > 2.0 {
		return 2.0
	}
	return temp
}

// buildMaxTokens returns the max_tokens value or nil if not specified.
func (c *OpenAICompatClient) buildMaxTokens(maxTokens int) *int {
	if maxTokens <= 0 {
		return nil
	}
	return &maxTokens
}

// handleHTTPError parses the HTTP error response and returns an appropriate error.
func (c *OpenAICompatClient) handleHTTPError(resp *http.Response) error {
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

// chatCompletionRequest represents the request body for /chat/completions.
type chatCompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
}

// message represents a chat message.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatCompletionResponse represents the response body from /chat/completions.
type chatCompletionResponse struct {
	Choices []choice `json:"choices"`
}

// choice represents a single choice in the response.
type choice struct {
	Message message `json:"message"`
}
