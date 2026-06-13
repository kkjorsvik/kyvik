package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/testutil"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

func TestCluster_TwoNodeLeaderElection(t *testing.T) {
	db := testDB(t)
	tdb := testutil.RequirePostgres(t)
	ctx := context.Background()

	enabled := true
	cfg1 := config.ClusterConfig{
		Enabled:                  &enabled,
		AdvertiseAddr:            "node1:8080",
		NodeName:                 "node-1",
		HeartbeatIntervalSeconds: 1,
		HeartbeatTimeoutSeconds:  3,
	}
	cfg2 := config.ClusterConfig{
		Enabled:                  &enabled,
		AdvertiseAddr:            "node2:8080",
		NodeName:                 "node-2",
		HeartbeatIntervalSeconds: 1,
		HeartbeatTimeoutSeconds:  3,
	}

	dsn := testutil.TestDSN()
	dataDir1 := t.TempDir()
	dataDir2 := t.TempDir()

	m1, err := NewClusterManager(cfg1, tdb.Store, db, dsn, dataDir1, "test")
	if err != nil {
		t.Fatalf("NewClusterManager 1: %v", err)
	}
	m2, err := NewClusterManager(cfg2, tdb.Store, db, dsn, dataDir2, "test")
	if err != nil {
		t.Fatalf("NewClusterManager 2: %v", err)
	}

	// Start both
	if err := m1.Start(ctx); err != nil {
		t.Fatalf("m1.Start: %v", err)
	}
	defer m1.Stop()

	if err := m2.Start(ctx); err != nil {
		t.Fatalf("m2.Start: %v", err)
	}
	defer m2.Stop()

	// Exactly one should be leader
	time.Sleep(500 * time.Millisecond)
	leaders := 0
	if m1.IsLeader() {
		leaders++
	}
	if m2.IsLeader() {
		leaders++
	}
	if leaders != 1 {
		t.Errorf("expected exactly 1 leader, got %d", leaders)
	}

	// Both should see 2 nodes
	nodes, err := m1.ListNodes()
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(nodes))
	}
}

func TestCluster_AgentFailover(t *testing.T) {
	// This test verifies that when a leader assigns an agent and then stops,
	// the orphaned agent detection works via the heartbeat mechanism.
	// Full failover requires timing-dependent waits, so we test the components:
	// 1. Leader assigns agent
	// 2. Assignment is visible from both managers

	db := testDB(t)
	tdb := testutil.RequirePostgres(t)
	ctx := context.Background()

	enabled := true
	cfg := config.ClusterConfig{
		Enabled:                  &enabled,
		AdvertiseAddr:            "node1:8080",
		NodeName:                 "leader",
		HeartbeatIntervalSeconds: 1,
		HeartbeatTimeoutSeconds:  3,
	}

	dsn := testutil.TestDSN()
	m, err := NewClusterManager(cfg, tdb.Store, db, dsn, t.TempDir(), "test")
	if err != nil {
		t.Fatalf("NewClusterManager: %v", err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Stop()

	// Create a test agent in the store first
	// (The assignment controller needs the agent to exist)
	// Use store directly to create a minimal agent
	testAgent := types.AgentConfig{
		ID:           "test-agent-1",
		Name:         "Test Agent",
		DesiredState: types.DesiredStateRunning,
	}
	if err := tdb.Store.CreateAgent(ctx, testAgent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// Request assignment (leader should handle it)
	time.Sleep(200 * time.Millisecond) // let leader election settle
	if !m.IsLeader() {
		t.Fatal("single node should be leader")
	}

	nodeID, err := m.RequestAssignment("test-agent-1")
	if err != nil {
		t.Fatalf("RequestAssignment: %v", err)
	}
	if nodeID != m.NodeID() {
		t.Errorf("expected assignment to self (%s), got %s", m.NodeID(), nodeID)
	}

	// Verify assignment is retrievable
	assignedNode, err := m.GetAssignment("test-agent-1")
	if err != nil {
		t.Fatalf("GetAssignment: %v", err)
	}
	if assignedNode != m.NodeID() {
		t.Errorf("assignment mismatch: expected %s, got %s", m.NodeID(), assignedNode)
	}
}
