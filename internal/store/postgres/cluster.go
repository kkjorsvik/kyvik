package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/kkjorsvik/kyvik/pkg/types"
)

func (s *PostgresStore) RegisterNode(ctx context.Context, node types.NodeInfo) error {
	capacity, _ := json.Marshal(node.Capacity)
	labels, _ := json.Marshal(node.Labels)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cluster_nodes (node_id, node_name, address, status, last_heartbeat, capacity, labels, version)
		 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?, ?)
		 ON CONFLICT (node_id) DO UPDATE SET
		   node_name = EXCLUDED.node_name,
		   address = EXCLUDED.address,
		   status = EXCLUDED.status,
		   last_heartbeat = CURRENT_TIMESTAMP,
		   capacity = EXCLUDED.capacity,
		   labels = EXCLUDED.labels,
		   version = EXCLUDED.version`,
		node.NodeID, node.NodeName, node.Address, node.Status,
		string(capacity), string(labels), node.Version)
	return err
}

func (s *PostgresStore) UpdateHeartbeat(ctx context.Context, nodeID string, capacity types.NodeCapacity) error {
	cap, _ := json.Marshal(capacity)
	_, err := s.db.ExecContext(ctx,
		`UPDATE cluster_nodes SET last_heartbeat = CURRENT_TIMESTAMP, capacity = ? WHERE node_id = ?`,
		string(cap), nodeID)
	return err
}

func (s *PostgresStore) ListNodes(ctx context.Context) ([]types.NodeInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT node_id, node_name, address, status, is_leader, last_heartbeat, capacity, labels, version, created_at
		 FROM cluster_nodes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []types.NodeInfo
	for rows.Next() {
		var n types.NodeInfo
		var capJSON, labelsJSON string
		var heartbeat sql.NullTime
		err := rows.Scan(&n.NodeID, &n.NodeName, &n.Address, &n.Status, &n.IsLeader,
			&heartbeat, &capJSON, &labelsJSON, &n.Version, &n.CreatedAt)
		if err != nil {
			return nil, err
		}
		if heartbeat.Valid {
			n.LastHeartbeat = heartbeat.Time
		}
		_ = json.Unmarshal([]byte(capJSON), &n.Capacity)
		_ = json.Unmarshal([]byte(labelsJSON), &n.Labels)
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func (s *PostgresStore) GetDeadNodes(ctx context.Context, timeout time.Duration) ([]types.NodeInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT node_id, node_name, address, status FROM cluster_nodes
		 WHERE status = 'active' AND last_heartbeat < CURRENT_TIMESTAMP - make_interval(secs => ?)`,
		timeout.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []types.NodeInfo
	for rows.Next() {
		var n types.NodeInfo
		if err := rows.Scan(&n.NodeID, &n.NodeName, &n.Address, &n.Status); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func (s *PostgresStore) SetNodeStatus(ctx context.Context, nodeID, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE cluster_nodes SET status = ? WHERE node_id = ?`, status, nodeID)
	return err
}

func (s *PostgresStore) DeleteNode(ctx context.Context, nodeID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM cluster_nodes WHERE node_id = ?`, nodeID)
	return err
}

func (s *PostgresStore) AssignAgent(ctx context.Context, agentID, nodeID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_assignments (agent_id, node_id, assigned_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT (agent_id) DO UPDATE SET node_id = EXCLUDED.node_id, assigned_at = CURRENT_TIMESTAMP`,
		agentID, nodeID)
	return err
}

func (s *PostgresStore) GetAssignment(ctx context.Context, agentID string) (*types.Assignment, error) {
	var a types.Assignment
	var affinityJSON, prefJSON string
	err := s.db.QueryRowContext(ctx,
		`SELECT agent_id, node_id, assigned_at, node_affinity, node_preference
		 FROM agent_assignments WHERE agent_id = ?`, agentID).
		Scan(&a.AgentID, &a.NodeID, &a.AssignedAt, &affinityJSON, &prefJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(affinityJSON), &a.NodeAffinity)
	_ = json.Unmarshal([]byte(prefJSON), &a.NodePreference)
	return &a, nil
}

func (s *PostgresStore) GetNodeAgents(ctx context.Context, nodeID string) ([]types.Assignment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id, node_id, assigned_at FROM agent_assignments WHERE node_id = ?`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assignments []types.Assignment
	for rows.Next() {
		var a types.Assignment
		if err := rows.Scan(&a.AgentID, &a.NodeID, &a.AssignedAt); err != nil {
			return nil, err
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}

func (s *PostgresStore) GetOrphanedAgents(ctx context.Context, nodeID string) ([]types.Assignment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.agent_id, a.node_id, a.assigned_at, a.node_affinity, a.node_preference
		 FROM agent_assignments a
		 JOIN agents ag ON ag.id = a.agent_id
		 WHERE a.node_id = ? AND ag.desired_state = 'running'`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assignments []types.Assignment
	for rows.Next() {
		var a types.Assignment
		var affinityJSON, prefJSON string
		if err := rows.Scan(&a.AgentID, &a.NodeID, &a.AssignedAt, &affinityJSON, &prefJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(affinityJSON), &a.NodeAffinity)
		_ = json.Unmarshal([]byte(prefJSON), &a.NodePreference)
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}

func (s *PostgresStore) DeleteAssignment(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agent_assignments WHERE agent_id = ?`, agentID)
	return err
}
