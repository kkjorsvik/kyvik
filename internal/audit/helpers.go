package audit

import (
	"context"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// defaultRiskLevel returns a sensible risk level based on event type and decision.
func defaultRiskLevel(eventType types.EventType, decision string) string {
	if decision == "denied" {
		return "high"
	}
	switch eventType {
	case types.EventPermission:
		return "medium"
	case types.EventToolCall:
		return "medium"
	case types.EventAgentLifecycle:
		return "low"
	case types.EventSpending:
		return "low"
	case types.EventSecret:
		return "medium"
	default:
		return "low"
	}
}

// LogToolCall logs a tool invocation audit entry.
// The action is formatted as "tool.action" by joining the tool name and action.
func LogToolCall(ctx context.Context, logger Logger, agentID, tool, action, resource, decision, details string) error {
	return logger.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventToolCall,
		Action:    tool + "." + action,
		Resource:  resource,
		Decision:  decision,
		RiskLevel: defaultRiskLevel(types.EventToolCall, decision),
		Details:   details,
	})
}

// LogPermissionCheck logs a permission check audit entry.
func LogPermissionCheck(ctx context.Context, logger Logger, agentID, action, resource, decision, details string) error {
	return logger.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventPermission,
		Action:    action,
		Resource:  resource,
		Decision:  decision,
		RiskLevel: defaultRiskLevel(types.EventPermission, decision),
		Details:   details,
	})
}

// LogAgentLifecycle logs an agent lifecycle event (start, stop, pause, etc.).
// Decision is always "allowed" since lifecycle events are informational.
func LogAgentLifecycle(ctx context.Context, logger Logger, agentID, action, details string) error {
	return logger.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventAgentLifecycle,
		Action:    action,
		Decision:  "allowed",
		RiskLevel: defaultRiskLevel(types.EventAgentLifecycle, "allowed"),
		Details:   details,
	})
}

// LogSpending logs a spending-related audit entry.
func LogSpending(ctx context.Context, logger Logger, agentID, action, decision, details string) error {
	return logger.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventSpending,
		Action:    action,
		Decision:  decision,
		RiskLevel: defaultRiskLevel(types.EventSpending, decision),
		Details:   details,
	})
}

// LogSecret logs a secret vault audit entry.
func LogSecret(ctx context.Context, logger Logger, action, scope, key, details string) error {
	return logger.Log(ctx, types.AuditEntry{
		EventType: types.EventSecret,
		Action:    action,
		Resource:  scope + "/" + key,
		Decision:  "allowed",
		RiskLevel: defaultRiskLevel(types.EventSecret, "allowed"),
		Details:   details,
	})
}
