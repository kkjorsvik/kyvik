-- Add missing memory extraction and cluster columns to agents table.
ALTER TABLE agents ADD COLUMN max_memories INTEGER DEFAULT 0;
ALTER TABLE agents ADD COLUMN memory_extraction_interval INTEGER DEFAULT 0;
ALTER TABLE agents ADD COLUMN memory_max_extractions_per_run INTEGER DEFAULT 0;
ALTER TABLE agents ADD COLUMN memory_duplicate_threshold REAL DEFAULT 0;
ALTER TABLE agents ADD COLUMN memory_similar_threshold REAL DEFAULT 0;
ALTER TABLE agents ADD COLUMN node_affinity TEXT DEFAULT '';
ALTER TABLE agents ADD COLUMN node_preference TEXT DEFAULT '';
