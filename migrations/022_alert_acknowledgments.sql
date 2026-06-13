CREATE TABLE IF NOT EXISTS alert_acknowledgments (
    source_type TEXT NOT NULL,
    source_id TEXT NOT NULL,
    acknowledged_at DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (source_type, source_id)
);
CREATE INDEX IF NOT EXISTS idx_alert_ack_type ON alert_acknowledgments(source_type);
