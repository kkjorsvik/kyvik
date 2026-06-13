package ktp

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// --- PostgreSQL-backed test helpers ---

// openTestDB returns a PostgreSQL database connection for tests.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return testutil.RequirePostgres(t).DB
}

// dbAuditStore wraps *sql.DB to satisfy the AuditStore interface.
type dbAuditStore struct{ db *sql.DB }

func (s *dbAuditStore) InsertAuditEntry(ctx context.Context, entry types.AuditEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (agent_id, event_type, action, resource, decision, details, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		entry.AgentID, string(entry.EventType), entry.Action,
		entry.Resource, entry.Decision, entry.Details, entry.Timestamp,
	)
	return err
}

// queryAuditEntries reads all audit entries for a given agent from the database.
func queryAuditEntries(t *testing.T, db *sql.DB, agentID string) []types.AuditEntry {
	t.Helper()
	rows, err := db.Query(
		`SELECT agent_id, event_type, action, resource, decision, details, created_at
		 FROM audit_log WHERE agent_id = $1 ORDER BY created_at`, agentID)
	if err != nil {
		t.Fatalf("query audit entries: %v", err)
	}
	defer rows.Close()

	var entries []types.AuditEntry
	for rows.Next() {
		var e types.AuditEntry
		var eventType string
		if err := rows.Scan(&e.AgentID, &eventType, &e.Action,
			&e.Resource, &e.Decision, &e.Details, &e.Timestamp); err != nil {
			t.Fatalf("scan audit entry: %v", err)
		}
		e.EventType = types.EventType(eventType)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}
	return entries
}

// queryAllAuditEntries reads all audit entries from the database regardless of agent.
func queryAllAuditEntries(t *testing.T, db *sql.DB) []types.AuditEntry {
	t.Helper()
	rows, err := db.Query(
		`SELECT id, agent_id, event_type, action, resource, decision, details, created_at
		 FROM audit_log ORDER BY created_at`)
	if err != nil {
		t.Fatalf("query all audit entries: %v", err)
	}
	defer rows.Close()

	var entries []types.AuditEntry
	for rows.Next() {
		var e types.AuditEntry
		var eventType string
		if err := rows.Scan(&e.ID, &e.AgentID, &eventType, &e.Action,
			&e.Resource, &e.Decision, &e.Details, &e.Timestamp); err != nil {
			t.Fatalf("scan audit entry: %v", err)
		}
		e.EventType = types.EventType(eventType)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}
	return entries
}

// newIntegrationExecutor wires a full KTP pipeline backed by database.
func newIntegrationExecutor(t *testing.T, agents map[string]*types.AgentConfig, tools []Tool) (*Executor, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	auditStore := &dbAuditStore{db: db}
	auditLogger := NewStoreAuditLogger(auditStore)

	agentStore := &mockAgentStore{agents: agents}
	gate := NewPermissionGate(agentStore, auditLogger)

	registry := NewRegistry()
	for _, tool := range tools {
		if err := registry.Register(tool); err != nil {
			t.Fatalf("register tool: %v", err)
		}
	}

	executor := NewExecutor(registry, gate, auditLogger, ExecutorConfig{})
	return executor, db
}

// --- Integration tool helpers ---

// integrationEchoTool is a simple echo tool for integration tests.
type integrationEchoTool struct{}

func (e *integrationEchoTool) Declaration() ToolDeclaration {
	return ToolDeclaration{
		Name:    "echo",
		Version: "1.0.0",
		MinTier: TierWriter,
		Actions: []ActionSpec{{
			Name:        "echo",
			Description: "Echo the message back",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]JSONSchema{
					"message": {Type: "string"},
				},
				Required: []string{"message"},
			},
			Returns: JSONSchema{
				Type: "object",
				Properties: map[string]JSONSchema{
					"echo": {Type: "string"},
				},
			},
		}},
	}
}

func (e *integrationEchoTool) Execute(_ context.Context, req ToolRequest) (*ToolResponse, error) {
	msg, _ := req.Parameters["message"].(string)
	resp := NewToolResponse(req.ID, true, map[string]any{"echo": msg}, "", 0)
	return &resp, nil
}

// capTool is a tool with capability-gated actions for integration tests.
type capTool struct{}

func (c *capTool) Declaration() ToolDeclaration {
	return ToolDeclaration{
		Name:    "cap-tool",
		Version: "1.0.0",
		MinTier: TierReader,
		Actions: []ActionSpec{
			{
				Name:       "read-data",
				Parameters: JSONSchema{Type: "object"},
			},
			{
				Name:       "write-data",
				Parameters: JSONSchema{Type: "object"},
				RequiredCapabilities: []Capability{
					{Type: "filesystem", Access: "write", Resource: "{workspace}/*"},
				},
			},
		},
	}
}

func (c *capTool) Execute(_ context.Context, req ToolRequest) (*ToolResponse, error) {
	resp := NewToolResponse(req.ID, true, map[string]any{"action": req.Action}, "", 0)
	return &resp, nil
}

// --- Integration tests ---

func TestIntegration_FullPipeline(t *testing.T) {
	agents := map[string]*types.AgentConfig{
		"worker-1": {ID: "worker-1", Template: "worker"},
	}
	exec, db := newIntegrationExecutor(t, agents, []Tool{&integrationEchoTool{}})

	req := NewToolRequest("worker-1", "echo", "echo", map[string]any{"message": "hello integration"})
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", resp.Result)
	}
	if result["echo"] != "hello integration" {
		t.Fatalf("expected echo='hello integration', got %v", result["echo"])
	}

	// Verify audit entries persisted in the database.
	entries := queryAuditEntries(t, db, "worker-1")
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 audit entries (permission + execution), got %d", len(entries))
	}

	// Check permission entry.
	var hasPermission, hasExecution bool
	for _, e := range entries {
		if e.EventType == types.EventPermission && e.Decision == "allowed" {
			hasPermission = true
		}
		if e.EventType == types.EventToolCall && e.Decision == "success" {
			hasExecution = true
		}
	}
	if !hasPermission {
		t.Fatal("expected an 'allowed' permission audit entry in the database")
	}
	if !hasExecution {
		t.Fatal("expected a 'success' execution audit entry in the database")
	}
}

func TestIntegration_PermissionDenied_TierTooLow(t *testing.T) {
	// Echo tool requires TierWriter; reader agent should be denied.
	agents := map[string]*types.AgentConfig{
		"reader-1": {ID: "reader-1", Template: "reader"},
	}
	exec, db := newIntegrationExecutor(t, agents, []Tool{&integrationEchoTool{}})

	req := NewToolRequest("reader-1", "echo", "echo", map[string]any{"message": "should fail"})
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected denial for reader calling writer-tier tool")
	}
	if !strings.Contains(resp.Error, "tier") {
		t.Fatalf("expected tier-related denial, got: %s", resp.Error)
	}

	// Verify denial audit entry persisted.
	entries := queryAuditEntries(t, db, "reader-1")
	if len(entries) == 0 {
		t.Fatal("expected at least 1 audit entry for denied request")
	}

	var foundDenied bool
	for _, e := range entries {
		if e.Decision == "denied" {
			foundDenied = true
			break
		}
	}
	if !foundDenied {
		t.Fatal("expected a 'denied' audit entry in the database")
	}
}

func TestIntegration_PermissionDenied_ToolGrantRestriction(t *testing.T) {
	// Agent has ToolGrants limited to "allowed-tool" — calling "echo" should be denied.
	agents := map[string]*types.AgentConfig{
		"restricted-1": {
			ID:         "restricted-1",
			Template:   "worker",
			ToolGrants: []string{"allowed-tool"},
		},
	}
	exec, db := newIntegrationExecutor(t, agents, []Tool{&integrationEchoTool{}})

	req := NewToolRequest("restricted-1", "echo", "echo", map[string]any{"message": "blocked"})
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected denial due to tool grants restriction")
	}
	if !strings.Contains(resp.Error, "tool grants") {
		t.Fatalf("expected 'tool grants' in denial reason, got: %s", resp.Error)
	}

	// Verify denial audit entry with grant restriction reason.
	entries := queryAuditEntries(t, db, "restricted-1")
	var foundGrantDenial bool
	for _, e := range entries {
		if e.Decision == "denied" && strings.Contains(e.Details, "tool grants") {
			foundGrantDenial = true
			break
		}
	}
	if !foundGrantDenial {
		t.Fatal("expected a denied audit entry mentioning 'tool grants'")
	}
}

func TestIntegration_InvalidParams(t *testing.T) {
	// Echo tool requires "message" parameter; omitting it should cause validation failure.
	agents := map[string]*types.AgentConfig{
		"worker-1": {ID: "worker-1", Template: "worker"},
	}
	exec, db := newIntegrationExecutor(t, agents, []Tool{&integrationEchoTool{}})

	// Send request with no parameters — missing required "message".
	req := NewToolRequest("worker-1", "echo", "echo", map[string]any{})
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected validation failure for missing required param")
	}
	if !strings.Contains(resp.Error, "validation failed") {
		t.Fatalf("expected 'validation failed' in error, got: %s", resp.Error)
	}

	// Validation failures happen before permission check, so no audit entries
	// are expected (the executor short-circuits before the gate/audit).
	entries := queryAuditEntries(t, db, "worker-1")
	if len(entries) != 0 {
		t.Fatalf("expected no audit entries for validation failure (short-circuit), got %d", len(entries))
	}
}

func TestIntegration_MultipleToolsRegistered(t *testing.T) {
	agents := map[string]*types.AgentConfig{
		"worker-1": {ID: "worker-1", Template: "worker"},
		"reader-1": {ID: "reader-1", Template: "reader"},
	}

	// capTool has MinTier=reader, echoTool has MinTier=writer.
	tools := []Tool{&integrationEchoTool{}, &capTool{}}
	exec, _ := newIntegrationExecutor(t, agents, tools)

	// Worker can use echo tool.
	req := NewToolRequest("worker-1", "echo", "echo", map[string]any{"message": "hi"})
	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("worker should access echo tool, got: %s", resp.Error)
	}

	// Worker can use cap-tool's read-data action.
	req2 := NewToolRequest("worker-1", "cap-tool", "read-data", nil)
	resp2, err := exec.Execute(context.Background(), req2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp2.Success {
		t.Fatalf("worker should access cap-tool read-data, got: %s", resp2.Error)
	}

	// Reader can use cap-tool (MinTier=reader) but NOT echo tool (MinTier=writer).
	req3 := NewToolRequest("reader-1", "cap-tool", "read-data", nil)
	resp3, err := exec.Execute(context.Background(), req3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp3.Success {
		t.Fatalf("reader should access cap-tool read-data, got: %s", resp3.Error)
	}

	req4 := NewToolRequest("reader-1", "echo", "echo", map[string]any{"message": "nope"})
	resp4, err := exec.Execute(context.Background(), req4)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp4.Success {
		t.Fatal("reader should NOT access echo tool (writer tier)")
	}
}

func TestIntegration_AuditTrailComplete(t *testing.T) {
	agents := map[string]*types.AgentConfig{
		"worker-1": {ID: "worker-1", Template: "worker"},
		"reader-1": {ID: "reader-1", Template: "reader"},
	}
	exec, db := newIntegrationExecutor(t, agents, []Tool{&integrationEchoTool{}})

	// 1. Successful execution (worker).
	req1 := NewToolRequest("worker-1", "echo", "echo", map[string]any{"message": "ok"})
	if _, err := exec.Execute(context.Background(), req1); err != nil {
		t.Fatalf("req1: %v", err)
	}

	// 2. Permission denied (reader → writer tool).
	req2 := NewToolRequest("reader-1", "echo", "echo", map[string]any{"message": "denied"})
	if _, err := exec.Execute(context.Background(), req2); err != nil {
		t.Fatalf("req2: %v", err)
	}

	// 3. Validation failure (missing required param — no audit entries for this).
	req3 := NewToolRequest("worker-1", "echo", "echo", map[string]any{})
	if _, err := exec.Execute(context.Background(), req3); err != nil {
		t.Fatalf("req3: %v", err)
	}

	// Query all audit entries.
	entries := queryAllAuditEntries(t, db)

	// Expect: req1 → permission(allowed) + execution(success) = 2
	//         req2 → permission(denied) = 1
	//         req3 → validation short-circuit = 0
	// Total: 3
	if len(entries) != 3 {
		for i, e := range entries {
			t.Logf("  entry[%d]: agent=%s event=%s action=%s decision=%s", i, e.AgentID, e.EventType, e.Action, e.Decision)
		}
		t.Fatalf("expected 3 audit entries, got %d", len(entries))
	}

	// Verify the mix of decisions.
	decisions := map[string]int{}
	for _, e := range entries {
		decisions[e.Decision]++
	}
	if decisions["allowed"] != 1 {
		t.Fatalf("expected 1 'allowed' decision, got %d", decisions["allowed"])
	}
	if decisions["success"] != 1 {
		t.Fatalf("expected 1 'success' decision, got %d", decisions["success"])
	}
	if decisions["denied"] != 1 {
		t.Fatalf("expected 1 'denied' decision, got %d", decisions["denied"])
	}
}

func TestIntegration_CapabilityCheck(t *testing.T) {
	// Reader agent: has default reader caps (filesystem/read/{workspace}/*).
	// capTool "read-data" → no required capabilities → allowed.
	// capTool "write-data" → requires filesystem/write/{workspace}/* → denied for reader.
	agents := map[string]*types.AgentConfig{
		"reader-1": {ID: "reader-1", Template: "reader"},
		"worker-1": {ID: "worker-1", Template: "worker"},
	}
	exec, db := newIntegrationExecutor(t, agents, []Tool{&capTool{}})

	// Reader can read.
	req1 := NewToolRequest("reader-1", "cap-tool", "read-data", nil)
	resp1, err := exec.Execute(context.Background(), req1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp1.Success {
		t.Fatalf("reader should access read-data, got: %s", resp1.Error)
	}

	// Reader cannot write (missing filesystem/write capability).
	req2 := NewToolRequest("reader-1", "cap-tool", "write-data", nil)
	resp2, err := exec.Execute(context.Background(), req2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp2.Success {
		t.Fatal("reader should NOT access write-data (missing capability)")
	}
	if !strings.Contains(resp2.Error, "missing capability") {
		t.Fatalf("expected 'missing capability' in denial, got: %s", resp2.Error)
	}

	// Worker can write (worker template → writer tier → has filesystem/write).
	req3 := NewToolRequest("worker-1", "cap-tool", "write-data", nil)
	resp3, err := exec.Execute(context.Background(), req3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp3.Success {
		t.Fatalf("worker should access write-data, got: %s", resp3.Error)
	}

	// Verify audit trail captures both allowed and denied capability checks.
	entries := queryAuditEntries(t, db, "reader-1")

	var allowedCount, deniedCount int
	for _, e := range entries {
		switch e.Decision {
		case "allowed":
			allowedCount++
		case "denied":
			deniedCount++
		}
	}
	if allowedCount < 1 {
		t.Fatal("expected at least 1 allowed entry for reader-1")
	}
	if deniedCount < 1 {
		t.Fatal("expected at least 1 denied entry for reader-1")
	}

	// Verify the denied entry mentions the capability in details.
	for _, e := range entries {
		if e.Decision == "denied" {
			var details map[string]any
			if err := json.Unmarshal([]byte(e.Details), &details); err == nil {
				reason, _ := details["reason"].(string)
				if !strings.Contains(reason, "capability") {
					t.Fatalf("denied entry should mention 'capability', got reason: %s", reason)
				}
			}
		}
	}

	// Small delay to ensure worker entries are also persisted.
	_ = time.Now() // no-op, entries are synchronous
	workerEntries := queryAuditEntries(t, db, "worker-1")
	var workerAllowed bool
	for _, e := range workerEntries {
		if e.Decision == "allowed" && e.Resource == "cap-tool.write-data" {
			workerAllowed = true
			break
		}
	}
	if !workerAllowed {
		t.Fatal("expected worker-1 to have an allowed entry for cap-tool.write-data")
	}
}
