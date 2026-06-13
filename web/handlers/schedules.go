package handlers

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"

	"github.com/kkjorsvik/kyvik/internal/scheduler"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// ScheduleList renders the schedule list fragment for an agent.
func (h *Handlers) ScheduleList(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	sched := h.kyvik.Lifecycle.Scheduler
	if sched == nil {
		h.renderFragment(w, r, "schedule-list", map[string]any{
			"AgentID":      id,
			"Schedules":    nil,
			"HasScheduler": false,
		})
		return
	}

	schedules, err := sched.ListByType(ctx, id, types.ScheduleTypeTask)
	if err != nil {
		http.Error(w, "failed to list schedules", http.StatusInternalServerError)
		return
	}

	h.renderFragment(w, r, "schedule-list", map[string]any{
		"AgentID":      id,
		"Schedules":    schedules,
		"HasScheduler": true,
	})
}

// ScheduleCreate creates a new task schedule for an agent.
func (h *Handlers) ScheduleCreate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	sched := h.kyvik.Lifecycle.Scheduler
	if sched == nil {
		http.Error(w, "scheduler not enabled", http.StatusBadRequest)
		return
	}

	r.ParseForm()
	name := r.FormValue("schedule_name")
	cronExpr := r.FormValue("cron_expr")
	message := r.FormValue("message")

	if name == "" || cronExpr == "" || message == "" {
		http.Error(w, "name, cron_expr, and message are required", http.StatusBadRequest)
		return
	}

	now := timeutil.NowUTC()
	schedule := types.Schedule{
		ID:        uuid.New().String(),
		AgentID:   id,
		Name:      name,
		CronExpr:  cronExpr,
		Message:   message,
		Channel:   "internal",
		Type:      types.ScheduleTypeTask,
		Enabled:   true,
		Timezone:  h.configuredTimezone(),
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := sched.Add(ctx, schedule); err != nil {
		http.Error(w, fmt.Sprintf("failed to create schedule: %v", err), http.StatusBadRequest)
		return
	}

	// Re-render the list.
	h.ScheduleList(w, r)
}

// ScheduleUpdate updates an existing task schedule.
func (h *Handlers) ScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	schedID := r.PathValue("schedID")
	ctx := r.Context()

	sched := h.kyvik.Lifecycle.Scheduler
	if sched == nil {
		http.Error(w, "scheduler not enabled", http.StatusBadRequest)
		return
	}

	// List all schedules for the agent and find the one to update.
	schedules, err := sched.List(ctx, id)
	if err != nil {
		http.Error(w, "failed to list schedules", http.StatusInternalServerError)
		return
	}
	var existing *types.Schedule
	for i := range schedules {
		if schedules[i].ID == schedID {
			existing = &schedules[i]
			break
		}
	}
	if existing == nil {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return
	}

	r.ParseForm()
	if v := r.FormValue("schedule_name"); v != "" {
		existing.Name = v
	}
	if v := r.FormValue("cron_expr"); v != "" {
		existing.CronExpr = v
	}
	if v := r.FormValue("message"); v != "" {
		existing.Message = v
	}

	// Handle enabled checkbox — unchecked means field absent from form.
	if r.Form.Has("enabled") {
		existing.Enabled = true
	} else {
		existing.Enabled = false
	}

	existing.UpdatedAt = timeutil.NowUTC()

	if err := sched.Update(ctx, *existing); err != nil {
		http.Error(w, fmt.Sprintf("failed to update schedule: %v", err), http.StatusBadRequest)
		return
	}

	_ = id // keep for route pattern
	h.ScheduleList(w, r)
}

// ScheduleDelete removes a task schedule.
func (h *Handlers) ScheduleDelete(w http.ResponseWriter, r *http.Request) {
	_ = r.PathValue("id")
	schedID := r.PathValue("schedID")
	ctx := r.Context()

	sched := h.kyvik.Lifecycle.Scheduler
	if sched == nil {
		http.Error(w, "scheduler not enabled", http.StatusBadRequest)
		return
	}

	if err := sched.Remove(ctx, schedID); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete schedule: %v", err), http.StatusInternalServerError)
		return
	}

	h.ScheduleList(w, r)
}

// ScheduleToggle enables/disables a task schedule.
func (h *Handlers) ScheduleToggle(w http.ResponseWriter, r *http.Request) {
	_ = r.PathValue("id")
	schedID := r.PathValue("schedID")
	ctx := r.Context()

	sched := h.kyvik.Lifecycle.Scheduler
	if sched == nil {
		http.Error(w, "scheduler not enabled", http.StatusBadRequest)
		return
	}

	if err := sched.Toggle(ctx, schedID); err != nil {
		http.Error(w, fmt.Sprintf("failed to toggle schedule: %v", err), http.StatusInternalServerError)
		return
	}

	h.ScheduleList(w, r)
}

// ScheduleCronPreview renders a cron preview snippet for inline validation.
func (h *Handlers) ScheduleCronPreview(w http.ResponseWriter, r *http.Request) {
	expr := r.FormValue("cron_expr")
	if expr == "" {
		h.renderFragment(w, r, "cron-preview", map[string]any{"Error": true})
		return
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	if _, err := parser.Parse(expr); err != nil {
		h.renderFragment(w, r, "cron-preview", map[string]any{"Error": true})
		return
	}
	h.renderFragment(w, r, "cron-preview", map[string]any{
		"Preview": scheduler.CronPreview(expr),
	})
}

// ScheduleEditModal renders the edit dialog for a single schedule.
func (h *Handlers) ScheduleEditModal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	schedID := r.PathValue("schedID")
	ctx := r.Context()

	sched := h.kyvik.Lifecycle.Scheduler
	if sched == nil {
		http.Error(w, "scheduler not enabled", http.StatusBadRequest)
		return
	}

	schedules, err := sched.List(ctx, id)
	if err != nil {
		http.Error(w, "failed to list schedules", http.StatusInternalServerError)
		return
	}

	var target *types.Schedule
	for i := range schedules {
		if schedules[i].ID == schedID {
			target = &schedules[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "schedule not found", http.StatusNotFound)
		return
	}

	h.renderFragment(w, r, "schedule-edit-modal", map[string]any{
		"AgentID":  id,
		"Schedule": target,
	})
}
