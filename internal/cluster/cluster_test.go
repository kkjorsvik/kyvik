package cluster

import (
	"context"
	"testing"
)

func TestNoopManager(t *testing.T) {
	m := NewNoopManager()

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	if m.NodeID() == "" {
		t.Error("NodeID should not be empty")
	}
	if !m.IsLeader() {
		t.Error("noop manager should always be leader")
	}
	if !m.IsLocalAgent("any-agent") {
		t.Error("noop manager: all agents should be local")
	}

	nodeID, err := m.RequestAssignment("agent-1")
	if err != nil {
		t.Fatalf("RequestAssignment: %v", err)
	}
	if nodeID != m.NodeID() {
		t.Errorf("expected %s, got %s", m.NodeID(), nodeID)
	}

	nodes, err := m.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(nodes))
	}
}
