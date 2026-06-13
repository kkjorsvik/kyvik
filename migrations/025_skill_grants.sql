CREATE TABLE IF NOT EXISTS skill_grants (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    skill_name TEXT NOT NULL,
    granted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    granted_by TEXT DEFAULT 'dashboard',
    UNIQUE(agent_id, skill_name)
);
CREATE INDEX IF NOT EXISTS idx_skill_grants_agent ON skill_grants(agent_id);
