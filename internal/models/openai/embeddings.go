package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/kkjorsvik/kyvik/internal/models"
)

// Compile-time interface check.
var _ models.EmbeddingProvider = (*Client)(nil)

// --- Wire-format types ---

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Data  []embeddingData  `json:"data"`
	Usage *embeddingUsage  `json:"usage,omitempty"`
}

type embeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type embeddingUsage struct {
	PromptTokens int64 `json:"prompt_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// Embed returns a vector embedding for a single input string.
func (c *Client) Embed(ctx context.Context, input string) ([]float32, error) {
	results, err := c.EmbedBatch(ctx, []string{input})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("openai: empty embedding response")
	}
	return results[0], nil
}

// EmbedBatch returns vector embeddings for multiple input strings.
func (c *Client) EmbedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	body, err := json.Marshal(embeddingRequest{
		Model: c.embeddingModel,
		Input: inputs,
	})
	if err != nil {
		return nil, fmt.Errorf("openai: marshal embedding request: %w", err)
	}

	respBody, err := c.doRequestWithRetry(ctx, http.MethodPost, "/v1/embeddings", body)
	if err != nil {
		return nil, err
	}

	var er embeddingResponse
	if err := json.Unmarshal(respBody, &er); err != nil {
		return nil, fmt.Errorf("openai: unmarshal embedding response: %w", err)
	}

	// Sort by index to ensure correct ordering.
	sort.Slice(er.Data, func(i, j int) bool {
		return er.Data[i].Index < er.Data[j].Index
	})

	results := make([][]float32, len(er.Data))
	for i, d := range er.Data {
		results[i] = d.Embedding
	}
	return results, nil
}

// Model returns the embedding model identifier.
func (c *Client) Model() string {
	return c.embeddingModel
}

// Dimensions returns the dimensionality of the embedding vectors.
func (c *Client) Dimensions() int {
	return c.embeddingDims
}
