-- 017_host_paths: Add host_paths column to agents for power-tier host filesystem access.
-- Applied idempotently via ALTER TABLE in sqlite.go New().
ALTER TABLE agents ADD COLUMN host_paths TEXT DEFAULT NULL;
