-- Add missing columns to agents table that the store layer expects.
ALTER TABLE agents ADD COLUMN history_limit INTEGER NOT NULL DEFAULT 50;
ALTER TABLE agents ADD COLUMN memory_limit INTEGER NOT NULL DEFAULT 10;
ALTER TABLE agents ADD COLUMN auto_extract_memories INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN discord_auth_mode TEXT NOT NULL DEFAULT 'open';
ALTER TABLE agents ADD COLUMN security_config TEXT DEFAULT '';
ALTER TABLE agents ADD COLUMN heartbeat_config TEXT DEFAULT '';
ALTER TABLE agents ADD COLUMN compression_json TEXT DEFAULT '';
ALTER TABLE agents ADD COLUMN feedback_hooks_json TEXT DEFAULT '';
ALTER TABLE agents ADD COLUMN can_message_json TEXT DEFAULT '[]';
ALTER TABLE agents ADD COLUMN team_id TEXT DEFAULT '';
ALTER TABLE agents ADD COLUMN obsidian_vaults TEXT DEFAULT '[]';
