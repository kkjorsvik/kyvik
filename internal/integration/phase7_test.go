package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/backup"
	"github.com/kkjorsvik/kyvik/internal/circuitbreaker"
	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/retention"
	"github.com/kkjorsvik/kyvik/internal/scheduler"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// =============================================================================
// Circuit Breaker Tests (no DB)
// =============================================================================

func TestPhase7_CircuitBreaker_ErrorRate(t *testing.T) {
	tripCh := make(chan circuitbreaker.TripResult, 1)
	mgr := circuitbreaker.NewManager(func(tr circuitbreaker.TripResult) {
		select {
		case tripCh <- tr:
		default:
		}
	})

	agentCfg := types.AgentConfig{
		ID:                 "cb-err",
		CircuitBreakerJSON: `{"enabled":true, "error_threshold":3, "error_window_minutes":10}`,
		Limits:             types.SpendingLimits{MaxSpendPerDay: 100},
	}

	// First 2 errors should not trip.
	for i := 0; i < 2; i++ {
		trip := mgr.RecordToolCall(agentCfg, "file", "write", false, false)
		if trip != nil {
			t.Fatalf("unexpected trip on error %d: %+v", i+1, trip)
		}
	}

	// 3rd error should trip.
	trip := mgr.RecordToolCall(agentCfg, "file", "write", false, false)
	if trip == nil {
		t.Fatal("expected trip on 3rd error, got nil")
	}
	if trip.Trigger != "error_rate" {
		t.Errorf("expected trigger error_rate, got %s", trip.Trigger)
	}

	// Verify callback fired.
	select {
	case tr := <-tripCh:
		if tr.AgentID != "cb-err" {
			t.Errorf("expected agent ID cb-err, got %s", tr.AgentID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("onTrip callback was not fired")
	}

	// Verify status.
	status := mgr.Status("cb-err")
	if !status.Tripped {
		t.Error("expected Tripped=true")
	}
}

func TestPhase7_CircuitBreaker_SpendingVelocity(t *testing.T) {
	mgr := circuitbreaker.NewManager(nil)

	agentCfg := types.AgentConfig{
		ID:                 "cb-spend",
		CircuitBreakerJSON: `{"enabled":true, "spending_velocity_pct":50, "spending_window_minutes":5}`,
		Limits:             types.SpendingLimits{MaxSpendPerDay: 10.0},
	}

	// First spend: $3.0 → 30% of $10 daily budget. Should not trip.
	trip := mgr.RecordSpending(agentCfg, 3.0)
	if trip != nil {
		t.Fatalf("unexpected trip on first spend: %+v", trip)
	}

	// Second spend: $3.0 → total $6.0 > $5.0 threshold (50% of $10). Should trip.
	trip = mgr.RecordSpending(agentCfg, 3.0)
	if trip == nil {
		t.Fatal("expected trip on second spend, got nil")
	}
	if trip.Trigger != "spending_velocity" {
		t.Errorf("expected trigger spending_velocity, got %s", trip.Trigger)
	}
}

func TestPhase7_CircuitBreaker_LoopDetection(t *testing.T) {
	mgr := circuitbreaker.NewManager(nil)

	agentCfg := types.AgentConfig{
		ID:                 "cb-loop",
		CircuitBreakerJSON: `{"enabled":true, "loop_identical_count":3}`,
		Limits:             types.SpendingLimits{MaxSpendPerDay: 100},
	}

	// First 2 identical messages should not trip.
	for i := 0; i < 2; i++ {
		trip := mgr.RecordMessage(agentCfg, "I am stuck")
		if trip != nil {
			t.Fatalf("unexpected trip on message %d: %+v", i+1, trip)
		}
	}

	// 3rd identical message should trip.
	trip := mgr.RecordMessage(agentCfg, "I am stuck")
	if trip == nil {
		t.Fatal("expected trip on 3rd identical message, got nil")
	}
	if trip.Trigger != "loop_detection" {
		t.Errorf("expected trigger loop_detection, got %s", trip.Trigger)
	}
}

// =============================================================================
// Kill Switch Tests (PostgreSQL + Kyvik core)
// =============================================================================

func TestPhase7_KillSwitch_SingleAgent(t *testing.T) {
	h := newP7CoreHarness(t)
	ctx := context.Background()

	cfg := p7AgentConfig("kill-1")
	if err := h.kyvik.StartAgent(ctx, cfg); err != nil {
		t.Fatalf("StartAgent: %v", err)
	}
	waitForStatus(t, h.kyvik, "kill-1", types.AgentStatusRunning, 2*time.Second)

	if err := h.kyvik.KillAgent(ctx, "kill-1"); err != nil {
		t.Fatalf("KillAgent: %v", err)
	}

	// Verify agent status is killed in store.
	stored, err := h.kyvik.GetAgent(ctx, "kill-1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if stored.ActualState != types.AgentStatusKilled {
		t.Errorf("expected killed, got %s", stored.ActualState)
	}

	// Single kill should NOT set emergency stop.
	if h.kyvik.EmergencyStopActive() {
		t.Error("emergency stop should not be active after single kill")
	}

	// Starting another agent should succeed.
	cfg2 := p7AgentConfig("kill-2")
	if err := h.kyvik.StartAgent(ctx, cfg2); err != nil {
		t.Fatalf("StartAgent(kill-2): %v", err)
	}
	waitForStatus(t, h.kyvik, "kill-2", types.AgentStatusRunning, 2*time.Second)
}

func TestPhase7_KillAll_EmergencyStop(t *testing.T) {
	h := newP7CoreHarness(t)
	ctx := context.Background()

	// Start 2 agents.
	for _, id := range []string{"ka-1", "ka-2"} {
		cfg := p7AgentConfig(id)
		if err := h.kyvik.StartAgent(ctx, cfg); err != nil {
			t.Fatalf("StartAgent(%s): %v", id, err)
		}
		waitForStatus(t, h.kyvik, id, types.AgentStatusRunning, 2*time.Second)
	}

	// Kill all.
	if err := h.kyvik.KillAll(ctx); err != nil {
		t.Fatalf("KillAll: %v", err)
	}

	// Both should be killed or stopped (goroutine may finish before status is read).
	for _, id := range []string{"ka-1", "ka-2"} {
		stored, err := h.kyvik.GetAgent(ctx, id)
		if err != nil {
			t.Fatalf("GetAgent(%s): %v", id, err)
		}
		if stored.ActualState != types.AgentStatusKilled && stored.ActualState != types.AgentStatusStopped {
			t.Errorf("agent %s: expected killed or stopped, got %s", id, stored.ActualState)
		}
	}

	// Emergency stop should be active.
	if !h.kyvik.EmergencyStopActive() {
		t.Error("expected emergency stop to be active")
	}

	// Starting a new agent should fail.
	err := h.kyvik.StartAgent(ctx, p7AgentConfig("ka-blocked"))
	if err == nil {
		t.Fatal("expected error starting agent during emergency stop")
	}

	// Clear emergency stop.
	if err := h.kyvik.ClearEmergencyStop(ctx); err != nil {
		t.Fatalf("ClearEmergencyStop: %v", err)
	}
	if h.kyvik.EmergencyStopActive() {
		t.Error("expected emergency stop to be cleared")
	}

	// Now a new agent should start.
	cfg := p7AgentConfig("ka-3")
	if err := h.kyvik.StartAgent(ctx, cfg); err != nil {
		t.Fatalf("StartAgent(ka-3): %v", err)
	}
	waitForStatus(t, h.kyvik, "ka-3", types.AgentStatusRunning, 2*time.Second)
}

// =============================================================================
// Vacation Mode Test (PostgreSQL + Kyvik core)
// =============================================================================

func TestPhase7_VacationMode_RoundTrip(t *testing.T) {
	h := newP7CoreHarness(t)
	ctx := context.Background()

	// Start 2 agents.
	for _, id := range []string{"vm-1", "vm-2"} {
		cfg := p7AgentConfig(id)
		if err := h.kyvik.StartAgent(ctx, cfg); err != nil {
			t.Fatalf("StartAgent(%s): %v", id, err)
		}
		waitForStatus(t, h.kyvik, id, types.AgentStatusRunning, 2*time.Second)
	}

	// Activate vacation mode.
	if err := h.kyvik.ActivateVacationMode(ctx, "admin", "fishing"); err != nil {
		t.Fatalf("ActivateVacationMode: %v", err)
	}

	if !h.kyvik.VacationModeActive() {
		t.Error("expected vacation mode to be active")
	}

	// Both agents should be stopped.
	for _, id := range []string{"vm-1", "vm-2"} {
		status, err := h.kyvik.GetAgentStatus(ctx, id)
		if err != nil {
			t.Fatalf("GetAgentStatus(%s): %v", id, err)
		}
		if status != types.AgentStatusStopped {
			t.Errorf("agent %s: expected stopped, got %s", id, status)
		}
	}

	// Starting a new agent should fail with vacation mode error.
	err := h.kyvik.StartAgent(ctx, p7AgentConfig("vm-blocked"))
	if err == nil {
		t.Fatal("expected error starting agent during vacation mode")
	}

	// Vacation state should list previous agents.
	state, err := h.kyvik.GetVacationState(ctx)
	if err != nil {
		t.Fatalf("GetVacationState: %v", err)
	}
	if len(state.PreviousAgents) != 2 {
		t.Errorf("expected 2 previous agents, got %d", len(state.PreviousAgents))
	}

	// Deactivate vacation mode.
	if err := h.kyvik.DeactivateVacationMode(ctx); err != nil {
		t.Fatalf("DeactivateVacationMode: %v", err)
	}

	if h.kyvik.VacationModeActive() {
		t.Error("expected vacation mode to be inactive")
	}

	// Both agents should resume to running.
	for _, id := range []string{"vm-1", "vm-2"} {
		waitForStatus(t, h.kyvik, id, types.AgentStatusRunning, 3*time.Second)
	}
}

// =============================================================================
// Scheduler + Queue Tests (PostgreSQL store)
// =============================================================================

func TestPhase7_ScheduledTask_Fires(t *testing.T) {
	sh := newP7StoreHarness(t)
	ctx := context.Background()

	q := mustNewPQ(sh.db)
	t.Cleanup(func() { q.Stop() })

	sched, err := scheduler.New(sh.store, q, scheduler.Config{
		Enabled:         true,
		DefaultTimezone: "UTC",
	})
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}

	// Seed agent.
	p7SeedAgent(t, sh.store, "sched-1", "Scheduler Agent")

	// Start scheduler.
	if err := sched.Start(ctx); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	t.Cleanup(func() { sched.Stop() })

	// Add schedule that fires every second.
	now := time.Now()
	if err := sched.Add(ctx, types.Schedule{
		ID:        "task-1",
		AgentID:   "sched-1",
		Name:      "Test Task",
		CronExpr:  "@every 1s",
		Message:   "check-in",
		Channel:   "internal",
		Type:      types.ScheduleTypeTask,
		Enabled:   true,
		Timezone:  "UTC",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("scheduler.Add: %v", err)
	}

	// Poll the queue DB for the message (timeout 3s).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var sender, content string
		err := sh.db.QueryRow(
			`SELECT sender, content FROM message_queue WHERE agent_id = 'sched-1' LIMIT 1`,
		).Scan(&sender, &content)
		if err == nil {
			if sender != "scheduler" {
				t.Errorf("expected sender 'scheduler', got %q", sender)
			}
			if content != "check-in" {
				t.Errorf("expected content 'check-in', got %q", content)
			}
			return // success
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("scheduled message did not appear in queue within 3s")
}

func TestPhase7_Heartbeat_FiresWithContent(t *testing.T) {
	sh := newP7StoreHarness(t)
	ctx := context.Background()

	q := mustNewPQ(sh.db)
	t.Cleanup(func() { q.Stop() })

	sched, err := scheduler.New(sh.store, q, scheduler.Config{
		Enabled:         true,
		DefaultTimezone: "UTC",
	})
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	if err := sched.Start(ctx); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	t.Cleanup(func() { sched.Stop() })

	// Seed agent.
	p7SeedAgent(t, sh.store, "hb-1", "Heartbeat Agent")

	// Register heartbeat.
	if err := sched.RegisterHeartbeat(ctx, "hb-1", types.HeartbeatConfig{
		Enabled:  true,
		Interval: "1h",
		Prompt:   "status check",
	}, "UTC"); err != nil {
		t.Fatalf("RegisterHeartbeat: %v", err)
	}

	// Fire immediately via PulseNow.
	if err := sched.PulseNow(ctx, "hb-1"); err != nil {
		t.Fatalf("PulseNow: %v", err)
	}

	// Verify message in queue.
	var sender, content string
	err = sh.db.QueryRow(
		`SELECT sender, content FROM message_queue WHERE agent_id = 'hb-1' LIMIT 1`,
	).Scan(&sender, &content)
	if err != nil {
		t.Fatalf("query queue: %v", err)
	}
	if sender != "heartbeat" {
		t.Errorf("expected sender 'heartbeat', got %q", sender)
	}
	if content != "status check" {
		t.Errorf("expected content 'status check', got %q", content)
	}
}

func TestPhase7_Heartbeat_RespectsQuietHours(t *testing.T) {
	tests := []struct {
		name       string
		quietHours string
		timezone   string
		hour       int
		minute     int
		expected   bool
	}{
		{"23:00 in 22:00-07:00", "22:00-07:00", "UTC", 23, 0, true},
		{"03:00 in 22:00-07:00", "22:00-07:00", "UTC", 3, 0, true},
		{"10:00 in 22:00-07:00", "22:00-07:00", "UTC", 10, 0, false},
		{"06:59 in 22:00-07:00", "22:00-07:00", "UTC", 6, 59, true},
		{"07:00 in 22:00-07:00", "22:00-07:00", "UTC", 7, 0, false},
		{"empty range", "", "UTC", 12, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkTime := time.Date(2025, 6, 15, tt.hour, tt.minute, 0, 0, time.UTC)
			got := scheduler.IsInQuietHours(tt.quietHours, tt.timezone, checkTime)
			if got != tt.expected {
				t.Errorf("IsInQuietHours(%q, %q, %02d:%02d) = %v, want %v",
					tt.quietHours, tt.timezone, tt.hour, tt.minute, got, tt.expected)
			}
		})
	}
}

func TestPhase7_Heartbeat_LifecycleTiedToAgent(t *testing.T) {
	sh := newP7StoreHarness(t)
	ctx := context.Background()

	q := mustNewPQ(sh.db)
	t.Cleanup(func() { q.Stop() })

	sched, err := scheduler.New(sh.store, q, scheduler.Config{
		Enabled:         true,
		DefaultTimezone: "UTC",
	})
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	if err := sched.Start(ctx); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	t.Cleanup(func() { sched.Stop() })

	// Seed agent and register heartbeat.
	p7SeedAgent(t, sh.store, "hblc-1", "HB Lifecycle Agent")
	if err := sched.RegisterHeartbeat(ctx, "hblc-1", types.HeartbeatConfig{
		Enabled:  true,
		Interval: "1h",
		Prompt:   "ping",
	}, "UTC"); err != nil {
		t.Fatalf("RegisterHeartbeat: %v", err)
	}

	// Verify schedule exists.
	schedules, err := sched.List(ctx, "hblc-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(schedules) == 0 {
		t.Fatal("expected at least 1 schedule")
	}

	// Remove all agent schedules.
	if err := sched.RemoveAgentSchedules(ctx, "hblc-1"); err != nil {
		t.Fatalf("RemoveAgentSchedules: %v", err)
	}

	// Verify no schedules remain.
	schedules, err = sched.List(ctx, "hblc-1")
	if err != nil {
		t.Fatalf("List after remove: %v", err)
	}
	if len(schedules) != 0 {
		t.Errorf("expected 0 schedules after remove, got %d", len(schedules))
	}

	// PulseNow should fail.
	if err := sched.PulseNow(ctx, "hblc-1"); err == nil {
		t.Error("expected PulseNow to fail after schedules removed")
	}
}

// =============================================================================
// Retention Pruning Test (PostgreSQL store)
// =============================================================================

func TestPhase7_RetentionPruning_DeletesOldData(t *testing.T) {
	db := p7PrunerDB(t)
	// Do NOT close db — it is a shared PostgresStore DB used across tests.

	ctx := context.Background()
	timeFmt := "2006-01-02 15:04:05"

	// Insert 3 old audit rows (100 days old) and 1 recent.
	old := time.Now().AddDate(0, 0, -100).UTC().Format(timeFmt)
	recent := time.Now().AddDate(0, 0, -10).UTC().Format(timeFmt)

	for i := 0; i < 3; i++ {
		db.Exec("INSERT INTO audit_log (agent_id, action, decision, created_at) VALUES ('a1', 'test', 'allow', $1)", old)
	}
	db.Exec("INSERT INTO audit_log (agent_id, action, decision, created_at) VALUES ('a1', 'test', 'allow', $1)", recent)

	// Insert 2 old completed queue messages (48h) and 1 recent.
	oldQueue := time.Now().Add(-48 * time.Hour).UTC().Format(timeFmt)
	recentQueue := time.Now().Add(-1 * time.Hour).UTC().Format(timeFmt)

	db.Exec("INSERT INTO message_queue (agent_id, content, status, completed_at) VALUES ('a1', 'old1', 'completed', $1)", oldQueue)
	db.Exec("INSERT INTO message_queue (agent_id, content, status, completed_at) VALUES ('a1', 'old2', 'completed', $1)", oldQueue)
	db.Exec("INSERT INTO message_queue (agent_id, content, status, completed_at) VALUES ('a1', 'recent', 'completed', $1)", recentQueue)

	enabled := true
	cfg := config.RetentionConfig{
		Enabled:                 &enabled,
		AuditLogsDays:           90,
		ConversationHistoryDays: 180,
		CompletedQueueHours:     24,
		SecurityEventsDays:      90,
		ArchivedMemoriesDays:    365,
		WebConversationsDays:    365,
		Schedule:                "0 4 * * *",
	}

	p := retention.New(db, &p7StateStore{db}, cfg)
	result := p.RunNow(ctx)

	if result.AuditLogsDeleted != 3 {
		t.Errorf("expected 3 audit logs deleted, got %d", result.AuditLogsDeleted)
	}
	if result.QueueMessagesDeleted != 2 {
		t.Errorf("expected 2 queue messages deleted, got %d", result.QueueMessagesDeleted)
	}

	// Verify recent entries survived.
	var auditCount int
	db.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&auditCount)
	if auditCount != 1 {
		t.Errorf("expected 1 remaining audit entry, got %d", auditCount)
	}

	var queueCount int
	db.QueryRow("SELECT COUNT(*) FROM message_queue").Scan(&queueCount)
	if queueCount != 1 {
		t.Errorf("expected 1 remaining queue message, got %d", queueCount)
	}

	// Verify TotalDeleted is correct.
	if result.TotalDeleted() < 5 {
		t.Errorf("expected TotalDeleted() >= 5, got %d", result.TotalDeleted())
	}

	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got %v", result.Errors)
	}
}

// =============================================================================
// Backup Tests (PostgreSQL)
// =============================================================================

func TestPhase7_BackupRestore_RoundTrip(t *testing.T) {
	t.Skip("backup tests require reimplementation for PostgreSQL")
}

func TestPhase7_AgentExportImport_RoundTrip(t *testing.T) {
	sh := newP7StoreHarness(t)
	ctx := context.Background()

	// Seed agent.
	p7SeedAgent(t, sh.store, "exp-1", "Export Agent")

	// Create a schedule for the agent.
	now := time.Now()
	err := sh.store.CreateSchedule(ctx, types.Schedule{
		ID:        "sched-exp-1",
		AgentID:   "exp-1",
		Name:      "Daily Report",
		CronExpr:  "@daily",
		Message:   "generate report",
		Channel:   "internal",
		Type:      types.ScheduleTypeTask,
		Enabled:   true,
		Timezone:  "UTC",
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	// Export the agent.
	tmpDir := t.TempDir()
	deps := backup.ExportDeps{
		Store:         sh.store,
		ScheduleStore: sh.store,
	}
	archivePath, err := backup.ExportAgent(ctx, deps, "exp-1", "pass123", tmpDir)
	if err != nil {
		t.Fatalf("ExportAgent: %v", err)
	}

	// Import the agent.
	imported, err := backup.ImportAgent(ctx, deps, archivePath, "pass123")
	if err != nil {
		t.Fatalf("ImportAgent: %v", err)
	}

	// Imported agent should have a different ID.
	if imported.ID == "exp-1" {
		t.Error("imported agent should have a new ID")
	}

	// Name should contain "(imported)".
	if imported.Name != "Export Agent (imported)" {
		t.Errorf("expected name 'Export Agent (imported)', got %q", imported.Name)
	}

	// Verify imported schedules exist under the new ID.
	schedules, err := sh.store.ListSchedules(ctx, imported.ID)
	if err != nil {
		t.Fatalf("ListSchedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Errorf("expected 1 imported schedule, got %d", len(schedules))
	}
}

// =============================================================================
// Interaction Tests
// =============================================================================

func TestPhase7_CircuitBreaker_Callback(t *testing.T) {
	var results []circuitbreaker.TripResult
	mgr := circuitbreaker.NewManager(func(tr circuitbreaker.TripResult) {
		results = append(results, tr)
	})

	agentCfg := types.AgentConfig{
		ID:                 "cb-callback",
		CircuitBreakerJSON: `{"enabled":true, "error_threshold":5, "error_window_minutes":10}`,
		Limits:             types.SpendingLimits{MaxSpendPerDay: 100},
	}

	// Trip via 5 errors (default threshold).
	for i := 0; i < 5; i++ {
		mgr.RecordToolCall(agentCfg, "file", "write", false, false)
	}

	// Callback should have been called once.
	if len(results) != 1 {
		t.Fatalf("expected 1 callback, got %d", len(results))
	}
	if results[0].AgentID != "cb-callback" {
		t.Errorf("expected agent ID cb-callback, got %s", results[0].AgentID)
	}
	if results[0].Trigger != "error_rate" {
		t.Errorf("expected trigger error_rate, got %s", results[0].Trigger)
	}
	if results[0].Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}

	// Subsequent RecordToolCall returns nil (breaker latched).
	trip := mgr.RecordToolCall(agentCfg, "file", "write", false, false)
	if trip != nil {
		t.Errorf("expected nil from latched breaker, got %+v", trip)
	}

	// Status should be tripped.
	status := mgr.Status("cb-callback")
	if !status.Tripped {
		t.Error("expected Tripped=true")
	}
}

func TestPhase7_VacationMode_ScheduledTasks(t *testing.T) {
	sh := newP7StoreHarness(t)
	ctx := context.Background()

	q := mustNewPQ(sh.db)
	t.Cleanup(func() { q.Stop() })

	sched, err := scheduler.New(sh.store, q, scheduler.Config{
		Enabled:         true,
		DefaultTimezone: "UTC",
	})
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	if err := sched.Start(ctx); err != nil {
		t.Fatalf("scheduler.Start: %v", err)
	}
	t.Cleanup(func() { sched.Stop() })

	// Seed agent and register heartbeat.
	p7SeedAgent(t, sh.store, "vm-sched", "Vacation Scheduled Agent")
	if err := sched.RegisterHeartbeat(ctx, "vm-sched", types.HeartbeatConfig{
		Enabled:  true,
		Interval: "1h",
		Prompt:   "vacation heartbeat",
	}, "UTC"); err != nil {
		t.Fatalf("RegisterHeartbeat: %v", err)
	}

	// PulseNow while simulating vacation (scheduler has no vacation mode awareness).
	if err := sched.PulseNow(ctx, "vm-sched"); err != nil {
		t.Fatalf("PulseNow: %v", err)
	}

	// Verify message landed in queue (scheduler always enqueues).
	var sender, content string
	err = sh.db.QueryRow(
		`SELECT sender, content FROM message_queue WHERE agent_id = 'vm-sched' LIMIT 1`,
	).Scan(&sender, &content)
	if err != nil {
		t.Fatalf("query queue: %v", err)
	}
	if sender != "heartbeat" {
		t.Errorf("expected sender 'heartbeat', got %q", sender)
	}
	if content != "vacation heartbeat" {
		t.Errorf("expected content 'vacation heartbeat', got %q", content)
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkCircuitBreaker_RecordToolCall(b *testing.B) {
	mgr := circuitbreaker.NewManager(nil)
	agentCfg := types.AgentConfig{
		ID:                 "bench",
		CircuitBreakerJSON: `{"enabled":true}`,
		Limits:             types.SpendingLimits{MaxSpendPerDay: 100},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mgr.RecordToolCall(agentCfg, "file", "write", true, false)
	}
}

func BenchmarkPruner_10kRows(b *testing.B) {
	db := p7PrunerDB(b)
	// Do NOT close db — it is a shared PostgresStore DB used across tests.

	timeFmt := "2006-01-02 15:04:05"
	old := time.Now().AddDate(0, 0, -100).UTC().Format(timeFmt)

	// Batch insert 10k old audit rows.
	tx, err := db.Begin()
	if err != nil {
		b.Fatalf("begin tx: %v", err)
	}
	stmt, err := tx.Prepare("INSERT INTO audit_log (agent_id, action, decision, created_at) VALUES ('a1', 'test', 'allow', $1)")
	if err != nil {
		b.Fatalf("prepare: %v", err)
	}
	for i := 0; i < 10000; i++ {
		stmt.Exec(old)
	}
	stmt.Close()
	tx.Commit()

	enabled := true
	cfg := config.RetentionConfig{
		Enabled:                 &enabled,
		AuditLogsDays:           90,
		ConversationHistoryDays: 180,
		CompletedQueueHours:     24,
		SecurityEventsDays:      90,
		ArchivedMemoriesDays:    365,
		WebConversationsDays:    365,
		Schedule:                "0 4 * * *",
	}

	b.Run("first_run", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			// Ensure 10k rows exist before each prune.
			db.Exec("DELETE FROM audit_log")
			tx, _ := db.Begin()
			stmt, _ := tx.Prepare("INSERT INTO audit_log (agent_id, action, decision, created_at) VALUES ('a1', 'test', 'allow', $1)")
			for j := 0; j < 10000; j++ {
				stmt.Exec(old)
			}
			stmt.Close()
			tx.Commit()
			b.StartTimer()

			p := retention.New(db, &p7StateStore{db}, cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			p.RunNow(ctx)
			cancel()
		}
	})

	b.Run("empty_run", func(b *testing.B) {
		// Ensure table is empty.
		db.Exec("DELETE FROM audit_log")

		for i := 0; i < b.N; i++ {
			p := retention.New(db, &p7StateStore{db}, cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			p.RunNow(ctx)
			cancel()
		}
	})
}

// Ensure imported packages are used.
var _ = fmt.Sprintf
