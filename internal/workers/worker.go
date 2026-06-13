// Package workers implements ephemeral worker management for task delegation.
// Workers are short-lived, single-turn LLM calls that inherit their parent
// agent's identity and budget. They exist only in memory.
package workers

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/internal/memory"
	"github.com/kkjorsvik/kyvik/internal/models"
	"github.com/kkjorsvik/kyvik/internal/router"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// EphemeralWorker represents a short-lived worker that processes a single task.
type EphemeralWorker struct {
	ID          string           `json:"id"`
	ParentID    string           `json:"parent_id"`
	Task        string           `json:"task"`
	Status      string           `json:"status"` // "running", "completed", "failed", "timeout"
	Result      string           `json:"result"`
	Error       string           `json:"error,omitempty"`
	Model       router.ModelSlot `json:"model"`
	CreatedAt   time.Time        `json:"created_at"`
	CompletedAt time.Time        `json:"completed_at,omitempty"`
	TTL         time.Duration    `json:"ttl"`
}

// runningWorker tracks an active worker with its cancellation and completion.
type runningWorker struct {
	worker *EphemeralWorker
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
}

// WorkerManager manages the lifecycle of ephemeral workers.
type WorkerManager struct {
	registry *router.ProviderRegistry
	spending spending.Tracker
	memory   memory.MemoryStore
	active   map[string][]*runningWorker // parentID → workers
	mu       sync.Mutex
	stopCh   chan struct{}
	stopped  chan struct{}
}

// NewWorkerManager creates a new worker manager. All parameters are optional
// (nil-safe) except registry.
func NewWorkerManager(registry *router.ProviderRegistry, sp spending.Tracker, mem memory.MemoryStore) *WorkerManager {
	return &WorkerManager{
		registry: registry,
		spending: sp,
		memory:   mem,
		active:   make(map[string][]*runningWorker),
		stopCh:   make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// Start begins the background cleanup goroutine.
func (m *WorkerManager) Start(ctx context.Context) {
	go m.cleanupLoop()
}

// Stop signals the cleanup goroutine to exit and waits for it.
func (m *WorkerManager) Stop() {
	close(m.stopCh)
	<-m.stopped
}

// Spawn creates and launches an ephemeral worker for the given parent agent.
func (m *WorkerManager) Spawn(ctx context.Context, parent types.AgentConfig, task string) (*EphemeralWorker, error) {
	if !parent.Workers.Enabled {
		return nil, types.ErrWorkersDisabled
	}

	wc := types.NormalizeWorkerConfig(parent.Workers)

	if m.ActiveCount(parent.ID) >= wc.MaxConcurrent {
		return nil, types.ErrWorkerLimitReached
	}

	// Check parent budget
	if m.spending != nil {
		budget, err := m.spending.CheckBudget(ctx, parent.ID)
		if err != nil {
			return nil, fmt.Errorf("checking parent budget: %w", err)
		}
		if !budget.WithinBudget {
			return nil, types.ErrBudgetExceeded
		}
	}

	// Generate worker ID
	workerID := ulid.Make().String()

	// Resolve model slot
	slot, provider, err := m.resolveSlot(parent, wc.ModelSlot)
	if err != nil {
		return nil, fmt.Errorf("resolving worker slot: %w", err)
	}

	ttl := time.Duration(wc.TTLSeconds) * time.Second

	worker := &EphemeralWorker{
		ID:        workerID,
		ParentID:  parent.ID,
		Task:      task,
		Status:    "running",
		Model:     slot,
		CreatedAt: time.Now(),
		TTL:       ttl,
	}

	rw := &runningWorker{
		worker: worker,
		done:   make(chan struct{}),
	}

	m.mu.Lock()
	m.active[parent.ID] = append(m.active[parent.ID], rw)
	m.mu.Unlock()

	// Build messages
	messages := m.buildMessages(ctx, parent, task)

	// Launch worker goroutine
	workerCtx, cancel := context.WithTimeout(ctx, ttl)
	rw.cancel = cancel

	go m.runWorker(workerCtx, rw, parent, provider, slot, messages)

	return worker, nil
}

// Wait blocks until the specified worker completes and returns its final state.
func (m *WorkerManager) Wait(ctx context.Context, workerID string) (*EphemeralWorker, error) {
	rw := m.findWorker(workerID)
	if rw == nil {
		return nil, types.ErrWorkerNotFound
	}

	select {
	case <-rw.done:
		rw.mu.Lock()
		w := *rw.worker
		rw.mu.Unlock()
		return &w, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Cancel cancels a running worker.
func (m *WorkerManager) Cancel(ctx context.Context, workerID string) error {
	rw := m.findWorker(workerID)
	if rw == nil {
		return types.ErrWorkerNotFound
	}

	rw.cancel()

	// Wait for completion
	select {
	case <-rw.done:
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// ActiveCount returns the number of active workers for a parent agent.
func (m *WorkerManager) ActiveCount(parentID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, rw := range m.active[parentID] {
		rw.mu.Lock()
		if rw.worker.Status == "running" {
			count++
		}
		rw.mu.Unlock()
	}
	return count
}

// ActiveWorkers returns a snapshot of all workers for a parent agent.
func (m *WorkerManager) ActiveWorkers(parentID string) []EphemeralWorker {
	m.mu.Lock()
	rws := make([]*runningWorker, len(m.active[parentID]))
	copy(rws, m.active[parentID])
	m.mu.Unlock()

	result := make([]EphemeralWorker, 0, len(rws))
	for _, rw := range rws {
		rw.mu.Lock()
		result = append(result, *rw.worker)
		rw.mu.Unlock()
	}
	return result
}

// resolveSlot finds the model slot and provider for a worker.
func (m *WorkerManager) resolveSlot(parent types.AgentConfig, slotName string) (router.ModelSlot, models.Provider, error) {
	resolved, err := router.ResolveSlots(parent)
	if err != nil {
		return router.ModelSlot{}, nil, err
	}

	// Look for the requested slot name
	for _, s := range resolved.Config.Slots {
		if s.Name == slotName {
			if prov, ok := m.registry.GetProviderForSlot(s); ok {
				return s, prov, nil
			}
		}
	}

	// Fallback to default slot
	slot := resolved.DefaultSlot
	prov, ok := m.registry.GetProviderForSlot(slot)
	if !ok {
		return router.ModelSlot{}, nil, fmt.Errorf("%w: %s", types.ErrProviderUnavailable, slot.Provider)
	}
	return slot, prov, nil
}

// buildMessages constructs the LLM messages for a worker task.
func (m *WorkerManager) buildMessages(ctx context.Context, parent types.AgentConfig, task string) []models.ChatMessage {
	var messages []models.ChatMessage

	// System prompt from parent's soul + identity
	systemParts := ""
	if parent.SoulContent != "" {
		systemParts += parent.SoulContent + "\n\n"
	}
	if parent.IdentityContent != "" {
		systemParts += parent.IdentityContent + "\n\n"
	}
	systemParts += "You are a worker processing a delegated task. Focus on the task and provide a direct, concise response."

	messages = append(messages, models.ChatMessage{
		Role:    "system",
		Content: systemParts,
	})

	// Inject parent memories if available
	if m.memory != nil {
		memories, err := m.memory.ListRecent(ctx, parent.ID, 5)
		if err == nil {
			for _, mem := range memories {
				messages = append(messages, models.ChatMessage{
					Role:    "system",
					Content: fmt.Sprintf("[Memory] %s", mem.Content),
				})
			}
		}
	}

	// User task message
	messages = append(messages, models.ChatMessage{
		Role:    "user",
		Content: task,
	})

	return messages
}

// runWorker executes the LLM call and updates the worker status.
func (m *WorkerManager) runWorker(ctx context.Context, rw *runningWorker, parent types.AgentConfig, provider models.Provider, slot router.ModelSlot, messages []models.ChatMessage) {
	defer close(rw.done)
	log := slog.With("worker_id", rw.worker.ID, "parent_id", rw.worker.ParentID)

	// Attach parent agent ID for per-agent key resolution
	ctx = models.WithAgentID(ctx, parent.ID)

	req := models.CompletionRequest{
		Model:    slot.Model,
		Messages: messages,
	}

	log.Debug("worker calling model", "model", slot.Model)
	resp, err := provider.Complete(ctx, req)

	rw.mu.Lock()
	defer rw.mu.Unlock()

	rw.worker.CompletedAt = time.Now()

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			rw.worker.Status = "timeout"
			rw.worker.Error = "worker TTL exceeded"
			log.Debug("worker timed out")
		} else if ctx.Err() == context.Canceled {
			rw.worker.Status = "timeout"
			rw.worker.Error = "worker cancelled"
			log.Debug("worker cancelled")
		} else {
			rw.worker.Status = "failed"
			rw.worker.Error = err.Error()
			log.Error("worker failed", "error", err)
		}
		return
	}

	rw.worker.Status = "completed"
	rw.worker.Result = resp.Content

	// Record spending attributed to parent
	if m.spending != nil {
		costSource := ""
		if resp.Cost > 0 {
			costSource = spending.CostSourceProviderReported
		}
		_ = m.spending.Record(ctx, parent.ID, resp.TokensIn, resp.TokensOut, resp.Cost, spending.RecordOptions{
			Model:         slot.Model,
			ModelSlot:     slot.Name,
			RoutedBy:      "worker",
			Provider:      slot.Provider,
			ParentAgentID: parent.ID,
			CostSource:    costSource,
			UsageSource:   spending.UsageSourceProvider,
		})
	}

	log.Debug("worker completed", "tokens_in", resp.TokensIn, "tokens_out", resp.TokensOut)
}

// findWorker locates a running worker by ID across all parents.
func (m *WorkerManager) findWorker(workerID string) *runningWorker {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, rws := range m.active {
		for _, rw := range rws {
			if rw.worker.ID == workerID {
				return rw
			}
		}
	}
	return nil
}

// cleanupLoop periodically removes stale completed workers from the active map.
func (m *WorkerManager) cleanupLoop() {
	defer close(m.stopped)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			// Cancel all running workers on shutdown
			m.mu.Lock()
			for _, rws := range m.active {
				for _, rw := range rws {
					rw.mu.Lock()
					if rw.worker.Status == "running" {
						rw.cancel()
					}
					rw.mu.Unlock()
				}
			}
			m.mu.Unlock()
			return
		case <-ticker.C:
			m.cleanup()
		}
	}
}

// cleanup removes completed/failed/timeout workers after a retention period,
// and cancels workers that have exceeded their TTL.
func (m *WorkerManager) cleanup() {
	const retentionPeriod = 5 * time.Minute
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	for parentID, rws := range m.active {
		var kept []*runningWorker
		for _, rw := range rws {
			rw.mu.Lock()
			status := rw.worker.Status
			completedAt := rw.worker.CompletedAt
			createdAt := rw.worker.CreatedAt
			ttl := rw.worker.TTL
			rw.mu.Unlock()

			// Cancel workers past TTL
			if status == "running" && now.Sub(createdAt) > ttl {
				rw.cancel()
				continue // will be cleaned up on next pass
			}

			// Remove completed workers after retention period
			if status != "running" && !completedAt.IsZero() && now.Sub(completedAt) > retentionPeriod {
				continue // drop
			}

			kept = append(kept, rw)
		}

		if len(kept) == 0 {
			delete(m.active, parentID)
		} else {
			m.active[parentID] = kept
		}
	}
}
