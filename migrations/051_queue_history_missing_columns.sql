-- Add missing columns to message_queue and conversation_history.
ALTER TABLE message_queue ADD COLUMN conversation_id TEXT DEFAULT '';
ALTER TABLE conversation_history ADD COLUMN tool_call_id TEXT DEFAULT '';
ALTER TABLE conversation_history ADD COLUMN tool_calls_json TEXT DEFAULT '';
