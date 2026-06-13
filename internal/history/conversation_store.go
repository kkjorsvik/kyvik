package history

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/internal/sqlutil"
)

// Compile-time interface check.
var _ ConversationStore = (*Store)(nil)

var (
	ulidEntropy     = ulid.Monotonic(rand.Reader, 0)
	ulidEntropyOnce sync.Mutex
)

func newULID() string {
	ulidEntropyOnce.Lock()
	defer ulidEntropyOnce.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), ulidEntropy).String()
}

func (h *Store) CreateConversation(ctx context.Context, agentID, title string) (*WebConversation, error) {
	id := newULID()
	now := time.Now().UTC().Format(dbTimeFmt)

	_, err := sqlutil.ExecContext(ctx, h.db,
		`INSERT INTO web_conversations (id, agent_id, title, message_count, created_at, updated_at)
		 VALUES (?, ?, ?, 0, ?, ?)`,
		id, agentID, title, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create conversation: %w", err)
	}

	return &WebConversation{
		ID:           id,
		AgentID:      agentID,
		Title:        title,
		MessageCount: 0,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}, nil
}

func (h *Store) ListConversations(ctx context.Context, agentID string) ([]WebConversation, error) {
	rows, err := sqlutil.QueryContext(ctx, h.db,
		`SELECT id, agent_id, title, message_count, created_at, updated_at
		 FROM web_conversations
		 WHERE agent_id = ?
		 ORDER BY updated_at DESC, id DESC`,
		agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	return scanConversations(rows)
}

func (h *Store) GetConversation(ctx context.Context, id string) (*WebConversation, error) {
	var c WebConversation
	var createdAt, updatedAt string
	err := sqlutil.QueryRowContext(ctx, h.db,
		`SELECT id, agent_id, title, message_count, created_at, updated_at
		 FROM web_conversations WHERE id = ?`, id,
	).Scan(&c.ID, &c.AgentID, &c.Title, &c.MessageCount, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("conversation %s: not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get conversation: %w", err)
	}
	c.CreatedAt, _ = parseTime(createdAt)
	c.UpdatedAt, _ = parseTime(updatedAt)
	return &c, nil
}

func (h *Store) RenameConversation(ctx context.Context, id, title string) error {
	result, err := sqlutil.ExecContext(ctx, h.db,
		`UPDATE web_conversations SET title = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		title, id,
	)
	if err != nil {
		return fmt.Errorf("rename conversation: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("conversation %s: not found", id)
	}
	return nil
}

func (h *Store) DeleteConversation(ctx context.Context, id string) error {
	tx, err := sqlutil.BeginTx(ctx, h.db, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Delete conversation history messages.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM conversation_history WHERE channel = 'webui' AND channel_id = ?`, id,
	); err != nil {
		return fmt.Errorf("delete conversation history: %w", err)
	}

	// Delete the conversation metadata.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM web_conversations WHERE id = ?`, id,
	); err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}

	return tx.Commit()
}

func (h *Store) DeleteByAgent(ctx context.Context, agentID string) error {
	tx, err := sqlutil.BeginTx(ctx, h.db, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Delete conversation history messages for all webui conversations belonging to this agent.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM conversation_history
		 WHERE channel = 'webui' AND channel_id IN (
			SELECT id FROM web_conversations WHERE agent_id = ?
		 )`, agentID,
	); err != nil {
		return fmt.Errorf("delete agent conversation history: %w", err)
	}

	// Delete the conversation metadata.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM web_conversations WHERE agent_id = ?`, agentID,
	); err != nil {
		return fmt.Errorf("delete agent conversations: %w", err)
	}

	return tx.Commit()
}

func (h *Store) IncrementMessageCount(ctx context.Context, id string, delta int) error {
	_, err := sqlutil.ExecContext(ctx, h.db,
		`UPDATE web_conversations
		 SET message_count = message_count + ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		delta, id,
	)
	if err != nil {
		return fmt.Errorf("increment message count: %w", err)
	}
	return nil
}

func (h *Store) MostRecentConversation(ctx context.Context, agentID string) (*WebConversation, error) {
	var c WebConversation
	var createdAt, updatedAt string
	err := sqlutil.QueryRowContext(ctx, h.db,
		`SELECT id, agent_id, title, message_count, created_at, updated_at
		 FROM web_conversations
		 WHERE agent_id = ?
		 ORDER BY updated_at DESC, id DESC
		 LIMIT 1`, agentID,
	).Scan(&c.ID, &c.AgentID, &c.Title, &c.MessageCount, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("most recent conversation: %w", err)
	}
	c.CreatedAt, _ = parseTime(createdAt)
	c.UpdatedAt, _ = parseTime(updatedAt)
	return &c, nil
}

func scanConversations(rows *sql.Rows) ([]WebConversation, error) {
	var convs []WebConversation
	for rows.Next() {
		var c WebConversation
		var createdAt, updatedAt string
		if err := rows.Scan(&c.ID, &c.AgentID, &c.Title, &c.MessageCount, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan conversation: %w", err)
		}
		c.CreatedAt, _ = parseTime(createdAt)
		c.UpdatedAt, _ = parseTime(updatedAt)
		convs = append(convs, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate conversations: %w", err)
	}
	return convs, nil
}
