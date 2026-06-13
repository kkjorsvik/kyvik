// Package dbtool implements a KTP tool for querying pre-configured external databases
// with vault-backed credentials, parameterized queries, and capability-based action filtering.
package dbtool

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

const (
	defaultTimeout = 30 * time.Second
	defaultMaxRows = 100
	maxMaxRows     = 1000
	connTimeout    = 10 * time.Second
)

// ConnectionConfigsFunc resolves the configured database connections for an agent.
type ConnectionConfigsFunc func(agentID string) ([]types.DatabaseConnection, error)

// SecretResolverFunc resolves a secret from the vault with cascading lookup.
type SecretResolverFunc func(ctx context.Context, agentID, teamID, key string) (string, error)

// Option configures a Tool.
type Option func(*Tool)

// Tool implements database operations via pre-configured connections.
type Tool struct {
	connectionConfigs ConnectionConfigsFunc
	secretResolver    SecretResolverFunc
	pools             *PoolManager
}

// New creates a database tool.
func New(configs ConnectionConfigsFunc, secrets SecretResolverFunc, opts ...Option) *Tool {
	t := &Tool{
		connectionConfigs: configs,
		secretResolver:    secrets,
		pools:             NewPoolManager(),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Inline returns true so execution happens in-process where secrets are accessible.
func (t *Tool) Inline() bool { return true }

// Stop cleans up connection pools.
func (t *Tool) Stop() {
	t.pools.Close()
}

// Declaration returns the database tool's KTP declaration.
func (t *Tool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:         "database",
		Version:      "1.0.0",
		Description:  "Query and modify pre-configured external databases",
		MinTier:      ktp.TierReader,
		DefaultTiers: []string{ktp.TierReader, ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "list_connections",
				Description: "List all configured database connections for this agent",
				Parameters: ktp.JSONSchema{
					Type:       "object",
					Properties: map[string]ktp.JSONSchema{},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"connections": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "database", Access: "select", Resource: "*"}},
			},
			{
				Name:        "query",
				Description: "Execute a SELECT query and return results",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"connection": {Type: "string", Description: "Connection name"},
						"sql":        {Type: "string", Description: "SQL SELECT query with parameterized placeholders"},
						"params":     {Type: "array", Items: &ktp.JSONSchema{Type: "string"}, Description: "Query parameters"},
						"max_rows":   {Type: "integer", Description: "Maximum rows to return (default 100, max 1000)"},
					},
					Required: []string{"connection", "sql"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"columns":   {Type: "array", Items: &ktp.JSONSchema{Type: "string"}},
						"rows":      {Type: "array", Items: &ktp.JSONSchema{Type: "array"}},
						"row_count": {Type: "integer"},
						"truncated": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "database", Access: "select", Resource: "*"}},
			},
			{
				Name:        "describe_table",
				Description: "Get column information for a table",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"connection": {Type: "string", Description: "Connection name"},
						"table":      {Type: "string", Description: "Table name"},
					},
					Required: []string{"connection", "table"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"columns": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "database", Access: "select", Resource: "*"}},
			},
			{
				Name:        "execute",
				Description: "Execute an INSERT, UPDATE, or DELETE statement",
				Destructive: true,
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"connection": {Type: "string", Description: "Connection name"},
						"sql":        {Type: "string", Description: "SQL statement with parameterized placeholders"},
						"params":     {Type: "array", Items: &ktp.JSONSchema{Type: "string"}, Description: "Statement parameters"},
					},
					Required: []string{"connection", "sql"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"rows_affected":  {Type: "integer"},
						"statement_type": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "database", Access: "insert", Resource: "*"}},
			},
		},
	}
}

// Execute runs the requested database action.
func (t *Tool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	switch req.Action {
	case "list_connections":
		return t.execListConnections(ctx, req)
	case "query":
		return t.execQuery(ctx, req)
	case "describe_table":
		return t.execDescribeTable(ctx, req)
	case "execute":
		return t.execExecute(ctx, req)
	}
	return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
}

func (t *Tool) execListConnections(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	conns, err := t.connectionConfigs(req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to get connections: %v", err)), nil
	}

	result := make([]map[string]any, 0, len(conns))
	for _, c := range conns {
		result = append(result, map[string]any{
			"name":        c.Name,
			"description": c.Description,
			"driver":      c.Driver,
			"database":    c.Database,
			"read_only":   c.ReadOnly,
		})
	}

	return okResp(req.ID, map[string]any{"connections": result}), nil
}

func (t *Tool) execQuery(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	connName, err := strParam(req.Parameters, "connection")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	sqlStr, err := strParam(req.Parameters, "sql")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	stmtType, err := classifyStatement(sqlStr)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	if stmtType != "select" {
		return errResp(req.ID, "query action only supports SELECT statements; use execute for INSERT/UPDATE/DELETE"), nil
	}

	conn, db, err := t.getConnection(ctx, req.AgentID, connName)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	maxRows := intDefault(req.Parameters, "max_rows", defaultMaxRows)
	if maxRows < 1 {
		maxRows = 1
	}
	connMaxRows := conn.MaxRows
	if connMaxRows <= 0 {
		connMaxRows = maxMaxRows
	}
	if maxRows > connMaxRows {
		maxRows = connMaxRows
	}

	timeout := defaultTimeout
	if conn.TimeoutSeconds > 0 {
		timeout = time.Duration(conn.TimeoutSeconds) * time.Second
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	params := anySliceParam(req.Parameters, "params")
	rows, err := db.QueryContext(queryCtx, sqlStr, params...)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("query failed: %v", err)), nil
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("get columns: %v", err)), nil
	}

	var resultRows [][]any
	truncated := false
	for rows.Next() {
		if len(resultRows) >= maxRows {
			truncated = true
			break
		}
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return errResp(req.ID, fmt.Sprintf("scan row: %v", err)), nil
		}
		// Convert []byte to string for JSON serialization.
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = string(b)
			}
		}
		resultRows = append(resultRows, values)
	}
	if err := rows.Err(); err != nil {
		return errResp(req.ID, fmt.Sprintf("row iteration: %v", err)), nil
	}
	if resultRows == nil {
		resultRows = [][]any{}
	}

	return okResp(req.ID, map[string]any{
		"columns":   columns,
		"rows":      resultRows,
		"row_count": len(resultRows),
		"truncated": truncated,
	}), nil
}

func (t *Tool) execDescribeTable(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	connName, err := strParam(req.Parameters, "connection")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	table, err := strParam(req.Parameters, "table")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	if err := validateTableName(table); err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	conn, db, err := t.getConnection(ctx, req.AgentID, connName)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	timeout := defaultTimeout
	if conn.TimeoutSeconds > 0 {
		timeout = time.Duration(conn.TimeoutSeconds) * time.Second
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	columns, err := describeTable(queryCtx, db, conn.Driver, table)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("describe table: %v", err)), nil
	}

	return okResp(req.ID, map[string]any{"columns": columns}), nil
}

func (t *Tool) execExecute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	connName, err := strParam(req.Parameters, "connection")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	sqlStr, err := strParam(req.Parameters, "sql")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	stmtType, err := classifyStatement(sqlStr)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	if stmtType == "select" {
		return errResp(req.ID, "execute action does not support SELECT; use query action instead"), nil
	}

	conn, db, err := t.getConnection(ctx, req.AgentID, connName)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	if conn.ReadOnly {
		return errResp(req.ID, fmt.Sprintf("connection %q is read-only", connName)), nil
	}

	timeout := defaultTimeout
	if conn.TimeoutSeconds > 0 {
		timeout = time.Duration(conn.TimeoutSeconds) * time.Second
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	params := anySliceParam(req.Parameters, "params")
	result, err := db.ExecContext(execCtx, sqlStr, params...)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("execute failed: %v", err)), nil
	}

	rowsAffected, _ := result.RowsAffected()

	return okResp(req.ID, map[string]any{
		"rows_affected":  rowsAffected,
		"statement_type": stmtType,
	}), nil
}

// getConnection resolves a named connection config and returns the pool.
func (t *Tool) getConnection(ctx context.Context, agentID, connName string) (*types.DatabaseConnection, *sql.DB, error) {
	conns, err := t.connectionConfigs(agentID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get connections: %w", err)
	}

	var conn *types.DatabaseConnection
	for i := range conns {
		if conns[i].Name == connName {
			conn = &conns[i]
			break
		}
	}
	if conn == nil {
		return nil, nil, fmt.Errorf("connection %q not found", connName)
	}

	// Resolve credentials from vault.
	username, password := "", ""
	if conn.UsernameRef != "" {
		username, _ = t.secretResolver(ctx, agentID, "", conn.UsernameRef)
	}
	if conn.PasswordRef != "" {
		password, _ = t.secretResolver(ctx, agentID, "", conn.PasswordRef)
	}

	dsn := buildDSN(conn, username, password)
	poolKey := fmt.Sprintf("%s:%s", agentID, connName)
	db, err := t.pools.GetOrCreate(poolKey, conn.Driver, dsn)
	if err != nil {
		return nil, nil, err
	}

	// Verify connectivity.
	pingCtx, cancel := context.WithTimeout(ctx, connTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return nil, nil, fmt.Errorf("database ping failed: %w", err)
	}

	return conn, db, nil
}

// buildDSN constructs a driver-specific connection string.
func buildDSN(conn *types.DatabaseConnection, username, password string) string {
	switch conn.Driver {
	case "postgres":
		return buildPostgresDSN(conn, username, password)
	case "mysql":
		return buildMySQLDSN(conn, username, password)
	case "sqlserver", "mssql":
		return buildSQLServerDSN(conn, username, password)
	case "sqlite":
		return conn.Database
	default:
		return conn.Database
	}
}

func buildPostgresDSN(conn *types.DatabaseConnection, username, password string) string {
	host := conn.Host
	if host == "" {
		host = "localhost"
	}
	port := conn.Port
	if port == 0 {
		port = 5432
	}
	sslMode := conn.SSLMode
	if sslMode == "" {
		sslMode = "require"
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		url.PathEscape(username), url.PathEscape(password),
		host, port, conn.Database, sslMode)
	return dsn
}

func buildMySQLDSN(conn *types.DatabaseConnection, username, password string) string {
	host := conn.Host
	if host == "" {
		host = "localhost"
	}
	port := conn.Port
	if port == 0 {
		port = 3306
	}
	// go-sql-driver/mysql DSN format: user:password@tcp(host:port)/dbname?params
	tls := "true"
	if conn.SSLMode == "disable" || conn.SSLMode == "false" {
		tls = "false"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?tls=%s&parseTime=true",
		username, password, host, port, conn.Database, tls)
}

func buildSQLServerDSN(conn *types.DatabaseConnection, username, password string) string {
	host := conn.Host
	if host == "" {
		host = "localhost"
	}
	port := conn.Port
	if port == 0 {
		port = 1433
	}
	q := url.Values{}
	q.Set("database", conn.Database)
	encrypt := "true"
	if conn.SSLMode == "disable" || conn.SSLMode == "false" {
		encrypt = "disable"
	}
	q.Set("encrypt", encrypt)
	u := &url.URL{
		Scheme:   "sqlserver",
		User:     url.UserPassword(username, password),
		Host:     fmt.Sprintf("%s:%d", host, port),
		RawQuery: q.Encode(),
	}
	return u.String()
}

// --- SQL Safety ---

// classifyStatement determines the type of SQL statement and checks for blocked patterns.
func classifyStatement(sqlStr string) (string, error) {
	trimmed := strings.TrimSpace(sqlStr)
	if trimmed == "" {
		return "", fmt.Errorf("empty SQL statement")
	}

	if containsMultiStatement(trimmed) {
		return "", fmt.Errorf("multiple statements are not allowed")
	}

	lower := strings.ToLower(trimmed)

	// Check blocked patterns.
	blockedPatterns := []struct {
		pattern string
		extra   string // additional check
	}{
		{"pg_read_file", ""},
		{"pg_read_binary_file", ""},
		{"pg_write_file", ""},
		{"lo_import", ""},
		{"lo_export", ""},
		{"load_file", ""},  // MySQL file read
		{"into outfile", ""}, // MySQL file write
		{"into dumpfile", ""},
	}
	for _, bp := range blockedPatterns {
		if strings.Contains(lower, bp.pattern) {
			return "", fmt.Errorf("blocked SQL pattern: %s", bp.pattern)
		}
	}
	// COPY FROM PROGRAM (Postgres code execution).
	if strings.Contains(lower, "copy") && strings.Contains(lower, "from program") {
		return "", fmt.Errorf("blocked SQL pattern: COPY FROM PROGRAM")
	}

	// Extract first keyword.
	firstWord := strings.ToUpper(firstSQLKeyword(trimmed))
	switch firstWord {
	case "SELECT", "WITH", "EXPLAIN", "SHOW", "DESCRIBE", "DESC":
		return "select", nil
	case "INSERT":
		return "insert", nil
	case "UPDATE":
		return "update", nil
	case "DELETE", "TRUNCATE":
		return "delete", nil
	case "CREATE", "ALTER", "DROP":
		return "delete", nil
	default:
		return "", fmt.Errorf("unsupported SQL statement type: %s", firstWord)
	}
}

// firstSQLKeyword returns the first word in a SQL string, skipping comments and whitespace.
func firstSQLKeyword(sql string) string {
	s := strings.TrimSpace(sql)
	// Skip line comments.
	for strings.HasPrefix(s, "--") {
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = strings.TrimSpace(s[nl+1:])
		} else {
			return ""
		}
	}
	// Skip block comments.
	for strings.HasPrefix(s, "/*") {
		if end := strings.Index(s, "*/"); end >= 0 {
			s = strings.TrimSpace(s[end+2:])
		} else {
			return ""
		}
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// containsMultiStatement checks for semicolons not inside single-quoted strings.
func containsMultiStatement(sql string) bool {
	inString := false
	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		if ch == '\'' {
			if inString && i+1 < len(sql) && sql[i+1] == '\'' {
				i++ // escaped quote
				continue
			}
			inString = !inString
			continue
		}
		if ch == ';' && !inString {
			// Check if there's any non-whitespace after the semicolon.
			rest := strings.TrimSpace(sql[i+1:])
			return rest != ""
		}
	}
	return false
}

// validateTableName checks that a table name is safe (alphanumeric, underscores, dots for schema.table).
func validateTableName(name string) error {
	if name == "" {
		return fmt.Errorf("table name must not be empty")
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '.' {
			return fmt.Errorf("invalid character %q in table name", string(r))
		}
	}
	if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") {
		return fmt.Errorf("table name must not start or end with a dot")
	}
	return nil
}

// describeTable returns column metadata for a table, using driver-appropriate SQL.
func describeTable(ctx context.Context, db *sql.DB, driver, table string) ([]map[string]any, error) {
	var query string
	var args []any

	switch driver {
	case "sqlite":
		query = fmt.Sprintf("PRAGMA table_info(%s)", table)
	case "postgres":
		query = `SELECT column_name, data_type, is_nullable, column_default,
			CASE WHEN pk.column_name IS NOT NULL THEN true ELSE false END as primary_key
			FROM information_schema.columns c
			LEFT JOIN (
				SELECT ku.column_name FROM information_schema.table_constraints tc
				JOIN information_schema.key_column_usage ku ON tc.constraint_name = ku.constraint_name
				WHERE tc.table_name = $1 AND tc.constraint_type = 'PRIMARY KEY'
			) pk ON c.column_name = pk.column_name
			WHERE c.table_name = $1
			ORDER BY c.ordinal_position`
		args = []any{table}
	case "mysql":
		query = `SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT,
			IF(COLUMN_KEY = 'PRI', 'true', 'false') as primary_key
			FROM information_schema.COLUMNS
			WHERE TABLE_NAME = ? AND TABLE_SCHEMA = DATABASE()
			ORDER BY ORDINAL_POSITION`
		args = []any{table}
	case "sqlserver", "mssql":
		query = `SELECT c.COLUMN_NAME, c.DATA_TYPE, c.IS_NULLABLE, c.COLUMN_DEFAULT,
			CASE WHEN pk.COLUMN_NAME IS NOT NULL THEN 'true' ELSE 'false' END as primary_key
			FROM INFORMATION_SCHEMA.COLUMNS c
			LEFT JOIN (
				SELECT ku.COLUMN_NAME FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
				JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE ku ON tc.CONSTRAINT_NAME = ku.CONSTRAINT_NAME
				WHERE tc.TABLE_NAME = @p1 AND tc.CONSTRAINT_TYPE = 'PRIMARY KEY'
			) pk ON c.COLUMN_NAME = pk.COLUMN_NAME
			WHERE c.TABLE_NAME = @p1
			ORDER BY c.ORDINAL_POSITION`
		args = []any{table}
	default:
		return nil, fmt.Errorf("describe_table not supported for driver %q", driver)
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var result []map[string]any
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}

		row := make(map[string]any)
		if driver == "sqlite" {
			// PRAGMA table_info returns: cid, name, type, notnull, dflt_value, pk
			if len(values) >= 6 {
				row["name"] = asString(values[1])
				row["type"] = asString(values[2])
				notnull, _ := values[3].(int64)
				row["nullable"] = notnull == 0
				row["default"] = asString(values[4])
				pk, _ := values[5].(int64)
				row["primary_key"] = pk > 0
			}
		} else {
			// Standard information_schema columns.
			row["name"] = asString(values[0])
			row["type"] = asString(values[1])
			row["nullable"] = asString(values[2]) == "YES"
			row["default"] = asString(values[3])
			row["primary_key"] = asString(values[4]) == "true"
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if result == nil {
		result = []map[string]any{}
	}
	return result, nil
}

// --- Helpers ---

func asString(v any) string {
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	default:
		return fmt.Sprint(v)
	}
}

func okResp(reqID string, result any) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, true, result, "", 0)
	return &resp
}

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

func strParam(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing required parameter: %s", key)
	}
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("parameter %s must be a non-empty string", key)
	}
	return s, nil
}

func intDefault(params map[string]any, key string, def int) int {
	v, ok := params[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		parsed, err := strconv.Atoi(n)
		if err == nil {
			return parsed
		}
	}
	return def
}

func anySliceParam(params map[string]any, key string) []any {
	v, ok := params[key]
	if !ok {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	return list
}
