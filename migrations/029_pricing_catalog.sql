-- Pricing catalog and usage provenance metadata.

CREATE TABLE IF NOT EXISTS pricing_catalog (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL,
    model_pattern TEXT NOT NULL,
    input_per_m REAL NOT NULL DEFAULT 0.0,
    output_per_m REAL NOT NULL DEFAULT 0.0,
    currency TEXT NOT NULL DEFAULT 'USD',
    effective_from DATETIME NOT NULL,
    effective_to DATETIME DEFAULT NULL,
    source TEXT NOT NULL DEFAULT 'seed',
    source_version TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, model_pattern, effective_from)
);

CREATE INDEX IF NOT EXISTS idx_pricing_catalog_lookup
ON pricing_catalog(provider, model_pattern, effective_from, effective_to);

ALTER TABLE usage_records ADD COLUMN cost_source TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE usage_records ADD COLUMN usage_source TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE usage_records ADD COLUMN usage_complete INTEGER NOT NULL DEFAULT 1;
ALTER TABLE usage_records ADD COLUMN pricing_version TEXT DEFAULT '';

-- Seed baseline pricing entries for catalog-based cost computation.
INSERT OR IGNORE INTO pricing_catalog
    (provider, model_pattern, input_per_m, output_per_m, currency, effective_from, source, source_version)
VALUES
    ('openai', 'gpt-4o', 2.50, 10.00, 'USD', '1970-01-01 00:00:00', 'seed', 'v1'),
    ('openai', 'gpt-4o-mini', 0.15, 0.60, 'USD', '1970-01-01 00:00:00', 'seed', 'v1'),
    ('openai', 'gpt-4-turbo', 10.00, 30.00, 'USD', '1970-01-01 00:00:00', 'seed', 'v1'),
    ('openai', 'o1', 15.00, 60.00, 'USD', '1970-01-01 00:00:00', 'seed', 'v1'),
    ('openai', 'o1-mini', 1.10, 4.40, 'USD', '1970-01-01 00:00:00', 'seed', 'v1'),
    ('openai', 'o3-mini', 1.10, 4.40, 'USD', '1970-01-01 00:00:00', 'seed', 'v1'),
    ('anthropic', 'claude-opus-4-5-20250527', 15.00, 75.00, 'USD', '1970-01-01 00:00:00', 'seed', 'v1'),
    ('anthropic', 'claude-sonnet-4-20250514', 3.00, 15.00, 'USD', '1970-01-01 00:00:00', 'seed', 'v1'),
    ('anthropic', 'claude-haiku-4-5-20251001', 0.80, 4.00, 'USD', '1970-01-01 00:00:00', 'seed', 'v1'),
    ('anthropic', 'claude-3-5-sonnet-20241022', 3.00, 15.00, 'USD', '1970-01-01 00:00:00', 'seed', 'v1'),
    ('anthropic', 'claude-3-5-haiku-20241022', 0.80, 4.00, 'USD', '1970-01-01 00:00:00', 'seed', 'v1'),
    ('anthropic', 'claude-3-opus-20240229', 15.00, 75.00, 'USD', '1970-01-01 00:00:00', 'seed', 'v1');
