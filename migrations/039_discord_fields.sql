ALTER TABLE agents ADD COLUMN discord_mode TEXT NOT NULL DEFAULT 'none';
ALTER TABLE agents ADD COLUMN discord_channel_id TEXT NOT NULL DEFAULT '';
