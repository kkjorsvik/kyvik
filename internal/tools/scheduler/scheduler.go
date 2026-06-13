// Package scheduler implements a KTP tool that lets agents manage their own
// scheduled tasks (create, list, update, delete). Agents can only manage
// schedules targeting themselves.
package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/kkjorsvik/kyvik/internal/ktp"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// maxSchedulesPerAgent prevents runaway schedule creation.
const maxSchedulesPerAgent = 10

// SchedulerManager is the interface required from the scheduler subsystem.
type SchedulerManager interface {
	Add(ctx context.Context, sched types.Schedule) error
	Update(ctx context.Context, sched types.Schedule) error
	Remove(ctx context.Context, scheduleID string) error
	List(ctx context.Context, agentID string) ([]types.Schedule, error)
	DefaultTimezone() string
}

// Tool implements ktp.InlineTool for agent self-scheduling.
type Tool struct {
	sched SchedulerManager
}

// New creates a scheduler tool backed by the given scheduler manager.
func New(sched SchedulerManager) *Tool {
	return &Tool{sched: sched}
}

// Declaration returns the scheduler tool's KTP declaration.
func (t *Tool) Declaration() ktp.ToolDeclaration {
	return ktp.ToolDeclaration{
		Name:         "scheduler",
		Version:      "1.0.0",
		Description: "Create and manage your own scheduled tasks. When a schedule fires, you receive its message as a new conversation prompt. " +
			"Use this for recurring work: daily reports, periodic checks, reminders, data refreshes. " +
			"You can have up to 10 scheduled tasks. Cron syntax: '0 9 * * *' (9am daily), '0 9 * * 1-5' (weekday 9am), " +
			"'*/30 * * * *' (every 30 min), '@every 2h', '@daily', '@weekly'. " +
			"Always list_tasks first to check existing schedules before creating new ones.",
		MinTier:      ktp.TierReader,
		DefaultTiers: []string{ktp.TierReader, ktp.TierWriter, ktp.TierOperator, ktp.TierAdmin},
		Actions: []ktp.ActionSpec{
			{
				Name:        "create_task",
				Description: "Create a new scheduled task. The message you set will be sent to you as a conversation prompt each time the schedule fires. " +
					"Write the message as an instruction to yourself (e.g. 'Check the inbox and summarize new emails' rather than 'email summary'). " +
					"Use descriptive names so you can identify tasks in list_tasks.",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"name":      {Type: "string", Description: "Human-readable name for the task"},
						"cron_expr": {Type: "string", Description: "Cron expression (e.g. '@every 1h', '0 9 * * *' for 9am daily, '0 9 * * 1' for Monday 9am)"},
						"message":   {Type: "string", Description: "The message content you will receive when the schedule fires"},
						"enabled":   {Type: "boolean", Description: "Whether the schedule is active immediately", Default: true},
						"timezone":  {Type: "string", Description: "IANA timezone (e.g. 'America/New_York'). Defaults to system timezone"},
					},
					Required: []string{"name", "cron_expr", "message"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"id":   {Type: "string", Description: "The created schedule ID"},
						"name": {Type: "string"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "scheduler", Access: "write", Resource: "*"}},
			},
			{
				Name:        "list_tasks",
				Description: "List all your scheduled tasks with their IDs, cron expressions, enabled status, and last/next run times. " +
					"Call this before creating tasks to avoid duplicates, and to get schedule IDs for update/delete.",
				Parameters: ktp.JSONSchema{
					Type:       "object",
					Properties: map[string]ktp.JSONSchema{},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"tasks": {Type: "array", Items: &ktp.JSONSchema{Type: "object"}},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "scheduler", Access: "read", Resource: "*"}},
			},
			{
				Name:        "update_task",
				Description: "Update an existing scheduled task. Only provide fields you want to change; omitted fields keep their current values. " +
					"Use this to change the schedule frequency, update the message, or enable/disable a task.",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"id":        {Type: "string", Description: "Schedule ID to update"},
						"name":      {Type: "string", Description: "New name"},
						"cron_expr": {Type: "string", Description: "New cron expression"},
						"message":   {Type: "string", Description: "New message content"},
						"enabled":   {Type: "boolean", Description: "Enable or disable the schedule"},
						"timezone":  {Type: "string", Description: "New IANA timezone"},
					},
					Required: []string{"id"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"updated": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "scheduler", Access: "write", Resource: "*"}},
			},
			{
				Name:        "delete_task",
				Description: "Permanently delete a scheduled task. Use list_tasks first to confirm the correct ID. " +
					"Consider disabling via update_task instead if you might want to re-enable it later.",
				Parameters: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"id": {Type: "string", Description: "Schedule ID to delete"},
					},
					Required: []string{"id"},
				},
				Returns: ktp.JSONSchema{
					Type: "object",
					Properties: map[string]ktp.JSONSchema{
						"deleted": {Type: "boolean"},
					},
				},
				RequiredCapabilities: []ktp.Capability{{Type: "scheduler", Access: "write", Resource: "*"}},
				Destructive:          true,
			},
		},
	}
}

// Inline returns true because the scheduler tool accesses local state only.
func (t *Tool) Inline() bool { return true }

// Execute dispatches to the requested action.
func (t *Tool) Execute(ctx context.Context, req ktp.ToolRequest) (*ktp.ToolResponse, error) {
	start := time.Now()

	switch req.Action {
	case "create_task":
		return t.createTask(ctx, req, start)
	case "list_tasks":
		return t.listTasks(ctx, req, start)
	case "update_task":
		return t.updateTask(ctx, req, start)
	case "delete_task":
		return t.deleteTask(ctx, req, start)
	default:
		return errResp(req.ID, fmt.Sprintf("unknown action: %s", req.Action)), nil
	}
}

func (t *Tool) createTask(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	name, err := strParam(req.Parameters, "name")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	cronExpr, err := strParam(req.Parameters, "cron_expr")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	message, err := strParam(req.Parameters, "message")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}
	enabled := boolDefault(req.Parameters, "enabled", true)
	timezone := strDefault(req.Parameters, "timezone", t.sched.DefaultTimezone())

	// Enforce per-agent limit.
	existing, err := t.sched.List(ctx, req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to check existing schedules: %s", err)), nil
	}
	taskCount := 0
	for _, s := range existing {
		if s.Type == types.ScheduleTypeTask {
			taskCount++
		}
	}
	if taskCount >= maxSchedulesPerAgent {
		return errResp(req.ID, fmt.Sprintf("maximum of %d scheduled tasks reached", maxSchedulesPerAgent)), nil
	}

	now := timeutil.NowUTC()
	sched := types.Schedule{
		ID:        uuid.New().String(),
		AgentID:   req.AgentID,
		Name:      name,
		CronExpr:  cronExpr,
		Message:   message,
		Channel:   "internal",
		Type:      types.ScheduleTypeTask,
		Enabled:   enabled,
		Timezone:  timezone,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := t.sched.Add(ctx, sched); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to create schedule: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{
		"id":   sched.ID,
		"name": sched.Name,
	}, "", ms(start))
	return &resp, nil
}

func (t *Tool) listTasks(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	schedules, err := t.sched.List(ctx, req.AgentID)
	if err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to list schedules: %s", err)), nil
	}

	// Only show task-type schedules (not heartbeats).
	tasks := make([]map[string]any, 0, len(schedules))
	for _, s := range schedules {
		if s.Type != types.ScheduleTypeTask {
			continue
		}
		entry := map[string]any{
			"id":         s.ID,
			"name":       s.Name,
			"cron_expr":  s.CronExpr,
			"message":    s.Message,
			"enabled":    s.Enabled,
			"timezone":   s.Timezone,
			"created_at": s.CreatedAt.UTC().Format(time.RFC3339),
		}
		if s.LastRunAt != nil {
			entry["last_run_at"] = s.LastRunAt.UTC().Format(time.RFC3339)
		}
		if s.NextRunAt != nil {
			entry["next_run_at"] = s.NextRunAt.UTC().Format(time.RFC3339)
		}
		tasks = append(tasks, entry)
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"tasks": tasks}, "", ms(start))
	return &resp, nil
}

func (t *Tool) updateTask(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	id, err := strParam(req.Parameters, "id")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	// Find the schedule and verify ownership.
	sched, err := t.findOwnedTask(ctx, req.AgentID, id)
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	// Apply updates.
	if v, ok := req.Parameters["name"]; ok {
		if s, ok := v.(string); ok && s != "" {
			sched.Name = s
		}
	}
	if v, ok := req.Parameters["cron_expr"]; ok {
		if s, ok := v.(string); ok && s != "" {
			sched.CronExpr = s
		}
	}
	if v, ok := req.Parameters["message"]; ok {
		if s, ok := v.(string); ok && s != "" {
			sched.Message = s
		}
	}
	if v, ok := req.Parameters["enabled"]; ok {
		if b, ok := v.(bool); ok {
			sched.Enabled = b
		}
	}
	if v, ok := req.Parameters["timezone"]; ok {
		if s, ok := v.(string); ok && s != "" {
			sched.Timezone = s
		}
	}
	sched.UpdatedAt = timeutil.NowUTC()

	if err := t.sched.Update(ctx, *sched); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to update schedule: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"updated": true}, "", ms(start))
	return &resp, nil
}

func (t *Tool) deleteTask(ctx context.Context, req ktp.ToolRequest, start time.Time) (*ktp.ToolResponse, error) {
	id, err := strParam(req.Parameters, "id")
	if err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	// Verify ownership before deleting.
	if _, err := t.findOwnedTask(ctx, req.AgentID, id); err != nil {
		return errResp(req.ID, err.Error()), nil
	}

	if err := t.sched.Remove(ctx, id); err != nil {
		return errResp(req.ID, fmt.Sprintf("failed to delete schedule: %s", err)), nil
	}

	resp := ktp.NewToolResponse(req.ID, true, map[string]any{"deleted": true}, "", ms(start))
	return &resp, nil
}

// findOwnedTask looks up a schedule by ID and verifies that it belongs to the
// requesting agent and is a task (not a heartbeat).
func (t *Tool) findOwnedTask(ctx context.Context, agentID, scheduleID string) (*types.Schedule, error) {
	schedules, err := t.sched.List(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("failed to list schedules: %s", err)
	}
	for i := range schedules {
		if schedules[i].ID == scheduleID {
			if schedules[i].AgentID != agentID {
				return nil, fmt.Errorf("schedule does not belong to this agent")
			}
			if schedules[i].Type != types.ScheduleTypeTask {
				return nil, fmt.Errorf("cannot modify heartbeat schedules")
			}
			return &schedules[i], nil
		}
	}
	return nil, fmt.Errorf("schedule %s not found", scheduleID)
}

// --- parameter helpers ---

func strParam(params map[string]any, key string) (string, error) {
	raw, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing required parameter: %s", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("parameter %s must be a string", key)
	}
	return s, nil
}

func strDefault(params map[string]any, key, def string) string {
	raw, ok := params[key]
	if !ok {
		return def
	}
	s, ok := raw.(string)
	if !ok {
		return def
	}
	return s
}

func boolDefault(params map[string]any, key string, def bool) bool {
	raw, ok := params[key]
	if !ok {
		return def
	}
	b, ok := raw.(bool)
	if !ok {
		return def
	}
	return b
}

func errResp(reqID, msg string) *ktp.ToolResponse {
	resp := ktp.NewToolResponse(reqID, false, nil, msg, 0)
	return &resp
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
