CREATE TABLE IF NOT EXISTS internal_message_acks (
  message_id TEXT NOT NULL,
  to_agent TEXT NOT NULL,
  acked_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (message_id, to_agent)
);

CREATE INDEX IF NOT EXISTS idx_internal_message_acks_to ON internal_message_acks(to_agent, acked_at);
