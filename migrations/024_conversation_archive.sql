CREATE TABLE IF NOT EXISTS conversation_history_archive (
    id INTEGER PRIMARY KEY,
    agent_id TEXT NOT NULL,
    channel TEXT NOT NULL,
    channel_id TEXT DEFAULT '',
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    sender TEXT DEFAULT '',
    tokens INTEGER DEFAULT 0,
    attachments TEXT DEFAULT '',
    tool_call_id TEXT DEFAULT '',
    tool_calls_json TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    archived_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_archive_agent_channel
    ON conversation_history_archive(agent_id, channel, channel_id, created_at DESC);
