package models_test

import (
	"context"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/models"
)

func TestWithAgentID_RoundTrip(t *testing.T) {
	ctx := models.WithAgentID(context.Background(), "agent-42")
	got := models.AgentIDFromContext(ctx)
	if got != "agent-42" {
		t.Errorf("AgentIDFromContext() = %q, want %q", got, "agent-42")
	}
}

func TestAgentIDFromContext_EmptyContext(t *testing.T) {
	got := models.AgentIDFromContext(context.Background())
	if got != "" {
		t.Errorf("AgentIDFromContext() = %q, want empty string", got)
	}
}
