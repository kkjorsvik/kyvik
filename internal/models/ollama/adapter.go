// Package ollama implements the models.Provider and models.EmbeddingProvider
// interfaces for the Ollama local model server.
package ollama

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
	"time"

	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Compile-time interface checks.
var (
	_ models.Provider          = (*Client)(nil)
	_ models.EmbeddingProvider = (*Client)(nil)
)

// Default values.
const (
	defaultBaseURL        = "http://localhost:11434"
	defaultMaxRetries     = 3
	defaultEmbeddingModel = "nomic-embed-text"
	defaultEmbeddingDims  = 768
	maxBackoff            = 30 * time.Second
	maxScannerBuf         = 1 << 20 // 1 MB
)

// --- Wire-format types (unexported, JSON serialization only) ---

type chatRequest struct {
	Model    string       `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool         `json:"stream"`
	Options  *chatOptions `json:"options,omitempty"`
}

type chatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"` // base64-encoded image data
}

type chatOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  int      `json:"num_predict,omitempty"`
}

type chatResponse struct {
	Model           string      `json:"model"`
	Message         chatMessage `json:"message"`
	Done            bool        `json:"done"`
	PromptEvalCount int64       `json:"prompt_eval_count"`
	EvalCount       int64       `json:"eval_count"`
}

type embedRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"` // string or []string
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

type tagsResponse struct {
	Models []tagModel `json:"models"`
}

type tagModel struct {
	Name string `json:"name"`
}

// --- Public error type ---

// APIError represents an error returned by the Ollama API.
type APIError struct {
	StatusCode int
	Message    string
	Retryable  bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ollama: %s (status %d)", e.Message, e.StatusCode)
}

// --- Client ---

// Client implements models.Provider and models.EmbeddingProvider for Ollama.
type Client struct {
	baseURL        string
	httpClient     *http.Client
	maxRetries     int
	baseBackoff    time.Duration
	embeddingModel string
	embeddingDims  int
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the default Ollama base URL.
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

// WithEmbeddingModel sets the embedding model and its vector dimensions.
func WithEmbeddingModel(model string, dims int) Option {
	return func(c *Client) {
		c.embeddingModel = model
		c.embeddingDims = dims
	}
}

// New creates a new Ollama client. No API key is required.
func New(opts ...Option) *Client {
	c := &Client{
		baseURL:        defaultBaseURL,
		httpClient:     &http.Client{Timeout: 300 * time.Second},
		maxRetries:     defaultMaxRetries,
		baseBackoff:    time.Second,
		embeddingModel: defaultEmbeddingModel,
		embeddingDims:  defaultEmbeddingDims,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Name returns the provider identifier.
func (c *Client) Name() string { return "ollama" }

// Complete sends a non-streaming completion request and returns the full response.
func (c *Client) Complete(ctx context.Context, req models.CompletionRequest) (*models.CompletionResponse, error) {
	body, err := json.Marshal(buildChatRequest(req, false))
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
	}

	respBody, err := c.doRequestWithRetry(ctx, http.MethodPost, "/api/chat", body)
	if err != nil {
		return nil, err
	}

	var cr chatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, fmt.Errorf("ollama: unmarshal response: %w", err)
	}

	return &models.CompletionResponse{
		Content:    cr.Message.Content,
		StopReason: "end",
		Model:      cr.Model,
		TokensIn:   cr.PromptEvalCount,
		TokensOut:  cr.EvalCount,
		Cost:       0, // Local models are free.
	}, nil
}

// Stream sends a streaming completion request and returns a channel of response chunks.
func (c *Client) Stream(ctx context.Context, req models.CompletionRequest) (<-chan models.StreamChunk, error) {
	body, err := json.Marshal(buildChatRequest(req, true))
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal request: %w", err)
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
			if line == "" {
				continue
			}

			var cr chatResponse
			if err := json.Unmarshal([]byte(line), &cr); err != nil {
				send(ctx, ch, models.StreamChunk{Error: fmt.Sprintf("unmarshal chunk: %v", err)})
				return
			}

			if cr.Done {
				send(ctx, ch, models.StreamChunk{Done: true})
				return
			}

			if cr.Message.Content != "" {
				if !send(ctx, ch, models.StreamChunk{Content: cr.Message.Content}) {
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

// ListModels returns the models available in the local Ollama instance.
func (c *Client) ListModels(ctx context.Context) ([]models.ModelInfo, error) {
	respBody, err := c.doRequestWithRetry(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return nil, err
	}

	var tr tagsResponse
	if err := json.Unmarshal(respBody, &tr); err != nil {
		return nil, fmt.Errorf("ollama: unmarshal tags: %w", err)
	}

	out := make([]models.ModelInfo, len(tr.Models))
	for i, m := range tr.Models {
		out[i] = models.ModelInfo{
			ID:            m.Name,
			Name:          m.Name,
			Provider:      "ollama",
			CostPerMInput: 0,
			CostPerMOut:   0,
		}
	}
	return out, nil
}

// --- Embedding methods ---

// Embed returns a vector embedding for a single input string.
func (c *Client) Embed(ctx context.Context, input string) ([]float32, error) {
	results, err := c.EmbedBatch(ctx, []string{input})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("ollama: empty embedding response")
	}
	return results[0], nil
}

// EmbedBatch returns vector embeddings for multiple input strings.
func (c *Client) EmbedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	var input any
	if len(inputs) == 1 {
		input = inputs[0]
	} else {
		input = inputs
	}

	body, err := json.Marshal(embedRequest{
		Model: c.embeddingModel,
		Input: input,
	})
	if err != nil {
		return nil, fmt.Errorf("ollama: marshal embedding request: %w", err)
	}

	respBody, err := c.doRequestWithRetry(ctx, http.MethodPost, "/api/embed", body)
	if err != nil {
		return nil, err
	}

	var er embedResponse
	if err := json.Unmarshal(respBody, &er); err != nil {
		return nil, fmt.Errorf("ollama: unmarshal embedding response: %w", err)
	}

	return er.Embeddings, nil
}

// Model returns the embedding model identifier.
func (c *Client) Model() string {
	return c.embeddingModel
}

// Dimensions returns the dimensionality of the embedding vectors.
func (c *Client) Dimensions() int {
	return c.embeddingDims
}

// Ping checks whether Ollama is reachable. Returns nil if running, error otherwise.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return fmt.Errorf("ollama: build ping request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("Ollama not running at %s. Install from ollama.ai", c.baseURL)
	}
	resp.Body.Close()
	return nil
}

// --- Helpers ---

func buildChatRequest(req models.CompletionRequest, stream bool) chatRequest {
	cr := chatRequest{
		Model:  req.Model,
		Stream: stream,
	}

	var opts chatOptions
	var hasOpts bool

	if req.MaxTokens > 0 {
		opts.NumPredict = req.MaxTokens
		hasOpts = true
	}
	if req.Temperature != 0 {
		t := req.Temperature
		opts.Temperature = &t
		hasOpts = true
	}

	if hasOpts {
		cr.Options = &opts
	}

	for _, m := range req.Messages {
		cm := chatMessage{
			Role:    m.Role,
			Content: m.Content,
		}
		if types.HasAttachmentsWithData(m.Attachments) {
			for _, att := range m.Attachments {
				if len(att.Data) > 0 && types.IsImageMIME(att.ContentType) {
					cm.Images = append(cm.Images, base64.StdEncoding.EncodeToString(att.Data))
				}
			}
		} else if len(m.Attachments) > 0 {
			cm.Content = types.AnnotateAttachments(m.Attachments) + cm.Content
		}
		cr.Messages = append(cr.Messages, cm)
	}

	return cr
}

func (c *Client) setHeaders(req *http.Request) {
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
			return nil, fmt.Errorf("ollama: build request: %w", err)
		}
		c.setHeaders(httpReq)

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("Ollama not running at %s. Install from ollama.ai", c.baseURL)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			c.backoff(ctx, attempt)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("ollama: read response: %w", err)
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
		return nil, fmt.Errorf("ollama: max retries exceeded: %w", lastErr)
	}
	return nil, fmt.Errorf("ollama: max retries exceeded")
}

func (c *Client) doStreamRequest(ctx context.Context, body []byte) (*http.Response, error) {
	var lastErr error
	for attempt := range c.maxRetries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("ollama: build request: %w", err)
		}
		c.setHeaders(httpReq)

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			lastErr = fmt.Errorf("Ollama not running at %s. Install from ollama.ai", c.baseURL)
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
		return nil, fmt.Errorf("ollama: max retries exceeded: %w", lastErr)
	}
	return nil, fmt.Errorf("ollama: max retries exceeded")
}

func parseAPIError(statusCode int, body []byte) *APIError {
	apiErr := &APIError{
		StatusCode: statusCode,
		Retryable:  statusCode == 429 || statusCode >= 500,
	}

	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		apiErr.Message = errResp.Error
	} else {
		apiErr.Message = string(body)
	}

	// Provide helpful messages for common errors.
	if statusCode == 404 {
		apiErr.Message = "Model not found. Run 'ollama pull <model>' to download it."
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
