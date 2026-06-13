// Package openrouter implements the models.Provider interface for the
// OpenRouter API (OpenAI-compatible chat completions).
package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Compile-time interface check.
var _ models.Provider = (*Client)(nil)

// Default values.
const (
	defaultBaseURL    = "https://openrouter.ai"
	defaultMaxRetries = 3
	maxBackoff        = 30 * time.Second
	maxScannerBuf     = 1 << 20 // 1 MB
)

// --- Wire-format types (unexported, JSON serialization only) ---

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	Tools       []chatTool    `json:"tools,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	Provider    *providerPrefs `json:"provider,omitempty"`
}

type providerPrefs struct {
	Order          []string `json:"order,omitempty"`
	Ignore         []string `json:"ignore,omitempty"`
	AllowFallbacks *bool    `json:"allow_fallbacks,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"` // string or []contentPart
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  any `json:"parameters"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatResponse struct {
	Choices []chatChoice   `json:"choices"`
	Usage   *chatUsage     `json:"usage,omitempty"`
	Error   *chatRespError `json:"error,omitempty"`
	Model   string         `json:"model,omitempty"`
}

type chatChoice struct {
	Message      *chatMessage `json:"message,omitempty"`
	Delta        *chatMessage `json:"delta,omitempty"`
	FinishReason string       `json:"finish_reason,omitempty"`
}

type chatUsage struct {
	PromptTokens     int64    `json:"prompt_tokens"`
	CompletionTokens int64    `json:"completion_tokens"`
	TotalCost        *float64 `json:"total_cost,omitempty"`
}

type chatRespError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type modelsResponse struct {
	Data []modelEntry `json:"data"`
}

type modelEntry struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	ContextLength int          `json:"context_length"`
	Pricing       modelPricing `json:"pricing"`
}

type modelPricing struct {
	Prompt     string `json:"prompt"`
	Completion string `json:"completion"`
}

// --- Public error type ---

// APIError represents an error returned by the OpenRouter API.
type APIError struct {
	StatusCode int
	Message    string
	Type       string
	Retryable  bool
}

func (e *APIError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("openrouter: %s (status %d, type %s)", e.Message, e.StatusCode, e.Type)
	}
	return fmt.Sprintf("openrouter: %s (status %d)", e.Message, e.StatusCode)
}

// --- Client ---

// KeyResolverFunc resolves an API key for a specific agent. If it returns
// an error or an empty string, the client falls back to the default apiKey.
type KeyResolverFunc func(ctx context.Context, agentID string) (string, error)

// Client implements models.Provider for the OpenRouter API.
type Client struct {
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	maxRetries  int
	baseBackoff time.Duration // base duration for exponential backoff
	keyResolver KeyResolverFunc
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the default OpenRouter base URL.
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

// WithKeyResolver sets a function that resolves per-agent API keys.
// When set, the resolver is called for each request that has an agent ID
// in the context. If the resolver returns an error or empty string,
// the default API key is used.
func WithKeyResolver(fn KeyResolverFunc) Option {
	return func(c *Client) { c.keyResolver = fn }
}

// New creates a new OpenRouter client.
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
func (c *Client) Name() string { return "openrouter" }

// Complete sends a non-streaming completion request and returns the full response.
func (c *Client) Complete(ctx context.Context, req models.CompletionRequest) (*models.CompletionResponse, error) {
	body, err := json.Marshal(buildChatRequest(req, false))
	if err != nil {
		return nil, fmt.Errorf("openrouter: marshal request: %w", err)
	}

	respBody, err := c.doRequestWithRetry(ctx, http.MethodPost, "/api/v1/chat/completions", body)
	if err != nil {
		return nil, err
	}

	var cr chatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, fmt.Errorf("openrouter: unmarshal response: %w", err)
	}
	if cr.Error != nil {
		return nil, &APIError{
			StatusCode: 0,
			Message:    cr.Error.Message,
			Type:       cr.Error.Type,
		}
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("openrouter: empty choices in response")
	}

	msg := cr.Choices[0].Message
	if msg == nil {
		return nil, fmt.Errorf("openrouter: nil message in response")
	}

	contentStr, _ := msg.Content.(string)
	resp := &models.CompletionResponse{
		Content:    contentStr,
		StopReason: normalizeFinishReason(cr.Choices[0].FinishReason),
		Model:      cr.Model,
	}

	for _, tc := range msg.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, models.ToolUse{
			ID:         tc.ID,
			Name:       tc.Function.Name,
			Parameters: parseArguments(tc.Function.Arguments),
		})
	}

	if cr.Usage != nil {
		resp.TokensIn = cr.Usage.PromptTokens
		resp.TokensOut = cr.Usage.CompletionTokens
		if cr.Usage.TotalCost != nil {
			resp.Cost = *cr.Usage.TotalCost
		}
	}

	return resp, nil
}

// Stream sends a streaming completion request and returns a channel of response chunks.
func (c *Client) Stream(ctx context.Context, req models.CompletionRequest) (<-chan models.StreamChunk, error) {
	body, err := json.Marshal(buildChatRequest(req, true))
	if err != nil {
		return nil, fmt.Errorf("openrouter: marshal request: %w", err)
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

			// Skip empty lines and SSE comments.
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			// Strip "data: " prefix.
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}

			if data == "[DONE]" {
				send(ctx, ch, models.StreamChunk{Done: true})
				return
			}

			var cr chatResponse
			if err := json.Unmarshal([]byte(data), &cr); err != nil {
				send(ctx, ch, models.StreamChunk{Error: fmt.Sprintf("unmarshal chunk: %v", err)})
				return
			}
			if cr.Error != nil {
				send(ctx, ch, models.StreamChunk{Error: cr.Error.Message})
				return
			}

			if len(cr.Choices) > 0 && cr.Choices[0].Delta != nil {
				if content, ok := cr.Choices[0].Delta.Content.(string); ok && content != "" {
					if !send(ctx, ch, models.StreamChunk{Content: content}) {
						return
					}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			send(ctx, ch, models.StreamChunk{Error: fmt.Sprintf("read stream: %v", err)})
		}
	}()

	return ch, nil
}

// ListModels returns the models available through OpenRouter.
func (c *Client) ListModels(ctx context.Context) ([]models.ModelInfo, error) {
	respBody, err := c.doRequestWithRetry(ctx, http.MethodGet, "/api/v1/models", nil)
	if err != nil {
		return nil, err
	}

	var mr modelsResponse
	if err := json.Unmarshal(respBody, &mr); err != nil {
		return nil, fmt.Errorf("openrouter: unmarshal models: %w", err)
	}

	out := make([]models.ModelInfo, len(mr.Data))
	for i, m := range mr.Data {
		out[i] = models.ModelInfo{
			ID:            m.ID,
			Name:          m.Name,
			Provider:      "openrouter",
			ContextSize:   m.ContextLength,
			CostPerMInput: parsePricingPerMillion(m.Pricing.Prompt),
			CostPerMOut:   parsePricingPerMillion(m.Pricing.Completion),
		}
	}
	return out, nil
}

// --- Helpers ---

func buildChatRequest(req models.CompletionRequest, stream bool) chatRequest {
	cr := chatRequest{
		Model:  req.Model,
		Stream: stream,
	}
	if req.MaxTokens > 0 {
		cr.MaxTokens = req.MaxTokens
	}
	if req.Temperature != 0 {
		t := req.Temperature
		cr.Temperature = &t
	}

	for _, m := range req.Messages {
		cm := chatMessage{
			Role:       m.Role,
			ToolCallID: m.ToolCallID,
		}
		if types.HasAttachmentsWithData(m.Attachments) {
			var parts []contentPart
			for _, att := range m.Attachments {
				if len(att.Data) > 0 && types.IsImageMIME(att.ContentType) {
					parts = append(parts, contentPart{
						Type: "image_url",
						ImageURL: &imageURL{
							URL: "data:" + att.ContentType + ";base64," + base64.StdEncoding.EncodeToString(att.Data),
						},
					})
				}
			}
			if m.Content != "" {
				parts = append(parts, contentPart{Type: "text", Text: m.Content})
			}
			cm.Content = parts
		} else {
			content := m.Content
			if len(m.Attachments) > 0 {
				content = types.AnnotateAttachments(m.Attachments) + content
			}
			cm.Content = content
		}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, chatToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: chatFunctionCall{
					Name:      tc.Name,
					Arguments: marshalArguments(tc.Parameters),
				},
			})
		}
		cr.Messages = append(cr.Messages, cm)
	}

	for _, t := range req.Tools {
		cr.Tools = append(cr.Tools, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	// When tools are present and provider routing hints are configured,
	// add OpenRouter provider preferences to avoid incompatible upstreams.
	if len(cr.Tools) > 0 && len(req.ProviderIgnore) > 0 {
		allowFallbacks := true
		cr.Provider = &providerPrefs{
			Ignore:         req.ProviderIgnore,
			AllowFallbacks: &allowFallbacks,
		}
	}

	return cr
}

func (c *Client) setHeaders(ctx context.Context, req *http.Request) {
	apiKey := c.apiKey
	if c.keyResolver != nil {
		if agentID := models.AgentIDFromContext(ctx); agentID != "" {
			if resolved, err := c.keyResolver(ctx, agentID); err == nil && resolved != "" {
				apiKey = resolved
			}
		}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
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
			return nil, fmt.Errorf("openrouter: build request: %w", err)
		}
		c.setHeaders(ctx, httpReq)

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
			lastErr = fmt.Errorf("openrouter: read response: %w", err)
			c.backoff(ctx, attempt)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if len(respBody) == 0 || !json.Valid(respBody) {
				lastErr = fmt.Errorf("openrouter: invalid/truncated response body (%d bytes) with status %d", len(respBody), resp.StatusCode)
				c.backoff(ctx, attempt)
				continue
			}
			return respBody, nil
		}

		apiErr := parseAPIError(resp.StatusCode, respBody)
		lastErr = apiErr

		if resp.StatusCode == 400 {
			slog.Error("openrouter 400 response body", "body", string(respBody))
		}

		if !apiErr.Retryable {
			return nil, apiErr
		}

		c.backoff(ctx, attempt)
	}

	if lastErr != nil {
		return nil, fmt.Errorf("openrouter: max retries exceeded: %w", lastErr)
	}
	return nil, fmt.Errorf("openrouter: max retries exceeded")
}

func (c *Client) doStreamRequest(ctx context.Context, body []byte) (*http.Response, error) {
	var lastErr error
	for attempt := range c.maxRetries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("openrouter: build request: %w", err)
		}
		c.setHeaders(ctx, httpReq)

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
		return nil, fmt.Errorf("openrouter: max retries exceeded: %w", lastErr)
	}
	return nil, fmt.Errorf("openrouter: max retries exceeded")
}

func parseAPIError(statusCode int, body []byte) *APIError {
	apiErr := &APIError{
		StatusCode: statusCode,
		Retryable:  statusCode == 429 || statusCode >= 500,
	}

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		apiErr.Message = errResp.Error.Message
		apiErr.Type = errResp.Error.Type
	} else {
		apiErr.Message = string(body)
	}

	return apiErr
}

// normalizeFinishReason maps OpenAI/OpenRouter finish_reason to normalized values.
func normalizeFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return reason
	}
}

func parseArguments(raw string) any {
	if raw == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	return v
}

func marshalArguments(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func parsePricingPerMillion(perToken string) float64 {
	f, err := strconv.ParseFloat(perToken, 64)
	if err != nil {
		return 0
	}
	return f * 1_000_000
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
