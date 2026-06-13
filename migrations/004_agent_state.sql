-- Agent desired/actual state columns for restart recovery.
-- Each ALTER is executed individually; "duplicate column" errors are silenced
-- for idempotent re-runs.
ALTER TABLE agents ADD COLUMN desired_state TEXT NOT NULL DEFAULT 'stopped';
ALTER TABLE agents ADD COLUMN actual_state TEXT NOT NULL DEFAULT 'stopped';
ALTER TABLE agents ADD COLUMN last_error TEXT DEFAULT '';
