package models

import "context"

type contextKey string

const agentIDKey contextKey = "kyvik.agent_id"

// WithAgentID returns a new context with the given agent ID attached.
func WithAgentID(ctx context.Context, agentID string) context.Context {
	return context.WithValue(ctx, agentIDKey, agentID)
}

// AgentIDFromContext extracts the agent ID from the context, or returns ""
// if not set.
func AgentIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(agentIDKey).(string)
	return v
}
