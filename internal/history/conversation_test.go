package history_test

import (
	"context"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/history"
)

func newConvTestDB(t *testing.T) *history.Store {
	t.Helper()
	db := newTestDB(t)
	return history.New(db)
}

func TestCreateAndGetConversation(t *testing.T) {
	h := newConvTestDB(t)
	ctx := context.Background()

	conv, err := h.CreateConversation(ctx, "agent-1", "Hello world")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if conv.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if conv.Title != "Hello world" {
		t.Fatalf("expected title 'Hello world', got %q", conv.Title)
	}
	if conv.MessageCount != 0 {
		t.Fatalf("expected 0 messages, got %d", conv.MessageCount)
	}

	got, err := h.GetConversation(ctx, conv.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AgentID != "agent-1" {
		t.Fatalf("expected agent-1, got %q", got.AgentID)
	}
}

func TestListConversations(t *testing.T) {
	h := newConvTestDB(t)
	ctx := context.Background()

	h.CreateConversation(ctx, "agent-1", "First")
	h.CreateConversation(ctx, "agent-1", "Second")
	h.CreateConversation(ctx, "agent-2", "Other")

	convs, err := h.ListConversations(ctx, "agent-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(convs) != 2 {
		t.Fatalf("expected 2 conversations, got %d", len(convs))
	}
	// Most recent first
	if convs[0].Title != "Second" {
		t.Fatalf("expected 'Second' first, got %q", convs[0].Title)
	}
}

func TestRenameConversation(t *testing.T) {
	h := newConvTestDB(t)
	ctx := context.Background()

	conv, _ := h.CreateConversation(ctx, "agent-1", "Old title")
	if err := h.RenameConversation(ctx, conv.ID, "New title"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	got, _ := h.GetConversation(ctx, conv.ID)
	if got.Title != "New title" {
		t.Fatalf("expected 'New title', got %q", got.Title)
	}
}

func TestDeleteConversation(t *testing.T) {
	h := newConvTestDB(t)
	ctx := context.Background()

	conv, _ := h.CreateConversation(ctx, "agent-1", "To delete")

	// Add a history entry for this conversation.
	h.Append(ctx, history.HistoryEntry{
		AgentID:   "agent-1",
		Channel:   "webui",
		ChannelID: conv.ID,
		Role:      "user",
		Content:   "hello",
	})

	if err := h.DeleteConversation(ctx, conv.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Conversation should be gone.
	_, err := h.GetConversation(ctx, conv.ID)
	if err == nil {
		t.Fatal("expected error after delete")
	}

	// History entries should also be gone.
	entries, _ := h.Recent(ctx, "agent-1", "webui", conv.ID, 100)
	if len(entries) != 0 {
		t.Fatalf("expected 0 history entries after delete, got %d", len(entries))
	}
}

func TestIncrementMessageCount(t *testing.T) {
	h := newConvTestDB(t)
	ctx := context.Background()

	conv, _ := h.CreateConversation(ctx, "agent-1", "Count test")
	h.IncrementMessageCount(ctx, conv.ID, 1)
	h.IncrementMessageCount(ctx, conv.ID, 2)

	got, _ := h.GetConversation(ctx, conv.ID)
	if got.MessageCount != 3 {
		t.Fatalf("expected 3, got %d", got.MessageCount)
	}
}

func TestMostRecentConversation(t *testing.T) {
	h := newConvTestDB(t)
	ctx := context.Background()

	// No conversations yet.
	got, err := h.MostRecentConversation(ctx, "agent-1")
	if err != nil {
		t.Fatalf("most recent (empty): %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for no conversations")
	}

	h.CreateConversation(ctx, "agent-1", "First")
	conv2, _ := h.CreateConversation(ctx, "agent-1", "Second")

	got, err = h.MostRecentConversation(ctx, "agent-1")
	if err != nil {
		t.Fatalf("most recent: %v", err)
	}
	if got.ID != conv2.ID {
		t.Fatalf("expected most recent to be %q, got %q", conv2.ID, got.ID)
	}
}
