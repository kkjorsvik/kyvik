-- Add status column to memories table.
-- Replaces the separate archived/reviewed booleans with a unified lifecycle state.
-- Values: 'candidate' (auto-extracted, pending review), 'active' (approved), 'archived' (evicted/expired).
ALTER TABLE memories ADD COLUMN status TEXT NOT NULL DEFAULT 'active';

-- Migrate existing data: archived memories get status='archived', others get 'active'.
-- Use boolean literals (TRUE/FALSE) for PostgreSQL compatibility after type conversion.
UPDATE memories SET status = 'archived' WHERE archived = TRUE;
UPDATE memories SET status = 'active' WHERE archived = FALSE;

-- Migrate reviewed: all active memories are considered reviewed.
-- Candidates (auto-extracted, unreviewed) will use status='candidate' going forward.
UPDATE memories SET reviewed = TRUE WHERE status = 'active';

-- Index for efficient status filtering (replaces archived-based filtering).
CREATE INDEX IF NOT EXISTS idx_memories_agent_status ON memories(agent_id, status);
