-- Tool audit index: speeds up permission and execution lookups by agent.
CREATE INDEX IF NOT EXISTS idx_audit_tool_action
    ON audit_log(agent_id, action)
    WHERE action IN ('tool_permission', 'tool_execution');
