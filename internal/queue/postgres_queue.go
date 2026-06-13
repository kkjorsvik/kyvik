package queue

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kkjorsvik/kyvik/internal/sqlutil"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
)

// PostgresQueue implements Queue using PostgreSQL for persistence and buffered
// Go channels for fast in-process delivery. It filters messages by target_node_id
// so each node only processes messages routed to it.
type PostgresQueue struct {
	db       *sql.DB
	nodeID   string
	dsn      string
	config   Config
	mu       sync.RWMutex
	channels map[string]chan QueueMessage // agentID -> in-memory channel
	inFlight map[int64]struct{}           // message IDs already pushed to channel
	metrics  map[string]*agentQueueMetrics
	stopCh   chan struct{}
	done     chan struct{}
}

// Compile-time interface check.
var _ Queue = (*PostgresQueue)(nil)

// NewPostgresQueue creates a new PostgresQueue and starts the background cleanup goroutine.
// nodeID is this node's identity — only messages with a matching target_node_id (or empty)
// are delivered. dsn is kept for future LISTEN/NOTIFY support.
func NewPostgresQueue(db *sql.DB, nodeID string, dsn string, cfgs ...Config) (*PostgresQueue, error) {
	cfg := DefaultConfig()
	if len(cfgs) > 0 {
		cfg = cfgs[0]
		if cfg.Depth <= 0 {
			cfg.Depth = 50
		}
		if cfg.StaleTimeoutSeconds <= 0 {
			cfg.StaleTimeoutSeconds = 300
		}
		if cfg.RetentionHours <= 0 {
			cfg.RetentionHours = 24
		}
		if cfg.FullBehavior == "" {
			cfg.FullBehavior = BehaviorAcknowledge
		}
	}

	q := &PostgresQueue{
		db:       db,
		nodeID:   nodeID,
		dsn:      dsn,
		config:   cfg,
		channels: make(map[string]chan QueueMessage),
		inFlight: make(map[int64]struct{}),
		metrics:  make(map[string]*agentQueueMetrics),
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
	}

	go q.cleanupLoop()
	return q, nil
}

// Enqueue inserts a message into the database and, when possible, pushes it
// to the agent's in-memory channel for immediate delivery.
func (q *PostgresQueue) Enqueue(ctx context.Context, msg QueueMessage) (int64, error) {
	if msg.MaxAttempts <= 0 {
		msg.MaxAttempts = 3
	}
	if msg.Status == "" {
		msg.Status = StatusPending
	}
	if msg.MessageType == "" {
		msg.MessageType = "message"
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}

	var id int64
	err := sqlutil.QueryRowContext(ctx, q.db,
		`INSERT INTO message_queue (agent_id, channel, sender, content, attachments, priority, status, attempts, max_attempts, conversation_id, message_type, target_node_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		msg.AgentID, msg.Channel, msg.Sender, msg.Content, msg.Attachments,
		msg.Priority, msg.Status, msg.Attempts, msg.MaxAttempts, msg.ConversationID, msg.MessageType,
		msg.TargetNodeID, msg.CreatedAt.UTC().Format(queueTimeFmt),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert message: %w", err)
	}
	msg.ID = id

	// Send NOTIFY to wake the target node if specified.
	if msg.TargetNodeID != "" {
		channel := "kyvik_queue_" + sanitizeChannel(msg.TargetNodeID)
		_, notifyErr := q.db.ExecContext(ctx, "SELECT pg_notify($1, $2)", channel, fmt.Sprintf("%d", id))
		if notifyErr != nil {
			slog.Warn("pg_notify failed", "channel", channel, "error", notifyErr)
		}
	}

	// Check depth limit for non-operator messages.
	if msg.Priority < 2 {
		depth, err := q.Depth(ctx, msg.AgentID)
		if err != nil {
			slog.Warn("queue depth check failed", "agent_id", msg.AgentID, "error", err)
		} else if depth > q.config.Depth {
			q.addMetric(msg.AgentID, func(m *agentQueueMetrics) {
				m.EnqueueDepthBlocked++
			})
			slog.Warn("queue depth exceeded, message persisted but not pushed",
				"agent_id", msg.AgentID,
				"depth", depth,
				"limit", q.config.Depth,
				"behavior", q.config.FullBehavior,
			)
			return id, nil
		}
	}

	// Only push to in-memory channel if this message is for our node (or unrouted).
	if msg.TargetNodeID != "" && msg.TargetNodeID != q.nodeID {
		return id, nil
	}

	// Try to push to in-memory channel (non-blocking).
	q.mu.RLock()
	ch, ok := q.channels[msg.AgentID]
	q.mu.RUnlock()

	if ok {
		select {
		case ch <- msg:
			q.markInFlight(msg.ID)
		default:
			q.addMetric(msg.AgentID, func(m *agentQueueMetrics) {
				m.ChannelFull++
			})
			slog.Debug("in-memory channel full, message stays in DB only",
				"agent_id", msg.AgentID, "id", id)
		}
	}

	return id, nil
}

// Dequeue returns (or creates) the buffered channel for the given agent.
func (q *PostgresQueue) Dequeue(ctx context.Context, agentID string) <-chan QueueMessage {
	q.mu.Lock()
	defer q.mu.Unlock()

	ch, ok := q.channels[agentID]
	if !ok {
		ch = make(chan QueueMessage, 16)
		q.channels[agentID] = ch
	}
	return ch
}

// MarkProcessing sets a message's status to processing with the current timestamp.
func (q *PostgresQueue) MarkProcessing(ctx context.Context, id int64) error {
	nowUTC := timeutil.NowUTC().Format(queueTimeFmt)
	_, err := sqlutil.ExecContext(ctx, q.db,
		`UPDATE message_queue SET status = ?, started_at = ? WHERE id = ?`,
		StatusProcessing, nowUTC, id,
	)
	if err != nil {
		return fmt.Errorf("mark processing: %w", err)
	}
	return nil
}

// Complete marks a message as successfully completed.
func (q *PostgresQueue) Complete(ctx context.Context, id int64) error {
	nowUTC := timeutil.NowUTC().Format(queueTimeFmt)
	_, err := sqlutil.ExecContext(ctx, q.db,
		`UPDATE message_queue SET status = ?, completed_at = ? WHERE id = ?`,
		StatusCompleted, nowUTC, id,
	)
	if err != nil {
		return fmt.Errorf("complete message: %w", err)
	}
	q.clearInFlight(id)
	return nil
}

// Fail increments the attempt count. If under max_attempts, the message is
// reset to pending for retry; otherwise it is marked as failed.
func (q *PostgresQueue) Fail(ctx context.Context, id int64) error {
	var attempts, maxAttempts int
	err := sqlutil.QueryRowContext(ctx, q.db,
		`SELECT attempts, max_attempts FROM message_queue WHERE id = ?`, id,
	).Scan(&attempts, &maxAttempts)
	if err != nil {
		return fmt.Errorf("read attempts: %w", err)
	}

	attempts++

	if attempts >= maxAttempts {
		nowUTC := timeutil.NowUTC().Format(queueTimeFmt)
		_, err = sqlutil.ExecContext(ctx, q.db,
			`UPDATE message_queue SET status = ?, attempts = ?, completed_at = ? WHERE id = ?`,
			StatusFailed, attempts, nowUTC, id,
		)
		if err != nil {
			return fmt.Errorf("mark failed: %w", err)
		}
		q.clearInFlight(id)
		slog.Warn("message permanently failed", "id", id, "attempts", attempts)
		return nil
	}

	_, err = sqlutil.ExecContext(ctx, q.db,
		`UPDATE message_queue SET status = ?, attempts = ?, started_at = NULL WHERE id = ?`,
		StatusPending, attempts, id,
	)
	if err != nil {
		return fmt.Errorf("reset to pending: %w", err)
	}
	q.clearInFlight(id)

	q.repushMessage(ctx, id)
	return nil
}

// ResetAgentProcessing resets all processing messages for an agent back to
// pending with incremented attempts.
func (q *PostgresQueue) ResetAgentProcessing(ctx context.Context, agentID string) (int64, error) {
	result, err := sqlutil.ExecContext(ctx, q.db,
		`UPDATE message_queue SET status = ?, attempts = attempts + 1, started_at = NULL
		 WHERE agent_id = ? AND status = ?`,
		StatusPending, agentID, StatusProcessing,
	)
	if err != nil {
		return 0, fmt.Errorf("reset processing messages: %w", err)
	}
	return result.RowsAffected()
}

// Replay recovers all pending and stale-processing messages at startup,
// pushing them to their respective agent channels. Only processes messages
// targeted at this node (or with empty target_node_id).
func (q *PostgresQueue) Replay(ctx context.Context) error {
	// Reset stale processing messages for this node.
	threshold := time.Now().Add(-time.Duration(q.config.StaleTimeoutSeconds) * time.Second)
	result, err := sqlutil.ExecContext(ctx, q.db,
		`UPDATE message_queue SET status = ?, attempts = attempts + 1, started_at = NULL
		 WHERE status = ? AND started_at < ? AND (target_node_id = '' OR target_node_id = ?)`,
		StatusPending, StatusProcessing, threshold.UTC().Format(queueTimeFmt), q.nodeID,
	)
	if err != nil {
		return fmt.Errorf("reset stale messages: %w", err)
	}
	staleCount, _ := result.RowsAffected()
	if staleCount > 0 {
		slog.Info("reset stale processing messages", "count", staleCount)
	}

	// Query all pending messages for this node.
	rows, err := sqlutil.QueryContext(ctx, q.db,
		`SELECT id, agent_id, channel, conversation_id, sender, content, attachments, message_type, priority, status, attempts, max_attempts, target_node_id, created_at
		 FROM message_queue WHERE status = ? AND (target_node_id = '' OR target_node_id = ?) ORDER BY priority DESC, id ASC`,
		StatusPending, q.nodeID,
	)
	if err != nil {
		return fmt.Errorf("query pending messages: %w", err)
	}
	defer rows.Close()

	var pushed, skipped int
	var skippedInFlight, skippedNoChannel, skippedChannelFull int
	for rows.Next() {
		var msg QueueMessage
		var createdAt string
		if err := rows.Scan(
			&msg.ID, &msg.AgentID, &msg.Channel, &msg.ConversationID, &msg.Sender, &msg.Content,
			&msg.Attachments, &msg.MessageType, &msg.Priority, &msg.Status, &msg.Attempts, &msg.MaxAttempts,
			&msg.TargetNodeID, &createdAt,
		); err != nil {
			return fmt.Errorf("scan message: %w", err)
		}
		parsedCreatedAt, err := parseQueueTime(createdAt)
		if err != nil {
			return fmt.Errorf("parse created_at for message %d: %w", msg.ID, err)
		}
		msg.CreatedAt = parsedCreatedAt

		if q.isInFlight(msg.ID) {
			skipped++
			skippedInFlight++
			continue
		}

		q.mu.RLock()
		ch, ok := q.channels[msg.AgentID]
		q.mu.RUnlock()

		if !ok {
			skipped++
			skippedNoChannel++
			q.addMetric(msg.AgentID, func(m *agentQueueMetrics) {
				m.ReplaySkipped++
				m.ReplayNoChannelSkip++
			})
			continue
		}

		select {
		case ch <- msg:
			q.markInFlight(msg.ID)
			pushed++
			q.addMetric(msg.AgentID, func(m *agentQueueMetrics) {
				m.ReplayPushed++
			})
		default:
			skipped++
			skippedChannelFull++
			q.addMetric(msg.AgentID, func(m *agentQueueMetrics) {
				m.ReplaySkipped++
				m.ChannelFull++
			})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate messages: %w", err)
	}

	slog.Info("queue replay complete",
		"node_id", q.nodeID,
		"pushed", pushed,
		"skipped", skipped,
		"skipped_in_flight", skippedInFlight,
		"skipped_no_channel", skippedNoChannel,
		"skipped_channel_full", skippedChannelFull,
	)
	return nil
}

// ReplayAgent recovers pending messages for a single agent on this node.
func (q *PostgresQueue) ReplayAgent(ctx context.Context, agentID string) error {
	rows, err := sqlutil.QueryContext(ctx, q.db,
		`SELECT id, agent_id, channel, conversation_id, sender, content, attachments, message_type, priority, status, attempts, max_attempts, target_node_id, created_at
		 FROM message_queue WHERE status = ? AND agent_id = ? AND (target_node_id = '' OR target_node_id = ?) ORDER BY priority DESC, id ASC`,
		StatusPending, agentID, q.nodeID,
	)
	if err != nil {
		return fmt.Errorf("query pending messages for agent %s: %w", agentID, err)
	}
	defer rows.Close()

	q.mu.RLock()
	ch, ok := q.channels[agentID]
	q.mu.RUnlock()
	if !ok {
		return nil
	}

	var pushed int
	var skippedInFlight int
	var skippedChannelFull int
	for rows.Next() {
		var msg QueueMessage
		var createdAt string
		if err := rows.Scan(
			&msg.ID, &msg.AgentID, &msg.Channel, &msg.ConversationID, &msg.Sender, &msg.Content,
			&msg.Attachments, &msg.MessageType, &msg.Priority, &msg.Status, &msg.Attempts, &msg.MaxAttempts,
			&msg.TargetNodeID, &createdAt,
		); err != nil {
			return fmt.Errorf("scan message: %w", err)
		}
		parsedCreatedAt, err := parseQueueTime(createdAt)
		if err != nil {
			return fmt.Errorf("parse created_at for message %d: %w", msg.ID, err)
		}
		msg.CreatedAt = parsedCreatedAt

		if q.isInFlight(msg.ID) {
			skippedInFlight++
			continue
		}

		select {
		case ch <- msg:
			q.markInFlight(msg.ID)
			pushed++
			q.addMetric(agentID, func(m *agentQueueMetrics) {
				m.ReplayPushed++
			})
		default:
			skippedChannelFull++
			q.addMetric(agentID, func(m *agentQueueMetrics) {
				m.ChannelFull++
			})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate messages: %w", err)
	}

	if pushed > 0 || skippedInFlight > 0 || skippedChannelFull > 0 {
		q.addMetric(agentID, func(m *agentQueueMetrics) {
			m.ReplaySkipped += int64(skippedInFlight + skippedChannelFull)
			m.ReplayInFlightSkip += int64(skippedInFlight)
		})
		slog.Info("agent replay complete",
			"agent_id", agentID,
			"node_id", q.nodeID,
			"pushed", pushed,
			"skipped_in_flight", skippedInFlight,
			"skipped_channel_full", skippedChannelFull,
		)
	}
	return nil
}

// Cleanup deletes completed messages older than the given retention period.
func (q *PostgresQueue) Cleanup(ctx context.Context, retentionHours int) (int64, error) {
	threshold := time.Now().Add(-time.Duration(retentionHours) * time.Hour)
	result, err := sqlutil.ExecContext(ctx, q.db,
		`DELETE FROM message_queue WHERE status = ? AND completed_at < ?`,
		StatusCompleted, threshold.UTC().Format(queueTimeFmt),
	)
	if err != nil {
		return 0, fmt.Errorf("cleanup: %w", err)
	}
	return result.RowsAffected()
}

// Depth returns the number of pending + processing messages for an agent.
func (q *PostgresQueue) Depth(ctx context.Context, agentID string) (int, error) {
	var count int
	err := sqlutil.QueryRowContext(ctx, q.db,
		`SELECT COUNT(*) FROM message_queue WHERE agent_id = ? AND status IN (?, ?)`,
		agentID, StatusPending, StatusProcessing,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("depth query: %w", err)
	}
	return count, nil
}

// PriorityUsers returns the configured list of priority user IDs.
func (q *PostgresQueue) PriorityUsers() []string {
	return q.config.PriorityUsers
}

// ListMessages returns messages for an agent, optionally filtered by status.
func (q *PostgresQueue) ListMessages(ctx context.Context, agentID string, statusFilter string, limit int) ([]QueueMessage, error) {
	if limit <= 0 {
		limit = 100
	}

	var query string
	var args []any
	if statusFilter != "" {
		query = `SELECT id, agent_id, channel, conversation_id, sender, content, attachments, message_type, priority, status, attempts, max_attempts, target_node_id, created_at, started_at, completed_at
			FROM message_queue WHERE agent_id = ? AND status = ? ORDER BY created_at DESC LIMIT ?`
		args = []any{agentID, statusFilter, limit}
	} else {
		query = `SELECT id, agent_id, channel, conversation_id, sender, content, attachments, message_type, priority, status, attempts, max_attempts, target_node_id, created_at, started_at, completed_at
			FROM message_queue WHERE agent_id = ? ORDER BY created_at DESC LIMIT ?`
		args = []any{agentID, limit}
	}

	rows, err := sqlutil.QueryContext(ctx, q.db, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	var messages []QueueMessage
	for rows.Next() {
		var msg QueueMessage
		var createdAt string
		var startedAt, completedAt sql.NullString
		if err := rows.Scan(
			&msg.ID, &msg.AgentID, &msg.Channel, &msg.ConversationID, &msg.Sender, &msg.Content,
			&msg.Attachments, &msg.MessageType, &msg.Priority, &msg.Status, &msg.Attempts, &msg.MaxAttempts,
			&msg.TargetNodeID, &createdAt, &startedAt, &completedAt,
		); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		parsedCreatedAt, err := parseQueueTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at for message %d: %w", msg.ID, err)
		}
		msg.CreatedAt = parsedCreatedAt
		msg.StartedAt, err = parseNullableQueueTime(startedAt)
		if err != nil {
			return nil, fmt.Errorf("parse started_at for message %d: %w", msg.ID, err)
		}
		msg.CompletedAt, err = parseNullableQueueTime(completedAt)
		if err != nil {
			return nil, fmt.Errorf("parse completed_at for message %d: %w", msg.ID, err)
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}
	return messages, nil
}

// Stats returns per-status message counts for an agent.
func (q *PostgresQueue) Stats(ctx context.Context, agentID string) (map[string]int, error) {
	rows, err := sqlutil.QueryContext(ctx, q.db,
		`SELECT status, COUNT(*) FROM message_queue WHERE agent_id = ? GROUP BY status`,
		agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("stats query: %w", err)
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan stats: %w", err)
		}
		result[status] = count
	}
	return result, rows.Err()
}

// RetryMessage resets a failed message to pending for re-processing.
func (q *PostgresQueue) RetryMessage(ctx context.Context, id int64) error {
	result, err := sqlutil.ExecContext(ctx, q.db,
		`UPDATE message_queue SET status = ?, attempts = 0, started_at = NULL, completed_at = NULL
		 WHERE id = ? AND status = ?`,
		StatusPending, id, StatusFailed,
	)
	if err != nil {
		return fmt.Errorf("retry message: %w", err)
	}
	q.clearInFlight(id)
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("message %d not found or not in failed status", id)
	}

	q.repushMessage(ctx, id)
	return nil
}

// DeleteMessage removes a message from the queue.
func (q *PostgresQueue) DeleteMessage(ctx context.Context, id int64) error {
	result, err := sqlutil.ExecContext(ctx, q.db,
		`DELETE FROM message_queue WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	q.clearInFlight(id)
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("message %d not found", id)
	}
	return nil
}

// DeleteMessages removes messages for an agent, optionally filtered by status.
func (q *PostgresQueue) DeleteMessages(ctx context.Context, agentID, statusFilter string) (int64, error) {
	if strings.TrimSpace(agentID) == "" {
		return 0, fmt.Errorf("agent_id is required")
	}

	var (
		ids []int64
		err error
	)
	if statusFilter == "" {
		ids, err = q.listMessageIDs(ctx, agentID, "")
	} else {
		ids, err = q.listMessageIDs(ctx, agentID, statusFilter)
	}
	if err != nil {
		return 0, err
	}

	var res sql.Result
	if statusFilter == "" {
		res, err = sqlutil.ExecContext(ctx, q.db,
			`DELETE FROM message_queue WHERE agent_id = ?`, agentID,
		)
	} else {
		res, err = sqlutil.ExecContext(ctx, q.db,
			`DELETE FROM message_queue WHERE agent_id = ? AND status = ?`, agentID, statusFilter,
		)
	}
	if err != nil {
		return 0, fmt.Errorf("delete messages: %w", err)
	}
	deleted, _ := res.RowsAffected()
	for _, id := range ids {
		q.clearInFlight(id)
	}
	return deleted, nil
}

// Stop shuts down the cleanup goroutine and closes all in-memory channels.
func (q *PostgresQueue) Stop() {
	close(q.stopCh)
	<-q.done

	q.mu.Lock()
	defer q.mu.Unlock()
	for id, ch := range q.channels {
		close(ch)
		delete(q.channels, id)
	}
	q.inFlight = make(map[int64]struct{})
}

func (q *PostgresQueue) listMessageIDs(ctx context.Context, agentID, statusFilter string) ([]int64, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if statusFilter == "" {
		rows, err = sqlutil.QueryContext(ctx, q.db,
			`SELECT id FROM message_queue WHERE agent_id = ?`, agentID,
		)
	} else {
		rows, err = sqlutil.QueryContext(ctx, q.db,
			`SELECT id FROM message_queue WHERE agent_id = ? AND status = ?`, agentID, statusFilter,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list message ids: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan message id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate message ids: %w", err)
	}
	return ids, nil
}

// cleanupLoop runs hourly to remove old completed messages.
func (q *PostgresQueue) cleanupLoop() {
	defer close(q.done)
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-q.stopCh:
			return
		case <-ticker.C:
			deleted, err := q.Cleanup(context.Background(), q.config.RetentionHours)
			if err != nil {
				slog.Error("queue cleanup failed", "error", err)
			} else if deleted > 0 {
				slog.Info("queue cleanup complete", "deleted", deleted)
			}
		}
	}
}

// repushMessage reads a message from the DB and pushes it to the agent's channel.
func (q *PostgresQueue) repushMessage(ctx context.Context, id int64) {
	var msg QueueMessage
	var createdAt string
	err := sqlutil.QueryRowContext(ctx, q.db,
		`SELECT id, agent_id, channel, conversation_id, sender, content, attachments, message_type, priority, status, attempts, max_attempts, target_node_id, created_at
		 FROM message_queue WHERE id = ?`, id,
	).Scan(
		&msg.ID, &msg.AgentID, &msg.Channel, &msg.ConversationID, &msg.Sender, &msg.Content,
		&msg.Attachments, &msg.MessageType, &msg.Priority, &msg.Status, &msg.Attempts, &msg.MaxAttempts,
		&msg.TargetNodeID, &createdAt,
	)
	if err != nil {
		slog.Error("repush: failed to read message", "id", id, "error", err)
		return
	}
	parsedCreatedAt, err := parseQueueTime(createdAt)
	if err != nil {
		slog.Error("repush: failed to parse created_at", "id", id, "created_at", createdAt, "error", err)
		return
	}
	msg.CreatedAt = parsedCreatedAt

	q.mu.RLock()
	ch, ok := q.channels[msg.AgentID]
	q.mu.RUnlock()

	if !ok {
		return
	}

	select {
	case ch <- msg:
		q.markInFlight(msg.ID)
	default:
		q.addMetric(msg.AgentID, func(m *agentQueueMetrics) {
			m.ChannelFull++
		})
		slog.Debug("repush: channel full", "agent_id", msg.AgentID, "id", id)
	}
}

func (q *PostgresQueue) addMetric(agentID string, fn func(*agentQueueMetrics)) {
	if agentID == "" {
		return
	}
	q.mu.Lock()
	m := q.metrics[agentID]
	if m == nil {
		m = &agentQueueMetrics{}
		q.metrics[agentID] = m
	}
	fn(m)
	q.mu.Unlock()
}

func (q *PostgresQueue) markInFlight(id int64) {
	q.mu.Lock()
	q.inFlight[id] = struct{}{}
	q.mu.Unlock()
}

func (q *PostgresQueue) clearInFlight(id int64) {
	q.mu.Lock()
	delete(q.inFlight, id)
	q.mu.Unlock()
}

func (q *PostgresQueue) isInFlight(id int64) bool {
	q.mu.RLock()
	_, ok := q.inFlight[id]
	q.mu.RUnlock()
	return ok
}

// sanitizeChannel removes characters that are not valid in PostgreSQL NOTIFY channel names.
func sanitizeChannel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
