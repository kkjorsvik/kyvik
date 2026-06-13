package scheduler

import (
	"context"
	"fmt"
	"testing"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// mockScheduler implements SchedulerManager for testing.
type mockScheduler struct {
	schedules []types.Schedule
	tz        string
}

func newMockScheduler() *mockScheduler {
	return &mockScheduler{tz: "UTC"}
}

func (m *mockScheduler) Add(_ context.Context, sched types.Schedule) error {
	m.schedules = append(m.schedules, sched)
	return nil
}

func (m *mockScheduler) Update(_ context.Context, sched types.Schedule) error {
	for i, s := range m.schedules {
		if s.ID == sched.ID {
			m.schedules[i] = sched
			return nil
		}
	}
	return fmt.Errorf("not found")
}

func (m *mockScheduler) Remove(_ context.Context, id string) error {
	for i, s := range m.schedules {
		if s.ID == id {
			m.schedules = append(m.schedules[:i], m.schedules[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("not found")
}

func (m *mockScheduler) List(_ context.Context, agentID string) ([]types.Schedule, error) {
	var out []types.Schedule
	for _, s := range m.schedules {
		if s.AgentID == agentID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *mockScheduler) DefaultTimezone() string { return m.tz }

func makeReq(agentID, action string, params map[string]any) ktp.ToolRequest {
	return ktp.ToolRequest{
		ID:         "test-req",
		AgentID:    agentID,
		Tool:       "scheduler",
		Action:     action,
		Parameters: params,
		Tier:       ktp.TierWriter,
	}
}

func TestCreateTask(t *testing.T) {
	mock := newMockScheduler()
	tool := New(mock)
	ctx := context.Background()

	req := makeReq("agent-1", "create_task", map[string]any{
		"name":      "Daily report",
		"cron_expr": "0 9 * * *",
		"message":   "Generate the daily report",
	})

	resp, err := tool.Execute(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	if result["name"] != "Daily report" {
		t.Errorf("expected name 'Daily report', got %v", result["name"])
	}
	if result["id"] == "" {
		t.Error("expected non-empty schedule ID")
	}

	if len(mock.schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(mock.schedules))
	}
	s := mock.schedules[0]
	if s.AgentID != "agent-1" {
		t.Errorf("expected agent-1, got %s", s.AgentID)
	}
	if s.Type != types.ScheduleTypeTask {
		t.Errorf("expected type 'task', got %s", s.Type)
	}
	if s.Channel != "internal" {
		t.Errorf("expected channel 'internal', got %s", s.Channel)
	}
	if !s.Enabled {
		t.Error("expected enabled=true by default")
	}
}

func TestCreateTaskMissingParams(t *testing.T) {
	tool := New(newMockScheduler())
	ctx := context.Background()

	req := makeReq("agent-1", "create_task", map[string]any{
		"name": "Missing fields",
	})

	resp, err := tool.Execute(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for missing params")
	}
}

func TestCreateTaskLimit(t *testing.T) {
	mock := newMockScheduler()
	tool := New(mock)
	ctx := context.Background()

	// Fill up to the limit.
	for i := 0; i < maxSchedulesPerAgent; i++ {
		mock.schedules = append(mock.schedules, types.Schedule{
			ID:      fmt.Sprintf("sched-%d", i),
			AgentID: "agent-1",
			Type:    types.ScheduleTypeTask,
		})
	}

	req := makeReq("agent-1", "create_task", map[string]any{
		"name":      "One too many",
		"cron_expr": "@every 1h",
		"message":   "test",
	})

	resp, err := tool.Execute(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure when at limit")
	}
	if len(mock.schedules) != maxSchedulesPerAgent {
		t.Errorf("expected %d schedules, got %d", maxSchedulesPerAgent, len(mock.schedules))
	}
}

func TestListTasks(t *testing.T) {
	mock := newMockScheduler()
	mock.schedules = []types.Schedule{
		{ID: "s1", AgentID: "agent-1", Name: "Task 1", Type: types.ScheduleTypeTask},
		{ID: "s2", AgentID: "agent-1", Name: "Heartbeat", Type: types.ScheduleTypeHeartbeat},
		{ID: "s3", AgentID: "agent-2", Name: "Other agent", Type: types.ScheduleTypeTask},
	}

	tool := New(mock)
	ctx := context.Background()

	resp, err := tool.Execute(ctx, makeReq("agent-1", "list_tasks", map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	result := resp.Result.(map[string]any)
	tasks := result["tasks"].([]map[string]any)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task (excluding heartbeat and other agent), got %d", len(tasks))
	}
	if tasks[0]["name"] != "Task 1" {
		t.Errorf("expected 'Task 1', got %v", tasks[0]["name"])
	}
}

func TestUpdateTask(t *testing.T) {
	mock := newMockScheduler()
	mock.schedules = []types.Schedule{
		{ID: "s1", AgentID: "agent-1", Name: "Old name", CronExpr: "0 9 * * *", Type: types.ScheduleTypeTask, Enabled: true},
	}

	tool := New(mock)
	ctx := context.Background()

	resp, err := tool.Execute(ctx, makeReq("agent-1", "update_task", map[string]any{
		"id":      "s1",
		"name":    "New name",
		"enabled": false,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	if mock.schedules[0].Name != "New name" {
		t.Errorf("expected 'New name', got %s", mock.schedules[0].Name)
	}
	if mock.schedules[0].Enabled {
		t.Error("expected enabled=false after update")
	}
	// cron_expr should be unchanged.
	if mock.schedules[0].CronExpr != "0 9 * * *" {
		t.Errorf("expected unchanged cron_expr, got %s", mock.schedules[0].CronExpr)
	}
}

func TestUpdateTaskWrongAgent(t *testing.T) {
	mock := newMockScheduler()
	mock.schedules = []types.Schedule{
		{ID: "s1", AgentID: "agent-2", Name: "Other agent's task", Type: types.ScheduleTypeTask},
	}

	tool := New(mock)
	ctx := context.Background()

	resp, err := tool.Execute(ctx, makeReq("agent-1", "update_task", map[string]any{
		"id":   "s1",
		"name": "Hijacked",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure when updating another agent's task")
	}
}

func TestUpdateHeartbeatBlocked(t *testing.T) {
	mock := newMockScheduler()
	mock.schedules = []types.Schedule{
		{ID: "hb1", AgentID: "agent-1", Name: "Heartbeat", Type: types.ScheduleTypeHeartbeat},
	}

	tool := New(mock)
	ctx := context.Background()

	resp, err := tool.Execute(ctx, makeReq("agent-1", "update_task", map[string]any{
		"id":   "hb1",
		"name": "Hijacked heartbeat",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure when modifying heartbeat schedule")
	}
}

func TestDeleteTask(t *testing.T) {
	mock := newMockScheduler()
	mock.schedules = []types.Schedule{
		{ID: "s1", AgentID: "agent-1", Name: "To delete", Type: types.ScheduleTypeTask},
	}

	tool := New(mock)
	ctx := context.Background()

	resp, err := tool.Execute(ctx, makeReq("agent-1", "delete_task", map[string]any{
		"id": "s1",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
	if len(mock.schedules) != 0 {
		t.Errorf("expected 0 schedules after delete, got %d", len(mock.schedules))
	}
}

func TestDeleteTaskNotFound(t *testing.T) {
	tool := New(newMockScheduler())
	ctx := context.Background()

	resp, err := tool.Execute(ctx, makeReq("agent-1", "delete_task", map[string]any{
		"id": "nonexistent",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Success {
		t.Fatal("expected failure for nonexistent schedule")
	}
}

func TestDeclaration(t *testing.T) {
	tool := New(newMockScheduler())
	decl := tool.Declaration()

	if decl.Name != "scheduler" {
		t.Errorf("expected name 'scheduler', got %s", decl.Name)
	}
	if decl.MinTier != ktp.TierReader {
		t.Errorf("expected min tier '%s', got %s", ktp.TierReader, decl.MinTier)
	}
	if len(decl.Actions) != 4 {
		t.Errorf("expected 4 actions, got %d", len(decl.Actions))
	}

	// Verify delete_task is marked destructive.
	for _, a := range decl.Actions {
		if a.Name == "delete_task" && !a.Destructive {
			t.Error("delete_task should be marked destructive")
		}
	}
}

func TestInline(t *testing.T) {
	tool := New(newMockScheduler())
	if !tool.Inline() {
		t.Error("scheduler tool should be inline")
	}
}
