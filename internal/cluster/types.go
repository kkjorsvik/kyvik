package cluster

import "strings"

// ClusterEvent is sent via LISTEN/NOTIFY for cross-node coordination.
// Design constraint: payloads must be lightweight signal references
// (type + ID only, under 1KB). Full data is read from the database.
type ClusterEvent struct {
	Type    string `json:"type"`
	NodeID  string `json:"node_id,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
}

// NOTIFY channel names.
const (
	ChannelCluster = "kyvik_cluster"
	ChannelConfig  = "kyvik_config"
)

// NodeQueueChannel returns the per-node NOTIFY channel name.
// Strips hyphens from UUID to ensure valid PostgreSQL channel name.
func NodeQueueChannel(nodeID string) string {
	return "kyvik_queue_" + strings.ReplaceAll(nodeID, "-", "")
}

// ClusterEvent types.
const (
	EventAgentNeedsAssignment = "agent_needs_assignment"
	EventNodeJoined           = "node_joined"
	EventNodeLeft             = "node_left"
	EventLeaderChanged        = "leader_changed"
	EventAgentAssigned        = "agent_assigned"
)
