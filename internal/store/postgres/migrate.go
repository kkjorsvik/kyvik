package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"github.com/kkjorsvik/kyvik/migrations"
)

// allMigrations returns all embedded migration SQL in order.
func allMigrations() []string {
	return []string{
		migrations.CoreSchema,
		migrations.AuditSchema,
		migrations.SecretsSchema,
		migrations.QueueSchema,
		migrations.AgentStateSchema,
		migrations.SoulIdentitySchema,
		migrations.HistorySchema,
		migrations.MemorySchema,
		migrations.ChannelConfigSchema,
		migrations.HistoryAttachmentsSchema,
		migrations.ModelSlotsSchema,
		migrations.SpendingSlotsSchema,
		migrations.WorkerConfigSchema,
		migrations.AgentCapabilitiesSchema,
		migrations.ToolAuditSchema,
		migrations.WorkspacesSchema,
		migrations.HostPathsSchema,
		migrations.SecurityEventsSchema,
		migrations.WebConversationsSchema,
		migrations.CircuitBreakerSchema,
		migrations.SystemStateSchema,
		migrations.AlertAcknowledgmentsSchema,
		migrations.SchedulesSchema,
		migrations.ConversationArchiveSchema,
		migrations.SkillGrantsSchema,
		migrations.TeamsSchema,
		migrations.InternalMessageAcksSchema,
		migrations.QueueMessageTypeSchema,
		migrations.PricingCatalogSchema,
		migrations.UsersSessionsGroupsSchema,
		migrations.APIKeysSchema,
		migrations.AgentTemplatesSchema,
		migrations.HostFilesystemSchema,
		migrations.WebhookInboundSchema,
		migrations.OutboundWebhooksSchema,
		migrations.RESTAPIEndpointsSchema,
		migrations.GuideAgentSchema,
		migrations.ProvidersSchema,
		migrations.DiscordFieldsSchema,
		migrations.DiscordAuthSchema,
		migrations.HTTPAllowedHostsSchema,
		migrations.TimestampMessagesSchema,
		migrations.AuditRiskLevelSchema,
		migrations.ConversationCompressionSchema,
		migrations.WorkflowsSchema,
		migrations.ClusterSchema,
		migrations.MemoryStatusSchema,
		migrations.ObsidianVaultsSchema,
		migrations.AttachmentMaxSizeSchema,
		migrations.AgentsMissingColumnsSchema,
		migrations.QueueHistoryMissingColumnsSchema,
		migrations.AgentsMemoryClusterColumnsSchema,
	}
}

// reAutoincrement matches INTEGER PRIMARY KEY AUTOINCREMENT (case-insensitive).
var reAutoincrement = regexp.MustCompile(`(?i)\bINTEGER\s+PRIMARY\s+KEY\s+AUTOINCREMENT\b`)

// reInsertOrIgnore matches INSERT OR IGNORE INTO (case-insensitive).
var reInsertOrIgnore = regexp.MustCompile(`(?i)INSERT\s+OR\s+IGNORE\s+INTO`)

// reAlterAddColumn matches ALTER TABLE x ADD COLUMN y and injects IF NOT EXISTS.
var reAlterAddColumn = regexp.MustCompile(`(?i)(ALTER\s+TABLE\s+\S+\s+ADD\s+COLUMN\s+)`)

// sqliteToPostgres converts a SQLite migration statement to PostgreSQL-compatible DDL.
func sqliteToPostgres(ddl string) string {
	s := ddl

	// INTEGER PRIMARY KEY AUTOINCREMENT -> BIGSERIAL PRIMARY KEY
	s = reAutoincrement.ReplaceAllString(s, "BIGSERIAL PRIMARY KEY")

	// BLOB -> BYTEA
	s = strings.ReplaceAll(s, " BLOB ", " BYTEA ")
	s = strings.ReplaceAll(s, " BLOB\n", " BYTEA\n")
	s = strings.ReplaceAll(s, " BLOB,", " BYTEA,")

	// DATETIME -> TIMESTAMPTZ
	s = strings.ReplaceAll(s, "DATETIME", "TIMESTAMPTZ")
	s = strings.ReplaceAll(s, "datetime", "TIMESTAMPTZ")

	// TIMESTAMP (without TZ) -> TIMESTAMPTZ for column type declarations.
	// Protect CURRENT_TIMESTAMP and existing TIMESTAMPTZ from conversion.
	s = strings.ReplaceAll(s, "CURRENT_TIMESTAMP", "%%CURTS%%")
	s = strings.ReplaceAll(s, "TIMESTAMPTZ", "%%TSTZ%%")
	s = strings.ReplaceAll(s, "TIMESTAMP", "TIMESTAMPTZ")
	s = strings.ReplaceAll(s, "%%TSTZ%%", "TIMESTAMPTZ")
	s = strings.ReplaceAll(s, "%%CURTS%%", "CURRENT_TIMESTAMP")

	// REAL -> DOUBLE PRECISION
	s = strings.ReplaceAll(s, " REAL ", " DOUBLE PRECISION ")
	s = strings.ReplaceAll(s, " REAL\n", " DOUBLE PRECISION\n")
	s = strings.ReplaceAll(s, " REAL,", " DOUBLE PRECISION,")

	// BOOLEAN DEFAULT 0/1 -> FALSE/TRUE
	s = strings.ReplaceAll(s, "BOOLEAN NOT NULL DEFAULT 0", "BOOLEAN NOT NULL DEFAULT FALSE")
	s = strings.ReplaceAll(s, "BOOLEAN DEFAULT 0", "BOOLEAN DEFAULT FALSE")
	s = strings.ReplaceAll(s, "BOOLEAN NOT NULL DEFAULT 1", "BOOLEAN NOT NULL DEFAULT TRUE")
	s = strings.ReplaceAll(s, "BOOLEAN DEFAULT 1", "BOOLEAN DEFAULT TRUE")

	// INTEGER NOT NULL DEFAULT 0 used as boolean in SQLite (is_leader etc.)
	// Leave as BIGINT — the ensureMemoryBooleanColumns fixup handles specific columns.

	// ALTER TABLE ... ADD COLUMN ... -> ADD COLUMN IF NOT EXISTS ...
	s = reAlterAddColumn.ReplaceAllStringFunc(s, func(match string) string {
		upper := strings.ToUpper(match)
		if strings.Contains(upper, "IF NOT EXISTS") {
			return match
		}
		// Insert IF NOT EXISTS after ADD COLUMN
		return strings.Replace(match, "ADD COLUMN", "ADD COLUMN IF NOT EXISTS", 1)
	})

	return s
}

// ensureSchema applies all embedded migrations directly as PostgreSQL DDL.
// CREATE TABLE/INDEX use IF NOT EXISTS. ALTER TABLE ADD COLUMN uses IF NOT EXISTS.
// Only truly ignorable errors (already exists) are skipped; real errors fail the migration.
func ensureSchema(ctx context.Context, db *sql.DB) error {
	for i, migration := range allMigrations() {
		pgDDL := sqliteToPostgres(migration)

		stmts := splitStatements(pgDDL)
		for _, stmt := range stmts {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" || isCommentOnly(stmt) {
				continue
			}
			// INSERT OR IGNORE INTO -> INSERT INTO ... ON CONFLICT DO NOTHING
			if reInsertOrIgnore.MatchString(stmt) {
				stmt = reInsertOrIgnore.ReplaceAllString(stmt, "INSERT INTO")
				if !strings.Contains(strings.ToUpper(stmt), "ON CONFLICT") {
					stmt += " ON CONFLICT DO NOTHING"
				}
			}
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				if isAlreadyExistsErr(err) {
					continue
				}
				// ALTER TABLE ADD COLUMN IF NOT EXISTS still errors on some
				// PostgreSQL versions — treat "already exists" column errors as ok.
				if isAlterColumnErr(stmt, err) {
					continue
				}
				return fmt.Errorf("migration %d failed: %w\nstatement: %s", i+1, err, truncateSQL(stmt))
			}
		}
	}
	return nil
}

// isCommentOnly returns true if the statement is only SQL comments.
func isCommentOnly(stmt string) bool {
	for _, line := range strings.Split(stmt, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "--") {
			return false
		}
	}
	return true
}

// isAlterColumnErr returns true if this is an ALTER TABLE error for a column that already exists.
func isAlterColumnErr(stmt string, err error) bool {
	upper := strings.ToUpper(stmt)
	if !strings.Contains(upper, "ALTER TABLE") {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "42701") || // column already exists
		strings.Contains(msg, "duplicate column")
}

// isAlreadyExistsErr checks if a PostgreSQL error indicates an object already exists.
func isAlreadyExistsErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "42P07") || // relation already exists
		strings.Contains(msg, "42710") || // object already exists
		strings.Contains(msg, "42701") // column already exists
}

// truncateSQL returns the first 200 chars of a SQL statement for error messages.
func truncateSQL(s string) string {
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "..."
}

// splitStatements splits SQL text on semicolons, respecting quoted strings and -- line comments.
func splitStatements(sqlText string) []string {
	var stmts []string
	var current strings.Builder
	inSingleQuote := false
	inDoubleQuote := false
	inLineComment := false

	for i := 0; i < len(sqlText); i++ {
		ch := sqlText[i]
		switch {
		case ch == '\n' && inLineComment:
			inLineComment = false
			current.WriteByte(ch)
		case inLineComment:
			current.WriteByte(ch)
		case ch == '-' && i+1 < len(sqlText) && sqlText[i+1] == '-' && !inSingleQuote && !inDoubleQuote:
			inLineComment = true
			current.WriteByte(ch)
		case ch == '\'' && !inDoubleQuote:
			inSingleQuote = !inSingleQuote
			current.WriteByte(ch)
		case ch == '"' && !inSingleQuote:
			inDoubleQuote = !inDoubleQuote
			current.WriteByte(ch)
		case ch == ';' && !inSingleQuote && !inDoubleQuote:
			s := strings.TrimSpace(current.String())
			if s != "" {
				stmts = append(stmts, s)
			}
			current.Reset()
		default:
			current.WriteByte(ch)
		}
	}
	if s := strings.TrimSpace(current.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}
