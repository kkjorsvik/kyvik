package cluster

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

type drainStore interface {
	SetNodeStatus(ctx context.Context, nodeID, status string) error
	GetNodeAgents(ctx context.Context, nodeID string) ([]types.Assignment, error)
}

func drainNode(ctx context.Context, store drainStore, ac *assignmentController, nodeID string) error {
	if err := store.SetNodeStatus(ctx, nodeID, types.NodeStatusDraining); err != nil {
		return fmt.Errorf("set draining status: %w", err)
	}
	agents, err := store.GetNodeAgents(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("get node agents: %w", err)
	}
	for _, a := range agents {
		slog.Info("draining agent from node", "agent_id", a.AgentID, "node_id", nodeID)
		if _, err := ac.assignAgent(ctx, a.AgentID, a.NodeAffinity, a.NodePreference); err != nil {
			slog.Error("failed to reassign during drain", "agent_id", a.AgentID, "error", err)
		}
	}
	if err := store.SetNodeStatus(ctx, nodeID, types.NodeStatusDrained); err != nil {
		return fmt.Errorf("set drained status: %w", err)
	}
	slog.Info("node drained", "node_id", nodeID, "agents_migrated", len(agents))
	return nil
}
