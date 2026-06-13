-- REST API endpoints: per-agent pre-configured API endpoint definitions (stored as JSON).
ALTER TABLE agents ADD COLUMN rest_api_endpoints TEXT DEFAULT NULL;
