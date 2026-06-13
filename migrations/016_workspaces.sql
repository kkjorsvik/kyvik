-- Tracks per-agent sandbox workspaces.
CREATE TABLE IF NOT EXISTS agent_workspaces (
    agent_id       TEXT PRIMARY KEY,
    workspace_path TEXT NOT NULL,
    created_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    last_accessed  DATETIME NOT NULL DEFAULT (datetime('now')),
    size_bytes     INTEGER NOT NULL DEFAULT 0
);
