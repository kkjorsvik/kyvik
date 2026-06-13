-- 009_channel_config.sql
-- Adds per-agent Slack mode and WebUI configuration.
-- Applied idempotently via ALTER TABLE in sqlite.go.
--
-- slack_mode:    "none" (default), "primary", "dedicated"
-- slack_channel: Slack channel ID for primary mode
-- webui_enabled: 1 = enabled (default), 0 = disabled

ALTER TABLE agents ADD COLUMN slack_mode TEXT DEFAULT 'none';
ALTER TABLE agents ADD COLUMN slack_channel TEXT DEFAULT '';
ALTER TABLE agents ADD COLUMN webui_enabled INTEGER DEFAULT 1;
