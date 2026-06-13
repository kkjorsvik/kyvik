package guide

import (
	"context"
	"fmt"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// StaticWelcomeNoModel is the fallback welcome message when no model provider is configured.
const StaticWelcomeNoModel = "Welcome to Kyvik! I'm the built-in guide agent, but I need a model provider configured before I can chat. Head to Settings → Model Providers to set up your first provider, then come back and we'll get you sorted."

// StateStore is the subset of store.Store needed for welcome state tracking.
type StateStore interface {
	GetSystemState(ctx context.Context, key string) (string, error)
	SetSystemState(ctx context.Context, key, value string) error
}

// MessageSender sends a message to a specific agent.
type MessageSender interface {
	SendMessage(ctx context.Context, agentID string, msg types.Message) error
}

// ConversationWriter writes a message directly to conversation history without LLM processing.
type ConversationWriter interface {
	WriteHistory(ctx context.Context, agentID, conversationID, role, content, channel string) error
}

// SendWelcomeMessage sends the first-run welcome message to the guide agent.
// It's idempotent — it only sends once, tracked via system_state.
func SendWelcomeMessage(ctx context.Context, store StateStore, sender MessageSender, hasModel bool) error {
	sent, err := store.GetSystemState(ctx, "guide_welcome_sent")
	if err == nil && sent == "true" {
		return nil
	}

	if hasModel {
		msg := types.Message{
			AgentID:   GuideAgentID,
			Channel:   "webui",
			Role:      "user",
			Content:   "A new user has just logged in for the first time. This is a fresh Kyvik instance. Introduce yourself and offer to help with setup.",
			Timestamp: time.Now().UTC(),
		}
		if err := sender.SendMessage(ctx, GuideAgentID, msg); err != nil {
			return fmt.Errorf("send welcome message: %w", err)
		}
	}
	// When no model is available, the guide's first-interaction behavior in the
	// soul/identity handles the greeting. No static message is injected because
	// the user will see the guide's response when they first open the chat.

	if err := store.SetSystemState(ctx, "guide_welcome_sent", "true"); err != nil {
		return fmt.Errorf("set welcome_sent state: %w", err)
	}
	return nil
}
