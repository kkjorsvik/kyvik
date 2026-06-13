package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/audit"
	"github.com/kkjorsvik/kyvik/internal/queue"
	"github.com/kkjorsvik/kyvik/internal/spending"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

var errAgentNotFound = errors.New("agent not found")

// ---------------------------------------------------------------------------
// Mock Store
// ---------------------------------------------------------------------------

type mockStore struct {
	mu        sync.Mutex
	schedules map[string]types.Schedule
	agents    map[string]types.AgentConfig
}

func newMockStore() *mockStore {
	return &mockStore{
		schedules: make(map[string]types.Schedule),
		agents:    make(map[string]types.AgentConfig),
	}
}

func (m *mockStore) CreateSchedule(_ context.Context, s types.Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedules[s.ID] = s
	return nil
}

func (m *mockStore) GetSchedule(_ context.Context, id string) (*types.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.schedules[id]
	if !ok {
		return nil, types.ErrScheduleNotFound
	}
	return &s, nil
}

func (m *mockStore) UpdateSchedule(_ context.Context, s types.Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.schedules[s.ID]; !ok {
		return types.ErrScheduleNotFound
	}
	m.schedules[s.ID] = s
	return nil
}

func (m *mockStore) DeleteSchedule(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.schedules, id)
	return nil
}

func (m *mockStore) ListSchedules(_ context.Context, agentID string) ([]types.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []types.Schedule
	for _, s := range m.schedules {
		if s.AgentID == agentID {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *mockStore) ListSchedulesByType(_ context.Context, agentID, schedType string) ([]types.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []types.Schedule
	for _, s := range m.schedules {
		if s.AgentID == agentID && s.Type == schedType {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *mockStore) ListAllEnabledSchedules(_ context.Context) ([]types.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []types.Schedule
	for _, s := range m.schedules {
		if s.Enabled {
			result = append(result, s)
		}
	}
	return result, nil
}

func (m *mockStore) DeleteSchedulesByAgent(_ context.Context, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.schedules {
		if s.AgentID == agentID {
			delete(m.schedules, id)
		}
	}
	return nil
}

func (m *mockStore) GetAgent(_ context.Context, id string) (*types.AgentConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok {
		return nil, errAgentNotFound
	}
	return &a, nil
}

// Unused Store interface methods — stubs required for compilation.
func (m *mockStore) CreateAgent(_ context.Context, _ types.AgentConfig) error  { return nil }
func (m *mockStore) ListAgents(_ context.Context) ([]types.AgentConfig, error) { return nil, nil }
func (m *mockStore) UpdateAgent(_ context.Context, _ types.AgentConfig) error  { return nil }
func (m *mockStore) DeleteAgent(_ context.Context, _ string) error             { return nil }
func (m *mockStore) SetDesiredState(_ context.Context, _ string, _ types.DesiredState) error {
	return nil
}
func (m *mockStore) SetActualState(_ context.Context, _ string, _ types.AgentStatus, _ string) error {
	return nil
}
func (m *mockStore) InsertAuditEntry(_ context.Context, _ types.AuditEntry) error     { return nil }
func (m *mockStore) InsertAuditEntries(_ context.Context, _ []types.AuditEntry) error { return nil }
func (m *mockStore) ListAuditEntries(_ context.Context, _ audit.Filter) ([]types.AuditEntry, error) {
	return nil, nil
}
func (m *mockStore) InsertUsageRecord(_ context.Context, _ string, _, _ int64, _ float64, _, _, _, _, _ string) error {
	return nil
}
func (m *mockStore) AggregateUsage(_ context.Context, _, _ string) (*spending.Summary, error) {
	return nil, nil
}
func (m *mockStore) AggregateSlotUsage(_ context.Context, _, _ string) ([]spending.SlotUsageSummary, error) {
	return nil, nil
}
func (m *mockStore) AggregateProviderUsage(_ context.Context, _, _ string) ([]spending.ProviderUsageSummary, error) {
	return nil, nil
}
func (m *mockStore) InsertSecurityEvent(_ context.Context, _ types.SecurityEvent) error { return nil }
func (m *mockStore) QuerySecurityEvents(_ context.Context, _ string, _ int) ([]types.SecurityEvent, error) {
	return nil, nil
}
func (m *mockStore) QueryAllSecurityEvents(_ context.Context, _ string, _ int) ([]types.SecurityEvent, error) {
	return nil, nil
}
func (m *mockStore) GetSystemState(_ context.Context, _ string) (string, error) { return "", nil }
func (m *mockStore) SetSystemState(_ context.Context, _, _ string) error        { return nil }
func (m *mockStore) AcknowledgeAlert(_ context.Context, _, _ string) error      { return nil }
func (m *mockStore) IsAlertAcknowledged(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}
func (m *mockStore) ListAcknowledgedAlerts(_ context.Context) (map[string]time.Time, error) {
	return nil, nil
}
func (m *mockStore) GrantSkill(_ context.Context, _ types.SkillGrant) error { return nil }
func (m *mockStore) RevokeSkill(_ context.Context, _, _ string) error       { return nil }
func (m *mockStore) ListSkillGrants(_ context.Context, _ string) ([]types.SkillGrant, error) {
	return nil, nil
}
func (m *mockStore) DeleteSkillGrantsByAgent(_ context.Context, _ string) error { return nil }

func (m *mockStore) CreateTeam(_ context.Context, _ types.Team) error { return nil }
func (m *mockStore) GetTeam(_ context.Context, _ string) (*types.Team, error) {
	return nil, types.ErrTeamNotFound
}
func (m *mockStore) UpdateTeam(_ context.Context, _ types.Team) error  { return nil }
func (m *mockStore) DeleteTeam(_ context.Context, _ string) error      { return nil }
func (m *mockStore) ListTeams(_ context.Context) ([]types.Team, error) { return nil, nil }
func (m *mockStore) GetTeamByAgent(_ context.Context, _ string) (*types.Team, error) {
	return nil, types.ErrTeamNotFound
}

func (m *mockStore) CreateOutboundWebhook(_ context.Context, _ types.OutboundWebhook) error {
	return nil
}
func (m *mockStore) GetOutboundWebhook(_ context.Context, _ string) (*types.OutboundWebhook, error) {
	return nil, types.ErrOutboundWebhookNotFound
}
func (m *mockStore) UpdateOutboundWebhook(_ context.Context, _ types.OutboundWebhook) error {
	return nil
}
func (m *mockStore) DeleteOutboundWebhook(_ context.Context, _ string) error { return nil }
func (m *mockStore) ListOutboundWebhooks(_ context.Context, _ string) ([]types.OutboundWebhook, error) {
	return nil, nil
}
func (m *mockStore) ListAllEnabledOutboundWebhooks(_ context.Context) ([]types.OutboundWebhook, error) {
	return nil, nil
}
func (m *mockStore) InsertWebhookDelivery(_ context.Context, _ types.WebhookDelivery) error {
	return nil
}
func (m *mockStore) ListWebhookDeliveries(_ context.Context, _ string, _ int) ([]types.WebhookDelivery, error) {
	return nil, nil
}
func (m *mockStore) ListPendingRetries(_ context.Context) ([]types.WebhookDelivery, error) {
	return nil, nil
}
func (m *mockStore) UpdateDeliveryStatus(_ context.Context, _ string, _ types.WebhookDeliveryStatus, _ int, _, _ string) error {
	return nil
}
func (m *mockStore) PruneWebhookDeliveries(_ context.Context, _ time.Duration) (int64, error) {
	return 0, nil
}

func (m *mockStore) CreateProvider(context.Context, types.ProviderRecord) error { return nil }
func (m *mockStore) GetProvider(context.Context, string) (*types.ProviderRecord, error) {
	return nil, types.ErrProviderNotFound
}
func (m *mockStore) UpdateProvider(context.Context, types.ProviderRecord) error { return nil }
func (m *mockStore) DeleteProvider(context.Context, string) error               { return nil }
func (m *mockStore) ListProviders(context.Context) ([]types.ProviderRecord, error) {
	return nil, nil
}

func (m *mockStore) CreateDiscordAuth(context.Context, types.DiscordAuthorization) error { return nil }
func (m *mockStore) GetDiscordAuth(context.Context, string, string) (*types.DiscordAuthorization, error) {
	return nil, nil
}
func (m *mockStore) GetDiscordAuthByCode(context.Context, string) (*types.DiscordAuthorization, error) {
	return nil, nil
}
func (m *mockStore) UpdateDiscordAuth(context.Context, types.DiscordAuthorization) error { return nil }
func (m *mockStore) ListDiscordAuths(context.Context, string) ([]types.DiscordAuthorization, error) {
	return nil, nil
}
func (m *mockStore) DeleteDiscordAuth(context.Context, string) error { return nil }

func (m *mockStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// Mock Queue
// ---------------------------------------------------------------------------

type mockQueue struct {
	mu       sync.Mutex
	messages []queue.QueueMessage
}

func newMockQueue() *mockQueue {
	return &mockQueue{}
}

func (q *mockQueue) Enqueue(_ context.Context, msg queue.QueueMessage) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.messages = append(q.messages, msg)
	return int64(len(q.messages)), nil
}

func (q *mockQueue) getMessages() []queue.QueueMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	cp := make([]queue.QueueMessage, len(q.messages))
	copy(cp, q.messages)
	return cp
}

// Unused Queue interface methods.
func (q *mockQueue) Dequeue(_ context.Context, _ string) <-chan queue.QueueMessage { return nil }
func (q *mockQueue) MarkProcessing(_ context.Context, _ int64) error               { return nil }
func (q *mockQueue) Complete(_ context.Context, _ int64) error                     { return nil }
func (q *mockQueue) Fail(_ context.Context, _ int64) error                         { return nil }
func (q *mockQueue) ResetAgentProcessing(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (q *mockQueue) Replay(_ context.Context) error                  { return nil }
func (q *mockQueue) ReplayAgent(_ context.Context, _ string) error   { return nil }
func (q *mockQueue) Cleanup(_ context.Context, _ int) (int64, error) { return 0, nil }
func (q *mockQueue) Depth(_ context.Context, _ string) (int, error)  { return 0, nil }
func (q *mockQueue) PriorityUsers() []string                         { return nil }
func (q *mockQueue) ListMessages(_ context.Context, _ string, _ string, _ int) ([]queue.QueueMessage, error) {
	return nil, nil
}
func (q *mockQueue) Stats(_ context.Context, _ string) (map[string]int, error) { return nil, nil }
func (q *mockQueue) RetryMessage(_ context.Context, _ int64) error             { return nil }
func (q *mockQueue) DeleteMessage(_ context.Context, _ int64) error            { return nil }
func (q *mockQueue) DeleteMessages(_ context.Context, _ string, _ string) (int64, error) {
	return 0, nil
}
func (q *mockQueue) Stop() {}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func newTestScheduler(t *testing.T) (*Scheduler, *mockStore, *mockQueue) {
	t.Helper()
	ms := newMockStore()
	mq := newMockQueue()
	sc, err := New(ms, mq, Config{Enabled: true, DefaultTimezone: "UTC"})
	if err != nil {
		t.Fatalf("New scheduler: %v", err)
	}
	return sc, ms, mq
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNew_ValidTimezone(t *testing.T) {
	ms := newMockStore()
	mq := newMockQueue()
	sc, err := New(ms, mq, Config{Enabled: true, DefaultTimezone: "America/Chicago"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc.timezone.String() != "America/Chicago" {
		t.Errorf("timezone = %q, want America/Chicago", sc.timezone)
	}
}

func TestNew_InvalidTimezone(t *testing.T) {
	_, err := New(newMockStore(), newMockQueue(), Config{DefaultTimezone: "Invalid/Zone"})
	if err == nil {
		t.Fatal("expected error for invalid timezone")
	}
}

func TestScheduleCRUD(t *testing.T) {
	sc, ms, _ := newTestScheduler(t)
	ctx := context.Background()

	sched := types.Schedule{
		ID:       "s1",
		AgentID:  "agent-1",
		Name:     "Morning Brief",
		CronExpr: "@every 1h",
		Message:  "Generate morning brief",
		Channel:  "internal",
		Type:     types.ScheduleTypeTask,
		Enabled:  true,
	}

	// Add
	if err := sc.Add(ctx, sched); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Verify in store
	got, err := ms.GetSchedule(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.Name != "Morning Brief" {
		t.Errorf("name = %q, want Morning Brief", got.Name)
	}

	// Verify cron entry registered
	sc.mu.Lock()
	_, hasCron := sc.entries["s1"]
	sc.mu.Unlock()
	if !hasCron {
		t.Error("cron entry not registered for enabled schedule")
	}

	// List
	list, err := sc.List(ctx, "agent-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("List len = %d, want 1", len(list))
	}

	// Update
	sched.Message = "Updated message"
	if err := sc.Update(ctx, sched); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = ms.GetSchedule(ctx, "s1")
	if got.Message != "Updated message" {
		t.Errorf("message after update = %q, want Updated message", got.Message)
	}

	// Remove
	if err := sc.Remove(ctx, "s1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	sc.mu.Lock()
	_, hasCron = sc.entries["s1"]
	sc.mu.Unlock()
	if hasCron {
		t.Error("cron entry still present after Remove")
	}
	_, err = ms.GetSchedule(ctx, "s1")
	if err != types.ErrScheduleNotFound {
		t.Errorf("expected ErrScheduleNotFound after Remove, got %v", err)
	}
}

func TestAddDisabledSchedule_NoCronEntry(t *testing.T) {
	sc, _, _ := newTestScheduler(t)
	ctx := context.Background()

	sched := types.Schedule{
		ID:       "s-disabled",
		AgentID:  "agent-1",
		CronExpr: "@every 1h",
		Message:  "test",
		Type:     types.ScheduleTypeTask,
		Enabled:  false,
	}

	if err := sc.Add(ctx, sched); err != nil {
		t.Fatalf("Add: %v", err)
	}

	sc.mu.Lock()
	_, hasCron := sc.entries["s-disabled"]
	sc.mu.Unlock()
	if hasCron {
		t.Error("disabled schedule should not have a cron entry")
	}
}

func TestToggle(t *testing.T) {
	sc, ms, _ := newTestScheduler(t)
	ctx := context.Background()

	sched := types.Schedule{
		ID:       "s-toggle",
		AgentID:  "agent-1",
		CronExpr: "@every 1h",
		Message:  "test",
		Type:     types.ScheduleTypeTask,
		Enabled:  true,
	}
	ms.mu.Lock()
	ms.schedules["s-toggle"] = sched
	ms.mu.Unlock()

	// Register cron first
	sc.mu.Lock()
	_ = sc.registerCron(sched)
	sc.mu.Unlock()

	// Toggle off
	if err := sc.Toggle(ctx, "s-toggle"); err != nil {
		t.Fatalf("Toggle off: %v", err)
	}
	got, _ := ms.GetSchedule(ctx, "s-toggle")
	if got.Enabled {
		t.Error("expected disabled after toggle")
	}
	sc.mu.Lock()
	_, hasCron := sc.entries["s-toggle"]
	sc.mu.Unlock()
	if hasCron {
		t.Error("cron entry should be removed after toggle off")
	}

	// Toggle on
	if err := sc.Toggle(ctx, "s-toggle"); err != nil {
		t.Fatalf("Toggle on: %v", err)
	}
	got, _ = ms.GetSchedule(ctx, "s-toggle")
	if !got.Enabled {
		t.Error("expected enabled after second toggle")
	}
	sc.mu.Lock()
	_, hasCron = sc.entries["s-toggle"]
	sc.mu.Unlock()
	if !hasCron {
		t.Error("cron entry should be registered after toggle on")
	}
}

func TestInvalidCronExpression(t *testing.T) {
	sc, _, _ := newTestScheduler(t)
	ctx := context.Background()

	sched := types.Schedule{
		ID:       "s-bad",
		AgentID:  "agent-1",
		CronExpr: "not a valid cron",
		Message:  "test",
		Type:     types.ScheduleTypeTask,
		Enabled:  true,
	}

	err := sc.Add(ctx, sched)
	if err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestStartLoadsEnabledSchedules(t *testing.T) {
	ms := newMockStore()
	mq := newMockQueue()

	// Seed schedules before Start.
	ms.schedules["s1"] = types.Schedule{
		ID: "s1", AgentID: "a1", CronExpr: "@every 1h",
		Message: "test", Type: types.ScheduleTypeTask, Enabled: true,
	}
	ms.schedules["s2"] = types.Schedule{
		ID: "s2", AgentID: "a1", CronExpr: "@every 2h",
		Message: "test2", Type: types.ScheduleTypeTask, Enabled: false,
	}
	ms.schedules["hb1"] = types.Schedule{
		ID: "hb1", AgentID: "a2", CronExpr: "@every 30m",
		Message: "heartbeat", Type: types.ScheduleTypeHeartbeat, Enabled: true,
	}

	sc, err := New(ms, mq, Config{Enabled: true, DefaultTimezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}

	if err := sc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sc.Stop()

	sc.mu.Lock()
	numEntries := len(sc.entries)
	_, hasHB := sc.heartbeats["a2"]
	sc.mu.Unlock()

	// Only enabled schedules: s1 and hb1
	if numEntries != 2 {
		t.Errorf("entries = %d, want 2", numEntries)
	}
	if !hasHB {
		t.Error("heartbeat for a2 not tracked")
	}
}

func TestStartSkipsInvalidCron(t *testing.T) {
	ms := newMockStore()
	mq := newMockQueue()

	ms.schedules["bad"] = types.Schedule{
		ID: "bad", AgentID: "a1", CronExpr: "invalid cron",
		Message: "test", Type: types.ScheduleTypeTask, Enabled: true,
	}

	sc, err := New(ms, mq, Config{Enabled: true, DefaultTimezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}

	// Should not error — just skip the bad schedule with a warning.
	if err := sc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sc.Stop()

	sc.mu.Lock()
	numEntries := len(sc.entries)
	sc.mu.Unlock()
	if numEntries != 0 {
		t.Errorf("entries = %d, want 0 (bad cron skipped)", numEntries)
	}
}

func TestFire_TaskSchedule(t *testing.T) {
	sc, ms, mq := newTestScheduler(t)

	ms.mu.Lock()
	ms.agents["agent-1"] = types.AgentConfig{ID: "agent-1"}
	ms.schedules["s1"] = types.Schedule{
		ID: "s1", AgentID: "agent-1", CronExpr: "@every 1h",
		Message: "do the thing", Channel: "internal",
		Type: types.ScheduleTypeTask, Enabled: true,
	}
	ms.mu.Unlock()

	sched := ms.schedules["s1"]
	sc.fire(sched)

	msgs := mq.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 enqueued message, got %d", len(msgs))
	}
	if msgs[0].Sender != "scheduler" {
		t.Errorf("sender = %q, want scheduler", msgs[0].Sender)
	}
	if msgs[0].Content != "do the thing" {
		t.Errorf("content = %q, want 'do the thing'", msgs[0].Content)
	}
	if msgs[0].AgentID != "agent-1" {
		t.Errorf("agent_id = %q, want agent-1", msgs[0].AgentID)
	}
}

func TestFire_HeartbeatSchedule(t *testing.T) {
	sc, ms, mq := newTestScheduler(t)

	ms.mu.Lock()
	ms.agents["agent-1"] = types.AgentConfig{ID: "agent-1"}
	ms.schedules["hb-agent-1"] = types.Schedule{
		ID: "hb-agent-1", AgentID: "agent-1", CronExpr: "@every 30m",
		Message: "check yourself", Channel: "internal",
		Type: types.ScheduleTypeHeartbeat, Enabled: true,
	}
	ms.mu.Unlock()

	sched := ms.schedules["hb-agent-1"]
	sc.fire(sched)

	msgs := mq.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Sender != "heartbeat" {
		t.Errorf("sender = %q, want heartbeat", msgs[0].Sender)
	}
}

func TestFire_HeartbeatQuietHours_Skipped(t *testing.T) {
	sc, ms, mq := newTestScheduler(t)

	// Configure agent with quiet hours that cover current UTC time.
	now := time.Now().UTC()
	qh := now.Format("15:04") + "-" + now.Add(2*time.Hour).Format("15:04")

	ms.mu.Lock()
	ms.agents["agent-1"] = types.AgentConfig{
		ID:            "agent-1",
		HeartbeatJSON: `{"enabled":true,"interval":"30m","prompt":"check","quiet_hours":"` + qh + `"}`,
	}
	ms.schedules["hb-agent-1"] = types.Schedule{
		ID: "hb-agent-1", AgentID: "agent-1", CronExpr: "@every 30m",
		Message: "check", Channel: "internal",
		Type: types.ScheduleTypeHeartbeat, Enabled: true, Timezone: "UTC",
	}
	ms.mu.Unlock()

	sched := ms.schedules["hb-agent-1"]
	sc.fire(sched)

	msgs := mq.getMessages()
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages during quiet hours, got %d", len(msgs))
	}
}

func TestFire_HeartbeatOutsideQuietHours_Fires(t *testing.T) {
	sc, ms, mq := newTestScheduler(t)

	// Quiet hours that do NOT cover current UTC time.
	now := time.Now().UTC()
	start := now.Add(3 * time.Hour).Format("15:04")
	end := now.Add(5 * time.Hour).Format("15:04")
	qh := start + "-" + end

	ms.mu.Lock()
	ms.agents["agent-1"] = types.AgentConfig{
		ID:            "agent-1",
		HeartbeatJSON: `{"enabled":true,"interval":"30m","prompt":"check","quiet_hours":"` + qh + `"}`,
	}
	ms.schedules["hb-agent-1"] = types.Schedule{
		ID: "hb-agent-1", AgentID: "agent-1", CronExpr: "@every 30m",
		Message: "check", Channel: "internal",
		Type: types.ScheduleTypeHeartbeat, Enabled: true, Timezone: "UTC",
	}
	ms.mu.Unlock()

	sched := ms.schedules["hb-agent-1"]
	sc.fire(sched)

	msgs := mq.getMessages()
	if len(msgs) != 1 {
		t.Errorf("expected 1 message outside quiet hours, got %d", len(msgs))
	}
}

func TestRegisterHeartbeat_New(t *testing.T) {
	sc, ms, _ := newTestScheduler(t)
	ctx := context.Background()

	hbCfg := types.HeartbeatConfig{
		Enabled:  true,
		Interval: "30m",
		Prompt:   "check your tasks",
	}

	if err := sc.RegisterHeartbeat(ctx, "agent-1", hbCfg, "UTC"); err != nil {
		t.Fatalf("RegisterHeartbeat: %v", err)
	}

	// Verify schedule created
	sched, err := ms.GetSchedule(ctx, "hb-agent-1")
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if sched.Type != types.ScheduleTypeHeartbeat {
		t.Errorf("type = %q, want heartbeat", sched.Type)
	}
	if sched.CronExpr != "@every 30m" {
		t.Errorf("cron = %q, want @every 30m", sched.CronExpr)
	}

	// Verify heartbeat tracking
	sc.mu.Lock()
	trackedID := sc.heartbeats["agent-1"]
	sc.mu.Unlock()
	if trackedID != "hb-agent-1" {
		t.Errorf("heartbeats[agent-1] = %q, want hb-agent-1", trackedID)
	}
}

func TestRegisterHeartbeat_Upsert(t *testing.T) {
	sc, ms, _ := newTestScheduler(t)
	ctx := context.Background()

	hbCfg := types.HeartbeatConfig{
		Enabled: true, Interval: "30m", Prompt: "first prompt",
	}
	if err := sc.RegisterHeartbeat(ctx, "agent-1", hbCfg, "UTC"); err != nil {
		t.Fatal(err)
	}

	// Update with new interval
	hbCfg2 := types.HeartbeatConfig{
		Enabled: true, Interval: "1h", Prompt: "updated prompt",
	}
	if err := sc.RegisterHeartbeat(ctx, "agent-1", hbCfg2, "UTC"); err != nil {
		t.Fatal(err)
	}

	// Should still be one schedule, with updated values
	sched, _ := ms.GetSchedule(ctx, "hb-agent-1")
	if sched.CronExpr != "@every 1h" {
		t.Errorf("cron = %q, want @every 1h", sched.CronExpr)
	}
	if sched.Message != "updated prompt" {
		t.Errorf("message = %q, want 'updated prompt'", sched.Message)
	}

	// Should not create a second schedule
	all, _ := ms.ListSchedulesByType(ctx, "agent-1", types.ScheduleTypeHeartbeat)
	if len(all) != 1 {
		t.Errorf("heartbeat count = %d, want 1", len(all))
	}
}

func TestUnregisterHeartbeat(t *testing.T) {
	sc, ms, _ := newTestScheduler(t)
	ctx := context.Background()

	hbCfg := types.HeartbeatConfig{
		Enabled: true, Interval: "30m", Prompt: "check",
	}
	_ = sc.RegisterHeartbeat(ctx, "agent-1", hbCfg, "UTC")

	if err := sc.UnregisterHeartbeat(ctx, "agent-1"); err != nil {
		t.Fatalf("UnregisterHeartbeat: %v", err)
	}

	// Schedule still exists but is disabled (not deleted)
	sched, err := ms.GetSchedule(ctx, "hb-agent-1")
	if err != nil {
		t.Fatalf("heartbeat schedule should still exist: %v", err)
	}
	if sched.Enabled {
		t.Error("heartbeat should be disabled after UnregisterHeartbeat")
	}

	// Cron entry removed
	sc.mu.Lock()
	_, hasCron := sc.entries["hb-agent-1"]
	sc.mu.Unlock()
	if hasCron {
		t.Error("cron entry should be removed")
	}
}

func TestUnregisterHeartbeat_NoHeartbeat(t *testing.T) {
	sc, _, _ := newTestScheduler(t)
	// Should be a no-op, no error
	if err := sc.UnregisterHeartbeat(context.Background(), "nonexistent"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnableHeartbeat(t *testing.T) {
	sc, ms, _ := newTestScheduler(t)
	ctx := context.Background()

	hbCfg := types.HeartbeatConfig{Enabled: true, Interval: "30m", Prompt: "check"}
	_ = sc.RegisterHeartbeat(ctx, "agent-1", hbCfg, "UTC")
	_ = sc.UnregisterHeartbeat(ctx, "agent-1")

	// Re-enable
	if err := sc.EnableHeartbeat(ctx, "agent-1"); err != nil {
		t.Fatalf("EnableHeartbeat: %v", err)
	}

	sched, _ := ms.GetSchedule(ctx, "hb-agent-1")
	if !sched.Enabled {
		t.Error("heartbeat should be enabled")
	}

	sc.mu.Lock()
	_, hasCron := sc.entries["hb-agent-1"]
	sc.mu.Unlock()
	if !hasCron {
		t.Error("cron entry should be registered after re-enable")
	}
}

func TestEnableHeartbeat_AlreadyEnabled(t *testing.T) {
	sc, _, _ := newTestScheduler(t)
	ctx := context.Background()

	hbCfg := types.HeartbeatConfig{Enabled: true, Interval: "30m", Prompt: "check"}
	_ = sc.RegisterHeartbeat(ctx, "agent-1", hbCfg, "UTC")

	// Should be a no-op
	if err := sc.EnableHeartbeat(ctx, "agent-1"); err != nil {
		t.Fatalf("EnableHeartbeat on already-enabled: %v", err)
	}
}

func TestRemoveAgentSchedules(t *testing.T) {
	sc, ms, _ := newTestScheduler(t)
	ctx := context.Background()

	// Create multiple schedules for one agent
	_ = sc.Add(ctx, types.Schedule{
		ID: "s1", AgentID: "agent-1", CronExpr: "@every 1h",
		Message: "task1", Type: types.ScheduleTypeTask, Enabled: true,
	})
	_ = sc.Add(ctx, types.Schedule{
		ID: "s2", AgentID: "agent-1", CronExpr: "@every 2h",
		Message: "task2", Type: types.ScheduleTypeTask, Enabled: true,
	})
	hbCfg := types.HeartbeatConfig{Enabled: true, Interval: "30m", Prompt: "hb"}
	_ = sc.RegisterHeartbeat(ctx, "agent-1", hbCfg, "UTC")

	// Also add a schedule for a different agent
	_ = sc.Add(ctx, types.Schedule{
		ID: "s-other", AgentID: "agent-2", CronExpr: "@every 1h",
		Message: "other", Type: types.ScheduleTypeTask, Enabled: true,
	})

	if err := sc.RemoveAgentSchedules(ctx, "agent-1"); err != nil {
		t.Fatalf("RemoveAgentSchedules: %v", err)
	}

	// All agent-1 schedules gone
	remaining, _ := ms.ListSchedules(ctx, "agent-1")
	if len(remaining) != 0 {
		t.Errorf("agent-1 schedules = %d, want 0", len(remaining))
	}

	// Heartbeat tracking removed
	sc.mu.Lock()
	_, hasHB := sc.heartbeats["agent-1"]
	sc.mu.Unlock()
	if hasHB {
		t.Error("heartbeat tracking should be removed")
	}

	// Other agent's schedules unaffected
	others, _ := ms.ListSchedules(ctx, "agent-2")
	if len(others) != 1 {
		t.Errorf("agent-2 schedules = %d, want 1", len(others))
	}
}

func TestPulseNow(t *testing.T) {
	sc, ms, mq := newTestScheduler(t)
	ctx := context.Background()

	ms.mu.Lock()
	ms.agents["agent-1"] = types.AgentConfig{ID: "agent-1"}
	ms.mu.Unlock()

	hbCfg := types.HeartbeatConfig{Enabled: true, Interval: "1h", Prompt: "pulse check"}
	_ = sc.RegisterHeartbeat(ctx, "agent-1", hbCfg, "UTC")

	if err := sc.PulseNow(ctx, "agent-1"); err != nil {
		t.Fatalf("PulseNow: %v", err)
	}

	msgs := mq.getMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message from PulseNow, got %d", len(msgs))
	}
	if msgs[0].Sender != "heartbeat" {
		t.Errorf("sender = %q, want heartbeat", msgs[0].Sender)
	}
	if msgs[0].Content != "pulse check" {
		t.Errorf("content = %q, want 'pulse check'", msgs[0].Content)
	}
}

func TestPulseNow_NoHeartbeat(t *testing.T) {
	sc, _, _ := newTestScheduler(t)

	err := sc.PulseNow(context.Background(), "no-hb-agent")
	if err == nil {
		t.Fatal("expected error when no heartbeat configured")
	}
}

func TestGetHeartbeatSchedule(t *testing.T) {
	sc, _, _ := newTestScheduler(t)
	ctx := context.Background()

	// No heartbeat registered
	sched, err := sc.GetHeartbeatSchedule(ctx, "agent-1")
	if err != nil || sched != nil {
		t.Errorf("expected nil, nil for no heartbeat; got %v, %v", sched, err)
	}

	// Register one
	hbCfg := types.HeartbeatConfig{Enabled: true, Interval: "30m", Prompt: "check"}
	_ = sc.RegisterHeartbeat(ctx, "agent-1", hbCfg, "UTC")

	sched, err = sc.GetHeartbeatSchedule(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetHeartbeatSchedule: %v", err)
	}
	if sched == nil {
		t.Fatal("expected non-nil schedule")
	}
	if sched.Type != types.ScheduleTypeHeartbeat {
		t.Errorf("type = %q, want heartbeat", sched.Type)
	}
}

func TestListByType_FiltersCorrectly(t *testing.T) {
	sc, _, _ := newTestScheduler(t)
	ctx := context.Background()

	_ = sc.Add(ctx, types.Schedule{
		ID: "t1", AgentID: "agent-1", CronExpr: "@every 1h",
		Message: "task", Type: types.ScheduleTypeTask, Enabled: true,
	})
	_ = sc.Add(ctx, types.Schedule{
		ID: "t2", AgentID: "agent-1", CronExpr: "@every 2h",
		Message: "task2", Type: types.ScheduleTypeTask, Enabled: true,
	})

	hbCfg := types.HeartbeatConfig{Enabled: true, Interval: "30m", Prompt: "hb"}
	_ = sc.RegisterHeartbeat(ctx, "agent-1", hbCfg, "UTC")

	// List only tasks
	tasks, err := sc.ListByType(ctx, "agent-1", types.ScheduleTypeTask)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Errorf("task count = %d, want 2", len(tasks))
	}

	// List only heartbeats
	heartbeats, err := sc.ListByType(ctx, "agent-1", types.ScheduleTypeHeartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 {
		t.Errorf("heartbeat count = %d, want 1", len(heartbeats))
	}

	// Heartbeat not returned by task filter
	for _, s := range tasks {
		if s.Type == types.ScheduleTypeHeartbeat {
			t.Error("heartbeat returned in task list")
		}
	}
}

func TestIntervalToCron(t *testing.T) {
	tests := []struct {
		interval, want string
	}{
		{"15m", "@every 15m"},
		{"30m", "@every 30m"},
		{"1h", "@every 1h"},
		{"2h", "@every 2h"},
		{"4h", "@every 4h"},
		{"8h", "0 0,8,16 * * *"},
		{"daily", "@daily"},
		{"24h", "@daily"},
		{"@every 5m", "@every 5m"}, // pass-through
		{"0 9 * * *", "0 9 * * *"}, // pass-through cron expr
	}

	for _, tt := range tests {
		got := intervalToCron(tt.interval)
		if got != tt.want {
			t.Errorf("intervalToCron(%q) = %q, want %q", tt.interval, got, tt.want)
		}
	}
}

func TestFire_UpdatesLastRunAt(t *testing.T) {
	sc, ms, _ := newTestScheduler(t)

	ms.mu.Lock()
	ms.agents["agent-1"] = types.AgentConfig{ID: "agent-1"}
	ms.schedules["s1"] = types.Schedule{
		ID: "s1", AgentID: "agent-1", CronExpr: "@every 1h",
		Message: "test", Channel: "internal",
		Type: types.ScheduleTypeTask, Enabled: true,
	}
	ms.mu.Unlock()

	before := time.Now()
	sc.fire(ms.schedules["s1"])

	updated, _ := ms.GetSchedule(context.Background(), "s1")
	if updated.LastRunAt == nil {
		t.Fatal("LastRunAt should be set after fire")
	}
	if updated.LastRunAt.Before(before) {
		t.Error("LastRunAt should be >= time before fire")
	}
}

// Workflow store stubs (satisfy store.Store interface)
func (m *mockStore) CreateWorkflow(_ context.Context, _ types.Workflow) error              { return nil }
func (m *mockStore) GetWorkflow(_ context.Context, _ string) (*types.Workflow, error)      { return nil, types.ErrNotFound }
func (m *mockStore) GetWorkflowByName(_ context.Context, _, _ string) (*types.Workflow, error) { return nil, types.ErrNotFound }
func (m *mockStore) UpdateWorkflow(_ context.Context, _ types.Workflow) error              { return nil }
func (m *mockStore) DeleteWorkflow(_ context.Context, _ string) error                      { return nil }
func (m *mockStore) ListWorkflows(_ context.Context, _ string) ([]types.Workflow, error)   { return nil, nil }
func (m *mockStore) CreateWorkflowRun(_ context.Context, _ types.WorkflowRun) error        { return nil }
func (m *mockStore) GetWorkflowRun(_ context.Context, _ string) (*types.WorkflowRun, error) { return nil, types.ErrNotFound }
func (m *mockStore) UpdateWorkflowRun(_ context.Context, _ types.WorkflowRun) error        { return nil }
func (m *mockStore) ListWorkflowRuns(_ context.Context, _ string, _ int) ([]types.WorkflowRun, error) { return nil, nil }

func (m *mockStore) RegisterNode(_ context.Context, _ types.NodeInfo) error { return nil }
func (m *mockStore) UpdateHeartbeat(_ context.Context, _ string, _ types.NodeCapacity) error {
	return nil
}
func (m *mockStore) ListNodes(_ context.Context) ([]types.NodeInfo, error)  { return nil, nil }
func (m *mockStore) GetDeadNodes(_ context.Context, _ time.Duration) ([]types.NodeInfo, error) {
	return nil, nil
}
func (m *mockStore) SetNodeStatus(_ context.Context, _, _ string) error     { return nil }
func (m *mockStore) DeleteNode(_ context.Context, _ string) error           { return nil }
func (m *mockStore) AssignAgent(_ context.Context, _, _ string) error       { return nil }
func (m *mockStore) GetAssignment(_ context.Context, _ string) (*types.Assignment, error) {
	return nil, nil
}
func (m *mockStore) GetNodeAgents(_ context.Context, _ string) ([]types.Assignment, error) {
	return nil, nil
}
func (m *mockStore) GetOrphanedAgents(_ context.Context, _ string) ([]types.Assignment, error) {
	return nil, nil
}
func (m *mockStore) DeleteAssignment(_ context.Context, _ string) error { return nil }
