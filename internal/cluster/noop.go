package cluster

import (
	"context"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

// noopManager is the Manager implementation for single-node mode.
// It always reports as leader, treats all agents as local, and
// has zero overhead.
type noopManager struct {
	nodeID string
}

// NewNoopManager creates a Manager for single-node mode.
func NewNoopManager() Manager {
	return &noopManager{nodeID: "single-node"}
}

func (m *noopManager) Start(context.Context) error { return nil }
func (m *noopManager) Stop() error                 { return nil }
func (m *noopManager) NodeID() string               { return m.nodeID }
func (m *noopManager) IsLeader() bool               { return true }

func (m *noopManager) RequestAssignment(agentID string) (string, error) {
	return m.nodeID, nil
}

func (m *noopManager) GetAssignment(agentID string) (string, error) {
	return m.nodeID, nil
}

func (m *noopManager) IsLocalAgent(agentID string) bool { return true }

func (m *noopManager) ListNodes() ([]types.NodeInfo, error) {
	return []types.NodeInfo{{
		NodeID:   m.nodeID,
		NodeName: "local",
		Status:   types.NodeStatusActive,
		IsLeader: true,
	}}, nil
}

func (m *noopManager) DrainNode(nodeID string) error { return nil }

func (m *noopManager) OnAgentAssigned(func(string, string)) {}
func (m *noopManager) OnLeaderChange(func(bool))            {}
func (m *noopManager) OnNodeDead(func(string))              {}
