-- Spending tracker: add multi-slot and multi-provider columns to usage_records.
ALTER TABLE usage_records ADD COLUMN model_slot TEXT DEFAULT 'default';
ALTER TABLE usage_records ADD COLUMN routed_by TEXT DEFAULT 'default';
ALTER TABLE usage_records ADD COLUMN provider TEXT DEFAULT '';
ALTER TABLE usage_records ADD COLUMN parent_agent_id TEXT DEFAULT '';
