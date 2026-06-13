-- Cluster node registry
-- NOTE: Written in SQLite-compatible SQL because dbmigrate.EnsurePostgresSchemaFromSQLite
-- auto-converts to PostgreSQL types at startup. INTEGER maps to BOOLEAN, TEXT to TEXT,
-- TIMESTAMP to TIMESTAMPTZ. For capacity/labels, we use TEXT here; Postgres stores them
-- as TEXT but we marshal/unmarshal JSON in Go code (same pattern as other JSONB-like columns).
CREATE TABLE IF NOT EXISTS cluster_nodes (
    node_id TEXT PRIMARY KEY,
    node_name TEXT NOT NULL DEFAULT '',
    address TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'joining',
    is_leader INTEGER NOT NULL DEFAULT 0,
    last_heartbeat TIMESTAMP,
    capacity TEXT DEFAULT '{}',
    labels TEXT DEFAULT '{}',
    version TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Agent-to-node assignments
CREATE TABLE IF NOT EXISTS agent_assignments (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES cluster_nodes(node_id) ON DELETE CASCADE,
    assigned_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    node_affinity TEXT DEFAULT '{}',
    node_preference TEXT DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_agent_assignments_node_id ON agent_assignments(node_id);

-- Add target_node_id to existing message_queue table for cross-node message routing
ALTER TABLE message_queue ADD COLUMN target_node_id TEXT DEFAULT '';
