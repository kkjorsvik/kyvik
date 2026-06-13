-- Kyvik core schema
-- SQLite implementation — agents, permissions, usage, spending

CREATE TABLE IF NOT EXISTS agents (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT DEFAULT '',
    system_prompt TEXT DEFAULT '',
    model_provider TEXT NOT NULL,
    model_name TEXT NOT NULL,
    template TEXT NOT NULL DEFAULT 'reader',
    channels_json TEXT DEFAULT '[]',
    limits_json TEXT DEFAULT '{}',
    metadata_json TEXT DEFAULT '{}',
    context_budget_json TEXT DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'stopped',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS permission_overrides (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    tool TEXT NOT NULL,
    action TEXT NOT NULL,
    resource TEXT NOT NULL DEFAULT '*',
    grant_access BOOLEAN NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS usage_records (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    tokens_in INTEGER NOT NULL DEFAULT 0,
    tokens_out INTEGER NOT NULL DEFAULT 0,
    cost_usd REAL NOT NULL DEFAULT 0.0,
    model TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS spending_limits (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT DEFAULT '__global__',
    max_tokens_per_day INTEGER DEFAULT 0,
    max_tokens_per_month INTEGER DEFAULT 0,
    max_spend_per_day REAL DEFAULT 0.0,
    max_spend_per_month REAL DEFAULT 0.0,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(agent_id)
);

-- Indexes for common queries
CREATE INDEX IF NOT EXISTS idx_usage_agent ON usage_records(agent_id, created_at);
CREATE INDEX IF NOT EXISTS idx_overrides_agent ON permission_overrides(agent_id);
