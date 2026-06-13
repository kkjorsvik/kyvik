package retention

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/kkjorsvik/kyvik/internal/sqlutil"
)

// TableStats holds row counts and database size information.
type TableStats struct {
	Agents              int64  `json:"agents"`
	AuditLog            int64  `json:"audit_log"`
	ConversationHistory int64  `json:"conversation_history"`
	ConversationArchive int64  `json:"conversation_archive"`
	MessageQueue        int64  `json:"message_queue"`
	SecurityEvents      int64  `json:"security_events"`
	Memories            int64  `json:"memories"`
	WebConversations    int64  `json:"web_conversations"`
	UsageRecords        int64  `json:"usage_records"`
	Schedules           int64  `json:"schedules"`
	DatabaseSize        int64  `json:"database_size"`
	DatabaseSizeHuman   string `json:"database_size_human"`
}

// Stats queries the database for table row counts and file size.
func (p *Pruner) Stats(ctx context.Context) (*TableStats, error) {
	stats := &TableStats{}

	tables := []struct {
		name string
		dest *int64
	}{
		{"agents", &stats.Agents},
		{"audit_log", &stats.AuditLog},
		{"conversation_history", &stats.ConversationHistory},
		{"conversation_history_archive", &stats.ConversationArchive},
		{"message_queue", &stats.MessageQueue},
		{"security_events", &stats.SecurityEvents},
		{"memories", &stats.Memories},
		{"web_conversations", &stats.WebConversations},
		{"usage_records", &stats.UsageRecords},
		{"schedules", &stats.Schedules},
	}

	for _, t := range tables {
		err := sqlutil.QueryRowContext(ctx, p.db,
			fmt.Sprintf("SELECT COUNT(*) FROM %s", t.name),
		).Scan(t.dest)
		if err != nil {
			// Table might not exist — skip without error.
			*t.dest = 0
		}
	}

	// Get database size via PostgreSQL pg_database_size().
	var dbSize int64
	err := sqlutil.QueryRowContext(ctx, p.db,
		"SELECT pg_database_size(current_database())",
	).Scan(&dbSize)
	if err == nil {
		stats.DatabaseSize = dbSize
		stats.DatabaseSizeHuman = humanizeBytes(dbSize)
	}

	return stats, nil
}

// StatsFromDB is a convenience function for getting stats without a Pruner.
func StatsFromDB(ctx context.Context, db *sql.DB) (*TableStats, error) {
	p := &Pruner{db: db}
	return p.Stats(ctx)
}

// humanizeBytes converts bytes to a human-readable string.
func humanizeBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
