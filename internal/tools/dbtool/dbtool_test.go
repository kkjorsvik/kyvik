package dbtool

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/pkg/types"

	_ "modernc.org/sqlite"
)

// newTestTool creates a tool with a shared SQLite file database.
func newTestTool(t *testing.T) (*Tool, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	configs := func(agentID string) ([]types.DatabaseConnection, error) {
		return []types.DatabaseConnection{
			{
				Name:     "testdb",
				Driver:   "sqlite",
				Database: dbPath,
			},
		}, nil
	}
	secrets := func(ctx context.Context, agentID, teamID, key string) (string, error) {
		return "", nil
	}
	tool := New(configs, secrets)
	t.Cleanup(func() { tool.Stop() })
	return tool, dbPath
}

func newTestToolWithData(t *testing.T) *Tool {
	t.Helper()
	tool, dbPath := newTestTool(t)

	// Create table and insert data directly.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE test_users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO test_users (id, name) VALUES (1, 'alice'), (2, 'bob')")
	if err != nil {
		t.Fatal(err)
	}

	return tool
}

func newTestToolReadOnly(t *testing.T) *Tool {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// Create table and insert data directly.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE test_users (id INTEGER PRIMARY KEY, name TEXT NOT NULL)")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, err = db.Exec("INSERT INTO test_users (id, name) VALUES (1, 'alice'), (2, 'bob')")
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	configs := func(agentID string) ([]types.DatabaseConnection, error) {
		return []types.DatabaseConnection{
			{
				Name:     "testdb",
				Driver:   "sqlite",
				Database: dbPath,
				ReadOnly: true,
			},
		}, nil
	}
	secrets := func(ctx context.Context, agentID, teamID, key string) (string, error) {
		return "", nil
	}
	tool := New(configs, secrets)
	t.Cleanup(func() { tool.Stop() })
	return tool
}

func makeReq(action string, params map[string]any) ktp.ToolRequest {
	return ktp.NewToolRequest("test-agent", "database", action, params)
}

func TestDeclaration(t *testing.T) {
	tool, _ := newTestTool(t)
	decl := tool.Declaration()
	if decl.Name != "database" {
		t.Errorf("expected name database, got %s", decl.Name)
	}
	if decl.MinTier != ktp.TierReader {
		t.Errorf("expected min tier reader, got %s", decl.MinTier)
	}
	if len(decl.Actions) != 4 {
		t.Errorf("expected 4 actions, got %d", len(decl.Actions))
	}
	if !tool.Inline() {
		t.Error("expected Inline() to return true")
	}
}

func TestExecute_UnknownAction(t *testing.T) {
	tool, _ := newTestTool(t)
	resp, err := tool.Execute(context.Background(), makeReq("nonexistent", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected unknown action to fail")
	}
}

func TestExecute_ListConnections(t *testing.T) {
	tool, _ := newTestTool(t)
	resp, err := tool.Execute(context.Background(), makeReq("list_connections", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	conns := result["connections"].([]map[string]any)
	if len(conns) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(conns))
	}
	if conns[0]["name"] != "testdb" {
		t.Errorf("expected name testdb, got %v", conns[0]["name"])
	}
	if conns[0]["driver"] != "sqlite" {
		t.Errorf("expected driver sqlite, got %v", conns[0]["driver"])
	}
}

func TestExecute_Query(t *testing.T) {
	tool := newTestToolWithData(t)

	resp, err := tool.Execute(context.Background(), makeReq("query", map[string]any{
		"connection": "testdb",
		"sql":        "SELECT id, name FROM test_users ORDER BY id",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	columns := result["columns"].([]string)
	if len(columns) != 2 || columns[0] != "id" || columns[1] != "name" {
		t.Errorf("unexpected columns: %v", columns)
	}
	rowCount := result["row_count"].(int)
	if rowCount != 2 {
		t.Errorf("expected 2 rows, got %d", rowCount)
	}
	if result["truncated"] != false {
		t.Error("expected truncated=false")
	}
}

func TestExecute_Query_MaxRows(t *testing.T) {
	tool := newTestToolWithData(t)

	resp, err := tool.Execute(context.Background(), makeReq("query", map[string]any{
		"connection": "testdb",
		"sql":        "SELECT id, name FROM test_users ORDER BY id",
		"max_rows":   1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["row_count"].(int) != 1 {
		t.Errorf("expected 1 row, got %d", result["row_count"])
	}
	if result["truncated"] != true {
		t.Error("expected truncated=true")
	}
}

func TestExecute_Query_Parameterized(t *testing.T) {
	tool := newTestToolWithData(t)

	resp, err := tool.Execute(context.Background(), makeReq("query", map[string]any{
		"connection": "testdb",
		"sql":        "SELECT name FROM test_users WHERE id = ?",
		"params":     []any{1},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["row_count"].(int) != 1 {
		t.Errorf("expected 1 row, got %d", result["row_count"])
	}
	rows := result["rows"].([][]any)
	if len(rows) == 0 {
		t.Fatal("expected at least 1 row")
	}
	if asString(rows[0][0]) != "alice" {
		t.Errorf("expected alice, got %v", rows[0][0])
	}
}

func TestExecute_Query_RejectsWrite(t *testing.T) {
	tool := newTestToolWithData(t)

	resp, err := tool.Execute(context.Background(), makeReq("query", map[string]any{
		"connection": "testdb",
		"sql":        "INSERT INTO test_users VALUES (3, 'charlie')",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected query to reject INSERT")
	}
}

func TestExecute_Execute(t *testing.T) {
	tool := newTestToolWithData(t)

	resp, err := tool.Execute(context.Background(), makeReq("execute", map[string]any{
		"connection": "testdb",
		"sql":        "INSERT INTO test_users (id, name) VALUES (?, ?)",
		"params":     []any{3, "charlie"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if result["rows_affected"].(int64) != 1 {
		t.Errorf("expected 1 row affected, got %v", result["rows_affected"])
	}
	if result["statement_type"] != "insert" {
		t.Errorf("expected statement_type insert, got %v", result["statement_type"])
	}
}

func TestExecute_Execute_RejectsSelect(t *testing.T) {
	tool := newTestToolWithData(t)

	resp, err := tool.Execute(context.Background(), makeReq("execute", map[string]any{
		"connection": "testdb",
		"sql":        "SELECT * FROM test_users",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected execute to reject SELECT")
	}
}

func TestExecute_Execute_BlockedPattern(t *testing.T) {
	tool := newTestToolWithData(t)

	patterns := []string{
		"COPY users FROM PROGRAM 'ls'",
		"SELECT pg_read_file('/etc/passwd')",
		"SELECT lo_import('/etc/passwd')",
		"SELECT load_file('/etc/passwd')",
		"SELECT 1 INTO OUTFILE '/tmp/x'",
	}
	for _, sql := range patterns {
		t.Run(sql, func(t *testing.T) {
			resp, err := tool.Execute(context.Background(), makeReq("execute", map[string]any{
				"connection": "testdb",
				"sql":        sql,
			}))
			if err != nil {
				t.Fatal(err)
			}
			if resp.Success {
				t.Errorf("expected blocked pattern to fail: %s", sql)
			}
		})
	}
}

func TestExecute_Execute_MultiStatement(t *testing.T) {
	tool := newTestToolWithData(t)

	resp, err := tool.Execute(context.Background(), makeReq("execute", map[string]any{
		"connection": "testdb",
		"sql":        "INSERT INTO test_users VALUES (3, 'x'); DROP TABLE test_users",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected multi-statement to be rejected")
	}
}

func TestExecute_Execute_ReadOnlyConnection(t *testing.T) {
	tool := newTestToolReadOnly(t)

	resp, err := tool.Execute(context.Background(), makeReq("execute", map[string]any{
		"connection": "testdb",
		"sql":        "INSERT INTO test_users VALUES (3, 'x')",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected read-only connection to reject INSERT")
	}
	if !strings.Contains(resp.Error, "read-only") {
		t.Errorf("expected read-only error, got: %s", resp.Error)
	}
}

func TestExecute_DescribeTable(t *testing.T) {
	tool := newTestToolWithData(t)

	resp, err := tool.Execute(context.Background(), makeReq("describe_table", map[string]any{
		"connection": "testdb",
		"table":      "test_users",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	result := resp.Result.(map[string]any)
	columns := result["columns"].([]map[string]any)
	if len(columns) < 2 {
		t.Errorf("expected at least 2 columns, got %d", len(columns))
	}
	// Check that we get name and type fields.
	found := false
	for _, col := range columns {
		if col["name"] == "id" {
			found = true
			if col["primary_key"] != true {
				t.Error("expected id to be primary key")
			}
		}
	}
	if !found {
		t.Error("expected to find column 'id'")
	}
}

func TestExecute_DescribeTable_InvalidName(t *testing.T) {
	tool := newTestToolWithData(t)

	resp, err := tool.Execute(context.Background(), makeReq("describe_table", map[string]any{
		"connection": "testdb",
		"table":      "users; DROP TABLE users",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected invalid table name to be rejected")
	}
}

func TestExecute_ConnectionNotFound(t *testing.T) {
	tool := newTestToolWithData(t)

	resp, err := tool.Execute(context.Background(), makeReq("query", map[string]any{
		"connection": "nonexistent",
		"sql":        "SELECT 1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Success {
		t.Error("expected connection not found error")
	}
	if !strings.Contains(resp.Error, "not found") {
		t.Errorf("expected 'not found' error, got: %s", resp.Error)
	}
}

func TestClassifyStatement(t *testing.T) {
	tests := []struct {
		sql      string
		expected string
		err      bool
	}{
		{"SELECT * FROM users", "select", false},
		{"  select id from users", "select", false},
		{"WITH cte AS (SELECT 1) SELECT * FROM cte", "select", false},
		{"INSERT INTO users (name) VALUES ($1)", "insert", false},
		{"UPDATE users SET name = $1", "update", false},
		{"DELETE FROM users WHERE id = $1", "delete", false},
		{"TRUNCATE TABLE users", "delete", false},
		{"CREATE TABLE foo (id int)", "delete", false},
		{"ALTER TABLE foo ADD bar text", "delete", false},
		{"DROP TABLE foo", "delete", false},
		{"EXPLAIN SELECT 1", "select", false},
		{"SHOW TABLES", "select", false},
		{"-- comment\nSELECT 1", "select", false},
		{"/* block */SELECT 1", "select", false},
		// Blocked patterns.
		{"COPY users FROM PROGRAM 'ls'", "", true},
		{"SELECT pg_read_file('/etc/passwd')", "", true},
		{"SELECT lo_import('/etc/passwd')", "", true},
		{"SELECT load_file('/etc/passwd')", "", true},
		{"SELECT 1 INTO OUTFILE '/tmp/x'", "", true},
		// Multi-statement.
		{"SELECT 1; DROP TABLE users", "", true},
		{"", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			result, err := classifyStatement(tt.sql)
			if tt.err {
				if err == nil {
					t.Errorf("expected error for %q", tt.sql)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error for %q: %v", tt.sql, err)
				return
			}
			if result != tt.expected {
				t.Errorf("expected %q, got %q for %q", tt.expected, result, tt.sql)
			}
		})
	}
}

func TestContainsMultiStatement(t *testing.T) {
	tests := []struct {
		sql  string
		want bool
	}{
		{"SELECT 1", false},
		{"SELECT 1;", false},                    // trailing semicolon is OK
		{"SELECT 1; SELECT 2", true},            // two statements
		{"SELECT 'a;b' FROM t", false},          // semicolon in string
		{"SELECT 'it''s' FROM t; DROP t", true}, // escaped quote + multi
	}
	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			got := containsMultiStatement(tt.sql)
			if got != tt.want {
				t.Errorf("containsMultiStatement(%q) = %v, want %v", tt.sql, got, tt.want)
			}
		})
	}
}

func TestValidateTableName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"users", true},
		{"public.users", true},
		{"my_table", true},
		{"", false},
		{"users; DROP", false},
		{".users", false},
		{"users.", false},
		{"users$(cmd)", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTableName(tt.name)
			if tt.valid && err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Error("expected invalid, got nil error")
			}
		})
	}
}

func TestPoolManager(t *testing.T) {
	pm := NewPoolManager()
	defer pm.Close()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db1, err := pm.GetOrCreate("test", "sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if db1 == nil {
		t.Fatal("expected non-nil db")
	}

	// Same key returns same pool.
	db2, err := pm.GetOrCreate("test", "sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if db1 != db2 {
		t.Error("expected same pool for same key")
	}

	// Different key returns different pool.
	dbPath2 := filepath.Join(t.TempDir(), "test2.db")
	db3, err := pm.GetOrCreate("other", "sqlite", dbPath2)
	if err != nil {
		t.Fatal(err)
	}
	if db1 == db3 {
		t.Error("expected different pool for different key")
	}
}

func TestPoolManager_UnsupportedDriver(t *testing.T) {
	pm := NewPoolManager()
	defer pm.Close()

	_, err := pm.GetOrCreate("test", "oracle", "fake-dsn")
	if err == nil {
		t.Error("expected error for unsupported driver")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("expected unsupported error, got: %v", err)
	}
}

func TestBuildDSN(t *testing.T) {
	tests := []struct {
		name     string
		conn     types.DatabaseConnection
		user     string
		pass     string
		contains string
	}{
		{
			"postgres",
			types.DatabaseConnection{Driver: "postgres", Host: "db.example.com", Port: 5432, Database: "mydb"},
			"user", "pass",
			"postgres://user:pass@db.example.com:5432/mydb",
		},
		{
			"mysql",
			types.DatabaseConnection{Driver: "mysql", Host: "db.example.com", Port: 3306, Database: "mydb"},
			"user", "pass",
			"user:pass@tcp(db.example.com:3306)/mydb",
		},
		{
			"sqlserver",
			types.DatabaseConnection{Driver: "sqlserver", Host: "db.example.com", Port: 1433, Database: "mydb"},
			"user", "pass",
			"sqlserver://user:pass@db.example.com:1433",
		},
		{
			"sqlite",
			types.DatabaseConnection{Driver: "sqlite", Database: "/tmp/test.db"},
			"", "",
			"/tmp/test.db",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsn := buildDSN(&tt.conn, tt.user, tt.pass)
			if !strings.Contains(dsn, tt.contains) {
				t.Errorf("expected DSN to contain %q, got: %s", tt.contains, dsn)
			}
		})
	}
}

func TestBuildDSN_DefaultPorts(t *testing.T) {
	// Postgres with no port should default to 5432.
	dsn := buildDSN(&types.DatabaseConnection{Driver: "postgres", Database: "db"}, "u", "p")
	if !strings.Contains(dsn, ":5432/") {
		t.Errorf("expected default port 5432, got: %s", dsn)
	}

	// MySQL with no port should default to 3306.
	dsn = buildDSN(&types.DatabaseConnection{Driver: "mysql", Database: "db"}, "u", "p")
	if !strings.Contains(dsn, ":3306)") {
		t.Errorf("expected default port 3306, got: %s", dsn)
	}

	// SQL Server with no port should default to 1433.
	dsn = buildDSN(&types.DatabaseConnection{Driver: "sqlserver", Database: "db"}, "u", "p")
	if !strings.Contains(dsn, ":1433") {
		t.Errorf("expected default port 1433, got: %s", dsn)
	}
}

// Ensure the file is used to avoid lint warnings.
var _ = os.Getenv
