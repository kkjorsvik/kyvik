// Package anthropic implements the models.Provider interface for the
// Anthropic Messages API.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Compile-time interface check.
var _ models.Provider = (*Client)(nil)

// Default values.
const (
	defaultBaseURL    = "https://api.anthropic.com"
	defaultMaxRetries = 3
	apiVersion        = "2023-06-01"
	defaultMaxTokens  = 4096
	maxBackoff        = 30 * time.Second
	maxScannerBuf     = 1 << 20 // 1 MB
)

// --- Wire-format types (unexported, JSON serialization only) ---

type msgRequest struct {
	Model       string       `json:"model"`
	MaxTokens   int          `json:"max_tokens"`
	System      string       `json:"system,omitempty"`
	Messages    []msgMessage `json:"messages"`
	Temperature *float64     `json:"temperature,omitempty"`
	Tools       []msgTool    `json:"tools,omitempty"`
	Stream      bool         `json:"stream,omitempty"`
}

type msgMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string for simple text, []contentBlock for tool results/mixed
}

type contentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`        // type=text
	ID        string `json:"id,omitempty"`          // type=tool_use
	Name      string `json:"name,omitempty"`        // type=tool_use
	Input     any    `json:"input,omitempty"`       // type=tool_use
	ToolUseID string `json:"tool_use_id,omitempty"` // type=tool_result
	Content   string `json:"content,omitempty"`     // type=tool_result (result text)
}

type msgTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type msgResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    []contentBlock `json:"content"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Usage      *msgUsage      `json:"usage,omitempty"`
}

type msgUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

type streamEvent struct {
	Type         string          `json:"type"`
	Message      *msgResponse    `json:"message,omitempty"`
	Index        int             `json:"index,omitempty"`
	ContentBlock *contentBlock   `json:"content_block,omitempty"`
	Delta        json.RawMessage `json:"delta,omitempty"`
	Usage        *msgUsage       `json:"usage,omitempty"`
}

type textDelta struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// --- Public error type ---

// APIError represents an error returned by the Anthropic API.
type APIError struct {
	StatusCode int
	Message    string
	Type       string
	Retryable  bool
}

func (e *APIError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("anthropic: %s (status %d, type %s)", e.Message, e.StatusCode, e.Type)
	}
	return fmt.Sprintf("anthropic: %s (status %d)", e.Message, e.StatusCode)
}

// --- Client ---

// Client implements models.Provider for the Anthropic Messages API.
type Client struct {
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	maxRetries  int
	baseBackoff time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the default Anthropic base URL.
func WithBaseURL(url string) Option {
	return func(c *Client) { c.baseURL = url }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithMaxRetries sets the maximum number of retry attempts.
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = n }
}

// WithBaseBackoff sets the base backoff duration for retries (default 1s).
func WithBaseBackoff(d time.Duration) Option {
	return func(c *Client) { c.baseBackoff = d }
}

// New creates a new Anthropic client.
func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:      apiKey,
		baseURL:     defaultBaseURL,
		httpClient:  &http.Client{Timeout: 120 * time.Second},
		maxRetries:  defaultMaxRetries,
		baseBackoff: time.Second,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Name returns the provider identifier.
func (c *Client) Name() string { return "anthropic" }

// Complete sends a non-streaming completion request and returns the full response.
func (c *Client) Complete(ctx context.Context, req models.CompletionRequest) (*models.CompletionResponse, error) {
	body, err := json.Marshal(buildRequest(req, false))
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	respBody, err := c.doRequestWithRetry(ctx, http.MethodPost, "/v1/messages", body)
	if err != nil {
		return nil, err
	}

	var mr msgResponse
	if err := json.Unmarshal(respBody, &mr); err != nil {
		return nil, fmt.Errorf("anthropic: unmarshal response: %w", err)
	}

	resp := &models.CompletionResponse{
		Model:      mr.Model,
		StopReason: normalizeStopReason(mr.StopReason),
	}

	// Extract text content and tool calls from content blocks.
	var textParts []string
	for _, block := range mr.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "tool_use":
			resp.ToolCalls = append(resp.ToolCalls, models.ToolUse{
				ID:         block.ID,
				Name:       block.Name,
				Parameters: block.Input,
			})
		}
	}
	resp.Content = strings.Join(textParts, "")

	if mr.Usage != nil {
		resp.TokensIn = mr.Usage.InputTokens
		resp.TokensOut = mr.Usage.OutputTokens
		resp.Cost = calculateCost(mr.Model, int64(resp.TokensIn), int64(resp.TokensOut))
	}

	return resp, nil
}

// Stream sends a streaming completion request and returns a channel of response chunks.
func (c *Client) Stream(ctx context.Context, req models.CompletionRequest) (<-chan models.StreamChunk, error) {
	body, err := json.Marshal(buildRequest(req, true))
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	resp, err := c.doStreamRequest(ctx, body)
	if err != nil {
		return nil, err
	}

	ch := make(chan models.StreamChunk, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 0, 4096)
		scanner.Buffer(buf, maxScannerBuf)

		for scanner.Scan() {
			line := scanner.Text()

			// Skip empty lines, SSE comments, and event type lines.
			if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
				continue
			}

			// Strip "data: " prefix.
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}

			var event streamEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				send(ctx, ch, models.StreamChunk{Error: fmt.Sprintf("unmarshal chunk: %v", err)})
				return
			}

			switch event.Type {
			case "content_block_delta":
				if len(event.Delta) > 0 {
					var td textDelta
					if err := json.Unmarshal(event.Delta, &td); err == nil && td.Type == "text_delta" && td.Text != "" {
						if !send(ctx, ch, models.StreamChunk{Content: td.Text}) {
							return
						}
					}
				}
			case "message_stop":
				send(ctx, ch, models.StreamChunk{Done: true})
				return
			case "error":
				msg := "unknown stream error"
				if len(event.Delta) > 0 {
					var errDelta struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					}
					if json.Unmarshal(event.Delta, &errDelta) == nil && errDelta.Message != "" {
						msg = errDelta.Message
					}
				}
				send(ctx, ch, models.StreamChunk{Error: msg})
				return
			}
		}

		if err := scanner.Err(); err != nil {
			send(ctx, ch, models.StreamChunk{Error: fmt.Sprintf("read stream: %v", err)})
		}
	}()

	return ch, nil
}

// ListModels returns a static list of Anthropic models from the pricing table.
func (c *Client) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	var out []models.ModelInfo
	for id := range pricingTable {
		inputPerM, outputPerM := getPricing(id)
		out = append(out, models.ModelInfo{
			ID:            id,
			Name:          id,
			Provider:      "anthropic",
			CostPerMInput: inputPerM,
			CostPerMOut:   outputPerM,
		})
	}
	return out, nil
}

// --- Helpers ---

func buildRequest(req models.CompletionRequest, stream bool) msgRequest {
	mr := msgRequest{
		Model:  req.Model,
		Stream: stream,
	}

	// max_tokens is required by Anthropic.
	if req.MaxTokens > 0 {
		mr.MaxTokens = req.MaxTokens
	} else {
		mr.MaxTokens = defaultMaxTokens
	}

	if req.Temperature != 0 {
		t := req.Temperature
		mr.Temperature = &t
	}

	// Extract system messages to top-level field.
	var systemParts []string
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemParts = append(systemParts, m.Content)
		}
	}
	if len(systemParts) > 0 {
		mr.System = strings.Join(systemParts, "\n\n")
	}

	// Convert non-system messages.
	// Anthropic requires alternating user/assistant roles.
	// Consecutive tool results (role:"tool") get merged into a single user message.
	var pendingToolResults []contentBlock

	flushToolResults := func() {
		if len(pendingToolResults) == 0 {
			return
		}
		// Convert to []any for JSON marshalling as mixed content blocks.
		blocks := make([]any, len(pendingToolResults))
		for i, b := range pendingToolResults {
			blocks[i] = b
		}
		mr.Messages = append(mr.Messages, msgMessage{
			Role:    "user",
			Content: blocks,
		})
		pendingToolResults = nil
	}

	for _, m := range req.Messages {
		if m.Role == "system" {
			continue
		}

		if m.Role == "tool" {
			// Accumulate tool results to merge consecutive ones.
			pendingToolResults = append(pendingToolResults, contentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			})
			continue
		}

		// Flush any pending tool results before a non-tool message.
		flushToolResults()

		if m.Role == "assistant" {
			// Build content blocks for assistant messages.
			var blocks []any
			if m.Content != "" {
				blocks = append(blocks, contentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, contentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: tc.Parameters,
				})
			}
			if len(blocks) == 1 && m.Content != "" && len(m.ToolCalls) == 0 {
				// Simple text-only assistant message — use plain string.
				mr.Messages = append(mr.Messages, msgMessage{
					Role:    "assistant",
					Content: m.Content,
				})
			} else if len(blocks) > 0 {
				mr.Messages = append(mr.Messages, msgMessage{
					Role:    "assistant",
					Content: blocks,
				})
			}
		} else {
			// User message
			if types.HasAttachmentsWithData(m.Attachments) {
				var blocks []any
				for _, att := range m.Attachments {
					if len(att.Data) > 0 && types.IsImageMIME(att.ContentType) {
						blocks = append(blocks, map[string]any{
							"type": "image",
							"source": map[string]any{
								"type":       "base64",
								"media_type": att.ContentType,
								"data":       base64.StdEncoding.EncodeToString(att.Data),
							},
						})
					}
				}
				if m.Content != "" {
					blocks = append(blocks, contentBlock{Type: "text", Text: m.Content})
				}
				mr.Messages = append(mr.Messages, msgMessage{Role: m.Role, Content: blocks})
			} else {
				content := m.Content
				if len(m.Attachments) > 0 {
					content = types.AnnotateAttachments(m.Attachments) + content
				}
				mr.Messages = append(mr.Messages, msgMessage{
					Role:    m.Role,
					Content: content,
				})
			}
		}
	}

	// Flush any trailing tool results.
	flushToolResults()

	// Map tool definitions.
	for _, t := range req.Tools {
		mr.Tools = append(mr.Tools, msgTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}

	return mr
}

// normalizeStopReason maps Anthropic stop_reason to normalized values.
func normalizeStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "end"
	case "tool_use":
		return "tool_use"
	case "max_tokens":
		return "max_tokens"
	default:
		return reason
	}
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", apiVersion)
	req.Header.Set("Content-Type", "application/json")
}

func (c *Client) doRequestWithRetry(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := range c.maxRetries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}

		httpReq, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
		if err != nil {
			return nil, fmt.Errorf("anthropic: build request: %w", err)
		}
		c.setHeaders(httpReq)

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			c.backoff(ctx, attempt)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("anthropic: read response: %w", err)
			c.backoff(ctx, attempt)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return respBody, nil
		}

		apiErr := parseAPIError(resp.StatusCode, respBody)
		lastErr = apiErr

		if !apiErr.Retryable {
			return nil, apiErr
		}

		c.backoff(ctx, attempt)
	}

	if lastErr != nil {
		return nil, fmt.Errorf("anthropic: max retries exceeded: %w", lastErr)
	}
	return nil, fmt.Errorf("anthropic: max retries exceeded")
}

func (c *Client) doStreamRequest(ctx context.Context, body []byte) (*http.Response, error) {
	var lastErr error
	for attempt := range c.maxRetries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("anthropic: build request: %w", err)
		}
		c.setHeaders(httpReq)

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			c.backoff(ctx, attempt)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		apiErr := parseAPIError(resp.StatusCode, respBody)
		lastErr = apiErr

		if !apiErr.Retryable {
			return nil, apiErr
		}

		c.backoff(ctx, attempt)
	}

	if lastErr != nil {
		return nil, fmt.Errorf("anthropic: max retries exceeded: %w", lastErr)
	}
	return nil, fmt.Errorf("anthropic: max retries exceeded")
}

func parseAPIError(statusCode int, body []byte) *APIError {
	apiErr := &APIError{
		StatusCode: statusCode,
		Retryable:  statusCode == 429 || statusCode == 529 || statusCode >= 500,
	}

	var errResp struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		apiErr.Message = errResp.Error.Message
		apiErr.Type = errResp.Error.Type
	} else {
		apiErr.Message = string(body)
	}

	if statusCode == 401 {
		apiErr.Message = "Invalid API key. Set ANTHROPIC_API_KEY or store as anthropic:api_key in secrets vault."
	}

	return apiErr
}

func (c *Client) backoff(ctx context.Context, attempt int) {
	d := min(time.Duration(math.Pow(2, float64(attempt)))*c.baseBackoff, maxBackoff)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// send performs a context-aware channel send. Returns false if context was cancelled.
func send(ctx context.Context, ch chan<- models.StreamChunk, chunk models.StreamChunk) bool {
	select {
	case ch <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}
