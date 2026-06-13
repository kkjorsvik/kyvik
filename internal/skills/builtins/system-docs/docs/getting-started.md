# Getting Started with Kyvik

## First-Run Flow

When Kyvik starts for the first time it:

1. Creates the database and applies migrations
2. Installs built-in skills (system-docs, file-manager)
3. Starts the web dashboard on the configured port (default 8080)
4. Waits for you to create your first agent

## Creating an Agent

From the dashboard, click **New Agent** and configure:

- **Name** — a unique identifier (lowercase, hyphens allowed)
- **Description** — what this agent does
- **System Prompt** — the agent's core instructions and personality
- **Permission Tier** — determines what tools and actions the agent can use (see [Permissions](permissions.md))
  - `reader` — read-only, safe default for information retrieval
  - `writer` — can create/modify files and make HTTP requests
  - `operator` — writer capabilities plus broader coordination
  - `admin` — full tool access including shell and code execution
  - `power` — admin plus host filesystem access via allowlists
  - `unrestricted` — no restrictions (still audited)
- **Model Configuration** — which LLM provider and model to use (see [Models](models.md))

Start with `reader` or `writer` tier and escalate only when needed.

## Sending Your First Message

Once created, start the agent and send a message through:

- **Web Dashboard** — the built-in chat interface
- **Slack** — if a Slack channel adapter is configured
- **REST API** — programmatic access (see [API](api.md))

The agent processes your message through the configured model, with all tool calls gated by permissions and logged to the audit trail.

## Configuration

Main configuration lives in `kyvik.yaml`. Key sections:

- `server` — host, port, TLS settings
- `auth` — dashboard authentication
- `storage` — database backend (PostgreSQL recommended, SQLite for development)
- `spending` — global budget limits
- `providers` — LLM provider credentials and settings
- `channels` — Slack and other communication adapters
- `logging` — log level and output

Secrets (API keys, tokens) are set via environment variables:
- `KYVIK_OPENROUTER_API_KEY`
- `KYVIK_SLACK_BOT_TOKEN` / `KYVIK_SLACK_APP_TOKEN`
- `KYVIK_MASTER_KEY` — base64-encoded 32-byte key for secret encryption
