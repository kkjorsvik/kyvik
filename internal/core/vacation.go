package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

const vacationStateKey = "vacation_mode"

// ActivateVacationMode gracefully pauses all running agents and blocks new starts.
// Previously-running agent IDs are persisted so they can be resumed on deactivation.
func (p *Kyvik) ActivateVacationMode(ctx context.Context, activatedBy, message string) error {
	// Snapshot running agent IDs under read lock.
	p.mu.RLock()
	runningIDs := make([]string, 0, len(p.agents))
	for id := range p.agents {
		runningIDs = append(runningIDs, id)
	}
	p.mu.RUnlock()

	// Build vacation state.
	state := types.VacationState{
		Active:         true,
		ActivatedBy:    activatedBy,
		ActivatedAt:    time.Now(),
		Message:        message,
		PreviousAgents: runningIDs,
	}

	// Persist to store first (crash recovery).
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal vacation state: %w", err)
	}
	if err := p.store.SetSystemState(ctx, vacationStateKey, string(stateJSON)); err != nil {
		return fmt.Errorf("persist vacation state: %w", err)
	}

	// Set in-memory flag.
	p.vacationMode.Store(true)

	// Gracefully stop each running agent (preserves queued messages).
	var firstErr error
	for _, id := range runningIDs {
		if err := p.StopAgent(ctx, id); err != nil && firstErr == nil {
			slog.Warn("vacation mode: failed to stop agent", "agent_id", id, "error", err)
			firstErr = err
		}
	}

	// Audit log.
	_ = p.audit.Log(ctx, types.AuditEntry{
		EventType: types.EventAgentLifecycle,
		Action:    "vacation_mode_activated",
		Details:   fmt.Sprintf("vacation mode activated by %s, stopped %d agents: %s", activatedBy, len(runningIDs), message),
		Timestamp: time.Now(),
	})

	// Notify.
	if p.Communication.Notifier != nil {
		detail := fmt.Sprintf("Vacation mode activated by %s. %d agents paused.", activatedBy, len(runningIDs))
		if message != "" {
			detail += " Message: " + message
		}
		_ = p.Communication.Notifier.Send(ctx, notifications.Event{
			Type:      "vacation_mode",
			Severity:  "info",
			Title:     "Vacation mode activated",
			Detail:    detail,
			Timestamp: time.Now(),
		})
	}

	return firstErr
}

// DeactivateVacationMode resumes previously-running agents and allows new starts.
func (p *Kyvik) DeactivateVacationMode(ctx context.Context) error {
	// Load persisted state to get PreviousAgents list.
	state, err := p.GetVacationState(ctx)
	if err != nil {
		return fmt.Errorf("load vacation state: %w", err)
	}

	// Clear in-memory flag BEFORE calling ResumeAgent so resume calls won't hit the guard.
	p.vacationMode.Store(false)

	// Clear persisted state.
	if err := p.store.SetSystemState(ctx, vacationStateKey, ""); err != nil {
		slog.Warn("vacation mode: failed to clear persisted state", "error", err)
	}

	// Resume each agent from PreviousAgents.
	var firstErr error
	resumed := 0
	for _, id := range state.PreviousAgents {
		if err := p.ResumeAgent(ctx, id); err != nil {
			slog.Warn("vacation mode: failed to resume agent", "agent_id", id, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			resumed++
		}
	}

	// Audit log.
	_ = p.audit.Log(ctx, types.AuditEntry{
		EventType: types.EventAgentLifecycle,
		Action:    "vacation_mode_deactivated",
		Details:   fmt.Sprintf("vacation mode deactivated, resumed %d/%d agents", resumed, len(state.PreviousAgents)),
		Timestamp: time.Now(),
	})

	// Notify.
	if p.Communication.Notifier != nil {
		_ = p.Communication.Notifier.Send(ctx, notifications.Event{
			Type:      "vacation_mode",
			Severity:  "info",
			Title:     "Vacation mode deactivated",
			Detail:    fmt.Sprintf("Vacation mode deactivated. %d agents resumed.", resumed),
			Timestamp: time.Now(),
		})
	}

	return firstErr
}

// VacationModeActive returns whether vacation mode is currently active.
func (p *Kyvik) VacationModeActive() bool {
	return p.vacationMode.Load()
}

// GetVacationState loads the full vacation state from the store.
func (p *Kyvik) GetVacationState(ctx context.Context) (*types.VacationState, error) {
	raw, err := p.store.GetSystemState(ctx, vacationStateKey)
	if err != nil {
		return nil, fmt.Errorf("get vacation state: %w", err)
	}
	if raw == "" {
		return &types.VacationState{}, nil
	}

	var state types.VacationState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, fmt.Errorf("unmarshal vacation state: %w", err)
	}
	return &state, nil
}

// LoadVacationState restores the in-memory vacation mode flag from the DB.
// Called at startup to recover state after a restart.
func (p *Kyvik) LoadVacationState(ctx context.Context) error {
	state, err := p.GetVacationState(ctx)
	if err != nil {
		return err
	}
	p.vacationMode.Store(state.Active)
	if state.Active {
		slog.Info("vacation mode restored from persisted state",
			"activated_by", state.ActivatedBy,
			"agents_paused", len(state.PreviousAgents))
	}
	return nil
}

// Store returns the store for use by subsystems (alerts, etc.).
func (p *Kyvik) Store() interface{} { return p.store }
