-- Agent long-term memory storage.
CREATE TABLE IF NOT EXISTS memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    category TEXT NOT NULL,
    content TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'agent',
    relevance_score REAL DEFAULT 0.5,
    pinned BOOLEAN DEFAULT FALSE,
    archived BOOLEAN DEFAULT FALSE,
    reviewed BOOLEAN DEFAULT TRUE,
    source_channel TEXT DEFAULT '',
    source_channel_id TEXT DEFAULT '',
    embedding BLOB,
    embedding_model TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    access_count INTEGER DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_memories_agent ON memories(agent_id);
CREATE INDEX IF NOT EXISTS idx_memories_agent_category ON memories(agent_id, category);
CREATE INDEX IF NOT EXISTS idx_memories_agent_pinned ON memories(agent_id, pinned);
CREATE INDEX IF NOT EXISTS idx_memories_agent_reviewed ON memories(agent_id, reviewed);
CREATE INDEX IF NOT EXISTS idx_memories_agent_source ON memories(agent_id, source);
