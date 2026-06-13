// Package postgres implements the store.Store interface using PostgreSQL via pgx.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/permissions"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Compile-time interface check.
var _ store.Store = (*PostgresStore)(nil)

// StoreOptions configures PostgreSQL store behavior.
type StoreOptions struct {
	MaxConnections  int           // default 15
	ConnMaxLifetime time.Duration // default 1 hour
}

// PostgresStore implements store.Store backed by PostgreSQL.
type PostgresStore struct {
	db *pgDB
}

// New opens PostgreSQL, ensures schema exists, and returns a store.
func New(dsn string, opts StoreOptions) (*PostgresStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	maxConns := opts.MaxConnections
	if maxConns <= 0 {
		maxConns = 15
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	lifetime := opts.ConnMaxLifetime
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	db.SetConnMaxLifetime(lifetime)
	if err := db.PingContext(context.Background()); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), "SET TIME ZONE 'UTC'"); err != nil {
		return nil, fmt.Errorf("set timezone: %w", err)
	}

	if err := ensureSchema(context.Background(), db); err != nil {
		return nil, fmt.Errorf("ensure schema: %w", err)
	}
	if err := ensureMemoryBooleanColumns(context.Background(), db); err != nil {
		return nil, fmt.Errorf("ensure memory booleans: %w", err)
	}
	if err := ensureUniqueConstraints(context.Background(), db); err != nil {
		return nil, fmt.Errorf("ensure unique constraints: %w", err)
	}

	if err := ensureSeedPricing(context.Background(), db); err != nil {
		return nil, fmt.Errorf("ensure seed pricing: %w", err)
	}

	// Migrate deprecated permission templates to admin.
	if _, err := db.ExecContext(context.Background(),
		`UPDATE agents SET template = 'admin' WHERE template IN ('power', 'unrestricted')`); err != nil {
		return nil, fmt.Errorf("migrate deprecated templates: %w", err)
	}

	// Migrate guide agent from reader to guide template.
	if _, err := db.ExecContext(context.Background(),
		`UPDATE agents SET template = 'guide' WHERE id = 'kyvik-guide' AND template = 'reader'`); err != nil {
		return nil, fmt.Errorf("migrate guide template: %w", err)
	}

	return &PostgresStore{db: newPGDB(db)}, nil
}

// NewAuditStore is not used for PostgreSQL deployments.
func NewAuditStore(_ string, _ StoreOptions) (*PostgresStore, error) {
	return nil, errors.New("separate audit database is not supported for PostgreSQL")
}

type pgDB struct {
	raw *sql.DB
}

func newPGDB(db *sql.DB) *pgDB {
	return &pgDB{raw: db}
}

func (p *pgDB) Exec(query string, args ...any) (sql.Result, error) {
	return p.raw.Exec(rebindPostgres(normalizePostgresSQL(query)), normalizeArgs(args)...)
}

func (p *pgDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.raw.ExecContext(ctx, rebindPostgres(normalizePostgresSQL(query)), normalizeArgs(args)...)
}

func (p *pgDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.raw.QueryContext(ctx, rebindPostgres(normalizePostgresSQL(query)), normalizeArgs(args)...)
}

func (p *pgDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.raw.QueryRowContext(ctx, rebindPostgres(normalizePostgresSQL(query)), normalizeArgs(args)...)
}

func (p *pgDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*pgTx, error) {
	tx, err := p.raw.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &pgTx{raw: tx}, nil
}

func (p *pgDB) Close() error {
	return p.raw.Close()
}

type pgTx struct {
	raw *sql.Tx
}

func (t *pgTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.raw.ExecContext(ctx, rebindPostgres(normalizePostgresSQL(query)), normalizeArgs(args)...)
}

func (t *pgTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.raw.QueryContext(ctx, rebindPostgres(normalizePostgresSQL(query)), normalizeArgs(args)...)
}

func (t *pgTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.raw.QueryRowContext(ctx, rebindPostgres(normalizePostgresSQL(query)), normalizeArgs(args)...)
}

func (t *pgTx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return t.raw.PrepareContext(ctx, rebindPostgres(normalizePostgresSQL(query)))
}

func (t *pgTx) Commit() error {
	return t.raw.Commit()
}

func (t *pgTx) Rollback() error {
	return t.raw.Rollback()
}

// quoteIdent quotes a PostgreSQL identifier to prevent SQL injection.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteIdentList quotes a comma-separated list of identifiers.
func quoteIdentList(commaSep string) string {
	parts := strings.Split(commaSep, ",")
	for i, p := range parts {
		parts[i] = quoteIdent(strings.TrimSpace(p))
	}
	return strings.Join(parts, ", ")
}

func normalizePostgresSQL(query string) string {
	q := query
	q = strings.ReplaceAll(q, "INSERT OR IGNORE INTO", "INSERT INTO")
	if strings.Contains(query, "INSERT OR IGNORE INTO") && !strings.Contains(q, "ON CONFLICT") {
		q += " ON CONFLICT DO NOTHING"
	}
	// JSON helper used by team lookup (PostgreSQL syntax).
	q = strings.ReplaceAll(
		q,
		"SELECT 1 FROM json_each(teams.member_ids_json) WHERE value = ?",
		"SELECT 1 FROM jsonb_array_elements_text(CASE WHEN teams.member_ids_json = '' THEN '[]'::jsonb ELSE teams.member_ids_json::jsonb END) AS value WHERE value = ?",
	)
	// Boolean literals are valid PostgreSQL syntax — no conversion needed.
	q = strings.ReplaceAll(q, "CURRENT_TIMESTAMP", "NOW()")
	return q
}

func ensureMemoryBooleanColumns(ctx context.Context, db *sql.DB) error {
	type columnFix struct {
		Table       string
		Name        string
		DefaultTrue bool
	}
	cols := []columnFix{
		// memories table
		{Table: "memories", Name: "pinned", DefaultTrue: false},
		{Table: "memories", Name: "archived", DefaultTrue: false},
		{Table: "memories", Name: "reviewed", DefaultTrue: true},
		// agents table — ensure BOOLEAN columns have correct type
		{Table: "agents", Name: "auto_extract_memories", DefaultTrue: false},
		{Table: "agents", Name: "timestamp_messages", DefaultTrue: false},
		{Table: "agents", Name: "webui_enabled", DefaultTrue: true},
		{Table: "agents", Name: "is_guide", DefaultTrue: false},
	}
	for _, col := range cols {
		typ, err := postgresColumnType(ctx, db, col.Table, col.Name)
		if err != nil {
			return err
		}
		if typ == "" {
			continue
		}
		if typ != "boolean" {
			if _, err := db.ExecContext(ctx, fmt.Sprintf(
				`ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT`, quoteIdent(col.Table), quoteIdent(col.Name),
			)); err != nil {
				return fmt.Errorf("drop default %s.%s: %w", col.Table, col.Name, err)
			}
			stmt := fmt.Sprintf(
				`ALTER TABLE %s ALTER COLUMN %s TYPE BOOLEAN USING (%s <> 0)`,
				quoteIdent(col.Table), quoteIdent(col.Name),
				quoteIdent(col.Name),
			)
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("alter %s.%s to boolean: %w", col.Table, col.Name, err)
			}
		}
		if col.DefaultTrue {
			if _, err := db.ExecContext(ctx, fmt.Sprintf(
				`ALTER TABLE %s ALTER COLUMN %s SET DEFAULT TRUE`, quoteIdent(col.Table), quoteIdent(col.Name),
			)); err != nil {
				return fmt.Errorf("set default %s.%s: %w", col.Table, col.Name, err)
			}
		} else {
			if _, err := db.ExecContext(ctx, fmt.Sprintf(
				`ALTER TABLE %s ALTER COLUMN %s SET DEFAULT FALSE`, quoteIdent(col.Table), quoteIdent(col.Name),
			)); err != nil {
				return fmt.Errorf("set default %s.%s: %w", col.Table, col.Name, err)
			}
		}
	}
	return nil
}

// ensureUniqueConstraints adds unique constraints that
// may be missing from early migrations.
//
func ensureUniqueConstraints(ctx context.Context, db *sql.DB) error {
	constraints := []struct {
		name  string
		table string
		cols  string
	}{
		{"uq_spending_limits_agent_id", "spending_limits", "agent_id"},
		{"uq_pricing_catalog_lookup", "pricing_catalog", "provider, model_pattern, effective_from"},
		{"uq_skill_grants_agent_skill", "skill_grants", "agent_id, skill_name"},
	}
	for _, c := range constraints {
		query := fmt.Sprintf(
			`DO $$ BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = '%s') THEN
					ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (%s);
				END IF;
			END $$`,
			c.name, quoteIdent(c.table), quoteIdent(c.name), quoteIdentList(c.cols),
		)
		if _, err := db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("ensure constraint %s: %w", c.name, err)
		}
	}
	return nil
}

// ensureSeedPricing inserts default pricing catalog entries if they don't exist.
func ensureSeedPricing(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
INSERT INTO pricing_catalog
    (provider, model_pattern, input_per_m, output_per_m, currency, effective_from, source, source_version)
VALUES
    ('openai', 'gpt-4o', 2.50, 10.00, 'USD', '1970-01-01 00:00:00+00', 'seed', 'v1'),
    ('openai', 'gpt-4o-mini', 0.15, 0.60, 'USD', '1970-01-01 00:00:00+00', 'seed', 'v1'),
    ('openai', 'gpt-4-turbo', 10.00, 30.00, 'USD', '1970-01-01 00:00:00+00', 'seed', 'v1'),
    ('openai', 'o1', 15.00, 60.00, 'USD', '1970-01-01 00:00:00+00', 'seed', 'v1'),
    ('openai', 'o1-mini', 1.10, 4.40, 'USD', '1970-01-01 00:00:00+00', 'seed', 'v1'),
    ('openai', 'o3-mini', 1.10, 4.40, 'USD', '1970-01-01 00:00:00+00', 'seed', 'v1'),
    ('anthropic', 'claude-opus-4-5-20250527', 15.00, 75.00, 'USD', '1970-01-01 00:00:00+00', 'seed', 'v1'),
    ('anthropic', 'claude-sonnet-4-20250514', 3.00, 15.00, 'USD', '1970-01-01 00:00:00+00', 'seed', 'v1'),
    ('anthropic', 'claude-haiku-4-5-20251001', 0.80, 4.00, 'USD', '1970-01-01 00:00:00+00', 'seed', 'v1'),
    ('anthropic', 'claude-3-5-sonnet-20241022', 3.00, 15.00, 'USD', '1970-01-01 00:00:00+00', 'seed', 'v1'),
    ('anthropic', 'claude-3-5-haiku-20241022', 0.80, 4.00, 'USD', '1970-01-01 00:00:00+00', 'seed', 'v1'),
    ('anthropic', 'claude-3-opus-20240229', 15.00, 75.00, 'USD', '1970-01-01 00:00:00+00', 'seed', 'v1')
ON CONFLICT (provider, model_pattern, effective_from) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("seed pricing catalog: %w", err)
	}
	return nil
}

func postgresColumnType(ctx context.Context, db *sql.DB, table, column string) (string, error) {
	var typ string
	err := db.QueryRowContext(ctx,
		`SELECT data_type FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`,
		table, column,
	).Scan(&typ)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lookup column type %s.%s: %w", table, column, err)
	}
	return strings.ToLower(strings.TrimSpace(typ)), nil
}

func normalizeArgs(args []any) []any {
	// Pass arguments through as-is; pgx handles Go bool natively for
	// PostgreSQL BOOLEAN columns.
	return args
}

func rebindPostgres(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	inSingle := false
	inDouble := false
	param := 1
	for i := 0; i < len(query); i++ {
		ch := query[i]
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
			b.WriteByte(ch)
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
			b.WriteByte(ch)
		case '?':
			if inSingle || inDouble {
				b.WriteByte(ch)
				continue
			}
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(param))
			param++
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

// ---------- Agent CRUD ----------

func (s *PostgresStore) CreateAgent(ctx context.Context, config types.AgentConfig) error {
	channelsJSON, err := json.Marshal(config.Channels)
	if err != nil {
		return fmt.Errorf("marshal channels: %w", err)
	}
	limitsJSON, err := json.Marshal(config.Limits)
	if err != nil {
		return fmt.Errorf("marshal limits: %w", err)
	}
	metadataJSON, err := json.Marshal(config.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	config.ContextBudget = types.NormalizeContextBudget(config.ContextBudget)
	contextBudgetJSON, err := json.Marshal(config.ContextBudget)
	if err != nil {
		return fmt.Errorf("marshal context budget: %w", err)
	}
	config.Workers = types.NormalizeWorkerConfig(config.Workers)
	workerConfigJSON, err := json.Marshal(config.Workers)
	if err != nil {
		return fmt.Errorf("marshal worker config: %w", err)
	}
	toolGrantsJSON, err := json.Marshal(config.ToolGrants)
	if err != nil {
		return fmt.Errorf("marshal tool grants: %w", err)
	}
	capabilityGrantsJSON, err := json.Marshal(config.CapabilityGrants)
	if err != nil {
		return fmt.Errorf("marshal capability grants: %w", err)
	}
	canMessageJSON, err := json.Marshal(config.CanMessage)
	if err != nil {
		return fmt.Errorf("marshal can_message: %w", err)
	}
	httpAllowedHostsJSON, err := json.Marshal(config.HTTPAllowedHosts)
	if err != nil {
		return fmt.Errorf("marshal http_allowed_hosts: %w", err)
	}
	shellAllowedCommandsJSON, err := json.Marshal(config.ShellAllowedCommands)
	if err != nil {
		return fmt.Errorf("marshal shell_allowed_commands: %w", err)
	}
	obsidianVaultsJSON, err := json.Marshal(config.ObsidianVaults)
	if err != nil {
		return fmt.Errorf("marshal obsidian_vaults: %w", err)
	}

	hostPathsJSON := marshalHostPaths(config.HostPaths)
	hostFilesystemJSON := marshalHostFilesystem(config.HostFilesystem)
	webhookInboundJSON := marshalWebhookInbound(config.WebhookInbound)

	var restAPIEndpointsJSON *string
	if config.RESTAPIEndpointsJSON != "" {
		restAPIEndpointsJSON = &config.RESTAPIEndpointsJSON
	}

	desiredState := string(config.DesiredState)
	if desiredState == "" {
		desiredState = string(types.DesiredStateStopped)
	}
	actualState := string(config.ActualState)
	if actualState == "" {
		actualState = string(types.AgentStatusStopped)
	}

	slackMode := config.SlackMode
	if slackMode == "" {
		slackMode = types.SlackModeNone
	}
	discordMode := config.DiscordMode
	if discordMode == "" {
		discordMode = types.DiscordModeNone
	}
	webuiEnabled := config.WebUIEnabled

	securityConfig := config.SecurityJSON
	if securityConfig == "" {
		securityConfig = "{}"
	}

	circuitBreaker := config.CircuitBreakerJSON
	if circuitBreaker == "" {
		circuitBreaker = "{}"
	}

	now := time.Now().UTC()
	createdAt := config.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := config.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO agents
			(id, name, description, system_prompt, soul_content, identity_content,
			 model_provider, model_name,
			 template, channels_json, limits_json, metadata_json,
			 desired_state, actual_state, last_error,
			 history_limit, memory_limit, auto_extract_memories, timestamp_messages,
			 context_budget_json, worker_config,
			 slack_mode, slack_channel, discord_mode, discord_channel_id, discord_auth_mode, webui_enabled,
			 model_slots, routing_config,
			 tool_grants, capability_grants, host_paths, host_filesystem, webhook_inbound,
			 rest_api_endpoints,
			 security_config, circuit_breaker, heartbeat_config,
			 compression_json, feedback_hooks_json,
			 can_message_json, team_id,
			 is_guide,
			 http_allowed_hosts, shell_allowed_commands,
			 obsidian_vaults,
			 attachment_max_size_mb,
			 created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		config.ID, config.Name, config.Description, config.SystemPrompt,
		config.SoulContent, config.IdentityContent,
		config.ModelConfig.Provider, config.ModelConfig.Model,
		config.Template,
		string(channelsJSON), string(limitsJSON), string(metadataJSON),
		desiredState, actualState, config.LastError,
		config.HistoryLimit, config.MemoryLimit, config.AutoExtractMemories, config.TimestampMessages,
		string(contextBudgetJSON), string(workerConfigJSON),
		slackMode, config.SlackChannel, discordMode, config.DiscordChannelID, config.DiscordAuthMode, webuiEnabled,
		config.ModelSlotsJSON, config.RoutingConfigJSON,
		string(toolGrantsJSON), string(capabilityGrantsJSON), hostPathsJSON, hostFilesystemJSON, webhookInboundJSON,
		restAPIEndpointsJSON,
		securityConfig, circuitBreaker, config.HeartbeatJSON,
		config.CompressionJSON, config.FeedbackHooksJSON,
		string(canMessageJSON), config.TeamID,
		config.IsGuide,
		string(httpAllowedHostsJSON), string(shellAllowedCommandsJSON),
		string(obsidianVaultsJSON),
		config.AttachmentMaxSizeMB,
		createdAt.UTC().Format(time.RFC3339),
		updatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert agent: %w", err)
	}
	return nil
}

// pgQueryable is satisfied by both *pgDB and *pgTx, allowing helpers
// to be reused inside or outside a transaction.
type pgQueryable interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *PostgresStore) GetAgent(ctx context.Context, id string) (*types.AgentConfig, error) {
	return getAgentQ(ctx, s.db, id)
}

// getAgentQ fetches a single agent using the provided pgQueryable (db or tx).
func getAgentQ(ctx context.Context, q pgQueryable, id string) (*types.AgentConfig, error) {
	var (
		config               types.AgentConfig
		channelsJSON         string
		limitsJSON           string
		metadataJSON         string
		contextBudgetJSON    string
		workerConfigJSON     string
		toolGrantsJSON       string
		capabilityGrantsJSON string
		hostPathsJSON        sql.NullString
		hostFilesystemJSON   sql.NullString
		webhookInboundJSON   sql.NullString
		desiredState         string
		actualState          string
		lastError            string
		slackMode            string
		discordMode          string
		discordAuthMode      string
		webuiEnabled         bool
		createdAt            string
		updatedAt            string
	)

	var securityConfigJSON string
	var circuitBreakerJSON string
	var heartbeatConfigJSON string
	var compressionJSON string
	var feedbackHooksJSON string
	var canMessageJSON string
	var restAPIEndpointsJSON sql.NullString
	var httpAllowedHostsJSON string
	var shellAllowedCommandsJSON string
	var obsidianVaultsJSON string
	var isGuide bool
	var nodeAffinityJSON string
	var nodePreferenceJSON string

	err := q.QueryRowContext(ctx,
		`SELECT id, name, description, system_prompt, soul_content, identity_content,
		        model_provider, model_name,
		        template, channels_json, limits_json, metadata_json,
		        desired_state, actual_state, last_error,
		        history_limit, memory_limit, auto_extract_memories, timestamp_messages,
		        context_budget_json, worker_config,
		        slack_mode, slack_channel, discord_mode, discord_channel_id, discord_auth_mode, webui_enabled,
		        model_slots, routing_config,
		        tool_grants, capability_grants, host_paths, host_filesystem, webhook_inbound,
		        rest_api_endpoints,
		        security_config, circuit_breaker, heartbeat_config,
		        compression_json, feedback_hooks_json,
		        can_message_json, team_id,
		        is_guide,
		        http_allowed_hosts, shell_allowed_commands,
		        obsidian_vaults,
		        attachment_max_size_mb,
		        max_memories, memory_extraction_interval, memory_max_extractions_per_run,
		        memory_duplicate_threshold, memory_similar_threshold,
		        node_affinity, node_preference,
		        created_at, updated_at
		 FROM agents WHERE id = ?`, id,
	).Scan(
		&config.ID, &config.Name, &config.Description, &config.SystemPrompt,
		&config.SoulContent, &config.IdentityContent,
		&config.ModelConfig.Provider, &config.ModelConfig.Model,
		&config.Template,
		&channelsJSON, &limitsJSON, &metadataJSON,
		&desiredState, &actualState, &lastError,
		&config.HistoryLimit, &config.MemoryLimit, &config.AutoExtractMemories, &config.TimestampMessages,
		&contextBudgetJSON, &workerConfigJSON,
		&slackMode, &config.SlackChannel, &discordMode, &config.DiscordChannelID, &discordAuthMode, &webuiEnabled,
		&config.ModelSlotsJSON, &config.RoutingConfigJSON,
		&toolGrantsJSON, &capabilityGrantsJSON, &hostPathsJSON, &hostFilesystemJSON, &webhookInboundJSON,
		&restAPIEndpointsJSON,
		&securityConfigJSON, &circuitBreakerJSON, &heartbeatConfigJSON,
		&compressionJSON, &feedbackHooksJSON,
		&canMessageJSON, &config.TeamID,
		&isGuide,
		&httpAllowedHostsJSON, &shellAllowedCommandsJSON,
		&obsidianVaultsJSON,
		&config.AttachmentMaxSizeMB,
		&config.MaxMemories, &config.MemoryExtractionInterval, &config.MemoryMaxExtractionsPerRun,
		&config.MemoryDuplicateThreshold, &config.MemorySimilarThreshold,
		&nodeAffinityJSON, &nodePreferenceJSON,
		&createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("agent %s: %w", id, types.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("query agent: %w", err)
	}

	if err := json.Unmarshal([]byte(channelsJSON), &config.Channels); err != nil {
		return nil, fmt.Errorf("unmarshal channels: %w", err)
	}
	if err := json.Unmarshal([]byte(limitsJSON), &config.Limits); err != nil {
		return nil, fmt.Errorf("unmarshal limits: %w", err)
	}
	if err := json.Unmarshal([]byte(metadataJSON), &config.Metadata); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}
	if contextBudgetJSON != "" {
		if err := json.Unmarshal([]byte(contextBudgetJSON), &config.ContextBudget); err != nil {
			return nil, fmt.Errorf("unmarshal context budget: %w", err)
		}
	}
	config.ContextBudget = types.NormalizeContextBudget(config.ContextBudget)
	if workerConfigJSON != "" {
		if err := json.Unmarshal([]byte(workerConfigJSON), &config.Workers); err != nil {
			return nil, fmt.Errorf("unmarshal worker config: %w", err)
		}
	}
	if toolGrantsJSON != "" {
		if err := json.Unmarshal([]byte(toolGrantsJSON), &config.ToolGrants); err != nil {
			return nil, fmt.Errorf("unmarshal tool grants: %w", err)
		}
	}
	if capabilityGrantsJSON != "" {
		if err := json.Unmarshal([]byte(capabilityGrantsJSON), &config.CapabilityGrants); err != nil {
			return nil, fmt.Errorf("unmarshal capability grants: %w", err)
		}
	}
	if hostPathsJSON.Valid && hostPathsJSON.String != "" {
		var hp types.HostPathConfig
		if err := json.Unmarshal([]byte(hostPathsJSON.String), &hp); err != nil {
			return nil, fmt.Errorf("unmarshal host paths: %w", err)
		}
		config.HostPaths = &hp
	}
	if hostFilesystemJSON.Valid && hostFilesystemJSON.String != "" {
		var hfs types.HostFilesystemConfig
		if err := json.Unmarshal([]byte(hostFilesystemJSON.String), &hfs); err != nil {
			return nil, fmt.Errorf("unmarshal host filesystem: %w", err)
		}
		config.HostFilesystem = &hfs
	}
	if webhookInboundJSON.Valid && webhookInboundJSON.String != "" {
		var wh types.InboundWebhookConfig
		if err := json.Unmarshal([]byte(webhookInboundJSON.String), &wh); err != nil {
			return nil, fmt.Errorf("unmarshal webhook_inbound: %w", err)
		}
		config.WebhookInbound = &wh
	}
	config.SecurityJSON = securityConfigJSON
	config.CircuitBreakerJSON = circuitBreakerJSON
	config.HeartbeatJSON = heartbeatConfigJSON
	config.CompressionJSON = compressionJSON
	config.FeedbackHooksJSON = feedbackHooksJSON
	if restAPIEndpointsJSON.Valid {
		config.RESTAPIEndpointsJSON = restAPIEndpointsJSON.String
	}
	if canMessageJSON != "" && canMessageJSON != "[]" {
		if err := json.Unmarshal([]byte(canMessageJSON), &config.CanMessage); err != nil {
			return nil, fmt.Errorf("unmarshal can_message: %w", err)
		}
	}
	if httpAllowedHostsJSON != "" && httpAllowedHostsJSON != "[]" {
		if err := json.Unmarshal([]byte(httpAllowedHostsJSON), &config.HTTPAllowedHosts); err != nil {
			return nil, fmt.Errorf("unmarshal http_allowed_hosts: %w", err)
		}
	}
	if shellAllowedCommandsJSON != "" && shellAllowedCommandsJSON != "[]" {
		if err := json.Unmarshal([]byte(shellAllowedCommandsJSON), &config.ShellAllowedCommands); err != nil {
			return nil, fmt.Errorf("unmarshal shell_allowed_commands: %w", err)
		}
	}
	if obsidianVaultsJSON != "" && obsidianVaultsJSON != "[]" {
		if err := json.Unmarshal([]byte(obsidianVaultsJSON), &config.ObsidianVaults); err != nil {
			return nil, fmt.Errorf("unmarshal obsidian_vaults: %w", err)
		}
	}
	if nodeAffinityJSON != "" {
		if err := json.Unmarshal([]byte(nodeAffinityJSON), &config.NodeAffinity); err != nil {
			return nil, fmt.Errorf("unmarshal node_affinity: %w", err)
		}
	}
	if nodePreferenceJSON != "" {
		if err := json.Unmarshal([]byte(nodePreferenceJSON), &config.NodePreference); err != nil {
			return nil, fmt.Errorf("unmarshal node_preference: %w", err)
		}
	}
	config.IsGuide = isGuide
	config.DesiredState = types.DesiredState(desiredState)
	config.ActualState = types.AgentStatus(actualState)
	config.LastError = lastError
	config.SlackMode = slackMode
	if config.SlackMode == "" {
		config.SlackMode = types.SlackModeNone
	}
	config.DiscordMode = discordMode
	if config.DiscordMode == "" {
		config.DiscordMode = types.DiscordModeNone
	}
	config.DiscordAuthMode = discordAuthMode
	if config.DiscordAuthMode == "" {
		config.DiscordAuthMode = types.DiscordAuthModeOpen
	}
	config.WebUIEnabled = webuiEnabled
	if config.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if config.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return &config, nil
}

func (s *PostgresStore) ListAgents(ctx context.Context) ([]types.AgentConfig, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, system_prompt, soul_content, identity_content,
		        model_provider, model_name,
		        template, channels_json, limits_json, metadata_json,
		        desired_state, actual_state, last_error,
		        history_limit, memory_limit, auto_extract_memories, timestamp_messages,
		        context_budget_json, worker_config,
		        slack_mode, slack_channel, discord_mode, discord_channel_id, discord_auth_mode, webui_enabled,
		        model_slots, routing_config,
		        tool_grants, capability_grants, host_paths, host_filesystem, webhook_inbound,
		        rest_api_endpoints,
		        security_config, circuit_breaker, heartbeat_config,
		        compression_json, feedback_hooks_json,
		        can_message_json, team_id,
		        is_guide,
		        http_allowed_hosts, shell_allowed_commands,
		        obsidian_vaults,
		        attachment_max_size_mb,
		        max_memories, memory_extraction_interval, memory_max_extractions_per_run,
		        memory_duplicate_threshold, memory_similar_threshold,
		        node_affinity, node_preference,
		        created_at, updated_at
		 FROM agents ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("query agents: %w", err)
	}
	defer rows.Close()

	var agents []types.AgentConfig
	for rows.Next() {
		var (
			config                   types.AgentConfig
			channelsJSON             string
			limitsJSON               string
			metadataJSON             string
			contextBudgetJSON        string
			workerConfigJSON         string
			toolGrantsJSON           string
			capabilityGrantsJSON     string
			hostPathsJSON            sql.NullString
			hostFilesystemJSON       sql.NullString
			webhookInboundJSON       sql.NullString
			restAPIEndpointsJSON     sql.NullString
			securityConfigJSON       string
			circuitBreakerJSON       string
			heartbeatConfigJSON      string
			compressionJSON          string
			feedbackHooksJSON        string
			canMessageJSON           string
			httpAllowedHostsJSON     string
			shellAllowedCommandsJSON string
			obsidianVaultsJSON       string
			isGuide                  bool
			desiredState             string
			actualState              string
			lastError                string
			slackMode                string
			discordMode              string
			discordAuthMode          string
			webuiEnabled             bool
			nodeAffinityJSON         string
			nodePreferenceJSON       string
			createdAt                string
			updatedAt                string
		)
		if err := rows.Scan(
			&config.ID, &config.Name, &config.Description, &config.SystemPrompt,
			&config.SoulContent, &config.IdentityContent,
			&config.ModelConfig.Provider, &config.ModelConfig.Model,
			&config.Template,
			&channelsJSON, &limitsJSON, &metadataJSON,
			&desiredState, &actualState, &lastError,
			&config.HistoryLimit, &config.MemoryLimit, &config.AutoExtractMemories, &config.TimestampMessages,
			&contextBudgetJSON, &workerConfigJSON,
			&slackMode, &config.SlackChannel, &discordMode, &config.DiscordChannelID, &discordAuthMode, &webuiEnabled,
			&config.ModelSlotsJSON, &config.RoutingConfigJSON,
			&toolGrantsJSON, &capabilityGrantsJSON, &hostPathsJSON, &hostFilesystemJSON, &webhookInboundJSON,
			&restAPIEndpointsJSON,
			&securityConfigJSON, &circuitBreakerJSON, &heartbeatConfigJSON,
			&compressionJSON, &feedbackHooksJSON,
			&canMessageJSON, &config.TeamID,
			&isGuide,
			&httpAllowedHostsJSON, &shellAllowedCommandsJSON,
			&obsidianVaultsJSON,
			&config.AttachmentMaxSizeMB,
			&config.MaxMemories, &config.MemoryExtractionInterval, &config.MemoryMaxExtractionsPerRun,
			&config.MemoryDuplicateThreshold, &config.MemorySimilarThreshold,
			&nodeAffinityJSON, &nodePreferenceJSON,
			&createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		if err := json.Unmarshal([]byte(channelsJSON), &config.Channels); err != nil {
			return nil, fmt.Errorf("unmarshal channels: %w", err)
		}
		if err := json.Unmarshal([]byte(limitsJSON), &config.Limits); err != nil {
			return nil, fmt.Errorf("unmarshal limits: %w", err)
		}
		if err := json.Unmarshal([]byte(metadataJSON), &config.Metadata); err != nil {
			return nil, fmt.Errorf("unmarshal metadata: %w", err)
		}
		if contextBudgetJSON != "" {
			if err := json.Unmarshal([]byte(contextBudgetJSON), &config.ContextBudget); err != nil {
				return nil, fmt.Errorf("unmarshal context budget: %w", err)
			}
		}
		config.ContextBudget = types.NormalizeContextBudget(config.ContextBudget)
		if workerConfigJSON != "" {
			if err := json.Unmarshal([]byte(workerConfigJSON), &config.Workers); err != nil {
				return nil, fmt.Errorf("unmarshal worker config: %w", err)
			}
		}
		if toolGrantsJSON != "" {
			if err := json.Unmarshal([]byte(toolGrantsJSON), &config.ToolGrants); err != nil {
				return nil, fmt.Errorf("unmarshal tool grants: %w", err)
			}
		}
		if capabilityGrantsJSON != "" {
			if err := json.Unmarshal([]byte(capabilityGrantsJSON), &config.CapabilityGrants); err != nil {
				return nil, fmt.Errorf("unmarshal capability grants: %w", err)
			}
		}
		if hostPathsJSON.Valid && hostPathsJSON.String != "" {
			var hp types.HostPathConfig
			if err := json.Unmarshal([]byte(hostPathsJSON.String), &hp); err != nil {
				return nil, fmt.Errorf("unmarshal host paths: %w", err)
			}
			config.HostPaths = &hp
		}
		if hostFilesystemJSON.Valid && hostFilesystemJSON.String != "" {
			var hfs types.HostFilesystemConfig
			if err := json.Unmarshal([]byte(hostFilesystemJSON.String), &hfs); err != nil {
				return nil, fmt.Errorf("unmarshal host filesystem: %w", err)
			}
			config.HostFilesystem = &hfs
		}
		if webhookInboundJSON.Valid && webhookInboundJSON.String != "" {
			var wh types.InboundWebhookConfig
			if err := json.Unmarshal([]byte(webhookInboundJSON.String), &wh); err != nil {
				return nil, fmt.Errorf("unmarshal webhook_inbound: %w", err)
			}
			config.WebhookInbound = &wh
		}
		config.SecurityJSON = securityConfigJSON
		config.CircuitBreakerJSON = circuitBreakerJSON
		config.HeartbeatJSON = heartbeatConfigJSON
		config.CompressionJSON = compressionJSON
		config.FeedbackHooksJSON = feedbackHooksJSON
		if restAPIEndpointsJSON.Valid {
			config.RESTAPIEndpointsJSON = restAPIEndpointsJSON.String
		}
		if canMessageJSON != "" && canMessageJSON != "[]" {
			if err := json.Unmarshal([]byte(canMessageJSON), &config.CanMessage); err != nil {
				return nil, fmt.Errorf("unmarshal can_message: %w", err)
			}
		}
		if httpAllowedHostsJSON != "" && httpAllowedHostsJSON != "[]" {
			if err := json.Unmarshal([]byte(httpAllowedHostsJSON), &config.HTTPAllowedHosts); err != nil {
				return nil, fmt.Errorf("unmarshal http_allowed_hosts: %w", err)
			}
		}
		if shellAllowedCommandsJSON != "" && shellAllowedCommandsJSON != "[]" {
			if err := json.Unmarshal([]byte(shellAllowedCommandsJSON), &config.ShellAllowedCommands); err != nil {
				return nil, fmt.Errorf("unmarshal shell_allowed_commands: %w", err)
			}
		}
		if obsidianVaultsJSON != "" && obsidianVaultsJSON != "[]" {
			if err := json.Unmarshal([]byte(obsidianVaultsJSON), &config.ObsidianVaults); err != nil {
				return nil, fmt.Errorf("unmarshal obsidian_vaults: %w", err)
			}
		}
		if nodeAffinityJSON != "" {
			if err := json.Unmarshal([]byte(nodeAffinityJSON), &config.NodeAffinity); err != nil {
				return nil, fmt.Errorf("unmarshal node_affinity: %w", err)
			}
		}
		if nodePreferenceJSON != "" {
			if err := json.Unmarshal([]byte(nodePreferenceJSON), &config.NodePreference); err != nil {
				return nil, fmt.Errorf("unmarshal node_preference: %w", err)
			}
		}
		config.IsGuide = isGuide
		config.DesiredState = types.DesiredState(desiredState)
		config.ActualState = types.AgentStatus(actualState)
		config.LastError = lastError
		config.SlackMode = slackMode
		if config.SlackMode == "" {
			config.SlackMode = types.SlackModeNone
		}
		config.DiscordMode = discordMode
		if config.DiscordMode == "" {
			config.DiscordMode = types.DiscordModeNone
		}
		config.DiscordAuthMode = discordAuthMode
		if config.DiscordAuthMode == "" {
			config.DiscordAuthMode = types.DiscordAuthModeOpen
		}
		config.WebUIEnabled = webuiEnabled
		if config.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		if config.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}
		agents = append(agents, config)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agents: %w", err)
	}
	return agents, nil
}

func (s *PostgresStore) UpdateAgent(ctx context.Context, config types.AgentConfig) error {
	channelsJSON, err := json.Marshal(config.Channels)
	if err != nil {
		return fmt.Errorf("marshal channels: %w", err)
	}
	limitsJSON, err := json.Marshal(config.Limits)
	if err != nil {
		return fmt.Errorf("marshal limits: %w", err)
	}
	metadataJSON, err := json.Marshal(config.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	config.ContextBudget = types.NormalizeContextBudget(config.ContextBudget)
	contextBudgetJSON, err := json.Marshal(config.ContextBudget)
	if err != nil {
		return fmt.Errorf("marshal context budget: %w", err)
	}
	config.Workers = types.NormalizeWorkerConfig(config.Workers)
	workerConfigJSON, err := json.Marshal(config.Workers)
	if err != nil {
		return fmt.Errorf("marshal worker config: %w", err)
	}
	toolGrantsJSON, err := json.Marshal(config.ToolGrants)
	if err != nil {
		return fmt.Errorf("marshal tool grants: %w", err)
	}
	capabilityGrantsJSON, err := json.Marshal(config.CapabilityGrants)
	if err != nil {
		return fmt.Errorf("marshal capability grants: %w", err)
	}
	canMessageJSON, err := json.Marshal(config.CanMessage)
	if err != nil {
		return fmt.Errorf("marshal can_message: %w", err)
	}
	httpAllowedHostsJSON, err := json.Marshal(config.HTTPAllowedHosts)
	if err != nil {
		return fmt.Errorf("marshal http_allowed_hosts: %w", err)
	}
	shellAllowedCommandsJSON, err := json.Marshal(config.ShellAllowedCommands)
	if err != nil {
		return fmt.Errorf("marshal shell_allowed_commands: %w", err)
	}
	obsidianVaultsJSON, err := json.Marshal(config.ObsidianVaults)
	if err != nil {
		return fmt.Errorf("marshal obsidian_vaults: %w", err)
	}

	hostPathsJSON := marshalHostPaths(config.HostPaths)
	hostFilesystemJSON := marshalHostFilesystem(config.HostFilesystem)
	webhookInboundJSON := marshalWebhookInbound(config.WebhookInbound)

	var restAPIEndpointsJSON *string
	if config.RESTAPIEndpointsJSON != "" {
		restAPIEndpointsJSON = &config.RESTAPIEndpointsJSON
	}

	desiredState := string(config.DesiredState)
	if desiredState == "" {
		desiredState = string(types.DesiredStateStopped)
	}
	actualState := string(config.ActualState)
	if actualState == "" {
		actualState = string(types.AgentStatusStopped)
	}

	slackMode := config.SlackMode
	if slackMode == "" {
		slackMode = types.SlackModeNone
	}
	discordMode := config.DiscordMode
	if discordMode == "" {
		discordMode = types.DiscordModeNone
	}
	webuiEnabled := config.WebUIEnabled

	securityConfig := config.SecurityJSON
	if securityConfig == "" {
		securityConfig = "{}"
	}

	circuitBreaker := config.CircuitBreakerJSON
	if circuitBreaker == "" {
		circuitBreaker = "{}"
	}

	nodeAffinityJSON := ""
	if len(config.NodeAffinity) > 0 {
		b, _ := json.Marshal(config.NodeAffinity)
		nodeAffinityJSON = string(b)
	}
	nodePreferenceJSON := ""
	if len(config.NodePreference) > 0 {
		b, _ := json.Marshal(config.NodePreference)
		nodePreferenceJSON = string(b)
	}

	result, err := s.db.ExecContext(ctx,
		`UPDATE agents SET
			name=?, description=?, system_prompt=?,
			soul_content=?, identity_content=?,
			model_provider=?, model_name=?, template=?,
			channels_json=?, limits_json=?, metadata_json=?,
			desired_state=?, actual_state=?, last_error=?,
			history_limit=?, memory_limit=?, auto_extract_memories=?, timestamp_messages=?,
			context_budget_json=?, worker_config=?,
			slack_mode=?, slack_channel=?, discord_mode=?, discord_channel_id=?, discord_auth_mode=?, webui_enabled=?,
			model_slots=?, routing_config=?,
			tool_grants=?, capability_grants=?, host_paths=?, host_filesystem=?, webhook_inbound=?,
			rest_api_endpoints=?,
			security_config=?, circuit_breaker=?, heartbeat_config=?,
			compression_json=?, feedback_hooks_json=?,
			can_message_json=?, team_id=?,
			is_guide=?,
			http_allowed_hosts=?, shell_allowed_commands=?,
			obsidian_vaults=?,
			attachment_max_size_mb=?,
			max_memories=?, memory_extraction_interval=?, memory_max_extractions_per_run=?,
			memory_duplicate_threshold=?, memory_similar_threshold=?,
			node_affinity=?, node_preference=?,
			updated_at=?
		 WHERE id=?`,
		config.Name, config.Description, config.SystemPrompt,
		config.SoulContent, config.IdentityContent,
		config.ModelConfig.Provider, config.ModelConfig.Model,
		config.Template,
		string(channelsJSON), string(limitsJSON), string(metadataJSON),
		desiredState, actualState, config.LastError,
		config.HistoryLimit, config.MemoryLimit, config.AutoExtractMemories, config.TimestampMessages,
		string(contextBudgetJSON), string(workerConfigJSON),
		slackMode, config.SlackChannel, discordMode, config.DiscordChannelID, config.DiscordAuthMode, webuiEnabled,
		config.ModelSlotsJSON, config.RoutingConfigJSON,
		string(toolGrantsJSON), string(capabilityGrantsJSON), hostPathsJSON, hostFilesystemJSON, webhookInboundJSON,
		restAPIEndpointsJSON,
		securityConfig, circuitBreaker, config.HeartbeatJSON,
		config.CompressionJSON, config.FeedbackHooksJSON,
		string(canMessageJSON), config.TeamID,
		config.IsGuide,
		string(httpAllowedHostsJSON), string(shellAllowedCommandsJSON),
		string(obsidianVaultsJSON),
		config.AttachmentMaxSizeMB,
		config.MaxMemories, config.MemoryExtractionInterval, config.MemoryMaxExtractionsPerRun,
		config.MemoryDuplicateThreshold, config.MemorySimilarThreshold,
		nodeAffinityJSON, nodePreferenceJSON,
		config.UpdatedAt.UTC().Format(time.RFC3339),
		config.ID,
	)
	if err != nil {
		return fmt.Errorf("update agent: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("agent %s: %w", config.ID, types.ErrNotFound)
	}
	return nil
}

func (s *PostgresStore) DeleteAgent(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("agent %s: %w", id, types.ErrNotFound)
	}
	return nil
}

// ---------- Audit ----------

func (s *PostgresStore) InsertAuditEntry(ctx context.Context, entry types.AuditEntry) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = timeutil.NowUTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (agent_id, event_type, action, resource, decision, details, risk_level, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.AgentID, string(entry.EventType), entry.Action,
		entry.Resource, entry.Decision, entry.Details, entry.RiskLevel, entry.Timestamp.UTC().Format(dbTimeFmt),
	)
	if err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}
	return nil
}

func (s *PostgresStore) InsertAuditEntries(ctx context.Context, entries []types.AuditEntry) error {
	if len(entries) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO audit_log (agent_id, event_type, action, resource, decision, details, risk_level, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, entry := range entries {
		if entry.Timestamp.IsZero() {
			entry.Timestamp = timeutil.NowUTC()
		}
		if _, err := stmt.ExecContext(ctx,
			entry.AgentID, string(entry.EventType), entry.Action,
			entry.Resource, entry.Decision, entry.Details, entry.RiskLevel, entry.Timestamp.UTC().Format(dbTimeFmt),
		); err != nil {
			return fmt.Errorf("insert audit entry: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListAuditEntries(ctx context.Context, filter audit.Filter) ([]types.AuditEntry, error) {
	query := `SELECT id, agent_id, event_type, action, resource, decision, details, risk_level, created_at
	          FROM audit_log`
	var conditions []string
	var args []any

	if filter.ID != "" {
		conditions = append(conditions, "id = ?")
		args = append(args, filter.ID)
	}
	if filter.AgentID != "" {
		conditions = append(conditions, "agent_id = ?")
		args = append(args, filter.AgentID)
	}
	if filter.EventType != "" {
		conditions = append(conditions, "event_type = ?")
		args = append(args, string(filter.EventType))
	}
	if filter.Decision != "" {
		conditions = append(conditions, "decision = ?")
		args = append(args, filter.Decision)
	}
	if filter.RiskLevel != "" {
		conditions = append(conditions, "risk_level = ?")
		args = append(args, filter.RiskLevel)
	}
	if filter.StartTime != nil {
		conditions = append(conditions, "created_at >= ?")
		args = append(args, filter.StartTime.UTC().Format(dbTimeFmt))
	}
	if filter.EndTime != nil {
		conditions = append(conditions, "created_at <= ?")
		args = append(args, filter.EndTime.UTC().Format(dbTimeFmt))
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at DESC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	if filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query audit entries: %w", err)
	}
	defer rows.Close()

	var entries []types.AuditEntry
	for rows.Next() {
		var (
			entry     types.AuditEntry
			id        int64
			createdAt string
		)
		if err := rows.Scan(
			&id, &entry.AgentID, &entry.EventType, &entry.Action,
			&entry.Resource, &entry.Decision, &entry.Details, &entry.RiskLevel, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		entry.ID = strconv.FormatInt(id, 10)
		if entry.Timestamp, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit entries: %w", err)
	}
	return entries, nil
}

// ---------- Permission Overrides ----------

func (s *PostgresStore) ListOverrides(ctx context.Context, agentID string) ([]permissions.Override, error) {
	return listOverridesQ(ctx, s.db, agentID)
}

// listOverridesQ fetches permission overrides using the provided pgQueryable (db or tx).
func listOverridesQ(ctx context.Context, q pgQueryable, agentID string) ([]permissions.Override, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT agent_id, tool, action, resource, grant_access
		 FROM permission_overrides WHERE agent_id = ?`, agentID)
	if err != nil {
		return nil, fmt.Errorf("query overrides: %w", err)
	}
	defer rows.Close()

	var overrides []permissions.Override
	for rows.Next() {
		var o permissions.Override
		var grant bool
		if err := rows.Scan(&o.AgentID, &o.Capability.Tool, &o.Capability.Action, &o.Capability.Resource, &grant); err != nil {
			return nil, fmt.Errorf("scan override: %w", err)
		}
		o.Grant = grant
		overrides = append(overrides, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate overrides: %w", err)
	}
	return overrides, nil
}

// GetAgentWithOverrides returns both the agent config and its permission
// overrides fetched inside a single read-only transaction.
func (s *PostgresStore) GetAgentWithOverrides(ctx context.Context, agentID string) (*types.AgentConfig, []permissions.Override, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	agent, err := getAgentQ(ctx, tx, agentID)
	if err != nil {
		return nil, nil, err
	}

	overrides, err := listOverridesQ(ctx, tx, agentID)
	if err != nil {
		return nil, nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit tx: %w", err)
	}

	return agent, overrides, nil
}

func (s *PostgresStore) AddOverride(ctx context.Context, override permissions.Override) error {
	// Check if the override already exists.
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM permission_overrides
		 WHERE agent_id = ? AND tool = ? AND action = ? AND resource = ?)`,
		override.AgentID, override.Capability.Tool, override.Capability.Action, override.Capability.Resource,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check existing override: %w", err)
	}

	if exists {
		_, err = s.db.ExecContext(ctx,
			`UPDATE permission_overrides SET grant_access = ?
			 WHERE agent_id = ? AND tool = ? AND action = ? AND resource = ?`,
			override.Grant,
			override.AgentID, override.Capability.Tool, override.Capability.Action, override.Capability.Resource,
		)
		if err != nil {
			return fmt.Errorf("update override: %w", err)
		}
	} else {
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO permission_overrides (agent_id, tool, action, resource, grant_access)
			 VALUES (?, ?, ?, ?, ?)`,
			override.AgentID, override.Capability.Tool, override.Capability.Action, override.Capability.Resource,
			override.Grant,
		)
		if err != nil {
			return fmt.Errorf("insert override: %w", err)
		}
	}
	return nil
}

func (s *PostgresStore) RemoveOverride(ctx context.Context, agentID string, cap types.Capability) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM permission_overrides
		 WHERE agent_id = ? AND tool = ? AND action = ? AND resource = ?`,
		agentID, cap.Tool, cap.Action, cap.Resource,
	)
	if err != nil {
		return fmt.Errorf("delete override: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("override for agent %s: %w", agentID, types.ErrNotFound)
	}
	return nil
}

func (s *PostgresStore) RemoveAllOverrides(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM permission_overrides WHERE agent_id = ?`, agentID)
	if err != nil {
		return fmt.Errorf("remove all overrides: %w", err)
	}
	return nil
}

// ---------- Usage ----------

func (s *PostgresStore) InsertUsageRecord(ctx context.Context, agentID string, tokensIn, tokensOut int64, cost float64,
	model, modelSlot, routedBy, provider, parentAgentID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_records (agent_id, tokens_in, tokens_out, cost_usd, model, model_slot, routed_by, provider, parent_agent_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, tokensIn, tokensOut, cost, model, modelSlot, routedBy, provider, parentAgentID,
	)
	if err != nil {
		return fmt.Errorf("insert usage record: %w", err)
	}
	return nil
}

// InsertUsageRecordDetailed writes usage and provenance metadata.
func (s *PostgresStore) InsertUsageRecordDetailed(
	ctx context.Context,
	agentID string,
	tokensIn, tokensOut int64,
	cost float64,
	model, modelSlot, routedBy, provider, parentAgentID string,
	costSource, usageSource string,
	usageComplete bool,
	pricingVersion string,
	category string,
) error {
	complete := 0
	if usageComplete {
		complete = 1
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage_records (
		    agent_id, tokens_in, tokens_out, cost_usd, model, model_slot, routed_by, provider, parent_agent_id,
		    cost_source, usage_source, usage_complete, pricing_version, category
		  ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, tokensIn, tokensOut, cost, model, modelSlot, routedBy, provider, parentAgentID,
		costSource, usageSource, complete, pricingVersion, category,
	)
	if err != nil {
		return fmt.Errorf("insert detailed usage record: %w", err)
	}
	return nil
}

// LookupModelPrice resolves active pricing for provider/model at a point in time.
// It tries exact model_pattern first, then longest-prefix match.
func (s *PostgresStore) LookupModelPrice(ctx context.Context, provider, model string, at time.Time) (float64, float64, string, bool, error) {
	ts := at.UTC().Format(dbTimeFmt)

	var inputPerM, outputPerM float64
	var version string

	// Exact match.
	err := s.db.QueryRowContext(ctx,
		`SELECT input_per_m, output_per_m, source_version
		   FROM pricing_catalog
		  WHERE provider = ?
		    AND model_pattern = ?
		    AND effective_from <= ?
		    AND (effective_to IS NULL OR effective_to > ?)
		  ORDER BY effective_from DESC
		  LIMIT 1`,
		provider, model, ts, ts,
	).Scan(&inputPerM, &outputPerM, &version)
	if err == nil {
		return inputPerM, outputPerM, version, true, nil
	}
	if err != sql.ErrNoRows {
		return 0, 0, "", false, fmt.Errorf("lookup exact model price: %w", err)
	}

	// Longest-prefix match.
	err = s.db.QueryRowContext(ctx,
		`SELECT input_per_m, output_per_m, source_version
		   FROM pricing_catalog
		  WHERE provider = ?
		    AND ? LIKE model_pattern || '%'
		    AND effective_from <= ?
		    AND (effective_to IS NULL OR effective_to > ?)
		  ORDER BY LENGTH(model_pattern) DESC, effective_from DESC
		  LIMIT 1`,
		provider, model, ts, ts,
	).Scan(&inputPerM, &outputPerM, &version)
	if err == nil {
		return inputPerM, outputPerM, version, true, nil
	}
	if err != sql.ErrNoRows {
		return 0, 0, "", false, fmt.Errorf("lookup prefix model price: %w", err)
	}

	return 0, 0, "", false, nil
}

func (s *PostgresStore) AggregateUsage(ctx context.Context, agentID string, period string) (*spending.Summary, error) {
	query := `SELECT COALESCE(SUM(tokens_in + tokens_out), 0),
	                 COALESCE(SUM(cost_usd), 0),
	                 COUNT(*)
	          FROM usage_records WHERE agent_id = ?`
	args := []any{agentID}

	var err error
	query, args, err = appendPeriodFilter(query, args, period)
	if err != nil {
		return nil, err
	}

	var summary spending.Summary
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&summary.TotalTokens, &summary.TotalCost, &summary.RequestCount,
	); err != nil {
		return nil, fmt.Errorf("aggregate usage: %w", err)
	}
	summary.AgentID = agentID
	summary.Period = period
	return &summary, nil
}

func (s *PostgresStore) AggregateSlotUsage(ctx context.Context, agentID, period string) ([]spending.SlotUsageSummary, error) {
	query := `SELECT model_slot, COALESCE(provider, ''), model,
	                 COALESCE(SUM(tokens_in + tokens_out), 0),
	                 COALESCE(SUM(cost_usd), 0),
	                 COUNT(*)
	          FROM usage_records WHERE agent_id = ?`
	args := []any{agentID}

	var err error
	query, args, err = appendPeriodFilter(query, args, period)
	if err != nil {
		return nil, err
	}

	query += " GROUP BY model_slot, model, provider ORDER BY SUM(cost_usd) DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregate slot usage: %w", err)
	}
	defer rows.Close()

	var results []spending.SlotUsageSummary
	for rows.Next() {
		var r spending.SlotUsageSummary
		if err := rows.Scan(&r.SlotName, &r.Provider, &r.Model, &r.TotalTokens, &r.TotalCost, &r.RequestCount); err != nil {
			return nil, fmt.Errorf("scan slot usage: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *PostgresStore) AggregateProviderUsage(ctx context.Context, agentID, period string) ([]spending.ProviderUsageSummary, error) {
	query := `SELECT COALESCE(provider, ''),
	                 COALESCE(SUM(tokens_in + tokens_out), 0),
	                 COALESCE(SUM(cost_usd), 0),
	                 COUNT(*)
	          FROM usage_records WHERE agent_id = ?`
	args := []any{agentID}

	var err error
	query, args, err = appendPeriodFilter(query, args, period)
	if err != nil {
		return nil, err
	}

	query += " GROUP BY provider ORDER BY SUM(cost_usd) DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregate provider usage: %w", err)
	}
	defer rows.Close()

	var results []spending.ProviderUsageSummary
	for rows.Next() {
		var r spending.ProviderUsageSummary
		if err := rows.Scan(&r.Provider, &r.TotalTokens, &r.TotalCost, &r.RequestCount); err != nil {
			return nil, fmt.Errorf("scan provider usage: %w", err)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// AggregateDailyUsage returns per-day aggregated usage for the last N days.
// If agentID is empty, returns totals across all agents.
func (s *PostgresStore) AggregateDailyUsage(ctx context.Context, agentID string, days int) ([]spending.DailyUsage, error) {
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format(dbTimeFmt)

	var query string
	var args []any

	if agentID == "" {
		query = `SELECT DATE(created_at) AS day,
		                COALESCE(SUM(tokens_in + tokens_out), 0),
		                COALESCE(SUM(cost_usd), 0),
		                COUNT(*)
		         FROM usage_records
		         WHERE created_at >= ?
		         GROUP BY DATE(created_at)
		         ORDER BY day ASC`
		args = []any{cutoff}
	} else {
		query = `SELECT DATE(created_at) AS day,
		                COALESCE(SUM(tokens_in + tokens_out), 0),
		                COALESCE(SUM(cost_usd), 0),
		                COUNT(*)
		         FROM usage_records
		         WHERE agent_id = ? AND created_at >= ?
		         GROUP BY DATE(created_at)
		         ORDER BY day ASC`
		args = []any{agentID, cutoff}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregate daily usage: %w", err)
	}
	defer rows.Close()

	var results []spending.DailyUsage
	for rows.Next() {
		var d spending.DailyUsage
		if err := rows.Scan(&d.Date, &d.TotalTokens, &d.TotalCost, &d.RequestCount); err != nil {
			return nil, fmt.Errorf("scan daily usage: %w", err)
		}
		results = append(results, d)
	}
	return results, rows.Err()
}

// appendPeriodFilter adds a date filter clause to a query based on the period.
func appendPeriodFilter(query string, args []any, period string) (string, []any, error) {
	now := time.Now()
	switch period {
	case "day":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		query += " AND created_at >= ?"
		args = append(args, start.UTC().Format(dbTimeFmt))
	case "month":
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		query += " AND created_at >= ?"
		args = append(args, start.UTC().Format(dbTimeFmt))
	case "all":
		// no additional filter
	default:
		return "", nil, fmt.Errorf("unknown period: %s", period)
	}
	return query, args, nil
}

// ---------- Spending Limits ----------

func (s *PostgresStore) GetSpendingLimit(ctx context.Context, agentID string) (*types.SpendingLimits, error) {
	var limit types.SpendingLimits
	err := s.db.QueryRowContext(ctx,
		`SELECT max_tokens_per_day, max_tokens_per_month, max_spend_per_day, max_spend_per_month
		 FROM spending_limits WHERE agent_id = ?`, agentID,
	).Scan(&limit.MaxTokensPerDay, &limit.MaxTokensPerMonth, &limit.MaxSpendPerDay, &limit.MaxSpendPerMonth)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("spending limit for %s: %w", agentID, types.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get spending limit: %w", err)
	}
	return &limit, nil
}

func (s *PostgresStore) SetSpendingLimit(ctx context.Context, agentID string, limit types.SpendingLimits) error {
	nowUTC := timeutil.NowUTC().Format(dbTimeFmt)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO spending_limits (agent_id, max_tokens_per_day, max_tokens_per_month, max_spend_per_day, max_spend_per_month, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(agent_id) DO UPDATE SET
		   max_tokens_per_day = excluded.max_tokens_per_day,
		   max_tokens_per_month = excluded.max_tokens_per_month,
		   max_spend_per_day = excluded.max_spend_per_day,
		   max_spend_per_month = excluded.max_spend_per_month,
		   updated_at = excluded.updated_at`,
		agentID, limit.MaxTokensPerDay, limit.MaxTokensPerMonth, limit.MaxSpendPerDay, limit.MaxSpendPerMonth, nowUTC,
	)
	if err != nil {
		return fmt.Errorf("set spending limit: %w", err)
	}
	return nil
}

// ---------- Agent State ----------

func (s *PostgresStore) SetDesiredState(ctx context.Context, agentID string, state types.DesiredState) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE agents SET desired_state=?, updated_at=? WHERE id=?`,
		string(state), time.Now().UTC().Format(time.RFC3339), agentID,
	)
	if err != nil {
		return fmt.Errorf("set desired state: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("agent %s: %w", agentID, types.ErrNotFound)
	}
	return nil
}

func (s *PostgresStore) SetActualState(ctx context.Context, agentID string, state types.AgentStatus, lastError string) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE agents SET actual_state=?, last_error=?, updated_at=? WHERE id=?`,
		string(state), lastError, time.Now().UTC().Format(time.RFC3339), agentID,
	)
	if err != nil {
		return fmt.Errorf("set actual state: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("agent %s: %w", agentID, types.ErrNotFound)
	}
	return nil
}

// ---------- Security Events ----------

func (s *PostgresStore) InsertSecurityEvent(ctx context.Context, event types.SecurityEvent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO security_events (id, agent_id, event_type, severity, details, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		event.ID, event.AgentID, event.EventType, event.Severity, event.Details,
		event.CreatedAt.UTC().Format(dbTimeFmt),
	)
	if err != nil {
		return fmt.Errorf("insert security event: %w", err)
	}
	return nil
}

func (s *PostgresStore) QuerySecurityEvents(ctx context.Context, agentID string, limit int) ([]types.SecurityEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, event_type, severity, details, created_at
		 FROM security_events WHERE agent_id = ?
		 ORDER BY created_at DESC LIMIT ?`, agentID, limit)
	if err != nil {
		return nil, fmt.Errorf("query security events: %w", err)
	}
	defer rows.Close()

	var events []types.SecurityEvent
	for rows.Next() {
		var e types.SecurityEvent
		var createdAt string
		if err := rows.Scan(&e.ID, &e.AgentID, &e.EventType, &e.Severity, &e.Details, &createdAt); err != nil {
			return nil, fmt.Errorf("scan security event: %w", err)
		}
		if e.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ---------- System State ----------

func (s *PostgresStore) GetSystemState(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM system_state WHERE key = ?`, key,
	).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get system state: %w", err)
	}
	return value, nil
}

func (s *PostgresStore) SetSystemState(ctx context.Context, key, value string) error {
	nowUTC := timeutil.NowUTC().Format(dbTimeFmt)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO system_state (key, value, updated_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET
		   value = excluded.value,
		   updated_at = excluded.updated_at`,
		key, value, nowUTC,
	)
	if err != nil {
		return fmt.Errorf("set system state: %w", err)
	}
	return nil
}

// ---------- Alert Acknowledgments ----------

func (s *PostgresStore) AcknowledgeAlert(ctx context.Context, sourceType, sourceID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO alert_acknowledgments (source_type, source_id)
		 VALUES (?, ?)`,
		sourceType, sourceID,
	)
	if err != nil {
		return fmt.Errorf("acknowledge alert: %w", err)
	}
	return nil
}

func (s *PostgresStore) IsAlertAcknowledged(ctx context.Context, sourceType, sourceID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM alert_acknowledgments WHERE source_type = ? AND source_id = ?)`,
		sourceType, sourceID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check alert acknowledged: %w", err)
	}
	return exists, nil
}

func (s *PostgresStore) ListAcknowledgedAlerts(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT source_type, source_id, acknowledged_at FROM alert_acknowledgments`)
	if err != nil {
		return nil, fmt.Errorf("list acknowledged alerts: %w", err)
	}
	defer rows.Close()

	result := make(map[string]time.Time)
	for rows.Next() {
		var sourceType, sourceID, ackedAt string
		if err := rows.Scan(&sourceType, &sourceID, &ackedAt); err != nil {
			return nil, fmt.Errorf("scan acknowledged alert: %w", err)
		}
		t, err := parseTime(ackedAt)
		if err != nil {
			return nil, fmt.Errorf("parse acknowledged_at: %w", err)
		}
		result[sourceType+":"+sourceID] = t
	}
	return result, rows.Err()
}

// ---------- Cross-Agent Security Events ----------

func (s *PostgresStore) QueryAllSecurityEvents(ctx context.Context, severity string, limit int) ([]types.SecurityEvent, error) {
	if limit <= 0 {
		limit = 50
	}

	var query string
	var args []any

	if severity != "" {
		query = `SELECT id, agent_id, event_type, severity, details, created_at
		         FROM security_events WHERE severity = ?
		         ORDER BY created_at DESC LIMIT ?`
		args = []any{severity, limit}
	} else {
		query = `SELECT id, agent_id, event_type, severity, details, created_at
		         FROM security_events
		         ORDER BY created_at DESC LIMIT ?`
		args = []any{limit}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query all security events: %w", err)
	}
	defer rows.Close()

	var events []types.SecurityEvent
	for rows.Next() {
		var e types.SecurityEvent
		var createdAt string
		if err := rows.Scan(&e.ID, &e.AgentID, &e.EventType, &e.Severity, &e.Details, &createdAt); err != nil {
			return nil, fmt.Errorf("scan security event: %w", err)
		}
		if e.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ---------- Lifecycle ----------

// ---------- Schedules ----------

func (s *PostgresStore) CreateSchedule(ctx context.Context, sched types.Schedule) error {
	enabled := 0
	if sched.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO schedules (id, agent_id, name, cron_expr, message, channel, type, enabled, timezone, created_at, updated_at, last_run_at, next_run_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sched.ID, sched.AgentID, sched.Name, sched.CronExpr,
		sched.Message, sched.Channel, sched.Type, enabled, sched.Timezone,
		sched.CreatedAt.UTC().Format(time.RFC3339),
		sched.UpdatedAt.UTC().Format(time.RFC3339),
		formatNullableTime(sched.LastRunAt),
		formatNullableTime(sched.NextRunAt),
	)
	if err != nil {
		return fmt.Errorf("insert schedule: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetSchedule(ctx context.Context, id string) (*types.Schedule, error) {
	var sched types.Schedule
	var enabled int
	var createdAt, updatedAt string
	var lastRunAt, nextRunAt sql.NullString

	err := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, name, cron_expr, message, channel, type, enabled, timezone,
		        created_at, updated_at, last_run_at, next_run_at
		 FROM schedules WHERE id = ?`, id,
	).Scan(
		&sched.ID, &sched.AgentID, &sched.Name, &sched.CronExpr,
		&sched.Message, &sched.Channel, &sched.Type, &enabled, &sched.Timezone,
		&createdAt, &updatedAt, &lastRunAt, &nextRunAt,
	)
	if err == sql.ErrNoRows {
		return nil, types.ErrScheduleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query schedule: %w", err)
	}
	sched.Enabled = enabled != 0
	var parseErr error
	if sched.CreatedAt, parseErr = parseTime(createdAt); parseErr != nil {
		return nil, fmt.Errorf("parse created_at: %w", parseErr)
	}
	if sched.UpdatedAt, parseErr = parseTime(updatedAt); parseErr != nil {
		return nil, fmt.Errorf("parse updated_at: %w", parseErr)
	}
	sched.LastRunAt = parseNullableTime(lastRunAt)
	sched.NextRunAt = parseNullableTime(nextRunAt)
	return &sched, nil
}

func (s *PostgresStore) UpdateSchedule(ctx context.Context, sched types.Schedule) error {
	enabled := 0
	if sched.Enabled {
		enabled = 1
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE schedules SET
			name=?, cron_expr=?, message=?, channel=?, type=?, enabled=?, timezone=?,
			updated_at=?, last_run_at=?, next_run_at=?
		 WHERE id=?`,
		sched.Name, sched.CronExpr, sched.Message, sched.Channel,
		sched.Type, enabled, sched.Timezone,
		sched.UpdatedAt.UTC().Format(time.RFC3339),
		formatNullableTime(sched.LastRunAt),
		formatNullableTime(sched.NextRunAt),
		sched.ID,
	)
	if err != nil {
		return fmt.Errorf("update schedule: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrScheduleNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteSchedule(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrScheduleNotFound
	}
	return nil
}

func (s *PostgresStore) ListSchedules(ctx context.Context, agentID string) ([]types.Schedule, error) {
	return s.querySchedules(ctx,
		`SELECT id, agent_id, name, cron_expr, message, channel, type, enabled, timezone,
		        created_at, updated_at, last_run_at, next_run_at
		 FROM schedules WHERE agent_id = ? ORDER BY created_at`, agentID)
}

func (s *PostgresStore) ListSchedulesByType(ctx context.Context, agentID string, schedType string) ([]types.Schedule, error) {
	return s.querySchedules(ctx,
		`SELECT id, agent_id, name, cron_expr, message, channel, type, enabled, timezone,
		        created_at, updated_at, last_run_at, next_run_at
		 FROM schedules WHERE agent_id = ? AND type = ? ORDER BY created_at`, agentID, schedType)
}

func (s *PostgresStore) ListAllEnabledSchedules(ctx context.Context) ([]types.Schedule, error) {
	return s.querySchedules(ctx,
		`SELECT id, agent_id, name, cron_expr, message, channel, type, enabled, timezone,
		        created_at, updated_at, last_run_at, next_run_at
		 FROM schedules WHERE enabled = 1 ORDER BY created_at`)
}

func (s *PostgresStore) DeleteSchedulesByAgent(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE agent_id = ?`, agentID)
	if err != nil {
		return fmt.Errorf("delete schedules by agent: %w", err)
	}
	return nil
}

// querySchedules is a shared helper for listing schedules with different filters.
func (s *PostgresStore) querySchedules(ctx context.Context, query string, args ...any) ([]types.Schedule, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query schedules: %w", err)
	}
	defer rows.Close()

	var schedules []types.Schedule
	for rows.Next() {
		var sched types.Schedule
		var enabled int
		var createdAt, updatedAt string
		var lastRunAt, nextRunAt sql.NullString

		if err := rows.Scan(
			&sched.ID, &sched.AgentID, &sched.Name, &sched.CronExpr,
			&sched.Message, &sched.Channel, &sched.Type, &enabled, &sched.Timezone,
			&createdAt, &updatedAt, &lastRunAt, &nextRunAt,
		); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		sched.Enabled = enabled != 0
		var parseErr error
		if sched.CreatedAt, parseErr = parseTime(createdAt); parseErr != nil {
			return nil, fmt.Errorf("parse created_at: %w", parseErr)
		}
		if sched.UpdatedAt, parseErr = parseTime(updatedAt); parseErr != nil {
			return nil, fmt.Errorf("parse updated_at: %w", parseErr)
		}
		sched.LastRunAt = parseNullableTime(lastRunAt)
		sched.NextRunAt = parseNullableTime(nextRunAt)
		schedules = append(schedules, sched)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schedules: %w", err)
	}
	return schedules, nil
}

// ---------- Skill Grants ----------

func (s *PostgresStore) GrantSkill(ctx context.Context, grant types.SkillGrant) error {
	if grant.ID == "" {
		grant.ID = ulid.Make().String()
	}
	if grant.GrantedBy == "" {
		grant.GrantedBy = "dashboard"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO skill_grants (id, agent_id, skill_name, granted_at, granted_by)
		 VALUES (?, ?, ?, ?, ?)`,
		grant.ID, grant.AgentID, grant.SkillName,
		grant.GrantedAt.UTC().Format(time.RFC3339), grant.GrantedBy,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") || strings.Contains(err.Error(), "unique constraint") || strings.Contains(err.Error(), "23505") {
			return types.ErrSkillAlreadyGranted
		}
		return fmt.Errorf("insert skill grant: %w", err)
	}
	return nil
}

func (s *PostgresStore) RevokeSkill(ctx context.Context, agentID, skillName string) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM skill_grants WHERE agent_id = ? AND skill_name = ?`,
		agentID, skillName,
	)
	if err != nil {
		return fmt.Errorf("delete skill grant: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListSkillGrants(ctx context.Context, agentID string) ([]types.SkillGrant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, skill_name, granted_at, granted_by
		 FROM skill_grants WHERE agent_id = ? ORDER BY granted_at`, agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("query skill grants: %w", err)
	}
	defer rows.Close()

	var grants []types.SkillGrant
	for rows.Next() {
		var g types.SkillGrant
		var grantedAt string
		if err := rows.Scan(&g.ID, &g.AgentID, &g.SkillName, &grantedAt, &g.GrantedBy); err != nil {
			return nil, fmt.Errorf("scan skill grant: %w", err)
		}
		if g.GrantedAt, err = parseTime(grantedAt); err != nil {
			return nil, fmt.Errorf("parse granted_at: %w", err)
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate skill grants: %w", err)
	}
	return grants, nil
}

func (s *PostgresStore) DeleteSkillGrantsByAgent(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM skill_grants WHERE agent_id = ?`, agentID)
	if err != nil {
		return fmt.Errorf("delete skill grants by agent: %w", err)
	}
	return nil
}

// ---------- Team CRUD ----------

func (s *PostgresStore) CreateTeam(ctx context.Context, team types.Team) error {
	if err := s.ensureAgentExists(ctx, team.LeaderID); err != nil {
		if errors.Is(err, types.ErrNotFound) {
			return fmt.Errorf("leader %s: %w", team.LeaderID, types.ErrNotFound)
		}
		return fmt.Errorf("validate leader: %w", err)
	}
	for _, memberID := range team.MemberIDs {
		if err := s.ensureAgentExists(ctx, memberID); err != nil {
			if errors.Is(err, types.ErrNotFound) {
				return fmt.Errorf("member %s: %w", memberID, types.ErrNotFound)
			}
			return fmt.Errorf("validate member %s: %w", memberID, err)
		}
	}

	memberIDsJSON, err := json.Marshal(team.MemberIDs)
	if err != nil {
		return fmt.Errorf("marshal member_ids: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO teams (id, name, description, leader_id, member_ids_json, communication, active, shared_context, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		team.ID, team.Name, team.Description, team.LeaderID,
		string(memberIDsJSON), string(team.Communication), team.Active, team.SharedContext,
		team.CreatedAt.UTC().Format(time.RFC3339),
		team.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert team: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetTeam(ctx context.Context, id string) (*types.Team, error) {
	var (
		team          types.Team
		memberIDsJSON string
		createdAt     string
		updatedAt     string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, leader_id, member_ids_json, communication, active, shared_context, created_at, updated_at
		 FROM teams WHERE id = ?`, id,
	).Scan(
		&team.ID, &team.Name, &team.Description, &team.LeaderID,
		&memberIDsJSON, &team.Communication, &team.Active, &team.SharedContext,
		&createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, types.ErrTeamNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query team: %w", err)
	}
	if err := json.Unmarshal([]byte(memberIDsJSON), &team.MemberIDs); err != nil {
		return nil, fmt.Errorf("unmarshal member_ids: %w", err)
	}
	if team.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	if team.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return &team, nil
}

func (s *PostgresStore) UpdateTeam(ctx context.Context, team types.Team) error {
	if err := s.ensureAgentExists(ctx, team.LeaderID); err != nil {
		if errors.Is(err, types.ErrNotFound) {
			return fmt.Errorf("leader %s: %w", team.LeaderID, types.ErrNotFound)
		}
		return fmt.Errorf("validate leader: %w", err)
	}
	for _, memberID := range team.MemberIDs {
		if err := s.ensureAgentExists(ctx, memberID); err != nil {
			if errors.Is(err, types.ErrNotFound) {
				return fmt.Errorf("member %s: %w", memberID, types.ErrNotFound)
			}
			return fmt.Errorf("validate member %s: %w", memberID, err)
		}
	}

	memberIDsJSON, err := json.Marshal(team.MemberIDs)
	if err != nil {
		return fmt.Errorf("marshal member_ids: %w", err)
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE teams SET name=?, description=?, leader_id=?, member_ids_json=?, communication=?, active=?, shared_context=?, updated_at=?
		 WHERE id=?`,
		team.Name, team.Description, team.LeaderID,
		string(memberIDsJSON), string(team.Communication), team.Active, team.SharedContext,
		team.UpdatedAt.UTC().Format(time.RFC3339),
		team.ID,
	)
	if err != nil {
		return fmt.Errorf("update team: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrTeamNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteTeam(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var (
		memberIDsJSON string
		leaderID      string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT member_ids_json, leader_id FROM teams WHERE id = ?`, id,
	).Scan(&memberIDsJSON, &leaderID)
	if err == sql.ErrNoRows {
		return types.ErrTeamNotFound
	}
	if err != nil {
		return fmt.Errorf("query team members: %w", err)
	}

	var memberIDs []string
	if err := json.Unmarshal([]byte(memberIDsJSON), &memberIDs); err != nil {
		return fmt.Errorf("unmarshal member_ids: %w", err)
	}
	memberIDs = append(memberIDs, leaderID)
	seen := make(map[string]struct{}, len(memberIDs))
	for _, memberID := range memberIDs {
		if memberID == "" {
			continue
		}
		if _, ok := seen[memberID]; ok {
			continue
		}
		seen[memberID] = struct{}{}
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET team_id = '' WHERE id = ?`, memberID); err != nil {
			return fmt.Errorf("clear team_id for member %s: %w", memberID, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE agents SET team_id = '' WHERE team_id = ?`, id); err != nil {
		return fmt.Errorf("clear team_id by team id: %w", err)
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM teams WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete team: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrTeamNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListTeams(ctx context.Context) ([]types.Team, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, leader_id, member_ids_json, communication, active, shared_context, created_at, updated_at
		 FROM teams ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("query teams: %w", err)
	}
	defer rows.Close()

	var teams []types.Team
	for rows.Next() {
		var (
			team          types.Team
			memberIDsJSON string
			createdAt     string
			updatedAt     string
		)
		if err := rows.Scan(
			&team.ID, &team.Name, &team.Description, &team.LeaderID,
			&memberIDsJSON, &team.Communication, &team.Active, &team.SharedContext,
			&createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		if err := json.Unmarshal([]byte(memberIDsJSON), &team.MemberIDs); err != nil {
			return nil, fmt.Errorf("unmarshal member_ids: %w", err)
		}
		if team.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		if team.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, fmt.Errorf("parse updated_at: %w", err)
		}
		teams = append(teams, team)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate teams: %w", err)
	}
	return teams, nil
}

func (s *PostgresStore) GetTeamByAgent(ctx context.Context, agentID string) (*types.Team, error) {
	var teamID string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM teams
		 WHERE leader_id = ?
		    OR EXISTS (
		        SELECT 1 FROM json_each(teams.member_ids_json) WHERE value = ?
		    )
		 ORDER BY created_at LIMIT 1`,
		agentID, agentID,
	).Scan(&teamID)
	if err == sql.ErrNoRows {
		return nil, types.ErrTeamNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query team by agent: %w", err)
	}
	return s.GetTeam(ctx, teamID)
}

// ---------- Users / Sessions / Groups ----------

func (s *PostgresStore) CreateUser(ctx context.Context, user types.User) error {
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}
	lastLogin := formatNullableTime(user.LastLoginAt)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users
		    (id, username, password_hash, display_name, is_admin, is_active, created_at, last_login_at, force_password_change)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID, user.Username, user.PasswordHash, user.DisplayName,
		boolToInt(user.IsAdmin), boolToInt(user.IsActive),
		user.CreatedAt.UTC().Format(time.RFC3339), lastLogin, boolToInt(user.ForcePasswordChange),
	)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetUser(ctx context.Context, id string) (*types.User, error) {
	return s.queryUser(ctx, `SELECT id, username, password_hash, display_name, is_admin, is_active, created_at, last_login_at, force_password_change FROM users WHERE id = ?`, id)
}

func (s *PostgresStore) GetUserByUsername(ctx context.Context, username string) (*types.User, error) {
	return s.queryUser(ctx, `SELECT id, username, password_hash, display_name, is_admin, is_active, created_at, last_login_at, force_password_change FROM users WHERE username = ?`, username)
}

func (s *PostgresStore) ListUsers(ctx context.Context) ([]types.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, username, password_hash, display_name, is_admin, is_active, created_at, last_login_at, force_password_change
		 FROM users ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []types.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	return users, nil
}

func (s *PostgresStore) UpdateUser(ctx context.Context, user types.User) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE users
		    SET username=?, password_hash=?, display_name=?, is_admin=?, is_active=?, last_login_at=?, force_password_change=?
		  WHERE id=?`,
		user.Username, user.PasswordHash, user.DisplayName,
		boolToInt(user.IsAdmin), boolToInt(user.IsActive), formatNullableTime(user.LastLoginAt),
		boolToInt(user.ForcePasswordChange), user.ID,
	)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrUserNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteUser(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrUserNotFound
	}
	return nil
}

func (s *PostgresStore) CreateAgentGroup(ctx context.Context, g types.AgentGroup) error {
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_groups (id, name, description, created_at) VALUES (?, ?, ?, ?)`,
		g.ID, g.Name, g.Description, g.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetAgentGroup(ctx context.Context, id string) (*types.AgentGroup, error) {
	var (
		g         types.AgentGroup
		createdAt string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, created_at FROM agent_groups WHERE id = ?`, id,
	).Scan(&g.ID, &g.Name, &g.Description, &createdAt)
	if err == sql.ErrNoRows {
		return nil, types.ErrGroupNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get group: %w", err)
	}
	g.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	return &g, nil
}

func (s *PostgresStore) ListAgentGroups(ctx context.Context) ([]types.AgentGroup, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, created_at FROM agent_groups ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()

	var groups []types.AgentGroup
	for rows.Next() {
		var (
			g         types.AgentGroup
			createdAt string
		)
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &createdAt); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		g.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate groups: %w", err)
	}
	return groups, nil
}

func (s *PostgresStore) UpdateAgentGroup(ctx context.Context, g types.AgentGroup) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE agent_groups SET name = ?, description = ? WHERE id = ?`,
		g.Name, g.Description, g.ID,
	)
	if err != nil {
		return fmt.Errorf("update group: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrGroupNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteAgentGroup(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM agent_groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrGroupNotFound
	}
	return nil
}

func (s *PostgresStore) SetAgentGroupMember(ctx context.Context, agentID, groupID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_group_members (agent_id, group_id) VALUES (?, ?)
		 ON CONFLICT(agent_id, group_id) DO NOTHING`,
		agentID, groupID,
	)
	if err != nil {
		return fmt.Errorf("set group member: %w", err)
	}
	return nil
}

func (s *PostgresStore) RemoveAgentGroupMember(ctx context.Context, agentID, groupID string) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_group_members WHERE agent_id = ? AND group_id = ?`,
		agentID, groupID,
	)
	if err != nil {
		return fmt.Errorf("remove group member: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListAgentIDsByGroup(ctx context.Context, groupID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id FROM agent_group_members WHERE group_id = ? ORDER BY agent_id`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list group agents: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan group agent: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *PostgresStore) ListGroupIDsByAgent(ctx context.Context, agentID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT group_id FROM agent_group_members WHERE agent_id = ? ORDER BY group_id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("list agent groups: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan agent group: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *PostgresStore) SetUserGroupRole(ctx context.Context, ugr types.UserGroupRole) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_group_roles (user_id, group_id, role) VALUES (?, ?, ?)
		 ON CONFLICT(user_id, group_id) DO UPDATE SET role = excluded.role`,
		ugr.UserID, ugr.GroupID, ugr.Role,
	)
	if err != nil {
		return fmt.Errorf("set user group role: %w", err)
	}
	return nil
}

func (s *PostgresStore) DeleteUserGroupRole(ctx context.Context, userID, groupID string) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM user_group_roles WHERE user_id = ? AND group_id = ?`,
		userID, groupID,
	)
	if err != nil {
		return fmt.Errorf("delete user group role: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListUserGroupRoles(ctx context.Context, userID string) ([]types.UserGroupRole, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, group_id, role FROM user_group_roles WHERE user_id = ? ORDER BY group_id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user group roles: %w", err)
	}
	defer rows.Close()

	var roles []types.UserGroupRole
	for rows.Next() {
		var r types.UserGroupRole
		if err := rows.Scan(&r.UserID, &r.GroupID, &r.Role); err != nil {
			return nil, fmt.Errorf("scan user group role: %w", err)
		}
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

func (s *PostgresStore) CreateSession(ctx context.Context, sess types.UserSession) error {
	lastSeen := formatNullableTime(sess.LastSeenAt)
	revoked := formatNullableTime(sess.RevokedAt)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, user_id, created_at, expires_at, last_seen_at, ip_address, user_agent, revoked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.CreatedAt.UTC().Format(time.RFC3339Nano), sess.ExpiresAt.UTC().Format(time.RFC3339Nano),
		lastSeen, sess.IPAddress, sess.UserAgent, revoked,
	)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetSession(ctx context.Context, id string) (*types.UserSession, error) {
	var (
		sess      types.UserSession
		createdAt string
		expiresAt string
		lastSeen  sql.NullString
		revokedAt sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, created_at, expires_at, last_seen_at, ip_address, user_agent, revoked_at
		 FROM sessions WHERE id = ?`, id,
	).Scan(&sess.ID, &sess.UserID, &createdAt, &expiresAt, &lastSeen, &sess.IPAddress, &sess.UserAgent, &revokedAt)
	if err == sql.ErrNoRows {
		return nil, types.ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	sess.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	sess.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse expires_at: %w", err)
	}
	sess.LastSeenAt = parseNullableTime(lastSeen)
	sess.RevokedAt = parseNullableTime(revokedAt)
	return &sess, nil
}

func (s *PostgresStore) DeleteSession(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrSessionNotFound
	}
	return nil
}

func (s *PostgresStore) UpdateSessionLastSeen(ctx context.Context, id string, at time.Time) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE id = ?`,
		at.UTC().Format(time.RFC3339), id,
	)
	if err != nil {
		return fmt.Errorf("update session last_seen: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrSessionNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at <= ? OR revoked_at IS NOT NULL`,
		now.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (s *PostgresStore) DeleteSessionsByUserID(ctx context.Context, userID string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, fmt.Errorf("delete sessions by user: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

func (s *PostgresStore) EnforceSessionLimit(ctx context.Context, userID string, maxSessions int) (int64, error) {
	if maxSessions <= 0 {
		return 0, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id
		 FROM sessions
		 WHERE user_id = ? AND revoked_at IS NULL
		 ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return 0, fmt.Errorf("list sessions for limit: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("scan session id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate session ids: %w", err)
	}
	if len(ids) <= maxSessions {
		return 0, nil
	}

	var deleted int64
	for _, id := range ids[maxSessions:] {
		result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
		if err != nil {
			return deleted, fmt.Errorf("delete excess session %s: %w", id, err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return deleted, fmt.Errorf("rows affected: %w", err)
		}
		deleted += n
	}
	return deleted, nil
}

func (s *PostgresStore) queryUser(ctx context.Context, query string, arg any) (*types.User, error) {
	row := s.db.QueryRowContext(ctx, query, arg)
	return scanUser(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(rs rowScanner) (*types.User, error) {
	var (
		u           types.User
		createdAt   string
		lastLoginAt sql.NullString
		isAdminInt  int
		isActiveInt int
		forcePwdInt int
	)
	if err := rs.Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.DisplayName,
		&isAdminInt, &isActiveInt, &createdAt, &lastLoginAt, &forcePwdInt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, types.ErrUserNotFound
		}
		return nil, fmt.Errorf("scan user: %w", err)
	}
	var err error
	u.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at: %w", err)
	}
	u.LastLoginAt = parseNullableTime(lastLoginAt)
	u.IsAdmin = isAdminInt == 1
	u.IsActive = isActiveInt == 1
	u.ForcePasswordChange = forcePwdInt == 1
	return &u, nil
}

func (s *PostgresStore) ensureAgentExists(ctx context.Context, id string) error {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, id).Scan(&exists)
	if err == sql.ErrNoRows {
		return types.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("query agent %s: %w", id, err)
	}
	return nil
}

// ---------- Discord Authorization ----------

func (s *PostgresStore) CreateDiscordAuth(ctx context.Context, auth types.DiscordAuthorization) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO discord_authorizations
			(id, agent_id, discord_user_id, status, pairing_code, added_by, code_expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		auth.ID, auth.AgentID, auth.DiscordUserID,
		auth.Status, auth.PairingCode, auth.AddedBy,
		auth.CodeExpiresAt,
		auth.CreatedAt.UTC().Format(time.RFC3339),
		auth.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert discord auth: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetDiscordAuth(ctx context.Context, agentID, discordUserID string) (*types.DiscordAuthorization, error) {
	return scanDiscordAuthPG(s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, discord_user_id, status, pairing_code, added_by, code_expires_at, created_at, updated_at
		 FROM discord_authorizations WHERE agent_id = ? AND discord_user_id = ?`,
		agentID, discordUserID,
	))
}

func (s *PostgresStore) GetDiscordAuthByCode(ctx context.Context, code string) (*types.DiscordAuthorization, error) {
	return scanDiscordAuthPG(s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, discord_user_id, status, pairing_code, added_by, code_expires_at, created_at, updated_at
		 FROM discord_authorizations WHERE pairing_code = ? AND status = 'pending'`,
		code,
	))
}

func (s *PostgresStore) UpdateDiscordAuth(ctx context.Context, auth types.DiscordAuthorization) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE discord_authorizations SET
			status=?, pairing_code=?, added_by=?, code_expires_at=?, updated_at=?
		 WHERE id=?`,
		auth.Status, auth.PairingCode, auth.AddedBy,
		auth.CodeExpiresAt,
		auth.UpdatedAt.UTC().Format(time.RFC3339),
		auth.ID,
	)
	if err != nil {
		return fmt.Errorf("update discord auth: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("discord auth %s: %w", auth.ID, types.ErrNotFound)
	}
	return nil
}

func (s *PostgresStore) ListDiscordAuths(ctx context.Context, agentID string) ([]types.DiscordAuthorization, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, discord_user_id, status, pairing_code, added_by, code_expires_at, created_at, updated_at
		 FROM discord_authorizations WHERE agent_id = ? ORDER BY created_at`,
		agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("query discord auths: %w", err)
	}
	defer rows.Close()

	var auths []types.DiscordAuthorization
	for rows.Next() {
		var (
			auth        types.DiscordAuthorization
			codeExpires sql.NullString
			createdAt   string
			updatedAt   string
		)
		if err := rows.Scan(
			&auth.ID, &auth.AgentID, &auth.DiscordUserID,
			&auth.Status, &auth.PairingCode, &auth.AddedBy,
			&codeExpires, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan discord auth: %w", err)
		}
		if codeExpires.Valid && codeExpires.String != "" {
			if t, err := parseTime(codeExpires.String); err == nil {
				auth.CodeExpiresAt = &t
			}
		}
		auth.CreatedAt, _ = parseTime(createdAt)
		auth.UpdatedAt, _ = parseTime(updatedAt)
		auths = append(auths, auth)
	}
	return auths, rows.Err()
}

func (s *PostgresStore) DeleteDiscordAuth(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM discord_authorizations WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("delete discord auth: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("discord auth %s: %w", id, types.ErrNotFound)
	}
	return nil
}

// scanDiscordAuthPG scans a single discord_authorizations row.
func scanDiscordAuthPG(row *sql.Row) (*types.DiscordAuthorization, error) {
	var (
		auth        types.DiscordAuthorization
		codeExpires sql.NullString
		createdAt   string
		updatedAt   string
	)
	err := row.Scan(
		&auth.ID, &auth.AgentID, &auth.DiscordUserID,
		&auth.Status, &auth.PairingCode, &auth.AddedBy,
		&codeExpires, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, types.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan discord auth: %w", err)
	}
	if codeExpires.Valid && codeExpires.String != "" {
		if t, err := parseTime(codeExpires.String); err == nil {
			auth.CodeExpiresAt = &t
		}
	}
	auth.CreatedAt, _ = parseTime(createdAt)
	auth.UpdatedAt, _ = parseTime(updatedAt)
	return &auth, nil
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

// ---------- Helpers ----------

// dbTimeFmt matches the database CURRENT_TIMESTAMP format.
const dbTimeFmt = "2006-01-02 15:04:05"

// parseTime tries RFC3339 first, then falls back to the database CURRENT_TIMESTAMP format.
func parseTime(s string) (time.Time, error) {
	return timeutil.ParseTimestampUTC(s)
}

// marshalHostPaths returns a *string with JSON-encoded HostPathConfig, or nil if hp is nil.
func marshalHostPaths(hp *types.HostPathConfig) *string {
	if hp == nil {
		return nil
	}
	b, err := json.Marshal(hp)
	if err != nil {
		return nil
	}
	v := string(b)
	return &v
}

// marshalHostFilesystem returns a *string with JSON-encoded HostFilesystemConfig, or nil if hf is nil.
func marshalHostFilesystem(hf *types.HostFilesystemConfig) *string {
	if hf == nil {
		return nil
	}
	b, err := json.Marshal(hf)
	if err != nil {
		return nil
	}
	v := string(b)
	return &v
}

// marshalWebhookInbound returns a *string with JSON-encoded InboundWebhookConfig, or nil if wh is nil.
func marshalWebhookInbound(wh *types.InboundWebhookConfig) *string {
	if wh == nil {
		return nil
	}
	b, err := json.Marshal(wh)
	if err != nil {
		return nil
	}
	v := string(b)
	return &v
}

// formatNullableTime returns an RFC3339 string pointer, or nil if t is nil.
func formatNullableTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// nullIfEmpty returns a sql.NullString that is NULL when s is empty.
func nullIfEmpty(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// parseNullableTime converts a sql.NullString to a *time.Time.
func parseNullableTime(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil
	}
	return &t
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// DB returns the underlying *sql.DB for use by subsystems (e.g., secrets vault)
// that need direct database access.
func (s *PostgresStore) DB() *sql.DB { return s.db.raw }

// --- API Key CRUD ---

func (s *PostgresStore) CreateAPIKey(ctx context.Context, key types.APIKey) error {
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now().UTC()
	}
	agentIDsJSON, err := json.Marshal(key.AgentIDs)
	if err != nil {
		return fmt.Errorf("marshal agent_ids: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO api_keys
		    (id, name, key_hash, key_prefix, scope, agent_ids, is_active, expires_at, created_at, last_used_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.ID, key.Name, key.KeyHash, key.KeyPrefix, key.Scope,
		string(agentIDsJSON), boolToInt(key.IsActive),
		formatNullableTime(key.ExpiresAt),
		key.CreatedAt.UTC().Format(time.RFC3339),
		formatNullableTime(key.LastUsedAt),
	)
	if err != nil {
		return fmt.Errorf("create api key: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetAPIKey(ctx context.Context, id string) (*types.APIKey, error) {
	return s.scanAPIKey(s.db.QueryRowContext(ctx,
		`SELECT id, name, key_hash, key_prefix, scope, agent_ids, is_active, expires_at, created_at, last_used_at
		 FROM api_keys WHERE id = ?`, id))
}

func (s *PostgresStore) GetAPIKeyByPrefix(ctx context.Context, prefix string) (*types.APIKey, error) {
	return s.scanAPIKey(s.db.QueryRowContext(ctx,
		`SELECT id, name, key_hash, key_prefix, scope, agent_ids, is_active, expires_at, created_at, last_used_at
		 FROM api_keys WHERE key_prefix = ?`, prefix))
}

func (s *PostgresStore) ListAPIKeys(ctx context.Context) ([]types.APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, key_hash, key_prefix, scope, agent_ids, is_active, expires_at, created_at, last_used_at
		 FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var keys []types.APIKey
	for rows.Next() {
		k, err := s.scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, *k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate api keys: %w", err)
	}
	return keys, nil
}

func (s *PostgresStore) DeleteAPIKey(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrAPIKeyNotFound
	}
	return nil
}

func (s *PostgresStore) UpdateAPIKeyLastUsed(ctx context.Context, id string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = ? WHERE id = ?`,
		at.UTC().Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("update api key last_used_at: %w", err)
	}
	return nil
}

func (s *PostgresStore) scanAPIKey(rs rowScanner) (*types.APIKey, error) {
	var (
		k            types.APIKey
		agentIDsJSON string
		isActiveInt  int
		expiresAt    sql.NullString
		createdAt    string
		lastUsedAt   sql.NullString
	)
	if err := rs.Scan(
		&k.ID, &k.Name, &k.KeyHash, &k.KeyPrefix, &k.Scope,
		&agentIDsJSON, &isActiveInt, &expiresAt, &createdAt, &lastUsedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, types.ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("scan api key: %w", err)
	}
	k.IsActive = isActiveInt != 0
	if err := json.Unmarshal([]byte(agentIDsJSON), &k.AgentIDs); err != nil {
		k.AgentIDs = nil
	}
	if k.AgentIDs == nil {
		k.AgentIDs = []string{}
	}
	k.ExpiresAt = parseNullableTime(expiresAt)
	t, _ := parseTime(createdAt)
	k.CreatedAt = t
	k.LastUsedAt = parseNullableTime(lastUsedAt)
	return &k, nil
}

// ─── Agent Template CRUD ────────────────────────────────────────────────────

// CreateTemplate inserts a new agent template.
func (s *PostgresStore) CreateTemplate(ctx context.Context, tmpl types.AgentTemplate) error {
	lockedJSON, err := json.Marshal(tmpl.LockedFields)
	if err != nil {
		return fmt.Errorf("marshal locked_fields: %w", err)
	}
	constrainedJSON, err := json.Marshal(tmpl.ConstrainedFields)
	if err != nil {
		return fmt.Errorf("marshal constrained_fields: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO agent_templates (id, name, description, group_id, config_json, locked_fields, constrained_fields, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tmpl.ID, tmpl.Name, tmpl.Description, nullIfEmpty(tmpl.GroupID),
		tmpl.ConfigJSON, string(lockedJSON), string(constrainedJSON),
		nullIfEmpty(tmpl.CreatedBy), now, now,
	)
	return err
}

// GetTemplate retrieves a template by ID.
func (s *PostgresStore) GetTemplate(ctx context.Context, id string) (*types.AgentTemplate, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, COALESCE(group_id,''), config_json, locked_fields, constrained_fields, COALESCE(created_by,''), created_at, updated_at
		 FROM agent_templates WHERE id = ?`, id)
	return scanTemplate(row)
}

// ListTemplates returns all agent templates.
func (s *PostgresStore) ListTemplates(ctx context.Context) ([]types.AgentTemplate, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, COALESCE(group_id,''), config_json, locked_fields, constrained_fields, COALESCE(created_by,''), created_at, updated_at
		 FROM agent_templates ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.AgentTemplate
	for rows.Next() {
		t, err := scanTemplateRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ListTemplatesByGroup returns templates for a specific group.
func (s *PostgresStore) ListTemplatesByGroup(ctx context.Context, groupID string) ([]types.AgentTemplate, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, COALESCE(group_id,''), config_json, locked_fields, constrained_fields, COALESCE(created_by,''), created_at, updated_at
		 FROM agent_templates WHERE group_id = ? ORDER BY name`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []types.AgentTemplate
	for rows.Next() {
		t, err := scanTemplateRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// UpdateTemplate updates a template's fields.
func (s *PostgresStore) UpdateTemplate(ctx context.Context, tmpl types.AgentTemplate) error {
	lockedJSON, err := json.Marshal(tmpl.LockedFields)
	if err != nil {
		return fmt.Errorf("marshal locked_fields: %w", err)
	}
	constrainedJSON, err := json.Marshal(tmpl.ConstrainedFields)
	if err != nil {
		return fmt.Errorf("marshal constrained_fields: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`UPDATE agent_templates SET name=?, description=?, group_id=?, config_json=?, locked_fields=?, constrained_fields=?, updated_at=?
		 WHERE id=?`,
		tmpl.Name, tmpl.Description, nullIfEmpty(tmpl.GroupID),
		tmpl.ConfigJSON, string(lockedJSON), string(constrainedJSON), now, tmpl.ID,
	)
	return err
}

// DeleteTemplate removes a template by ID.
func (s *PostgresStore) DeleteTemplate(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agent_templates WHERE id = ?`, id)
	return err
}

// scanTemplate scans a single template from a *sql.Row.
func scanTemplate(row *sql.Row) (*types.AgentTemplate, error) {
	var t types.AgentTemplate
	var lockedJSON, constrainedJSON, createdAt, updatedAt string
	if err := row.Scan(&t.ID, &t.Name, &t.Description, &t.GroupID, &t.ConfigJSON,
		&lockedJSON, &constrainedJSON, &t.CreatedBy, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, types.ErrNotFound
		}
		return nil, fmt.Errorf("scan template: %w", err)
	}
	_ = json.Unmarshal([]byte(lockedJSON), &t.LockedFields)
	if t.LockedFields == nil {
		t.LockedFields = []string{}
	}
	t.ConstrainedFields = make(map[string]types.ConstraintRule)
	_ = json.Unmarshal([]byte(constrainedJSON), &t.ConstrainedFields)
	t.CreatedAt, _ = parseTime(createdAt)
	t.UpdatedAt, _ = parseTime(updatedAt)
	return &t, nil
}

// scanTemplateRow scans a single template from a *sql.Rows.
func scanTemplateRow(rows *sql.Rows) (*types.AgentTemplate, error) {
	var t types.AgentTemplate
	var lockedJSON, constrainedJSON, createdAt, updatedAt string
	if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.GroupID, &t.ConfigJSON,
		&lockedJSON, &constrainedJSON, &t.CreatedBy, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("scan template row: %w", err)
	}
	_ = json.Unmarshal([]byte(lockedJSON), &t.LockedFields)
	if t.LockedFields == nil {
		t.LockedFields = []string{}
	}
	t.ConstrainedFields = make(map[string]types.ConstraintRule)
	_ = json.Unmarshal([]byte(constrainedJSON), &t.ConstrainedFields)
	t.CreatedAt, _ = parseTime(createdAt)
	t.UpdatedAt, _ = parseTime(updatedAt)
	return &t, nil
}

// ─── Outbound Webhook CRUD ───────────────────────────────────────────────────

func (s *PostgresStore) CreateOutboundWebhook(ctx context.Context, wh types.OutboundWebhook) error {
	eventsJSON, err := json.Marshal(wh.Events)
	if err != nil {
		return fmt.Errorf("marshal events: %w", err)
	}
	headersJSON, err := json.Marshal(wh.Headers)
	if err != nil {
		return fmt.Errorf("marshal headers: %w", err)
	}
	backoffJSON, err := json.Marshal(wh.BackoffSeconds)
	if err != nil {
		return fmt.Errorf("marshal backoff_seconds: %w", err)
	}
	now := timeutil.NowUTC().Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO outbound_webhooks
		    (id, name, url, agent_id, events, secret_ref, headers, payload_template,
		     max_retries, backoff_seconds, cb_threshold, cb_cooldown_secs, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wh.ID, wh.Name, wh.URL, nullIfEmpty(wh.AgentID),
		string(eventsJSON), wh.SecretRef, string(headersJSON), wh.PayloadTemplate,
		wh.MaxRetries, string(backoffJSON), wh.CBThreshold, wh.CBCooldownSecs,
		boolToInt(wh.Enabled), now, now,
	)
	if err != nil {
		return fmt.Errorf("create outbound webhook: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetOutboundWebhook(ctx context.Context, id string) (*types.OutboundWebhook, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, url, agent_id, events, secret_ref, headers, payload_template,
		        max_retries, backoff_seconds, cb_threshold, cb_cooldown_secs, enabled, created_at, updated_at
		 FROM outbound_webhooks WHERE id = ?`, id)
	return scanOutboundWebhookPG(row)
}

func (s *PostgresStore) UpdateOutboundWebhook(ctx context.Context, wh types.OutboundWebhook) error {
	eventsJSON, err := json.Marshal(wh.Events)
	if err != nil {
		return fmt.Errorf("marshal events: %w", err)
	}
	headersJSON, err := json.Marshal(wh.Headers)
	if err != nil {
		return fmt.Errorf("marshal headers: %w", err)
	}
	backoffJSON, err := json.Marshal(wh.BackoffSeconds)
	if err != nil {
		return fmt.Errorf("marshal backoff_seconds: %w", err)
	}
	now := timeutil.NowUTC().Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx,
		`UPDATE outbound_webhooks SET
		    name=?, url=?, agent_id=?, events=?, secret_ref=?, headers=?, payload_template=?,
		    max_retries=?, backoff_seconds=?, cb_threshold=?, cb_cooldown_secs=?, enabled=?, updated_at=?
		 WHERE id=?`,
		wh.Name, wh.URL, nullIfEmpty(wh.AgentID),
		string(eventsJSON), wh.SecretRef, string(headersJSON), wh.PayloadTemplate,
		wh.MaxRetries, string(backoffJSON), wh.CBThreshold, wh.CBCooldownSecs,
		boolToInt(wh.Enabled), now, wh.ID,
	)
	if err != nil {
		return fmt.Errorf("update outbound webhook: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return types.ErrOutboundWebhookNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteOutboundWebhook(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM outbound_webhooks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete outbound webhook: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return types.ErrOutboundWebhookNotFound
	}
	return nil
}

func (s *PostgresStore) ListOutboundWebhooks(ctx context.Context, agentID string) ([]types.OutboundWebhook, error) {
	var rows *sql.Rows
	var err error
	if agentID == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, name, url, agent_id, events, secret_ref, headers, payload_template,
			        max_retries, backoff_seconds, cb_threshold, cb_cooldown_secs, enabled, created_at, updated_at
			 FROM outbound_webhooks WHERE agent_id IS NULL ORDER BY name`)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, name, url, agent_id, events, secret_ref, headers, payload_template,
			        max_retries, backoff_seconds, cb_threshold, cb_cooldown_secs, enabled, created_at, updated_at
			 FROM outbound_webhooks WHERE agent_id = ? ORDER BY name`, agentID)
	}
	if err != nil {
		return nil, fmt.Errorf("list outbound webhooks: %w", err)
	}
	defer rows.Close()
	return scanOutboundWebhookRowsPG(rows)
}

func (s *PostgresStore) ListAllEnabledOutboundWebhooks(ctx context.Context) ([]types.OutboundWebhook, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, url, agent_id, events, secret_ref, headers, payload_template,
		        max_retries, backoff_seconds, cb_threshold, cb_cooldown_secs, enabled, created_at, updated_at
		 FROM outbound_webhooks WHERE enabled = 1`)
	if err != nil {
		return nil, fmt.Errorf("list enabled outbound webhooks: %w", err)
	}
	defer rows.Close()
	return scanOutboundWebhookRowsPG(rows)
}

// ─── Webhook Delivery CRUD ──────────────────────────────────────────────────

func (s *PostgresStore) InsertWebhookDelivery(ctx context.Context, d types.WebhookDelivery) error {
	now := timeutil.NowUTC().Format(time.RFC3339)
	if !d.CreatedAt.IsZero() {
		now = d.CreatedAt.UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO webhook_deliveries
		    (id, webhook_id, event_type, payload, status, http_code, response_body,
		     duration_ms, retry_count, next_retry_at, error_message, payload_sha256, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.WebhookID, d.EventType, d.Payload, string(d.Status),
		d.HTTPCode, d.ResponseBody, d.DurationMs, d.RetryCount,
		formatNullableTime(d.NextRetryAt), d.ErrorMessage, d.PayloadSha256, now,
	)
	if err != nil {
		return fmt.Errorf("insert webhook delivery: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListWebhookDeliveries(ctx context.Context, webhookID string, limit int) ([]types.WebhookDelivery, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, webhook_id, event_type, payload, status, http_code, response_body,
		        duration_ms, retry_count, next_retry_at, error_message, payload_sha256, created_at
		 FROM webhook_deliveries WHERE webhook_id = ? ORDER BY created_at DESC LIMIT ?`,
		webhookID, limit)
	if err != nil {
		return nil, fmt.Errorf("list webhook deliveries: %w", err)
	}
	defer rows.Close()
	return scanWebhookDeliveryRowsPG(rows)
}

func (s *PostgresStore) ListPendingRetries(ctx context.Context) ([]types.WebhookDelivery, error) {
	now := timeutil.NowUTC().Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx,
		`SELECT d.id, d.webhook_id, d.event_type, d.payload, d.status, d.http_code, d.response_body,
		        d.duration_ms, d.retry_count, d.next_retry_at, d.error_message, d.payload_sha256, d.created_at
		 FROM webhook_deliveries d
		 JOIN outbound_webhooks w ON w.id = d.webhook_id
		 WHERE d.status = 'pending_retry' AND d.next_retry_at <= ? AND w.enabled = 1
		 ORDER BY d.next_retry_at ASC`, now)
	if err != nil {
		return nil, fmt.Errorf("list pending retries: %w", err)
	}
	defer rows.Close()
	return scanWebhookDeliveryRowsPG(rows)
}

func (s *PostgresStore) UpdateDeliveryStatus(ctx context.Context, id string, status types.WebhookDeliveryStatus, httpCode int, responseBody, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webhook_deliveries SET status=?, http_code=?, response_body=?, error_message=? WHERE id=?`,
		string(status), httpCode, responseBody, errMsg, id)
	if err != nil {
		return fmt.Errorf("update delivery status: %w", err)
	}
	return nil
}

func (s *PostgresStore) PruneWebhookDeliveries(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := timeutil.NowUTC().Add(-olderThan).Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM webhook_deliveries WHERE created_at < ? AND status != 'pending_retry'`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune webhook deliveries: %w", err)
	}
	n, _ := result.RowsAffected()
	return n, nil
}

// ─── Outbound Webhook scan helpers ──────────────────────────────────────────

func scanOutboundWebhookFromPG(rs rowScanner) (*types.OutboundWebhook, error) {
	var (
		wh          types.OutboundWebhook
		agentID     sql.NullString
		eventsJSON  string
		headersJSON string
		backoffJSON string
		enabledInt  int
		createdAt   string
		updatedAt   string
	)
	if err := rs.Scan(
		&wh.ID, &wh.Name, &wh.URL, &agentID, &eventsJSON, &wh.SecretRef,
		&headersJSON, &wh.PayloadTemplate, &wh.MaxRetries, &backoffJSON,
		&wh.CBThreshold, &wh.CBCooldownSecs, &enabledInt, &createdAt, &updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, types.ErrOutboundWebhookNotFound
		}
		return nil, fmt.Errorf("scan outbound webhook: %w", err)
	}
	wh.Enabled = enabledInt != 0
	if agentID.Valid {
		wh.AgentID = agentID.String
	}
	if err := json.Unmarshal([]byte(eventsJSON), &wh.Events); err != nil {
		wh.Events = []string{"*"}
	}
	wh.Headers = make(map[string]string)
	_ = json.Unmarshal([]byte(headersJSON), &wh.Headers)
	if err := json.Unmarshal([]byte(backoffJSON), &wh.BackoffSeconds); err != nil {
		wh.BackoffSeconds = types.DefaultWebhookBackoff
	}
	wh.CreatedAt, _ = parseTime(createdAt)
	wh.UpdatedAt, _ = parseTime(updatedAt)
	return &wh, nil
}

func scanOutboundWebhookPG(row *sql.Row) (*types.OutboundWebhook, error) {
	return scanOutboundWebhookFromPG(row)
}

func scanOutboundWebhookRowsPG(rows *sql.Rows) ([]types.OutboundWebhook, error) {
	var out []types.OutboundWebhook
	for rows.Next() {
		wh, err := scanOutboundWebhookFromPG(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *wh)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbound webhooks: %w", err)
	}
	return out, nil
}

func scanWebhookDeliveryRowsPG(rows *sql.Rows) ([]types.WebhookDelivery, error) {
	var out []types.WebhookDelivery
	for rows.Next() {
		var (
			d           types.WebhookDelivery
			status      string
			nextRetryAt sql.NullString
			createdAt   string
		)
		if err := rows.Scan(
			&d.ID, &d.WebhookID, &d.EventType, &d.Payload, &status,
			&d.HTTPCode, &d.ResponseBody, &d.DurationMs, &d.RetryCount,
			&nextRetryAt, &d.ErrorMessage, &d.PayloadSha256, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan webhook delivery: %w", err)
		}
		d.Status = types.WebhookDeliveryStatus(status)
		d.NextRetryAt = parseNullableTime(nextRetryAt)
		d.CreatedAt, _ = parseTime(createdAt)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate webhook deliveries: %w", err)
	}
	return out, nil
}

// ── Provider CRUD ──────────────────────────────────────────────────────

func (s *PostgresStore) CreateProvider(ctx context.Context, p types.ProviderRecord) error {
	modelsJSON, err := json.Marshal(p.AllowedModels)
	if err != nil {
		return fmt.Errorf("marshal allowed_models: %w", err)
	}
	enabled := 0
	if p.IsEnabled {
		enabled = 1
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO providers (id, provider_type, display_name, api_key_enc, base_url,
		 default_model, allowed_models, is_enabled, source, config_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.ProviderType, p.DisplayName, p.APIKeyEnc, p.BaseURL,
		p.DefaultModel, string(modelsJSON), enabled, p.Source, p.ConfigJSON,
		p.CreatedAt.UTC().Format(time.RFC3339),
		p.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert provider: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetProvider(ctx context.Context, id string) (*types.ProviderRecord, error) {
	var p types.ProviderRecord
	var enabled int
	var modelsJSON, createdAt, updatedAt string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, provider_type, display_name, api_key_enc, base_url,
		        default_model, allowed_models, is_enabled, source, config_json,
		        created_at, updated_at
		 FROM providers WHERE id = ?`, id,
	).Scan(
		&p.ID, &p.ProviderType, &p.DisplayName, &p.APIKeyEnc, &p.BaseURL,
		&p.DefaultModel, &modelsJSON, &enabled, &p.Source, &p.ConfigJSON,
		&createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, types.ErrProviderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query provider: %w", err)
	}
	p.IsEnabled = enabled != 0
	_ = json.Unmarshal([]byte(modelsJSON), &p.AllowedModels)
	if p.AllowedModels == nil {
		p.AllowedModels = []string{}
	}
	var parseErr error
	if p.CreatedAt, parseErr = parseTime(createdAt); parseErr != nil {
		return nil, fmt.Errorf("parse created_at: %w", parseErr)
	}
	if p.UpdatedAt, parseErr = parseTime(updatedAt); parseErr != nil {
		return nil, fmt.Errorf("parse updated_at: %w", parseErr)
	}
	return &p, nil
}

func (s *PostgresStore) UpdateProvider(ctx context.Context, p types.ProviderRecord) error {
	modelsJSON, err := json.Marshal(p.AllowedModels)
	if err != nil {
		return fmt.Errorf("marshal allowed_models: %w", err)
	}
	enabled := 0
	if p.IsEnabled {
		enabled = 1
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE providers SET
			provider_type=?, display_name=?, api_key_enc=?, base_url=?,
			default_model=?, allowed_models=?, is_enabled=?, source=?,
			config_json=?, updated_at=?
		 WHERE id=?`,
		p.ProviderType, p.DisplayName, p.APIKeyEnc, p.BaseURL,
		p.DefaultModel, string(modelsJSON), enabled, p.Source,
		p.ConfigJSON, p.UpdatedAt.UTC().Format(time.RFC3339),
		p.ID,
	)
	if err != nil {
		return fmt.Errorf("update provider: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrProviderNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteProvider(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM providers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrProviderNotFound
	}
	return nil
}

func (s *PostgresStore) ListProviders(ctx context.Context) ([]types.ProviderRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, provider_type, display_name, api_key_enc, base_url,
		        default_model, allowed_models, is_enabled, source, config_json,
		        created_at, updated_at
		 FROM providers ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("query providers: %w", err)
	}
	defer rows.Close()

	var providers []types.ProviderRecord
	for rows.Next() {
		var p types.ProviderRecord
		var enabled int
		var modelsJSON, createdAt, updatedAt string
		if err := rows.Scan(
			&p.ID, &p.ProviderType, &p.DisplayName, &p.APIKeyEnc, &p.BaseURL,
			&p.DefaultModel, &modelsJSON, &enabled, &p.Source, &p.ConfigJSON,
			&createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan provider: %w", err)
		}
		p.IsEnabled = enabled != 0
		_ = json.Unmarshal([]byte(modelsJSON), &p.AllowedModels)
		if p.AllowedModels == nil {
			p.AllowedModels = []string{}
		}
		p.CreatedAt, _ = parseTime(createdAt)
		p.UpdatedAt, _ = parseTime(updatedAt)
		providers = append(providers, p)
	}
	if providers == nil {
		providers = []types.ProviderRecord{}
	}
	return providers, rows.Err()
}

// ---------- Workflows ----------

func (s *PostgresStore) CreateWorkflow(ctx context.Context, w types.Workflow) error {
	stepsJSON, err := json.Marshal(w.Steps)
	if err != nil {
		return fmt.Errorf("marshal workflow steps: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO workflows (id, agent_id, name, description, steps_json, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.AgentID, w.Name, w.Description, string(stepsJSON),
		boolToInt(w.Enabled),
		w.CreatedAt.UTC().Format(time.RFC3339),
		w.UpdatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert workflow: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetWorkflow(ctx context.Context, id string) (*types.Workflow, error) {
	var w types.Workflow
	var stepsJSON string
	var enabled int
	var createdAt, updatedAt string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, name, description, steps_json, enabled, created_at, updated_at
		 FROM workflows WHERE id = ?`, id,
	).Scan(&w.ID, &w.AgentID, &w.Name, &w.Description, &stepsJSON, &enabled, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, types.ErrWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query workflow: %w", err)
	}
	w.Enabled = enabled != 0
	if err := json.Unmarshal([]byte(stepsJSON), &w.Steps); err != nil {
		return nil, fmt.Errorf("unmarshal workflow steps: %w", err)
	}
	w.CreatedAt, _ = parseTime(createdAt)
	w.UpdatedAt, _ = parseTime(updatedAt)
	return &w, nil
}

func (s *PostgresStore) GetWorkflowByName(ctx context.Context, agentID, name string) (*types.Workflow, error) {
	var w types.Workflow
	var stepsJSON string
	var enabled int
	var createdAt, updatedAt string

	err := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, name, description, steps_json, enabled, created_at, updated_at
		 FROM workflows WHERE agent_id = ? AND name = ?`, agentID, name,
	).Scan(&w.ID, &w.AgentID, &w.Name, &w.Description, &stepsJSON, &enabled, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, types.ErrWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query workflow by name: %w", err)
	}
	w.Enabled = enabled != 0
	if err := json.Unmarshal([]byte(stepsJSON), &w.Steps); err != nil {
		return nil, fmt.Errorf("unmarshal workflow steps: %w", err)
	}
	w.CreatedAt, _ = parseTime(createdAt)
	w.UpdatedAt, _ = parseTime(updatedAt)
	return &w, nil
}

func (s *PostgresStore) UpdateWorkflow(ctx context.Context, w types.Workflow) error {
	stepsJSON, err := json.Marshal(w.Steps)
	if err != nil {
		return fmt.Errorf("marshal workflow steps: %w", err)
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE workflows SET name=?, description=?, steps_json=?, enabled=?, updated_at=? WHERE id=?`,
		w.Name, w.Description, string(stepsJSON), boolToInt(w.Enabled),
		w.UpdatedAt.UTC().Format(time.RFC3339), w.ID)
	if err != nil {
		return fmt.Errorf("update workflow: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrWorkflowNotFound
	}
	return nil
}

func (s *PostgresStore) DeleteWorkflow(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM workflows WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete workflow: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrWorkflowNotFound
	}
	return nil
}

func (s *PostgresStore) ListWorkflows(ctx context.Context, agentID string) ([]types.Workflow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, name, description, steps_json, enabled, created_at, updated_at
		 FROM workflows WHERE agent_id = ? ORDER BY created_at ASC`, agentID)
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}
	defer rows.Close()
	return scanWorkflowsPg(rows)
}

func scanWorkflowsPg(rows *sql.Rows) ([]types.Workflow, error) {
	var workflows []types.Workflow
	for rows.Next() {
		var w types.Workflow
		var stepsJSON string
		var enabled int
		var createdAt, updatedAt string

		if err := rows.Scan(&w.ID, &w.AgentID, &w.Name, &w.Description, &stepsJSON, &enabled, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan workflow: %w", err)
		}
		w.Enabled = enabled != 0
		_ = json.Unmarshal([]byte(stepsJSON), &w.Steps)
		w.CreatedAt, _ = parseTime(createdAt)
		w.UpdatedAt, _ = parseTime(updatedAt)
		workflows = append(workflows, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workflows: %w", err)
	}
	if workflows == nil {
		workflows = []types.Workflow{}
	}
	return workflows, nil
}

// ---------- Workflow Runs ----------

func (s *PostgresStore) CreateWorkflowRun(ctx context.Context, r types.WorkflowRun) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO workflow_runs (id, workflow_id, agent_id, status, steps_json, input_vars_json, error, duration_ms, started_at, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.WorkflowID, r.AgentID, r.Status, r.StepsJSON, r.InputVarsJSON,
		r.Error, r.DurationMs,
		r.StartedAt.UTC().Format(time.RFC3339),
		formatNullableTimePtrPg(r.CompletedAt),
	)
	if err != nil {
		return fmt.Errorf("insert workflow run: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetWorkflowRun(ctx context.Context, id string) (*types.WorkflowRun, error) {
	var r types.WorkflowRun
	var startedAt string
	var completedAt sql.NullString

	err := s.db.QueryRowContext(ctx,
		`SELECT id, workflow_id, agent_id, status, steps_json, input_vars_json, error, duration_ms, started_at, completed_at
		 FROM workflow_runs WHERE id = ?`, id,
	).Scan(&r.ID, &r.WorkflowID, &r.AgentID, &r.Status, &r.StepsJSON, &r.InputVarsJSON,
		&r.Error, &r.DurationMs, &startedAt, &completedAt)
	if err == sql.ErrNoRows {
		return nil, types.ErrWorkflowRunNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query workflow run: %w", err)
	}
	r.StartedAt, _ = parseTime(startedAt)
	if completedAt.Valid {
		t, _ := parseTime(completedAt.String)
		r.CompletedAt = &t
	}
	return &r, nil
}

func (s *PostgresStore) UpdateWorkflowRun(ctx context.Context, r types.WorkflowRun) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE workflow_runs SET status=?, steps_json=?, error=?, duration_ms=?, completed_at=? WHERE id=?`,
		r.Status, r.StepsJSON, r.Error, r.DurationMs,
		formatNullableTimePtrPg(r.CompletedAt), r.ID)
	if err != nil {
		return fmt.Errorf("update workflow run: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return types.ErrWorkflowRunNotFound
	}
	return nil
}

func (s *PostgresStore) ListWorkflowRuns(ctx context.Context, workflowID string, limit int) ([]types.WorkflowRun, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, workflow_id, agent_id, status, steps_json, input_vars_json, error, duration_ms, started_at, completed_at
		 FROM workflow_runs WHERE workflow_id = ? ORDER BY started_at DESC LIMIT ?`, workflowID, limit)
	if err != nil {
		return nil, fmt.Errorf("list workflow runs: %w", err)
	}
	defer rows.Close()
	return scanWorkflowRunsPg(rows)
}

func scanWorkflowRunsPg(rows *sql.Rows) ([]types.WorkflowRun, error) {
	var runs []types.WorkflowRun
	for rows.Next() {
		var r types.WorkflowRun
		var startedAt string
		var completedAt sql.NullString

		if err := rows.Scan(&r.ID, &r.WorkflowID, &r.AgentID, &r.Status, &r.StepsJSON, &r.InputVarsJSON,
			&r.Error, &r.DurationMs, &startedAt, &completedAt); err != nil {
			return nil, fmt.Errorf("scan workflow run: %w", err)
		}
		r.StartedAt, _ = parseTime(startedAt)
		if completedAt.Valid {
			t, _ := parseTime(completedAt.String)
			r.CompletedAt = &t
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workflow runs: %w", err)
	}
	if runs == nil {
		runs = []types.WorkflowRun{}
	}
	return runs, nil
}

// formatNullableTimePtrPg formats a *time.Time as RFC3339 or returns nil for SQL NULL.
func formatNullableTimePtrPg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}
