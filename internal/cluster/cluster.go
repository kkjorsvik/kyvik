package cluster

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/kkjorsvik/kyvik/internal/config"
	"github.com/kkjorsvik/kyvik/internal/store"
	"github.com/kkjorsvik/kyvik/pkg/types"
)

// Manager coordinates cluster membership and agent placement.
// When cluster.enabled is false, a no-op implementation is used.
type Manager interface {
	Start(ctx context.Context) error
	Stop() error

	// Identity
	NodeID() string
	IsLeader() bool

	// Agent placement
	RequestAssignment(agentID string) (nodeID string, err error)
	GetAssignment(agentID string) (nodeID string, err error)
	IsLocalAgent(agentID string) bool

	// Node management
	ListNodes() ([]types.NodeInfo, error)
	DrainNode(nodeID string) error

	// Events (for router and core to react to cluster changes)
	OnAgentAssigned(fn func(agentID, nodeID string))
	OnLeaderChange(fn func(isLeader bool))
	OnNodeDead(fn func(nodeID string))
}

// clusterManager is the real Manager implementation for multi-node mode.
type clusterManager struct {
	config  config.ClusterConfig
	store   store.Store
	dsn     string
	db      *sql.DB
	nodeID  string
	dataDir string
	version string

	leader     *leaderElector
	heartbeat  *heartbeat
	notifier   *notifier
	assignment *assignmentController

	onAssigned []func(string, string)
	onLeader   []func(bool)
	onDead     []func(string)
	mu         sync.RWMutex
}

// NewClusterManager creates a Manager for multi-node mode.
func NewClusterManager(cfg config.ClusterConfig, st store.Store, db *sql.DB, dsn, dataDir, version string) (Manager, error) {
	if cfg.AdvertiseAddr == "" {
		return nil, fmt.Errorf("cluster.advertise_addr is required when clustering is enabled")
	}

	nodeIDPath := filepath.Join(dataDir, "node.id")
	nodeID, err := loadOrCreateNodeID(nodeIDPath)
	if err != nil {
		return nil, fmt.Errorf("load node ID: %w", err)
	}

	m := &clusterManager{
		config:  cfg,
		store:   st,
		dsn:     dsn,
		db:      db,
		nodeID:  nodeID,
		dataDir: dataDir,
		version: version,
	}

	m.leader = newLeaderElector(db)
	m.heartbeat = newHeartbeat(nodeID, st, cfg.HeartbeatInterval(), cfg.HeartbeatTimeout())
	m.notifier = newNotifier(dsn, db)
	m.assignment = newAssignmentController(st, m.notifier)

	m.heartbeat.onDead = func(deadNodeID string) {
		if m.leader.isLeader() {
			m.assignment.reassignOrphans(context.Background(), deadNodeID)
		}
		m.mu.RLock()
		for _, fn := range m.onDead {
			fn(deadNodeID)
		}
		m.mu.RUnlock()
	}

	m.heartbeat.onTick = func() {
		if !m.leader.isLeader() {
			acquired, err := m.leader.tryAcquire(context.Background())
			if err != nil {
				slog.Debug("leader re-election attempt failed", "error", err)
				return
			}
			if acquired {
				slog.Info("this node promoted to leader", "node_id", m.nodeID)
				m.store.SetNodeStatus(context.Background(), m.nodeID, types.NodeStatusActive)
				m.notifier.publish(context.Background(), ChannelCluster, ClusterEvent{
					Type:   EventLeaderChanged,
					NodeID: m.nodeID,
				})
				m.mu.RLock()
				for _, fn := range m.onLeader {
					fn(true)
				}
				m.mu.RUnlock()
			}
		}
	}

	m.assignment.onAssign = func(agentID, nodeID string) {
		m.mu.RLock()
		for _, fn := range m.onAssigned {
			fn(agentID, nodeID)
		}
		m.mu.RUnlock()
	}

	return m, nil
}

func (m *clusterManager) Start(ctx context.Context) error {
	nodeName := m.config.NodeName
	if nodeName == "" {
		nodeName = m.nodeID[:8]
	}
	node := types.NodeInfo{
		NodeID:   m.nodeID,
		NodeName: nodeName,
		Address:  m.config.AdvertiseAddr,
		Status:   types.NodeStatusJoining,
		Labels:   m.config.Labels,
		Version:  m.version,
	}
	if err := m.store.RegisterNode(ctx, node); err != nil {
		return fmt.Errorf("register node: %w", err)
	}

	if err := m.notifier.start(ctx); err != nil {
		return fmt.Errorf("start notifier: %w", err)
	}

	m.notifier.subscribe(ChannelCluster, m.handleClusterEvent)
	m.notifier.subscribe(ChannelConfig, m.handleConfigEvent)
	m.notifier.subscribe(NodeQueueChannel(m.nodeID), m.handleQueueEvent)

	acquired, err := m.leader.tryAcquire(ctx)
	if err != nil {
		slog.Warn("leader election failed", "error", err)
	}
	if acquired {
		slog.Info("this node is the cluster leader", "node_id", m.nodeID)
	} else {
		slog.Info("this node is a follower", "node_id", m.nodeID)
	}
	m.store.SetNodeStatus(ctx, m.nodeID, types.NodeStatusActive)

	m.heartbeat.start(ctx)

	m.notifier.publish(ctx, ChannelCluster, ClusterEvent{
		Type:   EventNodeJoined,
		NodeID: m.nodeID,
	})

	return nil
}

func (m *clusterManager) Stop() error {
	ctx := context.Background()
	m.heartbeat.stop()
	m.leader.release()
	m.notifier.stop()
	m.store.SetNodeStatus(ctx, m.nodeID, types.NodeStatusDisconnected)
	return nil
}

func (m *clusterManager) NodeID() string { return m.nodeID }
func (m *clusterManager) IsLeader() bool { return m.leader.isLeader() }

func (m *clusterManager) RequestAssignment(agentID string) (string, error) {
	ctx := context.Background()
	agent, err := m.store.GetAgent(ctx, agentID)
	var affinity, preference map[string]string
	if err == nil && agent != nil {
		affinity = agent.NodeAffinity
		preference = agent.NodePreference
	}

	if !m.leader.isLeader() {
		m.notifier.publish(ctx, ChannelCluster, ClusterEvent{
			Type:    EventAgentNeedsAssignment,
			AgentID: agentID,
		})
		return "", fmt.Errorf("not leader — assignment request forwarded")
	}
	return m.assignment.assignAgent(ctx, agentID, affinity, preference)
}

func (m *clusterManager) GetAssignment(agentID string) (string, error) {
	a, err := m.store.GetAssignment(context.Background(), agentID)
	if err != nil {
		return "", err
	}
	if a == nil {
		return "", nil
	}
	return a.NodeID, nil
}

func (m *clusterManager) IsLocalAgent(agentID string) bool {
	nodeID, err := m.GetAssignment(agentID)
	if err != nil || nodeID == "" {
		return false
	}
	return nodeID == m.nodeID
}

func (m *clusterManager) ListNodes() ([]types.NodeInfo, error) {
	return m.store.ListNodes(context.Background())
}

func (m *clusterManager) DrainNode(nodeID string) error {
	return drainNode(context.Background(), m.store, m.assignment, nodeID)
}

func (m *clusterManager) OnAgentAssigned(fn func(string, string)) {
	m.mu.Lock()
	m.onAssigned = append(m.onAssigned, fn)
	m.mu.Unlock()
}

func (m *clusterManager) OnLeaderChange(fn func(bool)) {
	m.mu.Lock()
	m.onLeader = append(m.onLeader, fn)
	m.mu.Unlock()
}

func (m *clusterManager) OnNodeDead(fn func(string)) {
	m.mu.Lock()
	m.onDead = append(m.onDead, fn)
	m.mu.Unlock()
}

func (m *clusterManager) handleClusterEvent(ev ClusterEvent) {
	switch ev.Type {
	case EventAgentNeedsAssignment:
		if m.leader.isLeader() {
			ctx := context.Background()
			agent, err := m.store.GetAgent(ctx, ev.AgentID)
			var affinity, preference map[string]string
			if err == nil && agent != nil {
				affinity = agent.NodeAffinity
				preference = agent.NodePreference
			}
			m.assignment.assignAgent(ctx, ev.AgentID, affinity, preference)
		}
	case EventLeaderChanged:
		m.mu.RLock()
		for _, fn := range m.onLeader {
			fn(m.leader.isLeader())
		}
		m.mu.RUnlock()
	}
}

func (m *clusterManager) handleConfigEvent(ev ClusterEvent) {
	slog.Debug("config change notification", "event", ev)
}

func (m *clusterManager) handleQueueEvent(ev ClusterEvent) {
	switch ev.Type {
	case EventAgentAssigned:
		m.mu.RLock()
		for _, fn := range m.onAssigned {
			fn(ev.AgentID, ev.NodeID)
		}
		m.mu.RUnlock()
	}
}
