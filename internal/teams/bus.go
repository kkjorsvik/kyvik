// Package teams provides inter-agent communication via an internal message bus.
package teams

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/sqlutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// TeamStore is a local interface for resolving team membership during broadcast.
type TeamStore interface {
	GetTeam(ctx context.Context, id string) (*types.Team, error)
}

// Bus provides in-process message delivery with database persistence.
type Bus struct {
	db          *sql.DB
	audit       audit.Logger
	mu          sync.RWMutex
	subscribers map[string][]chan types.InternalMessage
}

// NewBus creates a new message bus backed by the given database.
func NewBus(db *sql.DB, auditLogger audit.Logger) *Bus {
	return &Bus{
		db:          db,
		audit:       auditLogger,
		subscribers: make(map[string][]chan types.InternalMessage),
	}
}

// Send persists a message and delivers it to any active subscribers.
func (b *Bus) Send(ctx context.Context, msg types.InternalMessage) error {
	if msg.ID == "" {
		msg.ID = ulid.Make().String()
	}
	if msg.Type == "" {
		msg.Type = types.MessageTypeMessage
	}
	if msg.Priority == "" {
		msg.Priority = types.MessagePriorityNormal
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}

	metadataJSON, err := json.Marshal(msg.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = sqlutil.ExecContext(ctx, b.db,
		`INSERT INTO internal_messages (id, from_agent, to_agent, content, type, priority, metadata_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.From, msg.To, msg.Content,
		string(msg.Type), string(msg.Priority), string(metadataJSON),
		msg.Timestamp.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("persist message: %w", err)
	}

	// Audit log (best-effort).
	if b.audit != nil {
		_ = b.audit.Log(ctx, types.AuditEntry{
			AgentID:   msg.From,
			EventType: types.EventInternalMessage,
			Action:    "send",
			Resource:  msg.To,
			Decision:  "allowed",
			Details:   fmt.Sprintf("type=%s priority=%s", msg.Type, msg.Priority),
		})
	}

	// Non-blocking push to subscribers.
	b.mu.RLock()
	channels := b.subscribers[msg.To]
	b.mu.RUnlock()

	for _, ch := range channels {
		select {
		case ch <- msg:
		default:
		}
	}

	return nil
}

// Subscribe returns a receive-only channel for the given agent's messages.
func (b *Bus) Subscribe(ctx context.Context, agentID string) <-chan types.InternalMessage {
	ch := make(chan types.InternalMessage, 32)

	b.mu.Lock()
	b.subscribers[agentID] = append(b.subscribers[agentID], ch)
	b.mu.Unlock()

	return ch
}

// Unsubscribe removes a channel from an agent's subscriber list and closes it.
func (b *Bus) Unsubscribe(agentID string, ch <-chan types.InternalMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subs := b.subscribers[agentID]
	for i, s := range subs {
		// Compare the underlying channel.
		if s == ch {
			b.subscribers[agentID] = append(subs[:i], subs[i+1:]...)
			close(s)
			return
		}
	}
}

// Broadcast sends a message to all members of a team except the sender.
func (b *Bus) Broadcast(ctx context.Context, from, teamID, content string, teamStore TeamStore) error {
	team, err := teamStore.GetTeam(ctx, teamID)
	if err != nil {
		return err
	}
	if !team.Active {
		return types.ErrTeamPaused
	}

	for _, memberID := range team.MemberIDs {
		if memberID == from {
			continue
		}
		if err := b.Send(ctx, types.InternalMessage{
			From:    from,
			To:      memberID,
			Content: content,
			Type:    types.MessageTypeMessage,
		}); err != nil {
			return fmt.Errorf("broadcast to %s: %w", memberID, err)
		}
	}
	return nil
}

// RecentMessages returns the most recent messages sent to the given agent.
func (b *Bus) RecentMessages(ctx context.Context, agentID string, limit int) ([]types.InternalMessage, error) {
	rows, err := sqlutil.QueryContext(ctx, b.db,
		`SELECT id, from_agent, to_agent, content, type, priority, metadata_json, created_at
		 FROM internal_messages WHERE to_agent = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		agentID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent messages: %w", err)
	}
	defer rows.Close()

	return scanMessages(rows)
}

// MessagesBetween returns the most recent messages exchanged between two agents.
func (b *Bus) MessagesBetween(ctx context.Context, agentA, agentB string, limit int) ([]types.InternalMessage, error) {
	rows, err := sqlutil.QueryContext(ctx, b.db,
		`SELECT id, from_agent, to_agent, content, type, priority, metadata_json, created_at
		 FROM internal_messages
		 WHERE (from_agent = ? AND to_agent = ?) OR (from_agent = ? AND to_agent = ?)
		 ORDER BY created_at DESC, id DESC LIMIT ?`,
		agentA, agentB, agentB, agentA, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query messages between: %w", err)
	}
	defer rows.Close()

	return scanMessages(rows)
}

// QueueDepth returns the persisted message count for a recipient agent.
func (b *Bus) QueueDepth(ctx context.Context, agentID string) (int, error) {
	var count int
	err := sqlutil.QueryRowContext(ctx, b.db,
		`SELECT COUNT(*) FROM internal_messages WHERE to_agent = ?`,
		agentID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("query queue depth: %w", err)
	}
	return count, nil
}

// PendingMessagesFor returns internal messages addressed to agentID (excluding
// result-type) that have no matching entry in the message_queue. Used to replay
// messages sent while the agent was offline.
func (b *Bus) PendingMessagesFor(ctx context.Context, agentID string) ([]types.InternalMessage, error) {
	rows, err := sqlutil.QueryContext(ctx, b.db,
		`SELECT im.id, im.from_agent, im.to_agent, im.content, im.type, im.priority, im.metadata_json, im.created_at
		 FROM internal_messages im
		 WHERE im.to_agent = ?
		   AND im.type != 'result'
		   AND NOT EXISTS (
		     SELECT 1 FROM internal_message_acks a
		     WHERE a.message_id = im.id
		       AND a.to_agent = im.to_agent
		   )
		   AND NOT EXISTS (
		     SELECT 1 FROM message_queue mq
		     WHERE mq.agent_id = im.to_agent
		       AND mq.channel = 'internal'
		       AND mq.sender = im.from_agent
		       AND mq.content = im.content
		       AND mq.created_at >= im.created_at
		   )
		 ORDER BY im.created_at ASC`,
		agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("query pending bus messages: %w", err)
	}
	defer rows.Close()

	return scanMessages(rows)
}

// AckPendingMessagesFor marks pending internal messages as acknowledged so they
// are not replayed into the queue.
func (b *Bus) AckPendingMessagesFor(ctx context.Context, agentID string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	result, err := sqlutil.ExecContext(ctx, b.db,
		`INSERT INTO internal_message_acks (message_id, to_agent, acked_at)
		 SELECT im.id, im.to_agent, ?
		 FROM internal_messages im
		 WHERE im.to_agent = ?
		   AND im.type != 'result'
		   AND NOT EXISTS (
		     SELECT 1 FROM internal_message_acks a
		     WHERE a.message_id = im.id
		       AND a.to_agent = im.to_agent
		   )`,
		now, agentID,
	)
	if err != nil {
		return 0, fmt.Errorf("ack pending messages: %w", err)
	}
	return result.RowsAffected()
}

func scanMessages(rows *sql.Rows) ([]types.InternalMessage, error) {
	var messages []types.InternalMessage
	for rows.Next() {
		var (
			msg          types.InternalMessage
			msgType      string
			priority     string
			metadataJSON string
		)
		if err := rows.Scan(
			&msg.ID, &msg.From, &msg.To, &msg.Content,
			&msgType, &priority, &metadataJSON, &msg.Timestamp,
		); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		msg.Type = types.MessageType(msgType)
		msg.Priority = types.MessagePriority(priority)
		if metadataJSON != "" && metadataJSON != "{}" {
			if err := json.Unmarshal([]byte(metadataJSON), &msg.Metadata); err != nil {
				return nil, fmt.Errorf("unmarshal metadata: %w", err)
			}
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return messages, nil
}
