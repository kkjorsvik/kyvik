# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Kyvik is a security-first, multi-agent AI framework written in Go. It manages AI agent lifecycles with built-in guardrails, sandboxed execution, and a web dashboard — self-contained, with no runtime dependencies beyond PostgreSQL. The project is in **pre-alpha/design phase** with interfaces defined but implementations mostly stubbed.

## Build & Development Commands

```bash
make build          # Build binary to ./build/kyvik
make run            # Build and run
make dev            # Run with `go run` (no build artifact)
make test           # Run all tests: go test ./... -v
make lint           # Run golangci-lint
make docker-build   # Build Docker image
make docker-run     # Run in Docker with volume mount on port 8080
make clean          # Remove build directory
```

Run a single test: `go test ./internal/store/... -v -run TestName`

## Architecture

### Central Runtime

The `Kyvik` struct in `internal/core/agent.go` is the central orchestrator. It holds references to all subsystems and manages agent lifecycles. Each agent gets an `AgentRunner` goroutine with its own inbox/outbox message channels.

```
Kyvik Core (single Go process)
  → Agent Managers (goroutines) — fast in-process coordination
    → Permission Gates — deny-by-default check on every tool call
      → Sandboxes (child processes) — isolated execution of tool calls
```

### Key Interfaces (all in `internal/`)

| Package | Interface | Purpose |
|---------|-----------|---------|
| `store` | `Store` | Data persistence (PostgreSQL) |
| `models` | `Provider` | LLM provider adapters (OpenRouter, OpenAI, Anthropic, Gemini, Ollama) |
| `permissions` | `Gate` | Permission enforcement with template-based roles |
| `sandbox` | `Sandbox` | Execution isolation (process, container, or WASM) |
| `tools` | `Tool` + `Registry` | Native tool protocol with capability declarations |
| `channels` | `Adapter` | Communication channels (Slack, Discord, Web UI) |
| `audit` | `Logger` | Audit logging for every action |
| `spending` | `Tracker` | Token counting and cost tracking with layered budgets |

### Shared Types

`pkg/types/types.go` defines all shared structs: `AgentConfig`, `Message`, `ToolCall`, `ToolResult`, `AuditEntry`, and status constants. This is the public package for extensions.

### Permission Model

Four built-in templates in `configs/templates/`: `reader` (read-only), `worker` (read-write, no delete/execute), `operator` (common operations), `admin` (full access, still audited). Permissions use a capability triplet: `(tool, action, resource)`. Everything is deny-by-default.

### Web Dashboard

HTMX + Go templates in `web/`. Handlers in `web/handlers/`, templates in `web/templates/`, static assets in `web/static/`. No JavaScript framework — server-rendered with HTMX for interactivity.

### Database

PostgreSQL via `internal/store/postgres/`. Schema is defined in embedded SQL migrations (`migrations/`), automatically applied at startup with SQLite-to-PostgreSQL syntax conversion. Tables: `agents`, `permission_overrides`, `audit_log`, `usage_records`, `spending_limits`, and many more. All access goes through the `Store` interface.

## Configuration

- Main config: `configs/kyvik.example.yaml` — server, auth, storage, spending limits, model providers, channels, logging
- Secrets via environment variables: `KYVIK_OPENROUTER_API_KEY`, `KYVIK_SLACK_BOT_TOKEN`, `KYVIK_SLACK_APP_TOKEN`, `KYVIK_DISCORD_BOT_TOKEN`
- PostgreSQL is the only supported database. No CGO required.

## Design Principles

When implementing features, follow these priorities (from DESIGN.md):

1. **Security first** — deny-by-default, audit everything, sandbox all execution
2. **Accessible** — web dashboard for non-technical users, sensible defaults
3. **Multi-agent native** — each agent isolated with own identity, permissions, sandbox
4. **Go-native simplicity** — self-contained binaries, no runtime dependencies, goroutine concurrency
