package memory

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

// Store implements MemoryStore backed by a SQL database (PostgreSQL).
type Store struct {
	db *sql.DB
}

// New creates a new database-backed memory store using an existing *sql.DB.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (m *Store) Create(ctx context.Context, mem Memory) (int64, error) {
	if mem.Status == "" {
		mem.Status = StatusActive
	}

	var id int64
	err := sqlutil.QueryRowContext(ctx, m.db,
		`INSERT INTO memories
			(agent_id, category, content, source, relevance_score, pinned, reviewed, source_channel, source_channel_id, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		mem.AgentID, mem.Category, mem.Content, mem.Source,
		mem.RelevanceScore, mem.Pinned, mem.Reviewed, mem.SourceChannel, mem.SourceChannelID, mem.Status,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert memory: %w", err)
	}
	return id, nil
}

func (m *Store) Get(ctx context.Context, id int64) (Memory, error) {
	var mem Memory
	var createdAt, accessedAt string
	var pinnedRaw, archivedRaw, reviewedRaw any
	var embeddingModel sql.NullString
	var statusRaw sql.NullString

	err := sqlutil.QueryRowContext(ctx, m.db,
		`SELECT id, agent_id, category, content, source,
		        relevance_score, pinned, archived, reviewed, source_channel, source_channel_id, embedding_model, status,
		        created_at, accessed_at, access_count
		 FROM memories WHERE id = ?`, id,
	).Scan(
		&mem.ID, &mem.AgentID, &mem.Category, &mem.Content, &mem.Source,
		&mem.RelevanceScore, &pinnedRaw, &archivedRaw, &reviewedRaw, &mem.SourceChannel, &mem.SourceChannelID, &embeddingModel, &statusRaw,
		&createdAt, &accessedAt, &mem.AccessCount,
	)
	if err == sql.ErrNoRows {
		return Memory{}, fmt.Errorf("memory %d: not found", id)
	}
	if err != nil {
		return Memory{}, fmt.Errorf("get memory: %w", err)
	}
	var convErr error
	if mem.Pinned, convErr = boolFromDB(pinnedRaw); convErr != nil {
		return Memory{}, fmt.Errorf("scan pinned: %w", convErr)
	}
	if mem.Archived, convErr = boolFromDB(archivedRaw); convErr != nil {
		return Memory{}, fmt.Errorf("scan archived: %w", convErr)
	}
	if mem.Reviewed, convErr = boolFromDB(reviewedRaw); convErr != nil {
		return Memory{}, fmt.Errorf("scan reviewed: %w", convErr)
	}
	mem.EmbeddingModel = embeddingModel.String
	mem.Status = statusRaw.String
	if mem.Status == "" {
		mem.Status = StatusActive
	}

	var parseErr error
	if mem.CreatedAt, parseErr = parseTime(createdAt); parseErr != nil {
		return Memory{}, fmt.Errorf("parse created_at: %w", parseErr)
	}
	if mem.AccessedAt, parseErr = parseTime(accessedAt); parseErr != nil {
		return Memory{}, fmt.Errorf("parse accessed_at: %w", parseErr)
	}
	return mem, nil
}

func (m *Store) Update(ctx context.Context, mem Memory) error {
	result, err := sqlutil.ExecContext(ctx, m.db,
		`UPDATE memories SET
			content = ?, category = ?, source = ?,
			relevance_score = ?, pinned = ?, reviewed = ?, source_channel = ?, source_channel_id = ?
		 WHERE id = ?`,
		mem.Content, mem.Category, mem.Source,
		mem.RelevanceScore, mem.Pinned, mem.Reviewed, mem.SourceChannel, mem.SourceChannelID, mem.ID,
	)
	if err != nil {
		return fmt.Errorf("update memory: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("memory %d: not found", mem.ID)
	}
	return nil
}

func (m *Store) Delete(ctx context.Context, id int64) error {
	result, err := sqlutil.ExecContext(ctx, m.db,
		`DELETE FROM memories WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("memory %d: not found", id)
	}
	return nil
}

func (m *Store) List(ctx context.Context, agentID string, opts ListOptions) ([]Memory, error) {
	query := `SELECT id, agent_id, category, content, source,
	                 relevance_score, pinned, archived, reviewed, source_channel, source_channel_id, embedding_model, status,
	                 created_at, accessed_at, access_count
	          FROM memories WHERE agent_id = ?`
	args := []any{agentID}

	if opts.Category != "" {
		query += " AND category = ?"
		args = append(args, opts.Category)
	}
	if opts.Source != "" {
		query += " AND source = ?"
		args = append(args, opts.Source)
	}
	if opts.Pinned != nil {
		query += " AND pinned = ?"
		args = append(args, *opts.Pinned)
	}
	if opts.Reviewed != nil {
		query += " AND reviewed = ?"
		args = append(args, *opts.Reviewed)
	}

	// Determine status filter
	statusFilter := opts.Status
	if statusFilter == "" {
		// Backward compatibility: map old fields to status
		if opts.ArchivedOnly {
			statusFilter = "archived"
		} else if opts.IncludeArchived != nil && *opts.IncludeArchived {
			statusFilter = "all"
		} else {
			statusFilter = "active"
		}
	}

	switch statusFilter {
	case "candidate":
		query += " AND status = 'candidate'"
	case "all":
		// no status filter
	case "archived":
		query += " AND status = 'archived'"
	default:
		query += " AND status = 'active'"
	}

	query += " ORDER BY pinned DESC, created_at DESC"

	if opts.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", opts.Limit)
	}
	if opts.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", opts.Offset)
	}

	rows, err := sqlutil.QueryContext(ctx, m.db, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close()

	return scanMemories(rows)
}

func (m *Store) CountFiltered(ctx context.Context, agentID string, opts ListOptions) (int, error) {
	query := `SELECT COUNT(*) FROM memories WHERE agent_id = ?`
	args := []any{agentID}

	if opts.Category != "" {
		query += " AND category = ?"
		args = append(args, opts.Category)
	}
	if opts.Source != "" {
		query += " AND source = ?"
		args = append(args, opts.Source)
	}
	if opts.Pinned != nil {
		query += " AND pinned = ?"
		args = append(args, *opts.Pinned)
	}
	if opts.Reviewed != nil {
		query += " AND reviewed = ?"
		args = append(args, *opts.Reviewed)
	}

	// Determine status filter
	statusFilter := opts.Status
	if statusFilter == "" {
		// Backward compatibility: map old fields to status
		if opts.ArchivedOnly {
			statusFilter = "archived"
		} else if opts.IncludeArchived != nil && *opts.IncludeArchived {
			statusFilter = "all"
		} else {
			statusFilter = "active"
		}
	}

	switch statusFilter {
	case "candidate":
		query += " AND status = 'candidate'"
	case "all":
		// no status filter
	case "archived":
		query += " AND status = 'archived'"
	default:
		query += " AND status = 'active'"
	}

	var count int
	if err := sqlutil.QueryRowContext(ctx, m.db, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count memories: %w", err)
	}
	return count, nil
}

func (m *Store) ListPinned(ctx context.Context, agentID string) ([]Memory, error) {
	rows, err := sqlutil.QueryContext(ctx, m.db,
		`SELECT id, agent_id, category, content, source,
		        relevance_score, pinned, archived, reviewed, source_channel, source_channel_id, embedding_model, status,
		        created_at, accessed_at, access_count
		 FROM memories WHERE agent_id = ? AND pinned = TRUE AND status = 'active'
		 ORDER BY created_at DESC`, agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list pinned memories: %w", err)
	}
	defer rows.Close()

	return scanMemories(rows)
}

func (m *Store) Touch(ctx context.Context, id int64) error {
	_, err := sqlutil.ExecContext(ctx, m.db,
		`UPDATE memories SET accessed_at = CURRENT_TIMESTAMP, access_count = access_count + 1
		 WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("touch memory: %w", err)
	}
	return nil
}

func (m *Store) SetEmbedding(ctx context.Context, id int64, embedding []float32, model string) error {
	buf := EncodeEmbedding(embedding)

	_, err := sqlutil.ExecContext(ctx, m.db,
		`UPDATE memories SET embedding = ?, embedding_model = ? WHERE id = ?`,
		buf, model, id,
	)
	if err != nil {
		return fmt.Errorf("set embedding: %w", err)
	}
	return nil
}

func (m *Store) ListWithEmbeddings(ctx context.Context, agentID string) ([]Memory, error) {
	rows, err := sqlutil.QueryContext(ctx, m.db,
		`SELECT id, agent_id, category, content, source,
		        relevance_score, pinned, archived, reviewed, source_channel, source_channel_id, embedding_model, status, embedding,
		        created_at, accessed_at, access_count
		 FROM memories WHERE agent_id = ? AND embedding IS NOT NULL AND status IN ('active', 'candidate')
		 ORDER BY created_at DESC`, agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list memories with embeddings: %w", err)
	}
	defer rows.Close()

	return scanMemoriesWithEmbeddings(rows)
}

func (m *Store) GetUnembedded(ctx context.Context, agentID string) ([]Memory, error) {
	rows, err := sqlutil.QueryContext(ctx, m.db,
		`SELECT id, agent_id, category, content, source,
		        relevance_score, pinned, archived, reviewed, source_channel, source_channel_id, embedding_model, status,
		        created_at, accessed_at, access_count
		 FROM memories WHERE agent_id = ? AND embedding IS NULL AND status = 'active'
		 ORDER BY created_at DESC`, agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("get unembedded memories: %w", err)
	}
	defer rows.Close()

	return scanMemories(rows)
}

func (m *Store) GetAllUnembedded(ctx context.Context, limit int) ([]Memory, error) {
	rows, err := sqlutil.QueryContext(ctx, m.db,
		`SELECT id, agent_id, category, content, source,
		        relevance_score, pinned, archived, reviewed, source_channel, source_channel_id, embedding_model, status,
		        created_at, accessed_at, access_count
		 FROM memories WHERE embedding IS NULL AND status IN ('active', 'candidate')
		 ORDER BY created_at ASC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get all unembedded memories: %w", err)
	}
	defer rows.Close()

	return scanMemories(rows)
}

func (m *Store) ListRecent(ctx context.Context, agentID string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := sqlutil.QueryContext(ctx, m.db,
		`SELECT id, agent_id, category, content, source,
		        relevance_score, pinned, archived, reviewed, source_channel, source_channel_id, embedding_model, status,
		        created_at, accessed_at, access_count
		 FROM memories WHERE agent_id = ? AND status = 'active'
		 ORDER BY accessed_at DESC, created_at DESC
		 LIMIT ?`, agentID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list recent memories: %w", err)
	}
	defer rows.Close()

	return scanMemories(rows)
}

func (m *Store) Count(ctx context.Context, agentID string) (int, error) {
	var count int
	err := sqlutil.QueryRowContext(ctx, m.db,
		`SELECT COUNT(*) FROM memories WHERE agent_id = ? AND status = 'active'`, agentID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count memories: %w", err)
	}
	return count, nil
}

func (m *Store) DeleteByAgent(ctx context.Context, agentID string) error {
	_, err := sqlutil.ExecContext(ctx, m.db,
		`DELETE FROM memories WHERE agent_id = ?`, agentID,
	)
	if err != nil {
		return fmt.Errorf("delete agent memories: %w", err)
	}
	return nil
}

func (m *Store) Import(ctx context.Context, agentID string, memories []Memory) (int, error) {
	count := 0
	for _, mem := range memories {
		mem.AgentID = agentID
		if _, err := m.Create(ctx, mem); err != nil {
			return count, fmt.Errorf("import memory %d: %w", count, err)
		}
		count++
	}
	return count, nil
}

func (m *Store) CreateFromAgent(ctx context.Context, agentID, category, content string) (int64, error) {
	return m.Create(ctx, Memory{
		AgentID:        agentID,
		Category:       category,
		Content:        content,
		Source:         SourceAgent,
		RelevanceScore: 0.5,
		Reviewed:       true,
	})
}

func (m *Store) Archive(ctx context.Context, id int64) error {
	_, err := sqlutil.ExecContext(ctx, m.db,
		"UPDATE memories SET archived = TRUE, status = 'archived' WHERE id = ?", id)
	return err
}

func (m *Store) Unarchive(ctx context.Context, id int64) error {
	_, err := sqlutil.ExecContext(ctx, m.db,
		"UPDATE memories SET archived = FALSE, reviewed = TRUE, status = 'active' WHERE id = ?", id)
	return err
}

func (m *Store) ArchiveStale(ctx context.Context, agentID string, staleAfter time.Duration) (int, error) {
	cutoff := time.Now().Add(-staleAfter).Format(dbTimeFmt)
	result, err := sqlutil.ExecContext(ctx, m.db,
		`UPDATE memories SET archived = TRUE, status = 'archived'
		 WHERE agent_id = ? AND pinned = FALSE AND status = 'active'
		 AND accessed_at < ?`, agentID, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// ListCandidates returns all candidate memories for an agent.
func (m *Store) ListCandidates(ctx context.Context, agentID string) ([]Memory, error) {
	rows, err := sqlutil.QueryContext(ctx, m.db,
		`SELECT id, agent_id, category, content, source,
		        relevance_score, pinned, archived, reviewed, source_channel, source_channel_id, embedding_model, status,
		        created_at, accessed_at, access_count
		 FROM memories WHERE agent_id = ? AND status = 'candidate'
		 ORDER BY created_at DESC`, agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list candidate memories: %w", err)
	}
	defer rows.Close()

	return scanMemories(rows)
}

// CountCandidates returns the number of candidate memories for an agent.
func (m *Store) CountCandidates(ctx context.Context, agentID string) (int, error) {
	var count int
	err := sqlutil.QueryRowContext(ctx, m.db,
		"SELECT COUNT(*) FROM memories WHERE agent_id = ? AND status = 'candidate'", agentID).Scan(&count)
	return count, err
}

// PromoteCandidate promotes a candidate memory to active status.
func (m *Store) PromoteCandidate(ctx context.Context, id int64) error {
	res, err := sqlutil.ExecContext(ctx, m.db,
		"UPDATE memories SET status = 'active', archived = FALSE, reviewed = TRUE WHERE id = ? AND status = 'candidate'", id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("memory %d is not a candidate or does not exist", id)
	}
	return nil
}

// RejectCandidate deletes a candidate memory.
func (m *Store) RejectCandidate(ctx context.Context, id int64) error {
	_, err := sqlutil.ExecContext(ctx, m.db,
		"DELETE FROM memories WHERE id = ? AND status = 'candidate'", id)
	return err
}

// EnforceCapAndStore checks the memory cap, evicts if needed, then stores
// the memory as active. Returns the new memory ID.
func (s *Store) EnforceCapAndStore(ctx context.Context, mem Memory, maxMemories int) (int64, error) {
	if maxMemories <= 0 {
		maxMemories = 100
	}

	var activeCount int
	err := sqlutil.QueryRowContext(ctx, s.db,
		"SELECT COUNT(*) FROM memories WHERE agent_id = ? AND status = 'active' AND pinned = FALSE",
		mem.AgentID).Scan(&activeCount)
	if err != nil {
		return 0, fmt.Errorf("count active memories: %w", err)
	}

	var pinnedCount int
	err = sqlutil.QueryRowContext(ctx, s.db,
		"SELECT COUNT(*) FROM memories WHERE agent_id = ? AND status = 'active' AND pinned = TRUE",
		mem.AgentID).Scan(&pinnedCount)
	if err != nil {
		return 0, fmt.Errorf("count pinned memories: %w", err)
	}

	effectiveCap := maxMemories - pinnedCount
	if effectiveCap < 0 {
		effectiveCap = 0
	}
	if activeCount >= effectiveCap {
		rows, err := sqlutil.QueryContext(ctx, s.db,
			`SELECT id, category, pinned, accessed_at, access_count
			 FROM memories WHERE agent_id = ? AND status = 'active' AND pinned = FALSE`,
			mem.AgentID)
		if err != nil {
			return 0, fmt.Errorf("load memories for eviction: %w", err)
		}
		defer rows.Close()

		var candidates []Memory
		for rows.Next() {
			var candidate Memory
			var accessedAt string
			if err := rows.Scan(&candidate.ID, &candidate.Category, &candidate.Pinned, &accessedAt, &candidate.AccessCount); err != nil {
				return 0, err
			}
			var parseErr error
			if candidate.AccessedAt, parseErr = parseTime(accessedAt); parseErr != nil {
				return 0, fmt.Errorf("parse accessed_at: %w", parseErr)
			}
			candidates = append(candidates, candidate)
		}
		if err := rows.Err(); err != nil {
			return 0, err
		}

		target := findEvictionTarget(candidates, time.Now())
		if target >= 0 {
			if _, err := sqlutil.ExecContext(ctx, s.db,
				"UPDATE memories SET status = 'archived', archived = TRUE WHERE id = ?", target); err != nil {
				return 0, fmt.Errorf("evict memory: %w", err)
			}
		}
	}

	mem.Status = StatusActive
	return s.Create(ctx, mem)
}

// scanMemories scans rows into a slice of Memory.
func scanMemories(rows *sql.Rows) ([]Memory, error) {
	var memories []Memory
	for rows.Next() {
		var mem Memory
		var createdAt, accessedAt string
		var pinnedRaw, archivedRaw, reviewedRaw any
		var embeddingModel sql.NullString
		var statusRaw sql.NullString
		if err := rows.Scan(
			&mem.ID, &mem.AgentID, &mem.Category, &mem.Content, &mem.Source,
			&mem.RelevanceScore, &pinnedRaw, &archivedRaw, &reviewedRaw, &mem.SourceChannel, &mem.SourceChannelID, &embeddingModel, &statusRaw,
			&createdAt, &accessedAt, &mem.AccessCount,
		); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		var err error
		if mem.Pinned, err = boolFromDB(pinnedRaw); err != nil {
			return nil, fmt.Errorf("scan pinned: %w", err)
		}
		if mem.Archived, err = boolFromDB(archivedRaw); err != nil {
			return nil, fmt.Errorf("scan archived: %w", err)
		}
		if mem.Reviewed, err = boolFromDB(reviewedRaw); err != nil {
			return nil, fmt.Errorf("scan reviewed: %w", err)
		}
		mem.EmbeddingModel = embeddingModel.String
		mem.Status = statusRaw.String
		if mem.Status == "" {
			mem.Status = StatusActive
		}

		var parseErr error
		if mem.CreatedAt, parseErr = parseTime(createdAt); parseErr != nil {
			return nil, fmt.Errorf("parse created_at: %w", parseErr)
		}
		if mem.AccessedAt, parseErr = parseTime(accessedAt); parseErr != nil {
			return nil, fmt.Errorf("parse accessed_at: %w", parseErr)
		}
		memories = append(memories, mem)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memories: %w", err)
	}
	return memories, nil
}

// scanMemoriesWithEmbeddings scans rows that include the embedding BLOB column.
func scanMemoriesWithEmbeddings(rows *sql.Rows) ([]Memory, error) {
	var memories []Memory
	for rows.Next() {
		var mem Memory
		var createdAt, accessedAt string
		var pinnedRaw, archivedRaw, reviewedRaw any
		var embeddingModel sql.NullString
		var statusRaw sql.NullString
		var embeddingBlob []byte
		if err := rows.Scan(
			&mem.ID, &mem.AgentID, &mem.Category, &mem.Content, &mem.Source,
			&mem.RelevanceScore, &pinnedRaw, &archivedRaw, &reviewedRaw, &mem.SourceChannel, &mem.SourceChannelID, &embeddingModel, &statusRaw, &embeddingBlob,
			&createdAt, &accessedAt, &mem.AccessCount,
		); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		var err error
		if mem.Pinned, err = boolFromDB(pinnedRaw); err != nil {
			return nil, fmt.Errorf("scan pinned: %w", err)
		}
		if mem.Archived, err = boolFromDB(archivedRaw); err != nil {
			return nil, fmt.Errorf("scan archived: %w", err)
		}
		if mem.Reviewed, err = boolFromDB(reviewedRaw); err != nil {
			return nil, fmt.Errorf("scan reviewed: %w", err)
		}
		mem.EmbeddingModel = embeddingModel.String
		mem.Status = statusRaw.String
		if mem.Status == "" {
			mem.Status = StatusActive
		}
		if len(embeddingBlob) > 0 {
			mem.Embedding = DecodeEmbedding(embeddingBlob)
		}

		var parseErr error
		if mem.CreatedAt, parseErr = parseTime(createdAt); parseErr != nil {
			return nil, fmt.Errorf("parse created_at: %w", parseErr)
		}
		if mem.AccessedAt, parseErr = parseTime(accessedAt); parseErr != nil {
			return nil, fmt.Errorf("parse accessed_at: %w", parseErr)
		}
		memories = append(memories, mem)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memories: %w", err)
	}
	return memories, nil
}

func boolFromDB(value any) (bool, error) {
	switch v := value.(type) {
	case nil:
		return false, nil
	case bool:
		return v, nil
	case int:
		return v != 0, nil
	case int32:
		return v != 0, nil
	case int64:
		return v != 0, nil
	case float32:
		return v != 0, nil
	case float64:
		return v != 0, nil
	case []byte:
		return parseBoolString(string(v))
	case string:
		return parseBoolString(v)
	default:
		return false, fmt.Errorf("unsupported type %T", value)
	}
}

func parseBoolString(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "f", "no", "n":
		return false, nil
	case "1", "true", "t", "yes", "y":
		return true, nil
	default:
		return false, fmt.Errorf("invalid bool %q", v)
	}
}

// parseTime tries RFC3339 first, then falls back to the database CURRENT_TIMESTAMP format.
func parseTime(s string) (time.Time, error) {
	return timeutil.ParseTimestampUTC(s)
}
