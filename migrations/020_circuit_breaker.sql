-- Circuit breaker per-agent configuration (JSON blob).
ALTER TABLE agents ADD COLUMN circuit_breaker TEXT DEFAULT '';
