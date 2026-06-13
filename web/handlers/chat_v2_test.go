package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/channels"
	"github.com/kkjorsvik/kyvik/internal/history"
)

func TestChatV2Tracker_StreamDoneSuppressesFallbackMessage(t *testing.T) {
	tracker := newChatV2RequestTracker()
	h := &Handlers{}

	tracker.enqueue("conv-1", "req-1")

	chunk := tracker.mapStream(h, channels.StreamEvent{
		Type:           "chunk",
		ConversationID: "conv-1",
		Content:        "hello",
	})
	if !chunk.emit {
		t.Fatal("expected chunk to emit")
	}
	if chunk.event.Type != "assistant_chunk" {
		t.Fatalf("chunk type = %q, want assistant_chunk", chunk.event.Type)
	}
	if chunk.event.RequestID != "req-1" {
		t.Fatalf("chunk request_id = %q, want req-1", chunk.event.RequestID)
	}

	done := tracker.mapStream(h, channels.StreamEvent{
		Type:           "done",
		ConversationID: "conv-1",
		Timestamp:      time.Now(),
	})
	if !done.emit || !done.terminal {
		t.Fatal("expected done to emit terminal event")
	}
	if done.event.Type != "assistant_done" {
		t.Fatalf("done type = %q, want assistant_done", done.event.Type)
	}
	if done.event.RequestID != "req-1" {
		t.Fatalf("done request_id = %q, want req-1", done.event.RequestID)
	}

	fallback := tracker.mapStream(h, channels.StreamEvent{
		Type:           "message",
		ConversationID: "conv-1",
		Content:        "full message",
		Timestamp:      time.Now(),
	})
	if fallback.emit {
		t.Fatal("expected redundant message fallback to be suppressed")
	}
}

func TestChatV2Tracker_NonStreamMessageMapsToPendingRequest(t *testing.T) {
	tracker := newChatV2RequestTracker()
	h := &Handlers{}

	tracker.enqueue("conv-2", "req-2")

	msg := tracker.mapStream(h, channels.StreamEvent{
		Type:           "message",
		ConversationID: "conv-2",
		Content:        "hi",
		Timestamp:      time.Now(),
	})
	if !msg.emit || !msg.terminal {
		t.Fatal("expected message to emit terminal event")
	}
	if msg.event.Type != "assistant_message" {
		t.Fatalf("message type = %q, want assistant_message", msg.event.Type)
	}
	if msg.event.RequestID != "req-2" {
		t.Fatalf("message request_id = %q, want req-2", msg.event.RequestID)
	}
}

func TestChatV2Tracker_OutOfBandMessageStillEmits(t *testing.T) {
	tracker := newChatV2RequestTracker()
	h := &Handlers{}

	msg := tracker.mapStream(h, channels.StreamEvent{
		Type:           "message",
		ConversationID: "conv-3",
		Content:        "out of band",
		Timestamp:      time.Now(),
	})
	if !msg.emit || !msg.terminal {
		t.Fatal("expected out-of-band message to emit terminal event")
	}
	if msg.event.RequestID != "" {
		t.Fatalf("request_id = %q, want empty", msg.event.RequestID)
	}
}

func TestBuildChatV2HistoryUserVisible_FiltersInternalAndEmptyAssistant(t *testing.T) {
	h := &Handlers{}
	now := time.Now()

	entries := []history.HistoryEntry{
		{ID: 10, Role: "user", Content: "hello", CreatedAt: now.Add(-2 * time.Minute)},
		{ID: 11, Role: "assistant", Content: "hi there", CreatedAt: now.Add(-time.Minute)},
		{ID: 12, Role: "assistant", Content: "   ", CreatedAt: now},
		{ID: 13, Role: "assistant", Content: "tool placeholder", ToolCallsJSON: `[{"id":"t1"}]`, CreatedAt: now},
		{ID: 14, Role: "tool", Content: "tool trace", CreatedAt: now},
	}

	got := buildChatV2HistoryUserVisible(h, entries, 10)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ID != 11 {
		t.Fatalf("id = %d, want 11", got[0].ID)
	}
	if got[0].Role != "assistant" {
		t.Fatalf("role = %q, want assistant", got[0].Role)
	}
	if got[0].Content != "hi there" {
		t.Fatalf("content = %q, want hi there", got[0].Content)
	}
	if got[0].Timestamp == "" {
		t.Fatal("expected non-empty timestamp")
	}
	if got[0].IsInternal {
		t.Fatal("expected user-visible message to not be internal")
	}
	if got[0].Source != "history" {
		t.Fatalf("source = %q, want history", got[0].Source)
	}
}

func TestBuildChatV2HistoryDebug_IncludesInternal(t *testing.T) {
	h := &Handlers{}
	now := time.Now()
	entries := []history.HistoryEntry{
		{ID: 20, Role: "assistant", Content: "ok", CreatedAt: now.Add(-time.Minute)},
		{ID: 21, Role: "assistant", Content: " ", ToolCallsJSON: `[{}]`, CreatedAt: now},
		{ID: 22, Role: "tool", Content: "trace", CreatedAt: now},
	}
	got := buildChatV2HistoryDebug(h, entries, 0)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].IsInternal {
		t.Fatal("assistant content should not be internal")
	}
	if !got[1].IsInternal {
		t.Fatal("assistant tool-call placeholder should be internal")
	}
	if !got[2].IsInternal {
		t.Fatal("tool role should be internal")
	}
}

func TestHistorySyncPayloadContainsCompatibilityAlias(t *testing.T) {
	msgs := []chatV2HistoryMessage{
		{ID: 1, Role: "assistant", Content: "hello", Timestamp: "12:00"},
	}
	ev := chatV2ServerEvent{
		Type:                "history_sync",
		ConversationID:      "conv-1",
		Messages:            msgs,
		MessagesUserVisible: msgs,
		MessagesDebug:       []chatV2HistoryMessage{},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["messages"]; !ok {
		t.Fatal("expected compatibility field messages")
	}
	if _, ok := out["messages_user_visible"]; !ok {
		t.Fatal("expected messages_user_visible field")
	}
}

func TestChatV2DefaultEnabled_ByDefault(t *testing.T) {
	// No env vars set — v2 should be the default
	t.Setenv("KYVIK_CHAT_V2", "")
	t.Setenv("KYVIK_CHAT_V2_DEFAULT", "")
	if !chatV2DefaultEnabled() {
		t.Fatal("expected chatV2DefaultEnabled=true by default")
	}
}

func TestChatV2DefaultDisabledWhenV2Off(t *testing.T) {
	t.Setenv("KYVIK_CHAT_V2", "false")
	if chatV2DefaultEnabled() {
		t.Fatal("expected chatV2DefaultEnabled=false when chat v2 is disabled")
	}
}

func TestChatV2DefaultDisabledExplicitly(t *testing.T) {
	t.Setenv("KYVIK_CHAT_V2", "")
	t.Setenv("KYVIK_CHAT_V2_DEFAULT", "false")
	if chatV2DefaultEnabled() {
		t.Fatal("expected chatV2DefaultEnabled=false when explicitly disabled")
	}
}
