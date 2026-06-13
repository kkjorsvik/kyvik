-- conversation_history: mark compressed entries
ALTER TABLE conversation_history ADD COLUMN compressed_by INTEGER DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_history_compressed ON conversation_history(compressed_by);

-- compression_log: track compression events
CREATE TABLE IF NOT EXISTS compression_log (
    id              TEXT PRIMARY KEY,
    agent_id        TEXT NOT NULL,
    channel         TEXT NOT NULL,
    channel_id      TEXT NOT NULL,
    messages_input  INTEGER NOT NULL,
    tokens_input    INTEGER NOT NULL,
    tokens_output   INTEGER NOT NULL,
    tokens_summarize INTEGER NOT NULL,
    model           TEXT NOT NULL,
    previous_summary_id INTEGER,
    duration_ms     INTEGER NOT NULL,
    created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_compression_log_agent ON compression_log(agent_id, created_at);

-- usage_records: tag spending by category (e.g. compression vs normal)
ALTER TABLE usage_records ADD COLUMN category TEXT DEFAULT '';
