CREATE TABLE IF NOT EXISTS providers (
    id              TEXT PRIMARY KEY,
    provider_type   TEXT NOT NULL,
    display_name    TEXT NOT NULL,
    api_key_enc     TEXT DEFAULT '',
    base_url        TEXT DEFAULT '',
    default_model   TEXT DEFAULT '',
    allowed_models  TEXT DEFAULT '[]',
    is_enabled      INTEGER NOT NULL DEFAULT 1,
    source          TEXT NOT NULL DEFAULT 'db',
    config_json     TEXT DEFAULT '{}',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_providers_type ON providers(provider_type);
CREATE INDEX IF NOT EXISTS idx_providers_enabled ON providers(is_enabled);
