package history_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/testutil"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return tdb.DB
}

func TestAppendAndRecent(t *testing.T) {
	db := newTestDB(t)
	h := history.New(db)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		err := h.Append(ctx, history.HistoryEntry{
			AgentID: "a1", Channel: "webui", ChannelID: "",
			Role: role, Content: "message " + string(rune('A'+i)),
			Sender: "test", Tokens: 10,
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	entries, err := h.Recent(ctx, "a1", "webui", "", 3)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if entries[0].Content != "message C" {
		t.Errorf("entries[0].Content = %q, want %q", entries[0].Content, "message C")
	}
	if entries[2].Content != "message E" {
		t.Errorf("entries[2].Content = %q, want %q", entries[2].Content, "message E")
	}
	if entries[0].ID == 0 {
		t.Error("expected non-zero ID")
	}
}

func TestRecentDefaultLimit(t *testing.T) {
	db := newTestDB(t)
	h := history.New(db)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		h.Append(ctx, history.HistoryEntry{
			AgentID: "a1", Channel: "webui", ChannelID: "",
			Role: "user", Content: "msg", Tokens: 1,
		})
	}

	entries, err := h.Recent(ctx, "a1", "webui", "", 0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("got %d entries, want 3", len(entries))
	}
}

func TestChannelIsolation(t *testing.T) {
	db := newTestDB(t)
	h := history.New(db)
	ctx := context.Background()

	h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "slack", ChannelID: "C123",
		Role: "user", Content: "slack msg", Tokens: 5,
	})
	h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "webui", ChannelID: "",
		Role: "user", Content: "webui msg", Tokens: 5,
	})

	slackEntries, _ := h.Recent(ctx, "a1", "slack", "C123", 10)
	webuiEntries, _ := h.Recent(ctx, "a1", "webui", "", 10)

	if len(slackEntries) != 1 {
		t.Errorf("slack entries: got %d, want 1", len(slackEntries))
	}
	if len(webuiEntries) != 1 {
		t.Errorf("webui entries: got %d, want 1", len(webuiEntries))
	}
}

func TestCount(t *testing.T) {
	db := newTestDB(t)
	h := history.New(db)
	ctx := context.Background()

	for i := 0; i < 7; i++ {
		h.Append(ctx, history.HistoryEntry{
			AgentID: "a1", Channel: "webui", ChannelID: "",
			Role: "user", Content: "msg", Tokens: 1,
		})
	}

	count, err := h.Count(ctx, "a1", "webui", "")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 7 {
		t.Errorf("count = %d, want 7", count)
	}

	count, _ = h.Count(ctx, "a1", "slack", "C123")
	if count != 0 {
		t.Errorf("slack count = %d, want 0", count)
	}
}

func TestTrim(t *testing.T) {
	db := newTestDB(t)
	h := history.New(db)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		h.Append(ctx, history.HistoryEntry{
			AgentID: "a1", Channel: "webui", ChannelID: "",
			Role: "user", Content: "msg", Tokens: 1,
		})
	}

	deleted, err := h.Trim(ctx, "a1", "webui", "", 3)
	if err != nil {
		t.Fatalf("Trim: %v", err)
	}
	if deleted != 7 {
		t.Errorf("deleted = %d, want 7", deleted)
	}

	count, _ := h.Count(ctx, "a1", "webui", "")
	if count != 3 {
		t.Errorf("count after trim = %d, want 3", count)
	}
}

func TestClear(t *testing.T) {
	db := newTestDB(t)
	h := history.New(db)
	ctx := context.Background()

	h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "webui", ChannelID: "",
		Role: "user", Content: "webui msg", Tokens: 1,
	})
	h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "slack", ChannelID: "C123",
		Role: "user", Content: "slack msg", Tokens: 1,
	})
	h.Append(ctx, history.HistoryEntry{
		AgentID: "a2", Channel: "webui", ChannelID: "",
		Role: "user", Content: "other agent msg", Tokens: 1,
	})

	if err := h.Clear(ctx, "a1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	c1, _ := h.Count(ctx, "a1", "webui", "")
	c2, _ := h.Count(ctx, "a1", "slack", "C123")
	c3, _ := h.Count(ctx, "a2", "webui", "")

	if c1 != 0 || c2 != 0 {
		t.Errorf("a1 counts: webui=%d, slack=%d; want 0, 0", c1, c2)
	}
	if c3 != 1 {
		t.Errorf("a2 count = %d, want 1", c3)
	}
}

func TestSearch(t *testing.T) {
	db := newTestDB(t)
	h := history.New(db)
	ctx := context.Background()

	h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "webui", ChannelID: "",
		Role: "user", Content: "tell me about kubernetes", Tokens: 5,
	})
	h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "webui", ChannelID: "",
		Role: "assistant", Content: "Kubernetes is an orchestration platform.", Tokens: 8,
	})
	h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "webui", ChannelID: "",
		Role: "user", Content: "what about docker?", Tokens: 4,
	})

	results, err := h.Search(ctx, "a1", "kubernetes", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("got %d results for 'kubernetes', want 2", len(results))
	}

	results, err = h.Search(ctx, "a1", "docker", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("got %d results for 'docker', want 1", len(results))
	}
}

func TestAppendAndRecentWithToolMetadata(t *testing.T) {
	db := newTestDB(t)
	h := history.New(db)
	ctx := context.Background()

	err := h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "webui", ChannelID: "",
		Role: "user", Content: "use the file tool", Tokens: 5,
	})
	if err != nil {
		t.Fatalf("Append user: %v", err)
	}

	toolCallsJSON := `[{"id":"tc_001","name":"file.list","parameters":{"path":"/"}}]`
	err = h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "webui", ChannelID: "",
		Role: "assistant", Content: "I'll list files for you.",
		Sender: "agent1", Tokens: 6,
		ToolCallsJSON: toolCallsJSON,
	})
	if err != nil {
		t.Fatalf("Append assistant with tool_calls: %v", err)
	}

	err = h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "webui", ChannelID: "",
		Role: "tool", Content: `{"files":["a.txt","b.txt"]}`,
		Tokens: 8, ToolCallID: "tc_001",
	})
	if err != nil {
		t.Fatalf("Append tool result: %v", err)
	}

	err = h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "webui", ChannelID: "",
		Role: "assistant", Content: "Found 2 files.",
		Sender: "agent1", Tokens: 4,
	})
	if err != nil {
		t.Fatalf("Append final assistant: %v", err)
	}

	entries, err := h.Recent(ctx, "a1", "webui", "", 10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}

	if entries[1].ToolCallsJSON != toolCallsJSON {
		t.Errorf("entries[1].ToolCallsJSON = %q, want %q", entries[1].ToolCallsJSON, toolCallsJSON)
	}
	if entries[2].ToolCallID != "tc_001" {
		t.Errorf("entries[2].ToolCallID = %q, want %q", entries[2].ToolCallID, "tc_001")
	}
	if entries[3].ToolCallsJSON != "" {
		t.Errorf("entries[3].ToolCallsJSON = %q, want empty", entries[3].ToolCallsJSON)
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"hi", 1},
		{"hello world!!", 3},
		{"a longer string that has more characters", 10},
	}
	for _, tt := range tests {
		got := history.EstimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
