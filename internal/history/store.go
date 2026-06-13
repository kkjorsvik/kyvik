package history

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/kkjorsvik/kyvik/internal/sqlutil"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
)

// dbTimeFmt matches the CURRENT_TIMESTAMP format used by the database.
const dbTimeFmt = "2006-01-02 15:04:05"

// Store implements HistoryStore backed by a SQL database (PostgreSQL).
type Store struct {
	db *sql.DB
}

// New creates a new database-backed history store using an existing *sql.DB.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (h *Store) Append(ctx context.Context, entry HistoryEntry) error {
	_, err := sqlutil.ExecContext(ctx, h.db,
		`INSERT INTO conversation_history
			(agent_id, channel, channel_id, role, content, sender, tokens, attachments, tool_call_id, tool_calls_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.AgentID, entry.Channel, entry.ChannelID,
		entry.Role, entry.Content, entry.Sender, entry.Tokens, entry.Attachments,
		entry.ToolCallID, entry.ToolCallsJSON,
	)
	if err != nil {
		return fmt.Errorf("append history: %w", err)
	}
	return nil
}

func (h *Store) Recent(ctx context.Context, agentID, channel, channelID string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}

	// Sub-select gets the most recent `limit` rows (DESC), then outer query re-orders ASC
	// so the caller gets oldest-first ordering suitable for injecting into a prompt.
	rows, err := sqlutil.QueryContext(ctx, h.db,
		`SELECT id, agent_id, channel, channel_id, role, content, sender, tokens, attachments, tool_call_id, tool_calls_json, compressed_by, created_at
		 FROM (
			SELECT id, agent_id, channel, channel_id, role, content, sender, tokens, attachments, tool_call_id, tool_calls_json, compressed_by, created_at
			FROM conversation_history
			WHERE agent_id = ? AND channel = ? AND channel_id = ? AND compressed_by = 0
			ORDER BY created_at DESC, id DESC
			LIMIT ?
		 ) sub ORDER BY created_at ASC, id ASC`,
		agentID, channel, channelID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent history: %w", err)
	}
	defer rows.Close()

	return scanEntries(rows)
}

func (h *Store) Count(ctx context.Context, agentID, channel, channelID string) (int, error) {
	var count int
	err := sqlutil.QueryRowContext(ctx, h.db,
		`SELECT COUNT(*) FROM conversation_history
		 WHERE agent_id = ? AND channel = ? AND channel_id = ? AND compressed_by = 0`,
		agentID, channel, channelID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count history: %w", err)
	}
	return count, nil
}

func (h *Store) Trim(ctx context.Context, agentID, channel, channelID string, keepLast int) (int64, error) {
	result, err := sqlutil.ExecContext(ctx, h.db,
		`DELETE FROM conversation_history
		 WHERE agent_id = ? AND channel = ? AND channel_id = ?
		   AND id NOT IN (
			SELECT id FROM conversation_history
			WHERE agent_id = ? AND channel = ? AND channel_id = ?
			ORDER BY created_at DESC, id DESC
			LIMIT ?
		 )`,
		agentID, channel, channelID,
		agentID, channel, channelID, keepLast,
	)
	if err != nil {
		return 0, fmt.Errorf("trim history: %w", err)
	}
	return result.RowsAffected()
}

func (h *Store) Clear(ctx context.Context, agentID string) error {
	_, err := sqlutil.ExecContext(ctx, h.db,
		`DELETE FROM conversation_history WHERE agent_id = ?`, agentID,
	)
	if err != nil {
		return fmt.Errorf("clear history: %w", err)
	}
	return nil
}

func (h *Store) Search(ctx context.Context, agentID string, query string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := sqlutil.QueryContext(ctx, h.db,
		`SELECT id, agent_id, channel, channel_id, role, content, sender, tokens, attachments, tool_call_id, tool_calls_json, compressed_by, created_at
		 FROM conversation_history
		 WHERE agent_id = ? AND LOWER(content) LIKE LOWER(?)
		 ORDER BY created_at DESC, id DESC
		 LIMIT ?`,
		agentID, "%"+query+"%", limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search history: %w", err)
	}
	defer rows.Close()

	return scanEntries(rows)
}

// scanEntries scans rows into a slice of HistoryEntry.
func scanEntries(rows *sql.Rows) ([]HistoryEntry, error) {
	var entries []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var createdAt string
		if err := rows.Scan(
			&e.ID, &e.AgentID, &e.Channel, &e.ChannelID,
			&e.Role, &e.Content, &e.Sender, &e.Tokens, &e.Attachments,
			&e.ToolCallID, &e.ToolCallsJSON, &e.CompressedBy, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan history entry: %w", err)
		}
		var err error
		if e.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history: %w", err)
	}
	return entries, nil
}

// parseTime tries RFC3339 first, then falls back to the database CURRENT_TIMESTAMP format.
func parseTime(s string) (time.Time, error) {
	return timeutil.ParseTimestampUTC(s)
}

func (h *Store) ActiveSummary(ctx context.Context, agentID, channel, channelID string) (*HistoryEntry, error) {
	var e HistoryEntry
	var createdAt string
	err := sqlutil.QueryRowContext(ctx, h.db,
		`SELECT id, agent_id, channel, channel_id, role, content, sender, tokens,
		        attachments, tool_call_id, tool_calls_json, compressed_by, created_at
		 FROM conversation_history
		 WHERE agent_id = ? AND channel = ? AND channel_id = ? AND role = 'summary' AND compressed_by = 0
		 ORDER BY created_at DESC, id DESC LIMIT 1`,
		agentID, channel, channelID,
	).Scan(&e.ID, &e.AgentID, &e.Channel, &e.ChannelID, &e.Role, &e.Content,
		&e.Sender, &e.Tokens, &e.Attachments, &e.ToolCallID, &e.ToolCallsJSON,
		&e.CompressedBy, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("active summary: %w", err)
	}
	var parseErr error
	if e.CreatedAt, parseErr = parseTime(createdAt); parseErr != nil {
		return nil, fmt.Errorf("parse summary created_at: %w", parseErr)
	}
	return &e, nil
}

func (h *Store) MarkCompressed(ctx context.Context, ids []int64, summaryID int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, summaryID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := fmt.Sprintf("UPDATE conversation_history SET compressed_by = ? WHERE id IN (%s)",
		strings.Join(placeholders, ","))
	_, err := sqlutil.ExecContext(ctx, h.db, query, args...)
	if err != nil {
		return fmt.Errorf("mark compressed: %w", err)
	}
	return nil
}

func (h *Store) AppendAndCompress(ctx context.Context, summary HistoryEntry, compressIDs []int64) error {
	tx, err := sqlutil.BeginTx(ctx, h.db, nil)
	if err != nil {
		return fmt.Errorf("append and compress begin tx: %w", err)
	}
	defer tx.Rollback()

	var summaryID int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO conversation_history (agent_id, channel, channel_id, role, content, sender, tokens, attachments, tool_call_id, tool_calls_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		summary.AgentID, summary.Channel, summary.ChannelID, summary.Role, summary.Content,
		summary.Sender, summary.Tokens, summary.Attachments, summary.ToolCallID, summary.ToolCallsJSON).Scan(&summaryID)
	if err != nil {
		return fmt.Errorf("append summary: %w", err)
	}

	if len(compressIDs) > 0 {
		placeholders := make([]string, len(compressIDs))
		args := make([]any, 0, len(compressIDs)+1)
		args = append(args, summaryID)
		for i, id := range compressIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		query := fmt.Sprintf("UPDATE conversation_history SET compressed_by = ? WHERE id IN (%s)",
			strings.Join(placeholders, ","))
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("mark compressed: %w", err)
		}
	}

	return tx.Commit()
}
