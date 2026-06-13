package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

type mockHeartbeatStore struct {
	heartbeats map[string]int
	deadNodes  []types.NodeInfo
}

func newMockHeartbeatStore() *mockHeartbeatStore {
	return &mockHeartbeatStore{heartbeats: make(map[string]int)}
}

func (m *mockHeartbeatStore) UpdateHeartbeat(ctx context.Context, nodeID string, cap types.NodeCapacity) error {
	m.heartbeats[nodeID]++
	return nil
}

func (m *mockHeartbeatStore) GetDeadNodes(ctx context.Context, timeout time.Duration) ([]types.NodeInfo, error) {
	return m.deadNodes, nil
}

func (m *mockHeartbeatStore) SetNodeStatus(ctx context.Context, nodeID, status string) error {
	return nil
}

func TestHeartbeat_Tick(t *testing.T) {
	store := newMockHeartbeatStore()
	hb := newHeartbeat("node-1", store, 50*time.Millisecond, 150*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hb.start(ctx)
	time.Sleep(180 * time.Millisecond)
	hb.stop()

	count := store.heartbeats["node-1"]
	if count < 2 {
		t.Errorf("expected at least 2 heartbeats, got %d", count)
	}
}
