package ktp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// AuditStore is the narrow persistence interface for the audit logger.
type AuditStore interface {
	InsertAuditEntry(ctx context.Context, entry types.AuditEntry) error
}

// StoreAuditLogger implements AuditLogger by writing to the audit_log table
// through a narrow AuditStore interface.
type StoreAuditLogger struct {
	store AuditStore
}

// NewStoreAuditLogger creates a StoreAuditLogger with the given store.
func NewStoreAuditLogger(store AuditStore) *StoreAuditLogger {
	return &StoreAuditLogger{store: store}
}

// LogToolPermission writes a permission check result to the audit log.
func (l *StoreAuditLogger) LogToolPermission(ctx context.Context, result PermissionResult) error {
	decision := "denied"
	if result.Allowed {
		decision = "allowed"
	}

	details, err := json.Marshal(result)
	if err != nil {
		slog.Warn("failed to marshal permission result for audit", "error", err)
		details = fmt.Appendf(nil, `{"error":"marshal failed: %v"}`, err)
	}

	entry := types.AuditEntry{
		ID:        ulid.Make().String(),
		AgentID:   result.AgentID,
		EventType: types.EventPermission,
		Action:    "tool_permission",
		Resource:  result.Tool + "." + result.Action,
		Decision:  decision,
		Details:   string(details),
		Timestamp: time.Now(),
	}

	return l.store.InsertAuditEntry(ctx, entry)
}

// LogToolExecution writes a tool execution result to the audit log.
func (l *StoreAuditLogger) LogToolExecution(ctx context.Context, req ToolRequest, resp *ToolResponse) error {
	decision := "success"
	if !resp.Success {
		decision = "failure"
	}

	// Truncate parameters for the audit log.
	paramStr := truncateJSON(req.Parameters, 500)

	summary := map[string]any{
		"tool":             req.Tool,
		"action":           req.Action,
		"success":          resp.Success,
		"execution_ms":     resp.ExecutionMs,
		"permission_token": "[REDACTED]",
		"parameters":       paramStr,
		"execution_mode":   executionMode(resp),
	}
	if req.Tier != "" {
		summary["tier"] = req.Tier
		summary["risk_level"] = riskLevelForTier(req.Tier)
	}
	if resp.Error != "" {
		summary["error"] = resp.Error
	}
	if resp.SandboxID != "" {
		summary["sandbox_id"] = resp.SandboxID
	}

	details, err := json.Marshal(summary)
	if err != nil {
		slog.Warn("failed to marshal execution summary for audit", "error", err)
		details = fmt.Appendf(nil, `{"error":"marshal failed: %v"}`, err)
	}

	entry := types.AuditEntry{
		ID:        ulid.Make().String(),
		AgentID:   req.AgentID,
		EventType: types.EventToolCall,
		Action:    "tool_execution",
		Resource:  req.Tool + "." + req.Action,
		Decision:  decision,
		RiskLevel: riskLevelForTier(req.Tier),
		Details:   string(details),
		Timestamp: time.Now(),
	}

	return l.store.InsertAuditEntry(ctx, entry)
}

// riskLevelForTier returns the risk level for a given KTP tier.
func riskLevelForTier(tier string) string {
	switch tier {
	case TierReader:
		return "low"
	case TierWriter:
		return "medium"
	case TierOperator:
		return "high"
	case TierAdmin:
		return "critical"
	default:
		return "low"
	}
}

// executionMode returns "sandbox" if the response has a SandboxID, otherwise "inline".
func executionMode(resp *ToolResponse) string {
	if resp != nil && resp.SandboxID != "" {
		return "sandbox"
	}
	return "inline"
}

// truncateJSON marshals params and truncates to maxLen characters.
func truncateJSON(params map[string]any, maxLen int) string {
	if params == nil {
		return "{}"
	}
	b, err := json.Marshal(params)
	if err != nil {
		return fmt.Sprintf(`{"error":"marshal failed: %v"}`, err)
	}
	s := string(b)
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
