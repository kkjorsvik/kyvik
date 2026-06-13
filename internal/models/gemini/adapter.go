// Package gemini implements the models.Provider interface for Google's
// Gemini (Generative Language) API.
package gemini

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
	defaultBaseURL    = "https://generativelanguage.googleapis.com"
	defaultMaxRetries = 3
	defaultMaxTokens  = 4096
	maxBackoff        = 30 * time.Second
	maxScannerBuf     = 1 << 20 // 1 MB
)

// --- Wire-format types (unexported, JSON serialization only) ---

type generateRequest struct {
	Contents          []content          `json:"contents"`
	SystemInstruction *content           `json:"systemInstruction,omitempty"`
	Tools             []toolDecl         `json:"tools,omitempty"`
	GenerationConfig  *generationConfig  `json:"generationConfig,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text             string            `json:"text,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
	InlineData       *inlineData       `json:"inlineData,omitempty"`
}

type functionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type functionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type inlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"` // base64
}

type toolDecl struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations,omitempty"`
}

type functionDeclaration struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
}

type generateResponse struct {
	Candidates    []candidate    `json:"candidates"`
	UsageMetadata *usageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion  string         `json:"modelVersion,omitempty"`
}

type candidate struct {
	Content      *content `json:"content,omitempty"`
	FinishReason string   `json:"finishReason,omitempty"`
}

type usageMetadata struct {
	PromptTokenCount     int64 `json:"promptTokenCount"`
	CandidatesTokenCount int64 `json:"candidatesTokenCount"`
	TotalTokenCount      int64 `json:"totalTokenCount"`
}

// --- Public error type ---

// APIError represents an error returned by the Gemini API.
type APIError struct {
	StatusCode int
	Message    string
	Retryable  bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gemini: %s (status %d)", e.Message, e.StatusCode)
}

// --- Client ---

// Client implements models.Provider for Google's Gemini API.
type Client struct {
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	maxRetries  int
	baseBackoff time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the default Gemini base URL.
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

// New creates a new Gemini client.
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
func (c *Client) Name() string { return "gemini" }

// Complete sends a non-streaming completion request and returns the full response.
func (c *Client) Complete(ctx context.Context, req models.CompletionRequest) (*models.CompletionResponse, error) {
	greq := buildRequest(req)
	body, err := json.Marshal(greq)
	if err != nil {
		return nil, fmt.Errorf("gemini: marshal request: %w", err)
	}

	path := fmt.Sprintf("/v1beta/models/%s:generateContent", req.Model)
	respBody, err := c.doRequestWithRetry(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}

	var gr generateResponse
	if err := json.Unmarshal(respBody, &gr); err != nil {
		return nil, fmt.Errorf("gemini: unmarshal response: %w", err)
	}

	return parseResponse(gr, req.Model), nil
}

// Stream sends a streaming completion request and returns a channel of response chunks.
func (c *Client) Stream(ctx context.Context, req models.CompletionRequest) (<-chan models.StreamChunk, error) {
	greq := buildRequest(req)
	body, err := json.Marshal(greq)
	if err != nil {
		return nil, fmt.Errorf("gemini: marshal request: %w", err)
	}

	path := fmt.Sprintf("/v1beta/models/%s:streamGenerateContent?alt=sse", req.Model)
	resp, err := c.doStreamRequest(ctx, path, body)
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

			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}

			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}

			var gr generateResponse
			if err := json.Unmarshal([]byte(data), &gr); err != nil {
				send(ctx, ch, models.StreamChunk{Error: fmt.Sprintf("unmarshal chunk: %v", err)})
				return
			}

			if len(gr.Candidates) > 0 && gr.Candidates[0].Content != nil {
				for _, p := range gr.Candidates[0].Content.Parts {
					if p.Text != "" {
						if !send(ctx, ch, models.StreamChunk{Content: p.Text}) {
							return
						}
					}
				}
				if gr.Candidates[0].FinishReason != "" {
					send(ctx, ch, models.StreamChunk{Done: true})
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			send(ctx, ch, models.StreamChunk{Error: fmt.Sprintf("read stream: %v", err)})
		}
	}()

	return ch, nil
}

// ListModels returns a static list of Gemini models from the pricing table.
func (c *Client) ListModels(_ context.Context) ([]models.ModelInfo, error) {
	var out []models.ModelInfo
	for id := range pricingTable {
		inputPerM, outputPerM := getPricing(id)
		out = append(out, models.ModelInfo{
			ID:            id,
			Name:          id,
			Provider:      "gemini",
			CostPerMInput: inputPerM,
			CostPerMOut:   outputPerM,
		})
	}
	return out, nil
}

// --- Helpers ---

func buildRequest(req models.CompletionRequest) generateRequest {
	gr := generateRequest{}

	// Generation config.
	gc := &generationConfig{}
	if req.MaxTokens > 0 {
		gc.MaxOutputTokens = req.MaxTokens
	} else {
		gc.MaxOutputTokens = defaultMaxTokens
	}
	if req.Temperature != 0 {
		t := req.Temperature
		gc.Temperature = &t
	}
	gr.GenerationConfig = gc

	// Extract system instruction.
	var systemParts []string
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemParts = append(systemParts, m.Content)
		}
	}
	if len(systemParts) > 0 {
		gr.SystemInstruction = &content{
			Parts: []part{{Text: strings.Join(systemParts, "\n\n")}},
		}
	}

	// Convert messages to contents.
	// Gemini uses "user" and "model" roles (not "assistant").
	for _, m := range req.Messages {
		if m.Role == "system" {
			continue
		}

		role := m.Role
		if role == "assistant" {
			role = "model"
		}

		if m.Role == "tool" {
			// Tool result → functionResponse part.
			gr.Contents = append(gr.Contents, content{
				Role: "user",
				Parts: []part{{
					FunctionResponse: &functionResponse{
						Name:     m.ToolCallID,
						Response: map[string]any{"result": m.Content},
					},
				}},
			})
			continue
		}

		if role == "model" && len(m.ToolCalls) > 0 {
			// Assistant with tool calls → functionCall parts.
			var parts []part
			if m.Content != "" {
				parts = append(parts, part{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				args, _ := toStringMap(tc.Parameters)
				parts = append(parts, part{
					FunctionCall: &functionCall{
						Name: tc.Name,
						Args: args,
					},
				})
			}
			gr.Contents = append(gr.Contents, content{Role: role, Parts: parts})
			continue
		}

		// Regular user or model message.
		var parts []part
		if types.HasAttachmentsWithData(m.Attachments) {
			for _, att := range m.Attachments {
				if len(att.Data) > 0 && types.IsImageMIME(att.ContentType) {
					parts = append(parts, part{
						InlineData: &inlineData{
							MIMEType: att.ContentType,
							Data:     base64.StdEncoding.EncodeToString(att.Data),
						},
					})
				}
			}
		}
		text := m.Content
		if len(m.Attachments) > 0 && !types.HasAttachmentsWithData(m.Attachments) {
			text = types.AnnotateAttachments(m.Attachments) + text
		}
		if text != "" {
			parts = append(parts, part{Text: text})
		}
		if len(parts) > 0 {
			gr.Contents = append(gr.Contents, content{Role: role, Parts: parts})
		}
	}

	// Tool definitions → functionDeclarations.
	if len(req.Tools) > 0 {
		var decls []functionDeclaration
		for _, t := range req.Tools {
			decls = append(decls, functionDeclaration{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
		gr.Tools = []toolDecl{{FunctionDeclarations: decls}}
	}

	return gr
}

func parseResponse(gr generateResponse, model string) *models.CompletionResponse {
	resp := &models.CompletionResponse{Model: model}

	if len(gr.Candidates) > 0 {
		cand := gr.Candidates[0]
		resp.StopReason = normalizeFinishReason(cand.FinishReason)

		if cand.Content != nil {
			var textParts []string
			for _, p := range cand.Content.Parts {
				if p.Text != "" {
					textParts = append(textParts, p.Text)
				}
				if p.FunctionCall != nil {
					resp.ToolCalls = append(resp.ToolCalls, models.ToolUse{
						ID:         p.FunctionCall.Name, // Gemini doesn't use separate IDs
						Name:       p.FunctionCall.Name,
						Parameters: p.FunctionCall.Args,
					})
				}
			}
			resp.Content = strings.Join(textParts, "")
		}
	}

	if gr.UsageMetadata != nil {
		resp.TokensIn = gr.UsageMetadata.PromptTokenCount
		resp.TokensOut = gr.UsageMetadata.CandidatesTokenCount
		resp.Cost = calculateCost(model, resp.TokensIn, resp.TokensOut)
	}

	return resp
}

func normalizeFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "end"
	case "MAX_TOKENS":
		return "max_tokens"
	case "SAFETY", "RECITATION", "OTHER":
		return reason
	default:
		// Gemini uses function calls without a separate finish reason;
		// if we got tool calls, infer tool_use.
		return reason
	}
}

func (c *Client) apiURL(path string) string {
	return fmt.Sprintf("%s%s?key=%s", c.baseURL, path, c.apiKey)
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

		httpReq, err := http.NewRequestWithContext(ctx, method, c.apiURL(path), bodyReader)
		if err != nil {
			return nil, fmt.Errorf("gemini: build request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")

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
			lastErr = fmt.Errorf("gemini: read response: %w", err)
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
		return nil, fmt.Errorf("gemini: max retries exceeded: %w", lastErr)
	}
	return nil, fmt.Errorf("gemini: max retries exceeded")
}

func (c *Client) doStreamRequest(ctx context.Context, path string, body []byte) (*http.Response, error) {
	var lastErr error
	for attempt := range c.maxRetries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL(path), bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("gemini: build request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")

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
		return nil, fmt.Errorf("gemini: max retries exceeded: %w", lastErr)
	}
	return nil, fmt.Errorf("gemini: max retries exceeded")
}

func parseAPIError(statusCode int, body []byte) *APIError {
	apiErr := &APIError{
		StatusCode: statusCode,
		Retryable:  statusCode == 429 || statusCode >= 500,
	}

	var errResp struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
		apiErr.Message = errResp.Error.Message
	} else {
		apiErr.Message = string(body)
	}

	if statusCode == 401 || statusCode == 403 {
		apiErr.Message = "Invalid API key. Set GEMINI_API_KEY or add via Providers settings."
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

func send(ctx context.Context, ch chan<- models.StreamChunk, chunk models.StreamChunk) bool {
	select {
	case ch <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}

// toStringMap converts an interface{} (typically map[string]interface{}) to map[string]any.
func toStringMap(v interface{}) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	if m, ok := v.(map[string]interface{}); ok {
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out, true
	}
	return nil, false
}
