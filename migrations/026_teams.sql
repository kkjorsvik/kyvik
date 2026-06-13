CREATE TABLE IF NOT EXISTS teams (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT DEFAULT '',
    leader_id TEXT NOT NULL,
    member_ids_json TEXT NOT NULL DEFAULT '[]',
    communication TEXT NOT NULL DEFAULT 'leader-mediated',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    shared_context TEXT DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS internal_messages (
    id TEXT PRIMARY KEY,
    from_agent TEXT NOT NULL,
    to_agent TEXT NOT NULL,
    content TEXT NOT NULL,
    type TEXT NOT NULL DEFAULT 'message',
    priority TEXT NOT NULL DEFAULT 'normal',
    metadata_json TEXT DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_internal_messages_to ON internal_messages(to_agent, created_at);
CREATE INDEX IF NOT EXISTS idx_internal_messages_from ON internal_messages(from_agent, created_at);
CREATE INDEX IF NOT EXISTS idx_internal_messages_pair ON internal_messages(from_agent, to_agent, created_at);

CREATE TABLE IF NOT EXISTS paired_conversations (
    id TEXT PRIMARY KEY,
    agent_a TEXT NOT NULL,
    agent_b TEXT NOT NULL,
    topic TEXT NOT NULL,
    max_turns INTEGER NOT NULL DEFAULT 10,
    turn_delay_ms INTEGER NOT NULL DEFAULT 2000,
    allow_user_injection INTEGER NOT NULL DEFAULT 1,
    auto_stop_phrases_json TEXT DEFAULT '[]',
    status TEXT NOT NULL DEFAULT 'active',
    current_turn INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    estimated_cost REAL NOT NULL DEFAULT 0.0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    completed_at DATETIME
);

CREATE TABLE IF NOT EXISTS paired_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    content TEXT NOT NULL,
    tokens INTEGER NOT NULL DEFAULT 0,
    cost REAL NOT NULL DEFAULT 0.0,
    turn_number INTEGER NOT NULL,
    injected_by TEXT DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (conversation_id) REFERENCES paired_conversations(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_paired_messages_conv ON paired_messages(conversation_id, turn_number);
