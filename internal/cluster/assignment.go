package cluster

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

type assignmentStore interface {
	ListNodes(ctx context.Context) ([]types.NodeInfo, error)
	AssignAgent(ctx context.Context, agentID, nodeID string) error
	GetAssignment(ctx context.Context, agentID string) (*types.Assignment, error)
	GetOrphanedAgents(ctx context.Context, nodeID string) ([]types.Assignment, error)
	DeleteAssignment(ctx context.Context, agentID string) error
}

type assignmentController struct {
	store    assignmentStore
	notifier *notifier
	onAssign func(agentID, nodeID string)
}

func newAssignmentController(store assignmentStore, n *notifier) *assignmentController {
	return &assignmentController{store: store, notifier: n}
}

func (ac *assignmentController) assignAgent(ctx context.Context, agentID string, affinity, preference map[string]string) (string, error) {
	nodes, err := ac.store.ListNodes(ctx)
	if err != nil {
		return "", fmt.Errorf("list nodes: %w", err)
	}
	nodeID := pickNode(nodes, affinity, preference)
	if nodeID == "" {
		return "", fmt.Errorf("no eligible nodes for agent %s", agentID)
	}
	if err := ac.store.AssignAgent(ctx, agentID, nodeID); err != nil {
		return "", fmt.Errorf("assign agent: %w", err)
	}
	slog.Info("agent assigned", "agent_id", agentID, "node_id", nodeID)
	if ac.notifier != nil {
		ev := ClusterEvent{Type: EventAgentAssigned, AgentID: agentID, NodeID: nodeID}
		if err := ac.notifier.publish(ctx, NodeQueueChannel(nodeID), ev); err != nil {
			slog.Error("notify assignment failed", "error", err)
		}
	}
	if ac.onAssign != nil {
		ac.onAssign(agentID, nodeID)
	}
	return nodeID, nil
}

func (ac *assignmentController) reassignOrphans(ctx context.Context, deadNodeID string) error {
	orphans, err := ac.store.GetOrphanedAgents(ctx, deadNodeID)
	if err != nil {
		return fmt.Errorf("get orphaned agents: %w", err)
	}
	for _, a := range orphans {
		slog.Info("reassigning orphaned agent", "agent_id", a.AgentID, "from_node", deadNodeID)
		if _, err := ac.assignAgent(ctx, a.AgentID, a.NodeAffinity, a.NodePreference); err != nil {
			slog.Error("failed to reassign agent", "agent_id", a.AgentID, "error", err)
		}
	}
	return nil
}

func pickNode(nodes []types.NodeInfo, affinity, preference map[string]string) string {
	var healthy []types.NodeInfo
	for _, n := range nodes {
		if n.Status == types.NodeStatusActive {
			healthy = append(healthy, n)
		}
	}
	var eligible []types.NodeInfo
	if len(affinity) > 0 {
		for _, n := range healthy {
			if matchesLabels(n.Labels, affinity) {
				eligible = append(eligible, n)
			}
		}
	} else {
		eligible = healthy
	}
	if len(eligible) == 0 {
		return ""
	}
	type scored struct {
		nodeID string
		score  float64
	}
	var candidates []scored
	for _, n := range eligible {
		s := capacityScore(n)
		if len(preference) > 0 && matchesLabels(n.Labels, preference) {
			s += 100
		}
		candidates = append(candidates, scored{n.NodeID, s})
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.score > best.score {
			best = c
		}
	}
	return best.nodeID
}

func matchesLabels(nodeLabels, required map[string]string) bool {
	for k, v := range required {
		if nodeLabels[k] != v {
			return false
		}
	}
	return true
}

func capacityScore(n types.NodeInfo) float64 {
	if n.Capacity.MaxAgents > 0 {
		return float64(n.Capacity.MaxAgents - n.Capacity.AgentCount)
	}
	return 1000 - float64(n.Capacity.AgentCount)
}
