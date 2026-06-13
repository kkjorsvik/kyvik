-- Add risk_level column to audit_log table
ALTER TABLE audit_log ADD COLUMN risk_level TEXT DEFAULT '';
