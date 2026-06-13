-- Web conversations metadata table for multi-conversation support.
CREATE TABLE IF NOT EXISTS web_conversations (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT 'New conversation',
    message_count INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_web_conv_agent ON web_conversations(agent_id, updated_at DESC);
