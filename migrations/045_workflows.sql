CREATE TABLE IF NOT EXISTS workflows (
    id          TEXT PRIMARY KEY,
    agent_id    TEXT NOT NULL,
    name        TEXT NOT NULL,
    description TEXT DEFAULT '',
    steps_json  TEXT NOT NULL,
    enabled     INTEGER DEFAULT 1,
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workflows_agent_name ON workflows(agent_id, name);
CREATE INDEX IF NOT EXISTS idx_workflows_agent ON workflows(agent_id);

CREATE TABLE IF NOT EXISTS workflow_runs (
    id              TEXT PRIMARY KEY,
    workflow_id     TEXT NOT NULL,
    agent_id        TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'running',
    steps_json      TEXT DEFAULT '[]',
    input_vars_json TEXT DEFAULT '{}',
    error           TEXT DEFAULT '',
    duration_ms     INTEGER DEFAULT 0,
    started_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at    TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_workflow ON workflow_runs(workflow_id, started_at);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_agent ON workflow_runs(agent_id, started_at);
