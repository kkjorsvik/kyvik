package core

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/kkjorsvik/kyvik/internal/notifications"
	"github.com/kkjorsvik/kyvik/internal/timeutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// KillAgent immediately terminates an agent without waiting for graceful completion.
// Unlike StopAgent, this cancels the agent context without waiting for the goroutine
// to finish, which propagates cancellation to sandbox child processes.
func (p *Kyvik) KillAgent(ctx context.Context, agentID string) error {
	log := slog.With("agent_id", agentID)

	p.mu.Lock()
	runner, ok := p.agents[agentID]
	if !ok {
		p.mu.Unlock()
		// Agent not running — check store and set killed state if it exists.
		if _, err := p.store.GetAgent(ctx, agentID); err != nil {
			return fmt.Errorf("agent not found: %s", agentID)
		}
		_ = p.store.SetDesiredState(ctx, agentID, types.DesiredStateKilled)
		_ = p.store.SetActualState(ctx, agentID, types.AgentStatusKilled, "")
		log.Info("agent killed (was not running)")
		_ = p.audit.Log(ctx, types.AuditEntry{
			AgentID:   agentID,
			EventType: types.EventAgentLifecycle,
			Action:    "agent_killed",
			Details:   fmt.Sprintf("agent %s killed (was not running)", agentID),
			Timestamp: timeutil.NowUTC(),
		})
		return nil
	}
	// Remove from map immediately to prevent double-kill races.
	delete(p.agents, agentID)
	p.mu.Unlock()

	// Cancel outbox consumer — don't wait for it.
	if runner.outboxCancel != nil {
		runner.outboxCancel()
	}

	// Cancel agent context — kills sandbox child processes immediately.
	runner.cancel()

	// Do NOT wait for runner.done — this is the key difference from stop.

	// Clean up circuit breaker state.
	if p.Lifecycle.Breaker != nil {
		p.Lifecycle.Breaker.Remove(agentID)
	}

	// Deprovision channel adapters.
	p.deprovisionChannels(ctx, agentID, log)

	// Persist killed state.
	_ = p.store.SetDesiredState(ctx, agentID, types.DesiredStateKilled)
	_ = p.store.SetActualState(ctx, agentID, types.AgentStatusKilled, "")

	// Reset processing queue messages so they go back to pending.
	if p.Storage.Queue != nil {
		if count, err := p.Storage.Queue.ResetAgentProcessing(ctx, agentID); err != nil {
			log.Warn("failed to reset processing queue messages", "error", err)
		} else if count > 0 {
			log.Info("reset processing queue messages", "count", count)
		}
	}

	log.Info("agent killed")

	_ = p.audit.Log(ctx, types.AuditEntry{
		AgentID:   agentID,
		EventType: types.EventAgentLifecycle,
		Action:    "agent_killed",
		Details:   fmt.Sprintf("agent %s killed", agentID),
		Timestamp: timeutil.NowUTC(),
	})

	if p.Communication.Notifier != nil {
		_ = p.Communication.Notifier.Send(ctx, notifications.Event{
			Type:      "agent_killed",
			Severity:  "critical",
			Agent:     agentID,
			Title:     "Agent killed",
			Detail:    fmt.Sprintf("Agent %s was immediately terminated", agentID),
			Timestamp: timeutil.NowUTC(),
		})
	}

	return nil
}

// KillAll kills every running agent and sets the emergency stop flag.
// While emergency stop is active, new agent starts are blocked.
func (p *Kyvik) KillAll(ctx context.Context) error {
	p.emergencyStop.Store(true)

	// Snapshot all running agent IDs under read lock.
	p.mu.RLock()
	ids := make([]string, 0, len(p.agents))
	for id := range p.agents {
		ids = append(ids, id)
	}
	p.mu.RUnlock()

	var firstErr error
	for _, id := range ids {
		if err := p.KillAgent(ctx, id); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	_ = p.audit.Log(ctx, types.AuditEntry{
		EventType: types.EventAgentLifecycle,
		Action:    "kill_all",
		Details:   fmt.Sprintf("killed %d agents, emergency stop activated", len(ids)),
		Timestamp: timeutil.NowUTC(),
	})

	if p.Communication.Notifier != nil {
		_ = p.Communication.Notifier.Send(ctx, notifications.Event{
			Type:      "kill_all",
			Severity:  "critical",
			Title:     "Emergency stop activated",
			Detail:    fmt.Sprintf("All %d running agents killed", len(ids)),
			Timestamp: timeutil.NowUTC(),
		})
	}

	return firstErr
}

// ClearEmergencyStop allows new agents to be started again after a KillAll.
func (p *Kyvik) ClearEmergencyStop(ctx context.Context) error {
	p.emergencyStop.Store(false)

	_ = p.audit.Log(ctx, types.AuditEntry{
		EventType: types.EventAgentLifecycle,
		Action:    "emergency_stop_cleared",
		Details:   "emergency stop cleared, new agent starts allowed",
		Timestamp: timeutil.NowUTC(),
	})

	return nil
}

// EmergencyStopActive returns whether the emergency stop flag is set.
func (p *Kyvik) EmergencyStopActive() bool {
	return p.emergencyStop.Load()
}
