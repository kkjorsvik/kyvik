// Package retention implements automated data retention and pruning for Kyvik's
// database. It deletes old audit logs, archives conversation history,
// cleans up completed queue messages, and removes stale security events.
package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/internal/queue"
)

// StateStore is the narrow interface used to persist prune results.
type StateStore interface {
	GetSystemState(ctx context.Context, key string) (string, error)
	SetSystemState(ctx context.Context, key, value string) error
}

// PruneResult holds the outcome of a single prune run.
type PruneResult struct {
	AuditLogsDeleted           int64     `json:"audit_logs_deleted"`
	ConversationsArchived      int64     `json:"conversations_archived"`
	ConversationsDeleted       int64     `json:"conversations_deleted"`
	QueueMessagesDeleted       int64     `json:"queue_messages_deleted"`
	SecurityEventsDeleted      int64     `json:"security_events_deleted"`
	ArchivedMemoriesDeleted    int64     `json:"archived_memories_deleted"`
	WebConversationsDeleted    int64     `json:"web_conversations_deleted"`
	WorkspacesDeleted          int64     `json:"workspaces_deleted"`
	Duration                   string    `json:"duration"`
	Errors                     []string  `json:"errors,omitempty"`
	Timestamp                  time.Time `json:"timestamp"`
}

// TotalDeleted returns the sum of all deleted/archived rows.
func (r PruneResult) TotalDeleted() int64 {
	return r.AuditLogsDeleted + r.ConversationsArchived + r.ConversationsDeleted +
		r.QueueMessagesDeleted + r.SecurityEventsDeleted +
		r.ArchivedMemoriesDeleted + r.WebConversationsDeleted +
		r.WorkspacesDeleted
}

// Pruner manages scheduled data retention and cleanup.
type Pruner struct {
	db         *sql.DB
	stateStore StateStore
	notifier   notifications.Notifier
	queue      queue.Queue
	config     config.RetentionConfig
	cron       *cron.Cron

	mu         sync.RWMutex
	lastResult *PruneResult
}

// New creates a new Pruner with the given database, state store, and config.
func New(db *sql.DB, stateStore StateStore, cfg config.RetentionConfig) *Pruner {
	return &Pruner{
		db:         db,
		stateStore: stateStore,
		config:     cfg,
		cron:       cron.New(),
	}
}

// SetNotifier configures operator notifications for prune results.
func (p *Pruner) SetNotifier(n notifications.Notifier) {
	p.notifier = n
}

// SetQueue sets the message queue for delegated cleanup.
func (p *Pruner) SetQueue(q queue.Queue) {
	p.queue = q
}

// Start registers the cron job and begins the scheduler.
func (p *Pruner) Start() {
	if p.config.Enabled != nil && !*p.config.Enabled {
		slog.Info("retention pruner disabled")
		return
	}

	_, err := p.cron.AddFunc(p.config.Schedule, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		result := p.RunNow(ctx)
		slog.Info("retention prune completed",
			"total_deleted", result.TotalDeleted(),
			"duration", result.Duration,
			"errors", len(result.Errors),
		)
	})
	if err != nil {
		slog.Error("retention pruner: invalid cron schedule", "schedule", p.config.Schedule, "error", err)
		return
	}

	p.cron.Start()
	slog.Info("retention pruner started", "schedule", p.config.Schedule)
}

// Stop halts the cron scheduler.
func (p *Pruner) Stop() {
	if p.cron != nil {
		p.cron.Stop()
	}
}

// RunNow executes all prune operations immediately and returns the result.
func (p *Pruner) RunNow(ctx context.Context) PruneResult {
	start := time.Now()
	result := PruneResult{Timestamp: start}

	// 1. Audit logs
	n, err := p.pruneAuditLogs(ctx)
	result.AuditLogsDeleted = n
	if err != nil {
		result.Errors = append(result.Errors, "audit_logs: "+err.Error())
	}

	// 2. Conversation history (archive then delete)
	archived, deleted, err := p.pruneConversationHistory(ctx)
	result.ConversationsArchived = archived
	result.ConversationsDeleted = deleted
	if err != nil {
		result.Errors = append(result.Errors, "conversation_history: "+err.Error())
	}

	// 3. Queue cleanup
	n, err = p.pruneQueue(ctx)
	result.QueueMessagesDeleted = n
	if err != nil {
		result.Errors = append(result.Errors, "queue: "+err.Error())
	}

	// 4. Security events
	n, err = p.pruneSecurityEvents(ctx)
	result.SecurityEventsDeleted = n
	if err != nil {
		result.Errors = append(result.Errors, "security_events: "+err.Error())
	}

	// 5. Archived memories
	n, err = p.pruneArchivedMemories(ctx)
	result.ArchivedMemoriesDeleted = n
	if err != nil {
		result.Errors = append(result.Errors, "archived_memories: "+err.Error())
	}

	// 6. Web conversations
	n, err = p.pruneWebConversations(ctx)
	result.WebConversationsDeleted = n
	if err != nil {
		result.Errors = append(result.Errors, "web_conversations: "+err.Error())
	}

	// 7. Workspace orphan cleanup
	if p.config.WorkspaceRoot != "" {
		deleted, errs := p.cleanOrphanWorkspaces(ctx)
		result.WorkspacesDeleted = deleted
		result.Errors = append(result.Errors, errs...)
	}

	result.Duration = time.Since(start).Round(time.Millisecond).String()

	// Persist result
	p.persistResult(ctx, result)

	// Cache result in memory
	p.mu.Lock()
	p.lastResult = &result
	p.mu.Unlock()

	// Send notification if there were errors or significant deletions
	if p.notifier != nil && len(result.Errors) > 0 {
		_ = p.notifier.Send(ctx, notifications.Event{
			Type:      "retention",
			Severity:  "warning",
			Title:     "Data retention prune completed with errors",
			Detail:    result.Errors[0],
			Timestamp: time.Now(),
		})
	}

	return result
}

// LastResult returns the most recent prune result from memory.
func (p *Pruner) LastResult() *PruneResult {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastResult
}

// LoadLastResult loads the last prune result from the database.
func (p *Pruner) LoadLastResult(ctx context.Context) (*PruneResult, error) {
	raw, err := p.stateStore.GetSystemState(ctx, "last_prune_result")
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var result PruneResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.lastResult = &result
	p.mu.Unlock()

	return &result, nil
}

// Config returns the current retention configuration.
func (p *Pruner) Config() config.RetentionConfig {
	return p.config
}
