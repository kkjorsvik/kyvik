// Package scheduler provides cron-based message injection for scheduled tasks
// and agent heartbeats. It uses robfig/cron to fire messages into the persistent
// queue at operator-defined intervals.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/kkjorsvik/kyvik/internal/queue"
	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Scheduler manages cron-triggered message injection for agents.
type Scheduler struct {
	store      store.Store
	queue      queue.Queue
	cron       *cron.Cron
	mu         sync.Mutex
	timezone   *time.Location
	entries    map[string]cron.EntryID // schedule.ID → cron entry
	heartbeats map[string]string       // agentID → schedule.ID
}

// Config holds scheduler configuration.
type Config struct {
	Enabled         bool
	DefaultTimezone string
}

// New creates a scheduler with the given store, queue, and configuration.
func New(s store.Store, q queue.Queue, cfg Config) (*Scheduler, error) {
	tz, err := time.LoadLocation(cfg.DefaultTimezone)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", cfg.DefaultTimezone, err)
	}

	c := cron.New(cron.WithLocation(tz))

	return &Scheduler{
		store:      s,
		queue:      q,
		cron:       c,
		timezone:   tz,
		entries:    make(map[string]cron.EntryID),
		heartbeats: make(map[string]string),
	}, nil
}

// Start loads all enabled schedules from the store, registers them with the cron
// engine, and begins firing. Should be called once after New.
func (sc *Scheduler) Start(ctx context.Context) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	schedules, err := sc.store.ListAllEnabledSchedules(ctx)
	if err != nil {
		return fmt.Errorf("load enabled schedules: %w", err)
	}

	for _, sched := range schedules {
		if err := sc.registerCron(sched); err != nil {
			slog.Warn("skipping schedule with invalid cron expression",
				"schedule_id", sched.ID, "agent_id", sched.AgentID, "error", err)
			continue
		}
		if sched.Type == types.ScheduleTypeHeartbeat {
			sc.heartbeats[sched.AgentID] = sched.ID
		}
	}

	sc.cron.Start()
	slog.Info("scheduler started", "schedules", len(sc.entries))
	return nil
}

// Stop gracefully stops the cron engine.
func (sc *Scheduler) Stop() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.cron.Stop()
	slog.Info("scheduler stopped")
}

// DefaultTimezone returns the scheduler's configured IANA timezone.
func (sc *Scheduler) DefaultTimezone() string {
	if sc.timezone == nil {
		return "UTC"
	}
	return sc.timezone.String()
}

// Add persists a new schedule and registers it with the cron engine.
func (sc *Scheduler) Add(ctx context.Context, sched types.Schedule) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if err := sc.store.CreateSchedule(ctx, sched); err != nil {
		return fmt.Errorf("persist schedule: %w", err)
	}
	if sched.Enabled {
		if err := sc.registerCron(sched); err != nil {
			return fmt.Errorf("register cron: %w", err)
		}
	}
	return nil
}

// Remove unregisters a schedule from cron and deletes it from the store.
func (sc *Scheduler) Remove(ctx context.Context, scheduleID string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.unregisterCron(scheduleID)

	if err := sc.store.DeleteSchedule(ctx, scheduleID); err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	return nil
}

// Update modifies a schedule: removes the old cron entry and re-adds it.
func (sc *Scheduler) Update(ctx context.Context, sched types.Schedule) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.unregisterCron(sched.ID)

	if err := sc.store.UpdateSchedule(ctx, sched); err != nil {
		return fmt.Errorf("update schedule: %w", err)
	}
	if sched.Enabled {
		if err := sc.registerCron(sched); err != nil {
			return fmt.Errorf("register cron: %w", err)
		}
	}
	return nil
}

// Toggle enables or disables a schedule.
func (sc *Scheduler) Toggle(ctx context.Context, scheduleID string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sched, err := sc.store.GetSchedule(ctx, scheduleID)
	if err != nil {
		return err
	}

	sched.Enabled = !sched.Enabled
	sched.UpdatedAt = timeutil.NowUTC()

	if err := sc.store.UpdateSchedule(ctx, *sched); err != nil {
		return err
	}

	if sched.Enabled {
		if err := sc.registerCron(*sched); err != nil {
			return fmt.Errorf("register cron: %w", err)
		}
	} else {
		sc.unregisterCron(scheduleID)
	}
	return nil
}

// List returns all schedules for an agent.
func (sc *Scheduler) List(ctx context.Context, agentID string) ([]types.Schedule, error) {
	return sc.store.ListSchedules(ctx, agentID)
}

// ListByType returns schedules for an agent filtered by type.
func (sc *Scheduler) ListByType(ctx context.Context, agentID, schedType string) ([]types.Schedule, error) {
	return sc.store.ListSchedulesByType(ctx, agentID, schedType)
}

// RegisterHeartbeat creates or updates a heartbeat schedule for an agent.
// Only one heartbeat per agent is allowed.
func (sc *Scheduler) RegisterHeartbeat(ctx context.Context, agentID string, hbCfg types.HeartbeatConfig, tz string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	cronExpr := intervalToCron(hbCfg.Interval)

	// Check if a heartbeat already exists for this agent.
	if existingID, ok := sc.heartbeats[agentID]; ok {
		// Upsert: update existing heartbeat.
		existing, err := sc.store.GetSchedule(ctx, existingID)
		if err != nil {
			// Schedule deleted from DB but still in map — clean up and create new.
			delete(sc.heartbeats, agentID)
			sc.unregisterCron(existingID)
		} else {
			sc.unregisterCron(existingID)
			existing.CronExpr = cronExpr
			existing.Message = hbCfg.Prompt
			existing.Enabled = hbCfg.Enabled
			existing.Timezone = tz
			existing.UpdatedAt = timeutil.NowUTC()

			if err := sc.store.UpdateSchedule(ctx, *existing); err != nil {
				return fmt.Errorf("update heartbeat schedule: %w", err)
			}
			if existing.Enabled {
				if err := sc.registerCron(*existing); err != nil {
					return fmt.Errorf("register heartbeat cron: %w", err)
				}
			}
			return nil
		}
	}

	// Create new heartbeat schedule.
	now := timeutil.NowUTC()
	sched := types.Schedule{
		ID:        fmt.Sprintf("hb-%s", agentID),
		AgentID:   agentID,
		Name:      "Heartbeat",
		CronExpr:  cronExpr,
		Message:   hbCfg.Prompt,
		Channel:   "internal",
		Type:      types.ScheduleTypeHeartbeat,
		Enabled:   hbCfg.Enabled,
		Timezone:  tz,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := sc.store.CreateSchedule(ctx, sched); err != nil {
		return fmt.Errorf("create heartbeat schedule: %w", err)
	}
	sc.heartbeats[agentID] = sched.ID
	if sched.Enabled {
		if err := sc.registerCron(sched); err != nil {
			return fmt.Errorf("register heartbeat cron: %w", err)
		}
	}
	return nil
}

// UnregisterHeartbeat disables (not deletes) the heartbeat for an agent.
func (sc *Scheduler) UnregisterHeartbeat(ctx context.Context, agentID string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	schedID, ok := sc.heartbeats[agentID]
	if !ok {
		return nil
	}

	sc.unregisterCron(schedID)

	sched, err := sc.store.GetSchedule(ctx, schedID)
	if err != nil {
		return nil // already gone
	}
	sched.Enabled = false
	sched.UpdatedAt = timeutil.NowUTC()
	return sc.store.UpdateSchedule(ctx, *sched)
}

// EnableHeartbeat re-enables a previously disabled heartbeat.
func (sc *Scheduler) EnableHeartbeat(ctx context.Context, agentID string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	schedID, ok := sc.heartbeats[agentID]
	if !ok {
		return nil
	}

	sched, err := sc.store.GetSchedule(ctx, schedID)
	if err != nil {
		return nil
	}
	if sched.Enabled {
		return nil // already enabled
	}

	sched.Enabled = true
	sched.UpdatedAt = timeutil.NowUTC()
	if err := sc.store.UpdateSchedule(ctx, *sched); err != nil {
		return err
	}
	return sc.registerCron(*sched)
}

// RemoveAgentSchedules deletes ALL schedules (tasks + heartbeats) for an agent.
func (sc *Scheduler) RemoveAgentSchedules(ctx context.Context, agentID string) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Unregister all cron entries for this agent's schedules.
	schedules, err := sc.store.ListSchedules(ctx, agentID)
	if err != nil {
		return err
	}
	for _, sched := range schedules {
		sc.unregisterCron(sched.ID)
	}

	// Remove heartbeat tracking.
	delete(sc.heartbeats, agentID)

	return sc.store.DeleteSchedulesByAgent(ctx, agentID)
}

// PulseNow fires an immediate heartbeat for an agent, bypassing the cron schedule.
func (sc *Scheduler) PulseNow(ctx context.Context, agentID string) error {
	sc.mu.Lock()
	schedID, ok := sc.heartbeats[agentID]
	sc.mu.Unlock()

	if !ok {
		return fmt.Errorf("no heartbeat configured for agent %s", agentID)
	}

	sched, err := sc.store.GetSchedule(ctx, schedID)
	if err != nil {
		return err
	}

	sc.fire(*sched)
	return nil
}

// GetHeartbeatSchedule returns the heartbeat schedule for an agent, if any.
func (sc *Scheduler) GetHeartbeatSchedule(ctx context.Context, agentID string) (*types.Schedule, error) {
	sc.mu.Lock()
	schedID, ok := sc.heartbeats[agentID]
	sc.mu.Unlock()

	if !ok {
		return nil, nil
	}
	return sc.store.GetSchedule(ctx, schedID)
}

// fire injects a message into the queue for a schedule trigger.
func (sc *Scheduler) fire(sched types.Schedule) {
	log := slog.With("schedule_id", sched.ID, "agent_id", sched.AgentID, "type", sched.Type)

	// For heartbeats, check quiet hours.
	if sched.Type == types.ScheduleTypeHeartbeat {
		agent, err := sc.store.GetAgent(context.Background(), sched.AgentID)
		if err != nil {
			log.Warn("skipping heartbeat: agent not found", "error", err)
			return
		}
		if agent.HeartbeatJSON != "" {
			var hbCfg types.HeartbeatConfig
			if json.Unmarshal([]byte(agent.HeartbeatJSON), &hbCfg) == nil && hbCfg.QuietHours != "" {
				if IsInQuietHours(hbCfg.QuietHours, sched.Timezone, timeutil.NowUTC()) {
					log.Debug("heartbeat skipped: quiet hours")
					return
				}
			}
		}
	}

	// Determine sender.
	sender := "scheduler"
	if sched.Type == types.ScheduleTypeHeartbeat {
		sender = "heartbeat"
	}

	// Enqueue the message.
	_, err := sc.queue.Enqueue(context.Background(), queue.QueueMessage{
		AgentID: sched.AgentID,
		Channel: "internal",
		Sender:  sender,
		Content: sched.Message,
	})
	if err != nil {
		log.Error("failed to enqueue scheduled message", "error", err)
		return
	}

	log.Info("scheduled message fired", "sender", sender)

	// Update last_run_at and next_run_at.
	now := timeutil.NowUTC()
	sched.LastRunAt = &now
	sched.UpdatedAt = now

	// Calculate next run from the cron entry.
	sc.mu.Lock()
	if entryID, ok := sc.entries[sched.ID]; ok {
		entry := sc.cron.Entry(entryID)
		if !entry.Next.IsZero() {
			sched.NextRunAt = &entry.Next
		}
	}
	sc.mu.Unlock()

	if err := sc.store.UpdateSchedule(context.Background(), sched); err != nil {
		log.Error("failed to update schedule run times", "error", err)
	}
}

// registerCron adds a schedule to the cron engine. Caller must hold sc.mu.
func (sc *Scheduler) registerCron(sched types.Schedule) error {
	entryID, err := sc.cron.AddFunc(sched.CronExpr, func() {
		// Re-fetch schedule to get latest state.
		current, err := sc.store.GetSchedule(context.Background(), sched.ID)
		if err != nil {
			slog.Warn("schedule no longer exists, skipping fire",
				"schedule_id", sched.ID, "error", err)
			return
		}
		if !current.Enabled {
			return
		}
		sc.fire(*current)
	})
	if err != nil {
		return fmt.Errorf("parse cron expression %q: %w", sched.CronExpr, err)
	}
	sc.entries[sched.ID] = entryID
	return nil
}

// unregisterCron removes a schedule from the cron engine. Caller must hold sc.mu.
func (sc *Scheduler) unregisterCron(scheduleID string) {
	if entryID, ok := sc.entries[scheduleID]; ok {
		sc.cron.Remove(entryID)
		delete(sc.entries, scheduleID)
	}
}

// intervalToCron converts a human-friendly interval to a cron expression.
func intervalToCron(interval string) string {
	switch interval {
	case "15m":
		return "@every 15m"
	case "30m":
		return "@every 30m"
	case "1h":
		return "@every 1h"
	case "2h":
		return "@every 2h"
	case "4h":
		return "@every 4h"
	case "8h":
		return "0 0,8,16 * * *"
	case "daily", "24h":
		return "@daily"
	default:
		// If it looks like a cron expression already, return as-is.
		return interval
	}
}
