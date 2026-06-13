# REST API

## Overview

Kyvik exposes a REST API for programmatic access. The API is enabled by default and uses the same authentication as the dashboard.

## Authentication

API requests authenticate with API keys:

- Keys have the format `kv_` followed by 64 hex characters
- Include the key in the `Authorization` header: `Authorization: Bearer kv_abc123...`
- Each key is scoped to a dashboard role (viewer, operator, manager, admin)
- Keys can optionally be restricted to specific agent IDs

## Roles and Rate Limits

| Role | Rate Limit | What It Can Do |
|------|-----------|----------------|
| viewer | 60 req/min | View agents, memories, history, spending, chat |
| operator | 120 req/min | Start/stop/kill/quarantine agents, view audit, teams, security |
| manager | 120 req/min | Create/edit agents, manage permissions, spending, teams, webhooks, skills |
| admin | 300 req/min | Delete agents, manage secrets, users, templates, backups, system config |

Roles are cumulative — admin includes all manager permissions, which includes all operator permissions, etc.

## Key Endpoints

### Agents

```bash
# List all agents
curl -H "Authorization: Bearer $KEY" https://kyvik.example/api/agents

# Get agent details
curl -H "Authorization: Bearer $KEY" https://kyvik.example/api/agents/{id}

# Create an agent (manager+)
curl -X POST -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-agent","tier":"writer"}' \
  https://kyvik.example/api/agents

# Start an agent (operator+)
curl -X POST -H "Authorization: Bearer $KEY" \
  https://kyvik.example/api/agents/{id}/start

# Stop an agent (operator+)
curl -X POST -H "Authorization: Bearer $KEY" \
  https://kyvik.example/api/agents/{id}/stop
```

### Chat

```bash
# Send a message to an agent
curl -X POST -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"content":"Hello, agent!"}' \
  https://kyvik.example/api/agents/{id}/chat

# Get conversation history
curl -H "Authorization: Bearer $KEY" \
  https://kyvik.example/api/agents/{id}/history
```

### Spending

```bash
# Get spending summary
curl -H "Authorization: Bearer $KEY" \
  https://kyvik.example/api/spending

# Get agent spending
curl -H "Authorization: Bearer $KEY" \
  https://kyvik.example/api/agents/{id}/spending
```

### Audit

```bash
# Query audit log (operator+)
curl -H "Authorization: Bearer $KEY" \
  https://kyvik.example/api/audit?agent_id={id}&limit=50
```

## Response Format

All responses are JSON. Errors include a `message` field:

```json
{
  "error": "permission_denied",
  "message": "API key does not have operator role"
}
```

## API Key Management

API keys are managed through the dashboard (admin role) or via the API:

```bash
# Create an API key (admin)
curl -X POST -H "Authorization: Bearer $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"name":"ci-key","role":"operator","agent_ids":["agent-1"]}' \
  https://kyvik.example/api/keys
```

The plain-text key is returned only at creation time — store it securely.
