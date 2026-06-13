package cluster

import (
	"testing"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestPlacement_BasicLoadBalancing(t *testing.T) {
	nodes := []types.NodeInfo{
		{NodeID: "n1", Status: types.NodeStatusActive, Capacity: types.NodeCapacity{AgentCount: 3, MaxAgents: 10}},
		{NodeID: "n2", Status: types.NodeStatusActive, Capacity: types.NodeCapacity{AgentCount: 1, MaxAgents: 10}},
		{NodeID: "n3", Status: types.NodeStatusActive, Capacity: types.NodeCapacity{AgentCount: 5, MaxAgents: 10}},
	}
	result := pickNode(nodes, nil, nil)
	if result != "n2" {
		t.Errorf("expected n2 (least loaded), got %s", result)
	}
}

func TestPlacement_AffinityFilter(t *testing.T) {
	nodes := []types.NodeInfo{
		{NodeID: "n1", Status: types.NodeStatusActive, Labels: map[string]string{"gpu": "true"}, Capacity: types.NodeCapacity{AgentCount: 5, MaxAgents: 10}},
		{NodeID: "n2", Status: types.NodeStatusActive, Labels: map[string]string{}, Capacity: types.NodeCapacity{AgentCount: 1, MaxAgents: 10}},
	}
	affinity := map[string]string{"gpu": "true"}
	result := pickNode(nodes, affinity, nil)
	if result != "n1" {
		t.Errorf("expected n1 (has gpu), got %s", result)
	}
}

func TestPlacement_NoHealthyNodes(t *testing.T) {
	nodes := []types.NodeInfo{
		{NodeID: "n1", Status: types.NodeStatusDisconnected},
		{NodeID: "n2", Status: types.NodeStatusDraining},
	}
	result := pickNode(nodes, nil, nil)
	if result != "" {
		t.Errorf("expected empty (no healthy nodes), got %s", result)
	}
}

func TestPlacement_PreferenceScoring(t *testing.T) {
	nodes := []types.NodeInfo{
		{NodeID: "n1", Status: types.NodeStatusActive, Labels: map[string]string{"zone": "us-east"}, Capacity: types.NodeCapacity{AgentCount: 5, MaxAgents: 10}},
		{NodeID: "n2", Status: types.NodeStatusActive, Labels: map[string]string{"zone": "eu-west"}, Capacity: types.NodeCapacity{AgentCount: 4, MaxAgents: 10}},
	}
	pref := map[string]string{"zone": "us-east"}
	result := pickNode(nodes, nil, pref)
	if result != "n1" {
		t.Errorf("expected n1 (preferred zone), got %s", result)
	}
}
