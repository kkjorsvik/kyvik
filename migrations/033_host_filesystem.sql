-- Add host filesystem allowlist config to agents
ALTER TABLE agents ADD COLUMN host_filesystem TEXT;
