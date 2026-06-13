CREATE TABLE IF NOT EXISTS agent_templates (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT DEFAULT '',
    group_id TEXT REFERENCES agent_groups(id) ON DELETE SET NULL,
    config_json TEXT NOT NULL,
    locked_fields TEXT DEFAULT '[]',
    constrained_fields TEXT DEFAULT '{}',
    created_by TEXT REFERENCES users(id),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_agent_templates_group ON agent_templates(group_id);
