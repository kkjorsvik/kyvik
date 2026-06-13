CREATE TABLE IF NOT EXISTS obsidian_vaults (
    id TEXT PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    path TEXT NOT NULL,
    sync_email TEXT DEFAULT '',
    sync_password TEXT DEFAULT '',
    sync_vault_id TEXT DEFAULT '',
    sync_enabled INTEGER NOT NULL DEFAULT 0,
    sync_status TEXT NOT NULL DEFAULT 'disabled',
    last_sync_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
