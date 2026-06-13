# Kyvik

A security-first, multi-agent AI framework written in Go.

Kyvik provides a managed environment for running AI agents with built-in guardrails, a web dashboard, and native multi-agent isolation — all deployable as a single binary.

## Status

**Pre-alpha.** Design phase. See [DESIGN.md](DESIGN.md) for the full architecture and planning document.

## Core Principles

1. **Security & guardrails first.** Deny-by-default permissions. Sandboxed execution. Audit everything.
2. **Accessible.** Web dashboard from day one. Agent creation should feel like filling out a form.
3. **Multi-agent native.** Agents are isolated by design, not by workaround.
4. **Simple deployment.** Single Go binary with PostgreSQL storage.

## Project Structure

```
kyvik/
├── cmd/
│   ├── kyvik/            # Main server binary
│   └── kyvik-sandbox/    # Sandbox runner binary (child process)
├── internal/             # Core implementation
│   ├── core/             # Agent lifecycle and routing
│   ├── router/           # Model router (provider selection, failover)
│   ├── models/           # LLM provider adapters (OpenRouter, OpenAI, Anthropic, Ollama)
│   ├── permissions/      # Permission gate and templates
│   ├── sandbox/          # Execution isolation (process sandbox)
│   ├── security/         # Prompt injection defense
│   ├── tools/            # Native tool protocol (KTP)
│   ├── ktp/              # Kyvik Tool Protocol implementation
│   ├── channels/         # Communication adapters (Slack, Web UI)
│   ├── queue/            # Message queue with priority and backpressure
│   ├── store/            # Data persistence (PostgreSQL)
│   ├── audit/            # Audit logging
│   ├── spending/         # Token counting and cost tracking
│   ├── ctxbudget/        # Per-request context budgets
│   ├── memory/           # Agent memory subsystem
│   ├── history/          # Conversation history management
│   ├── identity/         # Agent identity and credentials
│   ├── secrets/          # Encrypted secrets vault (AES-256-GCM)
│   ├── keymanager/       # Per-agent API key provisioning
│   ├── auth/             # Authentication middleware
│   ├── config/           # Configuration loading
│   ├── notifications/    # Operator alerts (Slack, spending, errors)
│   ├── workers/          # Background worker pool
│   └── integration/      # Integration tests
├── web/                  # Dashboard (HTMX + Go templates)
├── pkg/                  # Public interfaces for extensions
├── migrations/           # Database schema
└── configs/              # Configuration and permission templates
```

## Requirements

| Component | Minimum | Notes |
|-----------|---------|-------|
| OS | Ubuntu 24.04 LTS | Other Linux distributions work but this guide targets Ubuntu |
| Go | 1.24+ | Required to build from source |
| PostgreSQL | 14+ | Required database backend |
| RAM | 512 MB | More may be needed depending on agent count |
| CPU | 1 core | |
| Disk | 1 GB | Database grows with audit log and usage history |

## Quick Start

```bash
# Clone the repository
git clone https://github.com/kkjorsvik/kyvik.git
cd kyvik

# Build
make build

# Run (builds first, then starts the server)
make run

# Development mode (uses go run, no build artifact)
make dev
```

On first run without a `kyvik.yaml`, Kyvik falls back to `configs/kyvik.example.yaml` and logs a notice. The dashboard is available at `http://localhost:8080` with default credentials `admin` / `changeme`.

Other make targets:

```bash
make build-sandbox  # Build the sandbox runner binary
make build-all      # Build both kyvik and kyvik-sandbox
make install        # Build all and install to /opt/kyvik/bin (restarts service)
make generate-key   # Generate KYVIK_MASTER_KEY and write to /etc/kyvik/env
make test           # Run all tests
make lint           # Run golangci-lint
make clean          # Remove build directory
make docker-build   # Build Docker image
make docker-run     # Run in Docker on port 8080
```

## Production Deployment on Ubuntu

This section walks through deploying Kyvik on an Ubuntu 24.04 server as a long-running service.

### 1. Install dependencies

```bash
sudo apt update
sudo apt install -y golang-go make git postgresql-client
```

Verify Go is 1.24 or later:

```bash
go version
```

If your distribution ships an older Go version, install from [go.dev/dl](https://go.dev/dl/) instead.

### 2. Create a system user

```bash
sudo useradd --system --shell /usr/sbin/nologin kyvik
```

### 3. Build the binaries

```bash
git clone https://github.com/kkjorsvik/kyvik.git /tmp/kyvik-src
cd /tmp/kyvik-src
make build-all
```

This builds both `kyvik` (the main server) and `kyvik-sandbox` (the sandbox runner) into `./build/`.

### 4. Create the directory layout

```bash
sudo mkdir -p /opt/kyvik/bin
sudo mkdir -p /etc/kyvik
sudo mkdir -p /var/lib/kyvik
sudo mkdir -p /var/log/kyvik
```

| Path | Purpose |
|------|---------|
| `/opt/kyvik/bin/` | Binary |
| `/etc/kyvik/` | Configuration and environment file |
| `/var/lib/kyvik/` | Runtime data and backups |
| `/var/log/kyvik/` | Log output (from journald or redirect) |

### 5. Install files

```bash
# Binaries
sudo cp /tmp/kyvik-src/build/kyvik /opt/kyvik/bin/
sudo cp /tmp/kyvik-src/build/kyvik-sandbox /opt/kyvik/bin/
sudo chmod 755 /opt/kyvik/bin/kyvik /opt/kyvik/bin/kyvik-sandbox

# Configuration
sudo cp /tmp/kyvik-src/configs/kyvik.example.yaml /etc/kyvik/kyvik.yaml

# Permission templates (hardcoded path expectation: configs/templates/)
sudo mkdir -p /opt/kyvik/configs/templates
sudo cp /tmp/kyvik-src/configs/templates/* /opt/kyvik/configs/templates/

# Set ownership
sudo chown -R kyvik:kyvik /opt/kyvik
sudo chown -R kyvik:kyvik /etc/kyvik
sudo chown -R kyvik:kyvik /var/lib/kyvik
sudo chown -R kyvik:kyvik /var/log/kyvik
```

### 6. Configure Kyvik

Edit `/etc/kyvik/kyvik.yaml`:

```yaml
server:
  listen_addr: "127.0.0.1:8080"  # Bind to localhost only; nginx handles public traffic
  data_dir: "/var/lib/kyvik"

auth:
  type: "basic"
  # Credentials set via environment variables (see step 7)

storage:
  driver: "postgres"
  postgres:
    dsn: "postgres://kyvik:change-me@localhost:5432/kyvik?sslmode=disable"
  # wal_mode: true            # Legacy setting; ignored for postgres
  # separate_audit_db: false  # Legacy setting; ignored for postgres
  # write_batch_ms: 100       # Audit write batch window (default: 100)
  # max_connections: 15       # Connection pool size (default: 15)

spending:
  max_spend_per_day: 10.00
  max_spend_per_month: 100.00
  max_tokens_per_day: 0
  max_tokens_per_month: 0

models:
  openrouter:
    default_model: "deepseek/deepseek-chat"
    # API key set via KYVIK_OPENROUTER_API_KEY env var
  openai:
    default_model: "gpt-4o-mini"
    # API key set via KYVIK_OPENAI_API_KEY env var
  anthropic:
    default_model: "claude-sonnet-4-20250514"
    # API key set via KYVIK_ANTHROPIC_API_KEY env var
  ollama:
    enabled: false
    default_model: "llama3"

channels:
  slack:
    enabled: false
  webui:
    enabled: true

queue:
  depth: 50
  full_behavior: "acknowledge"  # acknowledge, reject, drop
  stale_timeout_seconds: 300
  retention_hours: 24

notifications:
  slack_channel: "#kyvik-alerts"
  events:
    circuit_breaker: true
    agent_error: true
    spending_threshold: 90
    security_alerts: true

sandbox:
  # workspace_root: "/var/lib/kyvik/workspaces"
  # runner_path: "/opt/kyvik/bin/kyvik-sandbox"
  # max_memory_mb: 1024
  # max_cpu_percent: 50
  # timeout_seconds: 60

logging:
  level: "info"
  audit:
    enabled: true
    retention_days: 90
```

### 7. Generate the master key

The master key encrypts the secrets vault (per-agent API keys, credentials). Generate it first:

```bash
make generate-key
```

This writes `KYVIK_MASTER_KEY` to `/etc/kyvik/env`. If the file already exists, it is not overwritten.

### 8. Create the environment file

Append credentials to `/etc/kyvik/env` (the master key is already there from step 7):

```bash
sudo tee -a /etc/kyvik/env > /dev/null <<'EOF'
KYVIK_AUTH_USER=admin
KYVIK_AUTH_PASS=a-strong-random-password
KYVIK_OPENROUTER_API_KEY=sk-or-your-key-here
# KYVIK_OPENAI_API_KEY=sk-...
# KYVIK_ANTHROPIC_API_KEY=sk-ant-...
# KYVIK_OPENROUTER_PROVISIONING_KEY=sk-or-...
# KYVIK_SLACK_BOT_TOKEN=xoxb-...
# KYVIK_SLACK_APP_TOKEN=xapp-...
# KYVIK_CHAT_V2=true
# KYVIK_CHAT_V2_DEFAULT=false
EOF

sudo chmod 600 /etc/kyvik/env
sudo chown kyvik:kyvik /etc/kyvik/env
```

## Systemd Service

Create `/etc/systemd/system/kyvik.service`:

```ini
[Unit]
Description=Kyvik AI Agent Framework
After=network.target
Documentation=https://github.com/kkjorsvik/kyvik

[Service]
Type=simple
User=kyvik
Group=kyvik
WorkingDirectory=/opt/kyvik
ExecStart=/opt/kyvik/bin/kyvik -config /etc/kyvik/kyvik.yaml
EnvironmentFile=/etc/kyvik/env
Restart=on-failure
RestartSec=5

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=kyvik

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/kyvik /var/lib/kyvik/workspaces /var/log/kyvik
ReadOnlyPaths=/etc/kyvik /opt/kyvik

[Install]
WantedBy=multi-user.target
```

Enable and start:

```bash
sudo systemctl daemon-reload
sudo systemctl enable kyvik
sudo systemctl start kyvik
sudo systemctl status kyvik
```

## Reverse Proxy (Nginx + HTTPS)

### Install Nginx and Certbot

```bash
sudo apt install -y nginx certbot python3-certbot-nginx
```

### Create the server block

Create `/etc/nginx/sites-available/kyvik`:

```nginx
server {
    listen 80;
    server_name kyvik.example.com;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # SSE and WebSocket support
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 86400s;
        proxy_buffering off;
    }
}
```

Enable the site and obtain a certificate:

```bash
sudo ln -s /etc/nginx/sites-available/kyvik /etc/nginx/sites-enabled/
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t
sudo systemctl reload nginx

# Obtain TLS certificate (follow the interactive prompts)
sudo certbot --nginx -d kyvik.example.com
```

Certbot will modify the server block to add HTTPS listeners and redirect HTTP to HTTPS. It also installs a systemd timer that renews the certificate automatically.

## Firewall (UFW)

```bash
sudo ufw allow OpenSSH
sudo ufw allow 'Nginx Full'
sudo ufw enable
sudo ufw status
```

This allows SSH (port 22), HTTP (port 80), and HTTPS (port 443). Kyvik's port 8080 is not opened directly because Nginx proxies all public traffic.

## Configuration Reference

### YAML options (`kyvik.yaml`)

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `server.listen_addr` | string | `":8080"` | Address and port the HTTP server binds to |
| `server.data_dir` | string | `"./data"` | Base directory for runtime data |
| `auth.type` | string | `"basic"` | Authentication method (`basic`; `oauth` planned) |
| `auth.username` | string | `"admin"` | Basic auth username (prefer env var) |
| `auth.password` | string | `"changeme"` | Basic auth password (prefer env var) |
| `storage.driver` | string | `"postgres"` | Database driver (`postgres` only) |
| `storage.postgres.dsn` | string | `""` | PostgreSQL DSN |
| `storage.wal_mode` | bool | `true` | Legacy setting; ignored for postgres |
| `storage.separate_audit_db` | bool | `false` | Legacy setting; ignored for postgres |
| `storage.write_batch_ms` | int | `100` | Audit write batch window in milliseconds |
| `storage.max_connections` | int | `15` | Max database connection pool size |
| `spending.max_spend_per_day` | float | `0` | Daily spend cap in USD (0 = unlimited) |
| `spending.max_spend_per_month` | float | `0` | Monthly spend cap in USD (0 = unlimited) |
| `spending.max_tokens_per_day` | int | `0` | Daily token cap (0 = unlimited) |
| `spending.max_tokens_per_month` | int | `0` | Monthly token cap (0 = unlimited) |
| `models.openrouter.api_key` | string | `""` | OpenRouter API key (prefer env var) |
| `models.openrouter.default_model` | string | `""` | Default model ID for OpenRouter |
| `models.openrouter.provisioning_key` | string | `""` | Management key for per-agent key provisioning (prefer env var) |
| `models.openai.api_key` | string | `""` | OpenAI API key (prefer env var) |
| `models.openai.default_model` | string | `""` | Default model ID for OpenAI |
| `models.openai.base_url` | string | `""` | Override for Azure OpenAI or proxies |
| `models.anthropic.api_key` | string | `""` | Anthropic API key (prefer env var) |
| `models.anthropic.default_model` | string | `""` | Default model ID for Anthropic |
| `models.anthropic.base_url` | string | `""` | Override for proxies |
| `models.ollama.enabled` | bool | `false` | Enable local Ollama provider |
| `models.ollama.base_url` | string | `""` | Override for remote Ollama instances |
| `models.ollama.default_model` | string | `""` | Default model ID for Ollama |
| `models.ollama.embedding_model` | string | `""` | Default embedding model for Ollama |
| `channels.slack.enabled` | bool | `false` | Enable Slack adapter |
| `channels.slack.bot_token` | string | `""` | Slack bot token (prefer env var) |
| `channels.slack.app_token` | string | `""` | Slack app token (prefer env var) |
| `channels.slack.auto_provision` | bool | `false` | Create Slack channels for new agents automatically |
| `channels.webui.enabled` | bool | `false` | Enable built-in web chat |
| `queue.depth` | int | `50` | Max pending+processing messages per agent |
| `queue.full_behavior` | string | `"acknowledge"` | Behavior when queue is full: `acknowledge`, `reject`, `drop` |
| `queue.priority_users` | []string | `[]` | User IDs that get priority 1 |
| `queue.stale_timeout_seconds` | int | `300` | Stale processing messages reset to pending after this |
| `queue.retention_hours` | int | `24` | Completed messages deleted after this |
| `notifications.slack_channel` | string | `""` | Slack channel for operator alerts |
| `notifications.events.circuit_breaker` | bool | `false` | Notify on circuit breaker events |
| `notifications.events.agent_error` | bool | `false` | Notify on agent errors |
| `notifications.events.spending_threshold` | int | `0` | Notify at this % of budget (0 = disabled) |
| `notifications.events.security_alerts` | bool | `false` | Notify on security events |
| `notifications.events.key_failure` | bool | `false` | Notify on key provisioning failures |
| `notifications.events.backup_status` | bool | `false` | Notify on backup events |
| `sandbox.workspace_root` | string | `"<data_dir>/workspaces"` | Base path for agent workspaces |
| `sandbox.runner_path` | string | `""` | Path to `kyvik-sandbox` binary (auto-detected) |
| `sandbox.max_memory_mb` | int | `1024` | Max virtual memory per sandbox (MB) |
| `sandbox.max_cpu_percent` | int | `50` | Max CPU usage per sandbox |
| `sandbox.timeout_seconds` | int | `60` | Max execution time per tool call |
| `sandbox.max_output_bytes` | int | `1048576` | Max stdout/stderr capture (1 MB) |
| `logging.level` | string | `"info"` | Log level: `debug`, `info`, `warn`, `error` |
| `logging.audit.enabled` | bool | `false` | Enable audit logging to database |
| `logging.audit.retention_days` | int | `0` | Days to retain audit records (0 = forever) |

### Environment variables

Environment variables override their YAML equivalents.

| Variable | Overrides | Description |
|----------|-----------|-------------|
| `KYVIK_MASTER_KEY` | — | Base64-encoded 32-byte key for the secrets vault |
| `KYVIK_AUTH_USER` | `auth.username` | Dashboard login username |
| `KYVIK_AUTH_PASS` | `auth.password` | Dashboard login password |
| `KYVIK_OPENROUTER_API_KEY` | `models.openrouter.api_key` | OpenRouter API key |
| `KYVIK_OPENROUTER_PROVISIONING_KEY` | `models.openrouter.provisioning_key` | OpenRouter management key for per-agent provisioning |
| `KYVIK_OPENAI_API_KEY` | `models.openai.api_key` | OpenAI API key |
| `KYVIK_ANTHROPIC_API_KEY` | `models.anthropic.api_key` | Anthropic API key |
| `KYVIK_SLACK_BOT_TOKEN` | `channels.slack.bot_token` | Slack bot token |
| `KYVIK_SLACK_APP_TOKEN` | `channels.slack.app_token` | Slack app-level token |
| `KYVIK_CHAT_V2` | — | Enable chat v2 routes (`/agents/{id}/chat2` and WS endpoint) |
| `KYVIK_CHAT_V2_DEFAULT` | — | When `KYVIK_CHAT_V2` is enabled, redirect `/agents/{id}/chat` to chat v2 by default |

## Managing Agents

Open the dashboard at `https://kyvik.example.com` (or `http://localhost:8080` for local development) and log in with your configured credentials.

From the dashboard you can:

- **Create agents** — name, model, permission template (`reader`, `worker`, `admin`, `power`, `unrestricted`), and spending limits
- **Start/stop agents** — each agent runs as an isolated goroutine with its own message inbox
- **Monitor agents** — view status, recent messages, and audit trail
- **Configure permissions** — override template defaults per agent

An HTTP API for agent management is planned but not yet implemented.

## Logging and Monitoring

### Application logs

Kyvik logs to stdout, which systemd captures in the journal:

```bash
# Follow live logs
sudo journalctl -u kyvik -f

# View logs from the last hour
sudo journalctl -u kyvik --since "1 hour ago"

# View only errors
sudo journalctl -u kyvik -p err
```

### Audit log

When `logging.audit.enabled` is `true`, every agent action (tool calls, permission checks, messages) is recorded in the `audit_log` table in PostgreSQL.

```bash
# Recent audit entries
SELECT timestamp, agent_id, action, detail FROM audit_log ORDER BY timestamp DESC LIMIT 20;

# Actions by a specific agent
SELECT timestamp, action, detail FROM audit_log WHERE agent_id = 'agent-name' ORDER BY timestamp DESC;

# Spending records
SELECT * FROM usage_records ORDER BY timestamp DESC LIMIT 10;
```

## Backup and Recovery

### Online backup (while Kyvik is running)

Use `pg_dump` to take consistent PostgreSQL backups:

```bash
pg_dump "postgres://kyvik:change-me@localhost:5432/kyvik?sslmode=disable" > /var/lib/kyvik/backup-$(date +%Y%m%d).sql
```

### Restore

```bash
psql "postgres://kyvik:change-me@localhost:5432/kyvik?sslmode=disable" < /var/lib/kyvik/backup-20260101.sql
```

### Automated daily backup (cron)

```bash
sudo crontab -u kyvik -e
```

Add:

```
0 3 * * * pg_dump "postgres://kyvik:change-me@localhost:5432/kyvik?sslmode=disable" > /var/lib/kyvik/backup-$(date +\%Y\%m\%d).sql
```

## Updating

```bash
cd /tmp/kyvik-src
git pull
make install
```

This builds both binaries and restarts the service.

Database migrations are embedded in the binary and applied automatically on startup — no manual migration step is needed.

Check that the service started correctly:

```bash
sudo systemctl status kyvik
sudo journalctl -u kyvik --since "1 minute ago"
```

## Troubleshooting

**Port already in use**

```
HTTP server error: listen tcp :8080: bind: address already in use
```

Another process is using port 8080. Find it with `ss -tlnp | grep 8080` and either stop that process or change `server.listen_addr` in your config.

**Permission denied on database**

```
Failed to open database: unable to open database file
```

Check that the `kyvik` user owns the data directory and database file:

```bash
ls -la /var/lib/kyvik/
sudo chown -R kyvik:kyvik /var/lib/kyvik
```

**No model providers available**

```
Warning: No OpenRouter API key configured; no model providers available
```

Set at least one provider API key in `/etc/kyvik/env` (`KYVIK_OPENROUTER_API_KEY`, `KYVIK_OPENAI_API_KEY`, or `KYVIK_ANTHROPIC_API_KEY`) and restart:

```bash
sudo systemctl restart kyvik
```

**Slack adapter failed to initialize**

```
Warning: Slack adapter failed to initialize: ...
```

Verify that `KYVIK_SLACK_BOT_TOKEN` and `KYVIK_SLACK_APP_TOKEN` are set correctly in the environment file, and that `channels.slack.enabled` is `true` in the config.

**Nginx returns 502 Bad Gateway**

Kyvik is not running or not listening on the expected address. Verify:

```bash
sudo systemctl status kyvik
curl -s http://127.0.0.1:8080/  # Should return HTML
```

If Kyvik is running but bound to `:8080` instead of `127.0.0.1:8080`, the proxy will still work. If it is bound to a different port, update the `proxy_pass` directive in the Nginx config to match.

**Config file not found**

```
Failed to load config: open kyvik.yaml: no such file or directory
```

Either pass the path explicitly with `-config /etc/kyvik/kyvik.yaml` or ensure the working directory contains the config file. The systemd unit above uses the `-config` flag.

## License

Kyvik is licensed under the [MIT License](LICENSE).
