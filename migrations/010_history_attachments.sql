-- Add attachments metadata column to conversation_history.
-- Stores JSON array of {filename, content_type, size} — no raw file data.
-- Applied idempotently via ALTER TABLE in internal/store/sqlite/sqlite.go.
ALTER TABLE conversation_history ADD COLUMN attachments TEXT DEFAULT '';
