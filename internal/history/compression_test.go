package history_test

import (
	"context"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/history"
	"github.com/kkjorsvik/kyvik/internal/testutil"
)

func newCompTestDB(t *testing.T) *history.Store {
	t.Helper()
	tdb := testutil.RequirePostgres(t)
	return history.New(tdb.DB)
}

func appendN(t *testing.T, h *history.Store, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		err := h.Append(ctx, history.HistoryEntry{
			AgentID:   "a1",
			Channel:   "web",
			ChannelID: "c1",
			Role:      "user",
			Content:   "msg",
			Tokens:    10,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecentExcludesCompressed(t *testing.T) {
	h := newCompTestDB(t)
	ctx := context.Background()
	appendN(t, h, 5)

	all, err := h.Recent(ctx, "a1", "web", "c1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("expected 5, got %d", len(all))
	}

	ids := []int64{all[0].ID, all[1].ID, all[2].ID}
	if err := h.MarkCompressed(ctx, ids, 999); err != nil {
		t.Fatal(err)
	}

	recent, err := h.Recent(ctx, "a1", "web", "c1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("expected 2 after compression, got %d", len(recent))
	}
}

func TestCountExcludesCompressed(t *testing.T) {
	h := newCompTestDB(t)
	ctx := context.Background()
	appendN(t, h, 5)

	all, _ := h.Recent(ctx, "a1", "web", "c1", 100)
	ids := []int64{all[0].ID, all[1].ID, all[2].ID}
	if err := h.MarkCompressed(ctx, ids, 999); err != nil {
		t.Fatal(err)
	}

	count, err := h.Count(ctx, "a1", "web", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected count 2, got %d", count)
	}
}

func TestActiveSummary(t *testing.T) {
	h := newCompTestDB(t)
	ctx := context.Background()

	// No summary yet.
	s, err := h.ActiveSummary(ctx, "a1", "web", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if s != nil {
		t.Fatal("expected nil summary")
	}

	// Insert a summary entry.
	if err := h.Append(ctx, history.HistoryEntry{
		AgentID:   "a1",
		Channel:   "web",
		ChannelID: "c1",
		Role:      "summary",
		Content:   "summary of conversation",
		Tokens:    50,
	}); err != nil {
		t.Fatal(err)
	}

	s, err = h.ActiveSummary(ctx, "a1", "web", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("expected non-nil summary")
	}
	if s.Content != "summary of conversation" {
		t.Fatalf("unexpected content: %s", s.Content)
	}
}

func TestActiveSummaryRollingMerge(t *testing.T) {
	h := newCompTestDB(t)
	ctx := context.Background()

	// Insert first summary.
	if err := h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "web", ChannelID: "c1",
		Role: "summary", Content: "first summary", Tokens: 50,
	}); err != nil {
		t.Fatal(err)
	}

	first, err := h.ActiveSummary(ctx, "a1", "web", "c1")
	if err != nil {
		t.Fatal(err)
	}

	// Insert second summary.
	if err := h.Append(ctx, history.HistoryEntry{
		AgentID: "a1", Channel: "web", ChannelID: "c1",
		Role: "summary", Content: "merged summary", Tokens: 80,
	}); err != nil {
		t.Fatal(err)
	}

	second, err := h.ActiveSummary(ctx, "a1", "web", "c1")
	if err != nil {
		t.Fatal(err)
	}

	// Mark the first as compressed by the second.
	if err := h.MarkCompressed(ctx, []int64{first.ID}, second.ID); err != nil {
		t.Fatal(err)
	}

	active, err := h.ActiveSummary(ctx, "a1", "web", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if active == nil {
		t.Fatal("expected active summary")
	}
	if active.Content != "merged summary" {
		t.Fatalf("expected merged summary, got: %s", active.Content)
	}
}

func TestMarkCompressed(t *testing.T) {
	h := newCompTestDB(t)
	ctx := context.Background()
	appendN(t, h, 2)

	all, _ := h.Recent(ctx, "a1", "web", "c1", 100)
	ids := []int64{all[0].ID, all[1].ID}
	if err := h.MarkCompressed(ctx, ids, 999); err != nil {
		t.Fatal(err)
	}

	recent, err := h.Recent(ctx, "a1", "web", "c1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 0 {
		t.Fatalf("expected 0 after marking all compressed, got %d", len(recent))
	}
}

func TestAppendAndCompress(t *testing.T) {
	h := newCompTestDB(t)
	ctx := context.Background()
	appendN(t, h, 3)

	all, _ := h.Recent(ctx, "a1", "web", "c1", 100)
	ids := make([]int64, len(all))
	for i, e := range all {
		ids[i] = e.ID
	}

	summary := history.HistoryEntry{
		AgentID:   "a1",
		Channel:   "web",
		ChannelID: "c1",
		Role:      "summary",
		Content:   "summary of 3 messages",
		Tokens:    30,
	}

	if err := h.AppendAndCompress(ctx, summary, ids); err != nil {
		t.Fatal(err)
	}

	// Original messages should be compressed (not returned by Recent).
	recent, err := h.Recent(ctx, "a1", "web", "c1", 100)
	if err != nil {
		t.Fatal(err)
	}
	// Only the summary should remain (compressed_by = 0).
	if len(recent) != 1 {
		t.Fatalf("expected 1 (summary), got %d", len(recent))
	}
	if recent[0].Role != "summary" {
		t.Fatalf("expected summary role, got %s", recent[0].Role)
	}

	// ActiveSummary should return the summary.
	s, err := h.ActiveSummary(ctx, "a1", "web", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if s == nil || s.Content != "summary of 3 messages" {
		t.Fatalf("unexpected active summary: %v", s)
	}
}
