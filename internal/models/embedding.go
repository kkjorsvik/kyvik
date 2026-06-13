package models

import "context"

// EmbeddingProvider defines the contract for text embedding providers.
type EmbeddingProvider interface {
	// Embed returns a vector embedding for a single input string.
	Embed(ctx context.Context, input string) ([]float32, error)

	// EmbedBatch returns vector embeddings for multiple input strings.
	EmbedBatch(ctx context.Context, inputs []string) ([][]float32, error)

	// Model returns the embedding model identifier.
	Model() string

	// Dimensions returns the dimensionality of the embedding vectors.
	Dimensions() int
}
