package retention

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kkjorsvik/kyvik/internal/sqlutil"
)

// pruneAuditLogs deletes audit log entries older than the configured retention.
func (p *Pruner) pruneAuditLogs(ctx context.Context) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -p.config.AuditLogsDays)
	result, err := sqlutil.ExecContext(ctx, p.db,
		`DELETE FROM audit_log WHERE created_at < ?`,
		cutoff.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return 0, fmt.Errorf("delete audit logs: %w", err)
	}
	return result.RowsAffected()
}

// pruneConversationHistory archives old conversations to the archive table,
// then deletes them from the main table.
func (p *Pruner) pruneConversationHistory(ctx context.Context) (archived, deleted int64, err error) {
	cutoff := time.Now().AddDate(0, 0, -p.config.ConversationHistoryDays)
	cutoffStr := cutoff.UTC().Format("2006-01-02 15:04:05")

	tx, err := sqlutil.BeginTx(ctx, p.db, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Archive: copy old rows to the archive table.
	archiveResult, err := tx.ExecContext(ctx,
		`INSERT INTO conversation_history_archive
			(id, agent_id, channel, channel_id, role, content, sender, tokens,
			 attachments, tool_call_id, tool_calls_json, created_at, archived_at)
		 SELECT id, agent_id, channel, channel_id, role, content, sender, tokens,
			    attachments, tool_call_id, tool_calls_json, created_at, CURRENT_TIMESTAMP
		 FROM conversation_history
		 WHERE created_at < ?`,
		cutoffStr,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("archive conversations: %w", err)
	}
	archived, _ = archiveResult.RowsAffected()

	// Delete from main table.
	deleteResult, err := tx.ExecContext(ctx,
		`DELETE FROM conversation_history WHERE created_at < ?`,
		cutoffStr,
	)
	if err != nil {
		return archived, 0, fmt.Errorf("delete conversations: %w", err)
	}
	deleted, _ = deleteResult.RowsAffected()

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit tx: %w", err)
	}

	return archived, deleted, nil
}

// pruneQueue delegates to the queue's Cleanup method if available,
// otherwise runs direct SQL deletion.
func (p *Pruner) pruneQueue(ctx context.Context) (int64, error) {
	if p.queue != nil {
		return p.queue.Cleanup(ctx, p.config.CompletedQueueHours)
	}

	// Fallback: direct SQL
	cutoff := time.Now().Add(-time.Duration(p.config.CompletedQueueHours) * time.Hour)
	result, err := sqlutil.ExecContext(ctx, p.db,
		`DELETE FROM message_queue WHERE status = 'completed' AND completed_at < ?`,
		cutoff.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return 0, fmt.Errorf("delete completed queue messages: %w", err)
	}
	return result.RowsAffected()
}

// pruneSecurityEvents deletes security events older than the configured retention.
func (p *Pruner) pruneSecurityEvents(ctx context.Context) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -p.config.SecurityEventsDays)
	result, err := sqlutil.ExecContext(ctx, p.db,
		`DELETE FROM security_events WHERE created_at < ?`,
		cutoff.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return 0, fmt.Errorf("delete security events: %w", err)
	}
	return result.RowsAffected()
}

// pruneArchivedMemories deletes memories that are archived and older than the configured retention.
func (p *Pruner) pruneArchivedMemories(ctx context.Context) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -p.config.ArchivedMemoriesDays)
	result, err := sqlutil.ExecContext(ctx, p.db,
		`DELETE FROM memories WHERE archived = true AND accessed_at < ?`,
		cutoff.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return 0, fmt.Errorf("delete archived memories: %w", err)
	}
	return result.RowsAffected()
}

// pruneWebConversations deletes web conversation metadata where the conversation
// is old and has no remaining messages in the main conversation_history table.
func (p *Pruner) pruneWebConversations(ctx context.Context) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -p.config.WebConversationsDays)
	result, err := sqlutil.ExecContext(ctx, p.db,
		`DELETE FROM web_conversations
		 WHERE updated_at < ?
		   AND NOT EXISTS (
		       SELECT 1 FROM conversation_history
		       WHERE conversation_history.channel_id = web_conversations.id
		   )`,
		cutoff.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return 0, fmt.Errorf("delete web conversations: %w", err)
	}
	return result.RowsAffected()
}

// persistResult saves the prune result to system_state as JSON.
func (p *Pruner) persistResult(ctx context.Context, result PruneResult) {
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	_ = p.stateStore.SetSystemState(ctx, "last_prune_result", string(data))
	_ = p.stateStore.SetSystemState(ctx, "last_prune_time", result.Timestamp.UTC().Format(time.RFC3339))
}
