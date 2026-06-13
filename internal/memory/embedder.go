package memory

import (
	"context"
	"log/slog"
	"time"

	"github.com/kkjorsvik/kyvik/internal/models"
)

// Embedder periodically finds unembedded memories and embeds them.
type Embedder struct {
	store     MemoryStore
	provider  models.EmbeddingProvider
	interval  time.Duration
	batchSize int
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewEmbedder creates a new background embedder.
func NewEmbedder(store MemoryStore, provider models.EmbeddingProvider) *Embedder {
	return &Embedder{
		store:     store,
		provider:  provider,
		interval:  30 * time.Second,
		batchSize: 10,
	}
}

// Start launches the background embedding goroutine.
func (e *Embedder) Start(ctx context.Context) {
	ctx, e.cancel = context.WithCancel(ctx)
	e.done = make(chan struct{})

	go func() {
		defer close(e.done)
		log := slog.With("component", "memory-embedder")
		log.Info("background embedder started", "interval", e.interval, "batch_size", e.batchSize)

		ticker := time.NewTicker(e.interval)
		defer ticker.Stop()

		// Run once immediately at startup.
		e.processBatch(ctx, log)

		for {
			select {
			case <-ctx.Done():
				log.Info("background embedder stopped")
				return
			case <-ticker.C:
				e.processBatch(ctx, log)
			}
		}
	}()
}

// Stop cancels the background goroutine and waits for it to exit.
func (e *Embedder) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	if e.done != nil {
		<-e.done
	}
}

// processBatch finds and embeds up to batchSize unembedded memories.
func (e *Embedder) processBatch(ctx context.Context, log *slog.Logger) {
	memories, err := e.store.GetAllUnembedded(ctx, e.batchSize)
	if err != nil {
		log.Warn("failed to get unembedded memories", "error", err)
		return
	}
	if len(memories) == 0 {
		return
	}

	log.Debug("processing unembedded memories", "count", len(memories))

	// Collect contents for batch embedding.
	contents := make([]string, len(memories))
	for i, mem := range memories {
		contents[i] = mem.Content
	}

	embeddings, err := e.provider.EmbedBatch(ctx, contents)
	if err != nil {
		log.Warn("batch embedding failed", "error", err)
		return
	}

	// Store embeddings.
	model := e.provider.Model()
	for i, mem := range memories {
		if i >= len(embeddings) {
			break
		}
		if err := e.store.SetEmbedding(ctx, mem.ID, embeddings[i], model); err != nil {
			log.Warn("failed to set embedding", "memory_id", mem.ID, "error", err)
		}
	}

	log.Debug("embedded memories", "count", len(memories))
}
