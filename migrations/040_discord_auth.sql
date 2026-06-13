CREATE TABLE IF NOT EXISTS discord_authorizations (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    discord_user_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    pairing_code TEXT DEFAULT '',
    added_by TEXT NOT NULL DEFAULT 'pairing',
    code_expires_at TEXT DEFAULT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(agent_id, discord_user_id)
);

CREATE INDEX IF NOT EXISTS idx_discord_auth_agent ON discord_authorizations(agent_id);
CREATE INDEX IF NOT EXISTS idx_discord_auth_code ON discord_authorizations(pairing_code) WHERE pairing_code != '';
