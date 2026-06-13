CREATE TABLE IF NOT EXISTS conversation_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    channel TEXT NOT NULL,
    channel_id TEXT DEFAULT '',
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    sender TEXT DEFAULT '',
    tokens INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_history_agent_channel ON conversation_history(agent_id, channel, channel_id, created_at DESC);
