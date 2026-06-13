CREATE TABLE IF NOT EXISTS outbound_webhooks (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    url TEXT NOT NULL,
    agent_id TEXT,
    events TEXT NOT NULL DEFAULT '["*"]',
    secret_ref TEXT DEFAULT '',
    headers TEXT DEFAULT '{}',
    payload_template TEXT DEFAULT '',
    max_retries INTEGER NOT NULL DEFAULT 3,
    backoff_seconds TEXT NOT NULL DEFAULT '[5,30,120]',
    cb_threshold INTEGER NOT NULL DEFAULT 10,
    cb_cooldown_secs INTEGER NOT NULL DEFAULT 3600,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_outbound_webhooks_agent ON outbound_webhooks(agent_id);
CREATE INDEX IF NOT EXISTS idx_outbound_webhooks_enabled ON outbound_webhooks(enabled);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id TEXT PRIMARY KEY,
    webhook_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    payload TEXT DEFAULT '',
    status TEXT NOT NULL DEFAULT 'success',
    http_code INTEGER NOT NULL DEFAULT 0,
    response_body TEXT DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0,
    retry_count INTEGER NOT NULL DEFAULT 0,
    next_retry_at DATETIME,
    error_message TEXT DEFAULT '',
    payload_sha256 TEXT DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (webhook_id) REFERENCES outbound_webhooks(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_webhook ON webhook_deliveries(webhook_id);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_status ON webhook_deliveries(status);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_retry ON webhook_deliveries(status, next_retry_at);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_created ON webhook_deliveries(created_at);
