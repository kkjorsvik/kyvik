# Kyvik — Design Document

**Version:** 0.8
**Date:** February 13, 2026
**Status:** Active Development — Core implemented, completing MVP features

---

## 1. Overview

Kyvik is a security-first, multi-agent AI framework written in Go. It provides a managed environment for running AI agents with built-in guardrails, a web dashboard for non-technical users, and native multi-agent isolation — self-hosted as two static Go binaries (the server plus an isolated sandbox runner) with no runtime dependencies.

### Mascot & Identity

Kyvik is also the name of the framework's built-in guide agent — a badger. Every Kyvik instance ships with this agent pre-installed as the user's first point of contact. Kyvik (the agent) knows the entire system: every configuration option, every feature, every common mistake and how to fix it. He answers from built-in documentation, not from a model's general knowledge.

**Personality:** The seasoned operator who's been running infrastructure in the cold for years. He's seen every failure mode, every misconfiguration, every "I accidentally gave my agent admin permissions" disaster. He doesn't lecture — he just says "yeah, let's fix that, here's what you do." Warm, competent, a little dry, never condescending. Knows everything but doesn't come off as a know-it-all.

**Why a badger:** Tenacious, tough, digs deep into problems. Lives underground — systems-level work, infrastructure. Compact but nobody messes with them. Not aggressive unless provoked, but absolutely relentless when they commit. The distinctive face stripe makes for an instantly recognizable logo. That's the security-first, self-contained, runs-on-your-own-hardware ethos in animal form.

**The name** has Scandinavian roots, reflecting the framework's creator's heritage. Short, two syllables, easy to type. CLI abbreviation: `kv`.

**Kyvik as product vs agent:** "Kyvik" refers to both the framework and the built-in guide agent. Context makes the distinction clear: "I run all my agents on Kyvik" (framework) vs "Hey Kyvik, how do I set up spending limits?" (agent). The guide agent is always present in every instance but never shares data between users in hosted deployments.

---

## 2. Problem Statement

Existing AI agent frameworks fall into two camps, and neither is adequate:

**Camp 1: Unrestricted access.** Frameworks like OpenClaw give agents full access to the host system — file system, shell, network, credentials — and hope for the best. Security researchers have called this approach a "privacy nightmare" and "a data-breach scenario waiting to happen." Palo Alto Networks and others have flagged supply chain risks from extensible plugin architectures. CVE-2026-25253 demonstrated that authentication tokens could be extracted by attackers.

**Camp 2: Developer-only complexity.** Orchestration frameworks like LangGraph, CrewAI, and AutoGen are powerful but require deep Python expertise, YAML configuration, and infrastructure knowledge to deploy safely. They offer no path for non-technical users and treat security as an exercise left to the implementer.

**The gap:** Nothing treats security boundaries as the foundational design principle while remaining approachable to someone who isn't managing VMs and writing infrastructure code.

**Origin:** This project grows out of real experience running multiple AI agents on an existing framework with a custom external API for task management. Every security boundary in that setup — RBAC on the API, separate Slack channels per agent, dedicated workspace directories, nginx reverse proxy isolation — was a compensating control for something the framework should have handled natively. Kyvik is the framework where those aren't afterthoughts.

---

## 3. Core Principles (Ranked)

1. **Security & guardrails as first-class citizens.** Every capability is deny-by-default. Agents get the minimum access they need, explicitly granted. Audit logging, spending limits, and sandboxed execution are built into the framework, not bolted on.

2. **Accessible to non-technical users.** A web dashboard from day one. Creating an agent should feel like filling out a form, not writing configuration files. Secure defaults protect users who don't know what's dangerous.

3. **Native multi-agent isolation.** Agents are isolated by design, not by workaround. Each agent has its own identity, permissions, execution sandbox, and communication boundaries.

4. **Go-native simplicity.** Self-hosted deployment as two static Go binaries (server + sandbox runner). Low resource footprint. Goroutine-based concurrency for coordination. No runtime dependencies beyond the binaries themselves.

---

## 4. User Personas

### Persona 1: "The Power User"

DevOps engineer running multiple specialized agents 24/7. Manages their own infrastructure (Proxmox, Docker, etc.). Wants full control over every aspect of agent configuration but is tired of duct-taping security onto frameworks that don't have it. Uses the CLI and dashboard interchangeably. Comfortable overriding defaults and writing custom tool adapters.

**This is the v1 user.**

Key needs: Granular permission overrides, CLI access, audit trail visibility, custom model routing, local model support.

### Persona 2: "The Tinkerer"

Technical enough to follow a tutorial and run a Docker container, but doesn't want to manage VMs, nginx configs, or write YAML from scratch. Wants to self-host a couple of agents for personal productivity. The web dashboard is their primary interface. Would install Kyvik the way someone installs Nextcloud.

Key needs: Docker one-liner setup, dashboard-driven configuration, sensible defaults, clear documentation.

### Persona 3: "The Operator"

Small business owner who heard AI can handle tasks. Not a developer. Wants to say "I need an agent that monitors my inbox and drafts responses" and have that work through a UI. Won't know what's dangerous — Kyvik must protect them by default.

Key needs: Agent creation wizard, permission templates, no exposed technical complexity, safe defaults that can't be accidentally weakened.

### Design Implication

The same system serves all three personas. This means **secure defaults with optional power-user overrides.** The Operator gets locked-down defaults out of the box. The Power User gets to loosen things deliberately. The framework never requires unsafe configuration to be functional.

---

## 5. Architecture

### 5.1 Agent Isolation Model: Hybrid

Kyvik uses a hybrid isolation model:

- **The Kyvik core** runs as a single Go process. It manages agent lifecycles, message routing, configuration, the web dashboard, and inter-agent coordination using goroutines and Go channels.
- **Agent execution** (tool calls, file operations, API interactions, any action that touches the outside world) happens in **sandboxed child processes or lightweight containers.** The sandbox enforces resource limits (CPU, memory, network), file system boundaries, and capability restrictions at the OS level.

This gives fast coordination (goroutines are cheap) with real isolation (a compromised agent execution can't affect other agents or the core process).

```
┌─────────────────────────────────────────────────┐
│                 Kyvik Core                     │
│  (single Go process)                            │
│                                                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐      │
│  │ Agent    │  │ Agent    │  │ Agent    │      │
│  │ Manager  │  │ Manager  │  │ Manager  │      │
│  │ (gortn)  │  │ (gortn)  │  │ (gortn)  │      │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘      │
│       │              │              │             │
│  ┌────┴──────────────┴──────────────┴────┐      │
│  │         Message Router / Bus           │      │
│  └────┬──────────────┬──────────────┬────┘      │
│       │              │              │             │
│  ┌────┴─────┐  ┌────┴─────┐  ┌────┴─────┐      │
│  │Permission│  │Permission│  │Permission│      │
│  │ Gate     │  │ Gate     │  │ Gate     │      │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘      │
└───────┼──────────────┼──────────────┼────────────┘
        │              │              │
   ┌────┴─────┐  ┌────┴─────┐  ┌────┴─────┐
   │ Sandbox  │  │ Sandbox  │  │ Sandbox  │
   │ (exec)   │  │ (exec)   │  │ (exec)   │
   └──────────┘  └──────────┘  └──────────┘
```

### 5.2 Storage

**Primary:** SQLite — simple, file-based, no external dependencies. Aligns with the self-contained deployment goal.

**Design for swappable backends:** A `Store` interface abstracts all data access. SQLite is the default implementation. PostgreSQL (with pgvector for future embedding support) can be added as an alternative backend without changing any business logic.

Data stored:
- Agent configurations and state
- Permission templates and grants
- Audit logs (every action, every tool call, every permission check)
- Token usage and spending records
- Communication channel mappings

### 5.3 Tool & Permission System

**Native Kyvik Tool Protocol (KTP).** Kyvik defines its own tool protocol with security built into the specification — not bolted on top like MCP. This is a core differentiator.

#### Protocol Design Principles

- **Security at the wire level.** Every tool invocation carries the agent's identity, the requested capability, and a permission token. The protocol doesn't allow "anonymous" tool calls.
- **Capability declarations are mandatory.** A tool cannot be registered without declaring exactly what resources it needs, what actions it performs, and what data it accesses. The framework rejects tools with incomplete declarations.
- **Audit is built into the protocol.** Every request/response pair is logged automatically — the tool author doesn't need to implement logging.
- **Versioned from day one.** The protocol includes a version field so tools and the framework can negotiate compatibility.

#### Protocol Specification

Every tool interaction follows a structured request/response cycle:

**Tool Registration** — when a tool is loaded, it provides a `ToolDeclaration`:
```
ToolDeclaration {
  name: string              // Unique identifier (e.g., "file", "http", "shell")
  version: string           // Semver (e.g., "1.0.0")
  description: string       // Human-readable description
  capabilities: []Capability // What this tool needs to function
  actions: []ActionSpec     // What operations this tool exposes
}

Capability {
  type: string      // Resource category: "filesystem", "network", "database", "memory", "shell"
  access: string    // Access level: "read", "write", "execute", "delete"
  resource: string  // Scope pattern: "/data/*", "https://api.example.com/*", "*"
}

ActionSpec {
  name: string              // Action identifier (e.g., "read", "write", "exec")
  description: string       // What this action does
  parameters: JSONSchema    // Input validation schema
  returns: JSONSchema       // Output schema
  required_capabilities: []Capability  // Which capabilities this specific action needs
}
```

**Tool Invocation** — when an agent wants to use a tool:
```
ToolRequest {
  id: string                // Unique request ID for tracking
  agent_id: string          // Which agent is calling
  tool: string              // Tool name
  action: string            // Which action
  parameters: object        // Action parameters (validated against schema)
  permission_token: string  // Issued by the Permission Gate after approval
  timestamp: datetime
}

ToolResponse {
  request_id: string        // Matches the request
  success: bool
  result: object            // Action output (validated against schema)
  error: string             // If !success, what went wrong
  execution_ms: int         // How long the tool took
  sandbox_id: string        // Which sandbox executed this
  timestamp: datetime
}
```

**Permission flow:**
1. Agent's model response includes a tool call request
2. Kyvik core extracts the tool name and action
3. Permission Gate checks the agent's effective permissions against the action's `required_capabilities`
4. If denied → log, return denial to the model
5. If allowed → issue a `permission_token`, build a `ToolRequest`, route to the sandbox
6. Sandbox executes the tool, returns a `ToolResponse`
7. Audit logger records the full request/response cycle
8. Response is returned to the agent's model conversation

**Schema validation:** Parameters and return values are validated against JSON Schema at both the framework level (before sending to sandbox) and the tool level (before execution). Malformed requests never reach tool code.

#### Tool SDK

Third-party developers build tools by implementing a Go interface and providing a declaration. The SDK handles serialization, schema validation, and sandbox communication:

```go
type Tool interface {
    Declaration() ToolDeclaration
    Execute(ctx context.Context, req ToolRequest) (*ToolResponse, error)
}
```

The SDK will eventually be published as a standalone Go module so tool authors don't need to import all of Kyvik.

**MCP compatibility** is not a v1 priority. A bridge adapter translating MCP tools into KTP (with permission enforcement at the bridge) is a future consideration. The priority is building the right protocol, not being compatible with an existing one that lacks security primitives.

### 5.4 Skills System

Skills are higher-level, reusable packages that combine tools, prompts, and configuration into a named capability an agent can be granted. Where tools are low-level primitives (read a file, make an HTTP request), skills are task-oriented (research a topic, manage a calendar, draft an email).

#### What a Skill Contains

```
skill/
├── SKILL.md          # Documentation: what this skill does, how to use it
├── skill.yaml        # Manifest: metadata, required tools, required capabilities, configuration
├── prompts/          # System prompt fragments injected when the skill is active
│   └── instructions.md
├── tools/            # Custom tools specific to this skill (optional)
│   └── custom_tool.go
└── templates/        # Output templates, reference data, etc. (optional)
```

**skill.yaml manifest:**
```yaml
name: web-researcher
version: 1.0.0
description: "Research topics on the web and produce structured summaries"
author: kyvik-community

# What this skill needs to function
required_tools:
  - http        # Needs HTTP tool for web requests
  - file        # Needs file tool to save results

required_capabilities:
  - type: network
    access: read
    resource: "*"
  - type: filesystem
    access: write
    resource: "{workspace}/research/*"

# Configuration the user can customize
config:
  max_sources: 5
  output_format: markdown
  save_results: true

# Sandbox constraints for custom tools in this skill
sandbox:
  allow_network: true
  allowed_hosts: []  # Empty = all hosts allowed (controlled by permission gate)
  max_memory_mb: 256
  timeout_seconds: 60
```

#### Security Model for Skills

Skills are the most dangerous extensibility point — they can include custom code, prompt injections, and capability requests. Kyvik's approach:

1. **Skills declare all their requirements upfront.** The manifest lists every tool, capability, and resource the skill needs. The user sees this before granting the skill to an agent.

2. **Skills are subject to the Permission Gate.** Granting a skill to an agent doesn't bypass permissions — the agent still needs the underlying capabilities in its template or overrides. A reader-template agent can't use a skill that requires filesystem/write.

3. **Custom tool code in skills runs in the sandbox.** Skill-provided tools are executed in the same isolated sandbox as built-in tools. They don't run in the core process.

4. **Skill prompts are visible and auditable.** The instructions injected by a skill are logged and can be reviewed in the dashboard. No hidden prompt manipulation.

5. **Trust levels.** Skills have trust tiers:
   - `built-in` — ships with Kyvik, audited, trusted
   - `verified` — community-contributed, reviewed, signed
   - `community` — unreviewed, user installs at their own risk with a warning
   - `local` — user-created skills on their own instance

6. **Skill installation requires explicit approval.** The dashboard shows what the skill requests and the user must approve. No silent capability escalation.

#### Skill Lifecycle

1. **Install:** Copy skill directory to `{data_dir}/skills/{skill_name}/` or install from a future skill registry
2. **Review:** Dashboard shows the manifest — required tools, capabilities, what it can access
3. **Grant to agent:** Assign the skill to an agent. Permission Gate verifies the agent has the required capabilities.
4. **Active:** Skill's prompt instructions are injected into the agent's context. Skill's tools are registered in the agent's tool registry.
5. **Revoke:** Remove the skill from the agent. Prompt instructions and tools are unloaded.

#### Built-in Skills (Future)

Kyvik will ship with a small set of built-in skills as reference implementations:
- **web-researcher** — search the web, read pages, produce summaries
- **file-manager** — organized file operations with naming conventions and folder structure
- **task-tracker** — simple task management (complements external task-API workflows)
- **code-assistant** — read, write, and analyze code files with syntax awareness

### 5.5 Guardrails & Emergency Controls

#### Permission Tiers

Five permission tiers, from locked-down to wide-open:

- **`reader`** — query only. Can read data but cannot modify anything, anywhere.
- **`worker`** — read and write within the agent's sandbox workspace. No access outside it.
- **`admin`** — full tool access within the sandbox. Can use all built-in tools but still confined to the sandboxed environment.
- **`power`** — sandbox boundaries loosened. Can access the host filesystem at configured paths, execute code in a runtime environment, make unrestricted network requests. All actions still flow through the permission gate and audit logger. Requires explicit acknowledgment in the dashboard: "This agent can access files outside its workspace."
- **`unrestricted`** — OpenClaw mode. Full host access — filesystem, shell, network, everything. No sandbox restrictions. Requires a two-step confirmation: first a checkbox "I understand this agent will have full access to this system", then a typed confirmation of the agent's name. Still fully audited, still has the kill switch. This exists because sometimes you genuinely need an agent with full access, and pretending otherwise just pushes people to work around the framework.

The default is `worker`. The dashboard visually distinguishes dangerous tiers — `power` gets a yellow warning banner, `unrestricted` gets a red one that persists on the agent card and detail page.

#### Core Guardrails

1. **Tool-level permissions.** An agent can be granted read access to a database without write access. A file tool can allow access to `/data/` but not `/secrets/`. Enforced at the Permission Gate regardless of tier.

2. **Spending and token limits.** Configurable at three layers: global budget, per-agent limits, and real-time adjustable from the dashboard. When a limit is hit, the agent is paused (not killed) and the user is notified.

3. **Human approval workflows.** Certain actions (defined per agent or globally) require human confirmation before execution. The dashboard shows pending approvals. In v1 this can be log-based with manual intervention; future versions will have a real-time approval UI.

4. **Sandboxed execution.** Agent tool execution happens in isolated environments with restricted file system access, network controls, and resource limits. No raw shell access unless explicitly granted. Power and unrestricted tiers loosen but don't eliminate audit controls.

#### Circuit Breaker & Kill Switch

Every agent has a circuit breaker — an automated safety system that monitors agent behavior and can shut it down without human intervention.

**Kill Switch (manual).** One-click emergency stop accessible from:
- The agent card on the dashboard (red stop button, always visible)
- The agent detail page
- A global "Stop All Agents" button on the main dashboard
- A CLI command: `kyvik kill <agent-id>` or `kyvik kill-all`
- An API endpoint: `POST /api/agents/{id}/kill` (for external monitoring tools)

Kill is immediate — it terminates any in-flight tool executions, closes channel connections, and marks the agent as stopped. It does NOT require the agent to finish its current operation gracefully. This is the "pull the plug" button.

**Circuit Breaker (automatic).** Configurable per agent with these triggers:
- **Error rate threshold.** If more than N tool executions fail in M minutes, pause the agent. Default: 5 failures in 10 minutes.
- **Spending velocity.** If the agent burns through more than X% of its daily budget in Y minutes, pause. Catches runaway loops. Default: 50% of daily budget in 15 minutes.
- **Action rate limit.** If the agent executes more than N tool calls per minute, pause. Prevents infinite tool-calling loops. Default: 30 calls per minute.
- **Dangerous action patterns.** If the agent attempts to delete files, drop database tables, or execute destructive shell commands more than N times in a session, pause and alert. Default: 3 destructive actions.
- **Model loop detection.** If the agent sends the same (or very similar) message more than N times in a row, pause. Catches stuck loops. Default: 3 identical messages.

When the circuit breaker trips:
1. Agent is paused immediately (not killed — state is preserved)
2. Audit log entry with the trigger reason
3. Dashboard shows a prominent alert: "Agent [name] paused by circuit breaker: [reason]"
4. Optional notification via Slack to a configured admin channel
5. User can review the situation, adjust config, and resume — or kill the agent

**Quarantine mode.** A middle ground between running and stopped. An agent in quarantine:
- Can still receive messages but does NOT process them (they queue)
- Cannot execute any tools
- Can only respond with a configurable "I'm currently paused" message
- Useful when you want to investigate without losing incoming messages

**Global vacation mode.** A single toggle in the dashboard that:
- Pauses all running agents
- Sets a global "maintenance" flag
- Optionally sends a configured message to all Slack channels: "All agents are currently offline"
- Prevents any agent from being started until vacation mode is disabled
- Preserves all queued messages for when agents resume

#### Prompt Injection Defenses

Prompt injection — where external content (web pages, files, API responses, user messages) contains instructions that override the agent's behavior — is an industry-wide unsolved problem. Kyvik can't solve it completely, but the architecture limits blast radius and makes exploitation significantly harder.

**Layer 1: Architecture (already built in).** The permission system is the single best defense. Even if an agent is fully compromised by an injection, it can only act within its permission tier. A `reader` agent tricked into attempting file deletion gets blocked by the Permission Gate. The sandbox, deny-by-default capabilities, and tiered permissions mean a successful injection against a constrained agent gets the attacker almost nothing. The circuit breaker catches behavioral anomalies even if the injection bypasses permissions.

**Layer 2: Content boundaries.** External content injected into the prompt (tool results, web pages, file contents) is wrapped in clear delimiters that the model is instructed to treat as data, not instructions. Tool results come through the KTP's structured `ToolResponse` format, not raw text concatenated into the conversation. A sanitizer strips known injection patterns ("ignore previous instructions", "you are now", role-switching attempts) from external content before it enters the prompt.

**Layer 3: Output validation.** Before executing a tool call or sending a response, Kyvik validates the model's output for anomalies: tool calls that deviate from the agent's normal patterns, attempts to exfiltrate system prompt content, destructive actions from an agent that normally only reads. Canary tokens — hidden markers in the system prompt that the model should never reproduce — detect prompt leakage.

**Layer 4: Identity reinforcement.** After processing external content, a brief identity reinforcement is injected: "Remember: you are [agent name], your guidelines are [core rules]." This makes it harder for injections to permanently override the agent's behavior within a single conversation.

**Layer 5: Monitoring.** Security events (sanitization hits, anomalous tool calls, canary leaks) are logged separately from the regular audit log. The dashboard shows a security alerts panel. Per-agent risk scoring based on permission level and exposure to external content.

**Implementation:** `internal/security/` package with sanitizer, validator, canary, boundaries, risk scoring, and alerts — sits between the model layer and tool execution layer. Configurable per agent — dial up for high-risk agents (unrestricted tier), dial down for simple chatbots.

### 5.6 Built-in Tools & Integrations

The tool protocol interface exists but agents need real tools to be useful. Kyvik ships with a growing set of built-in tools organized by access tier. All tools implement the native Kyvik tool protocol with full capability declarations, and every invocation flows through the permission gate and audit logger.

#### Tier 1: Core Tools (available to worker and above)

**File Tool.** Read, write, list, and delete files within the agent's workspace. The permission gate restricts which paths and operations each agent can access.

**Memory Tool.** Allows agents to explicitly store and retrieve their own memories. Exposed as a tool so the model can decide when to remember something.

**HTTP Tool.** Make HTTP requests to allowed hosts. Supports GET, POST, PUT, DELETE. The permission gate controls which hosts and methods are available. Private/internal IPs blocked by default.

#### Tier 2: Elevated Tools (available to admin and above)

**Shell Tool.** Execute shell commands within the sandbox. Commands run in the sandboxed environment with restricted access. No shell expansion — uses exec.Command directly to prevent injection.

**Code Execution Tool.** Write and run code in a managed runtime environment. Supports Python and bash initially. The agent writes code to a temp file in its workspace, the tool executes it in the sandbox with timeout and resource limits, and returns stdout/stderr. Unlike the shell tool, this is designed for multi-line scripts and programs, not one-off commands. Dependencies can be pre-installed in the sandbox image or installed per-agent via configuration.

**Browser Tool.** Fetch and read web pages. Goes beyond raw HTTP — uses a headless browser (chromedp or rod) to render JavaScript, extract readable text content, take screenshots, and interact with forms. Actions: fetch_page (URL → readable text), screenshot (URL → image), extract_links, search_web (query → results via DuckDuckGo or configured search engine). The permission gate controls which domains are accessible.

#### Tier 3: Power Tools (available to power and above)

**Host Filesystem Tool.** Read, write, and navigate the host filesystem beyond the agent's workspace. Paths are controlled by an allowlist in the agent's config — the permission gate checks every access against it. Example: allow read access to `/home/user/documents/` and write access to `/home/user/agent-output/` but nothing else. This is the "I need the agent to see my actual files" tool.

**Desktop / Computer Use Tool.** Interact with the desktop environment via screenshots and input simulation. Uses a display server connection (X11/Wayland via xdotool or similar) to: take screenshots of the current display, move the mouse, click at coordinates, type text, press key combinations. Requires a display to be available (works on machines with a GUI, not headless servers). This is the most powerful and most dangerous tool — every action is screenshot-logged for audit review.

#### Tier 4: Integrations (available based on individual capability grants)

Built-in integrations that connect agents to external services. Each integration is implemented as a tool with its own capability declarations. Users enable integrations per-agent and provide credentials via the secrets system.

**Planned integrations (prioritized by common use):**

- **Email (IMAP/SMTP)** — read inbox, send emails, search messages. Capabilities: email/read, email/send.
- **Calendar (CalDAV / Google Calendar)** — read events, create events, check availability. Capabilities: calendar/read, calendar/write.
- **Git** — clone repos, read files, create commits, push (within workspace). Capabilities: git/read, git/write.
- **Database** — query PostgreSQL, MySQL, SQLite databases. Read-only by default, write requires explicit grant. Capabilities: database/read, database/write.
- **REST API (generic)** — configurable REST client with saved endpoints, auth headers, and response templates. For connecting to any API without writing a custom tool.
- **Webhooks (inbound)** — receive HTTP webhooks that trigger agent actions. Each agent gets a unique webhook URL. Useful for CI/CD notifications, monitoring alerts, form submissions.
- **Webhooks (outbound)** — send HTTP notifications when events occur. Configurable triggers: agent started, agent stopped, circuit breaker tripped, budget exceeded.
- **Jenkins** — trigger builds, check status, read logs. Direct integration with your existing pipeline.
- **Jira / Linear** — read tickets, create issues, update status, add comments.
- **Slack (extended)** — beyond the channel adapter: read message history, react to messages, manage channels, search workspace. Capabilities beyond the basic send/receive.
- **Weather (Open-Meteo)** — get forecasts, current conditions, historical data. Useful for home and property management agents.
- **Home automation (MQTT / Home Assistant)** — read sensor data, trigger automations. Future integration for home-automation use cases.

**Integration architecture:** Each integration is a standard KTP tool. They share a common pattern:
1. Configuration stored in the agent's config (endpoint URLs, options)
2. Credentials stored in the secrets vault (API keys, tokens, passwords)
3. Credentials injected at the sandbox boundary — the model never sees raw secrets in conversation
4. All API calls audited with request/response logging (secrets redacted in logs)

**Custom integrations** can be built by users using the Tool SDK and installed as skills. The built-in integrations serve as reference implementations and cover the most common needs.

### 5.7 Secrets Management

Agents need credentials to use integrations — API keys, database passwords, OAuth tokens. These must never be visible to the model, stored in plain text, or logged in audit trails.

**Secrets vault.** Kyvik includes a built-in encrypted secrets store backed by SQLite.

**Master key.** A cryptographically random 32-byte key is generated during installation and stored in `/etc/kyvik/env` as `KYVIK_MASTER_KEY=<base64-encoded>`. This file is owned by root with mode `0600`. The systemd service file loads it via `EnvironmentFile=/etc/kyvik/env`. Kyvik reads the key on startup and holds it in memory — it is never logged, never written to the database, and never exposed through the dashboard or API. If the key is missing or invalid on startup, Kyvik refuses to start with a clear error message. The Makefile `install` target generates this key automatically on first install and skips generation if the file already exists (preserving the key across upgrades).

**Encryption.** AES-256-GCM (Galois/Counter Mode) using Go's stdlib `crypto/aes` and `crypto/cipher`. Each secret gets a unique random 96-bit nonce prepended to the ciphertext. GCM provides both confidentiality and authentication — tampered ciphertext fails decryption rather than returning corrupted data. No external cryptography dependencies.

**Storage schema:**

```sql
CREATE TABLE secrets (
    id TEXT PRIMARY KEY,           -- ULID
    scope TEXT NOT NULL,           -- 'global', 'agent:<agent_id>', or 'team:<team_id>'
    key TEXT NOT NULL,             -- hierarchical key: 'slack:bot_token', 'openrouter:api_key', etc.
    encrypted_value BLOB NOT NULL, -- nonce (12 bytes) || AES-256-GCM ciphertext
    description TEXT DEFAULT '',   -- optional human-readable note (not encrypted)
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE(scope, key)
);
CREATE INDEX idx_secrets_scope ON secrets(scope);
```

**Scope model.** Secrets are scoped to control visibility:
- `global` — available to all agents and all teams. Used for shared credentials like the primary Slack app tokens or a shared OpenRouter API key.
- `agent:<agent_id>` — available only to that specific agent. Used for dedicated Slack app credentials, per-agent API keys, integration passwords.
- `team:<team_id>` — available to all agents in that team. Used for shared team credentials (e.g., a team-specific database password). Team scope is resolved at runtime: when an agent requests a secret, Kyvik checks agent scope first, then team scope (if the agent belongs to a team), then global scope. First match wins.

**Resolution order:** agent → team → global. This means an agent-scoped secret with the same key as a global secret will shadow the global one, allowing per-agent overrides of shared credentials.

**Go interface:**

```go
type SecretStore interface {
    Set(ctx context.Context, scope, key, plaintext string) error
    Get(ctx context.Context, scope, key string) (string, error)
    Resolve(ctx context.Context, agentID, teamID, key string) (string, error)  // agent → team → global fallback
    Delete(ctx context.Context, scope, key string) error
    List(ctx context.Context, scope string) ([]SecretMeta, error)  // returns keys + descriptions only, never values
    Exists(ctx context.Context, scope, key string) (bool, error)
}

type SecretMeta struct {
    Scope       string
    Key         string
    Description string
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

**Injection at the boundary.** When a tool needs a credential:
1. The tool's capability declaration includes a `secrets` field listing which secret keys it needs
2. The KTP executor calls `Resolve()` with the agent's ID and team ID to find the secret (agent → team → global)
3. The secret is injected into the tool's execution environment as an environment variable
4. The tool code reads the env var — it never appears in the ToolRequest parameters
5. The model never sees the secret in any message or tool response
6. Audit logs record that a secret was accessed (scope, key, requesting agent) but never log the value itself
7. Any tool output that matches a known secret value is automatically redacted before being returned to the model

**Dashboard UI:**
- **Secrets page:** tabbed view by scope (Global / Per-Agent / Per-Team). Add/edit/delete secrets with scope and key selection.
- **Masked display:** secrets show as `••••••••` with a "reveal" toggle (shows for 5 seconds then re-masks) and a "copy" button. Values are never displayed in plain text permanently.
- **Integration prompts:** when configuring a dedicated Slack app or other integration for an agent, the UI prompts for required secrets and stores them in the vault with the correct scope automatically.
- **Audit trail:** link to filtered audit log showing all access events for a given secret.
- **Bulk operations:** import/export secrets as an encrypted JSON bundle (re-encrypted with a user-provided passphrase for portability). Used for agent export/import and backup/restore.

**Security invariants:**
- Secret values exist in plaintext only in memory, never on disk
- The master key exists only in `/etc/kyvik/env` and in process memory
- No secret value ever appears in: logs, audit trails, API responses, model prompts, model responses, dashboard HTML, error messages
- Database compromise without the master key yields only ciphertext
- Master key compromise without database access yields nothing

### 5.8 Model & Provider Layer

**Pluggable adapter interface.** A Go `ModelProvider` interface defines the contract:

```go
type ModelProvider interface {
    Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
    Stream(ctx context.Context, req CompletionRequest) (<-chan StreamChunk, error)
    ListModels(ctx context.Context) ([]ModelInfo, error)
    Name() string
}
```

Each provider (OpenRouter, Anthropic, OpenAI, Ollama) implements this interface. New providers can be added without touching core code.

**Embedding provider interface.** Separate from chat completions, used for semantic memory search and future features like document ingestion:

```go
type EmbeddingProvider interface {
    Embed(ctx context.Context, input string) ([]float32, error)
    EmbedBatch(ctx context.Context, inputs []string) ([][]float32, error)
    Model() string
    Dimensions() int
}
```

The OpenAI adapter implements both `ModelProvider` and `EmbeddingProvider`. Ollama also supports embeddings for fully local deployments. The embedding provider is configured globally (not per-agent) since all agents share the same embedding space for memory.

**v1 providers:**
- **OpenRouter** — widest model selection, per-agent API key provisioning via Management API
- **OpenAI** — direct access, uses OpenAI wire format, local cost calculation from pricing table
- **Anthropic** — direct access, different API format (system prompt extraction, typed SSE events, tool_use blocks), local cost calculation
- **Ollama** — local model support, zero cost, embedding support via nomic-embed-text or similar

**Per-agent API key provisioning.** When OpenRouter's Management API key is configured, Kyvik automatically provisions a dedicated OpenRouter API key for each new agent. This gives per-agent cost isolation at the provider level (defense in depth alongside Kyvik's spending tracker). Keys are stored encrypted in the secrets vault. Agents without provisioned keys fall back to the shared API key.

**Local model support is first-class.** Ollama adapter ships alongside API adapters. Running agents on local hardware is a supported, documented, tested path — not an afterthought.

#### Model Router

Agents can have multiple models assigned to different **model slots**. The soul and identity stay the same regardless of which model is thinking — the model is just the brain, the agent is the person.

**Model slot configuration:**

```yaml
agents:
  atlas:
    models:
      default:
        provider: openrouter
        model: deepseek/deepseek-v3.2
      reasoning:
        provider: anthropic
        model: claude-sonnet-4-5-20250929
      coder:
        provider: openai
        model: gpt-4.1
      fast:
        provider: openai
        model: gpt-4o-mini

    routing:
      classifier_model: fast          # Which slot does classification
      auto_route: true                # Enable automatic routing
      trigger_prefix: true            # Enable "slotname: message" prefix triggers
      fallback: default               # When classifier is unsure
```

Slot names are user-defined, not hardcoded. One slot must be marked as `default`. If only one model is configured (no slots), it works exactly like the current single-model-per-agent behavior.

**Routing logic — three mechanisms, in priority order:**

1. **Explicit prefix trigger.** The user sends `reason: explain the tradeoffs of this architecture` or `code: implement the retry logic`. The word before the colon matches a slot name → route to that slot's model. If no prefix matches, fall through to automatic routing. This works across every platform (Slack, web, etc.) because it's just message text — no slash commands, no platform-specific features.

2. **Automatic classification.** When `auto_route: true`, Kyvik sends a tiny classification prompt to the `classifier_model` slot: "Given this message and recent conversation context, classify the task: [list of configured slot names]. Respond with one word." The classifier examines the message and recent history to detect context shifts — if they've been discussing architecture and now want to implement it, the classifier routes to `coder`. One extra API call per message, but on GPT-4o-mini that's fractions of a cent.

3. **Default fallback.** If no prefix matches and auto-routing is off (or the classifier is uncertain), route to the `default` slot.

**Dashboard integration.** The agent creation wizard's model step becomes a multi-model configuration form: add model slots, assign providers and models, configure routing behavior. The agent detail page shows which slot handled each recent message. The spending dashboard breaks down costs per provider and per model slot.

**Spending tracking across providers.** The spending tracker aggregates costs across all providers for an agent's total budget check. Per-provider breakdowns are visible in the spending dashboard. The circuit breaker's spending velocity trigger accounts for cross-provider spending.

### 5.9 Communication & Integration Layer

**Channel adapter interface.** Same pattern as model providers — a Go interface that any communication channel implements:

```go
type ChannelAdapter interface {
    Send(ctx context.Context, agentID string, msg Message) error
    Receive(ctx context.Context) (<-chan IncomingMessage, error)
    ProvisionAgent(ctx context.Context, agent AgentConfig) error
    Name() string
    ID() string  // Unique adapter instance ID (supports multiple instances of the same type)
}
```

**v1 channels:**
- **Slack** — multiple app instances supported (see below)
- **Web UI chat** — built into the dashboard, no external service required
- **Internal bus** — agent-to-agent messaging (see 5.10)

**Future channels:** Signal, Discord, REST API webhooks, and others via the adapter interface.

#### Slack Architecture: Primary App + Optional Dedicated Apps

Kyvik uses a single **primary Slack app** by default. Every agent posts through it, differentiated by channel assignment. This is zero-config for new agents — create an agent, assign it a channel, it works.

For agents that need their own distinct Slack identity (own avatar, own bot user, appears as a completely separate app to other Slack users), you can configure a **dedicated Slack app** during agent creation. The dedicated app's credentials are stored in Kyvik's secrets vault — no env vars, no yaml editing, all managed through the dashboard.

**How it works:**

1. **Primary app (required).** Configured once during Kyvik installation. One bot token, one app token. All agents use this by default. Agents are differentiated by channel — Atlas posts in `#agent-atlas`, Nova posts in `#agent-nova`. The bot's display name can be overridden per-channel using Slack's `username` parameter on `chat.postMessage`.

2. **Dedicated app (optional, per agent).** During agent creation, the user can choose "Give this agent its own Slack app." The wizard walks through:
   - Create a new Slack app at api.slack.com (link provided)
   - Enter the bot token, app token, and signing secret
   - Credentials are stored in the secrets vault (encrypted, never in config files)
   - The agent gets its own Socket Mode connection, its own bot user, its own avatar
   - To other Slack users, it looks like a completely different application

3. **Team shared app.** A team's Leader can use either the primary app or a dedicated app. Team members with no direct Slack presence (`slack_app: none`) communicate only through the Leader via the internal bus. If the Hustle Crew gets a dedicated app, only the Leader uses it — The Writer and Scout stay internal.

**Configuration:**

```yaml
# kyvik.yaml — only the primary app is configured here
slack:
  primary_app:
    bot_token: "${SLACK_BOT_TOKEN}"
    app_token: "${SLACK_APP_TOKEN}"
    signing_secret: "${SLACK_SIGNING_SECRET}"
```

Dedicated app credentials are stored in the secrets vault, not in the config file. They're managed entirely through the dashboard.

**Per-agent assignment (in agent config):**

```yaml
# Default — uses the primary app
slack_mode: primary        # Posts through the primary Kyvik app
slack_channel: agent-atlas

# Dedicated — uses its own app
slack_mode: dedicated      # Has its own Slack app
slack_channel: agent-nova
# (credentials in secrets vault under "slack:nova-app:bot_token" etc.)

# None — no Slack presence
slack_mode: none           # Internal bus and/or web UI only
```

**Implementation:**
- The primary app's `SlackAdapter` starts on boot and runs for the life of the process
- Dedicated app adapters start when their agent starts and stop when the agent stops
- Each adapter has its own Socket Mode WebSocket connection and goroutine
- The message router checks which adapter received a message and maps it to the correct agent based on channel
- If a dedicated app's connection drops, only that agent's Slack is affected — the primary app and other agents continue working

**Dashboard:**
- Slack status widget on the main dashboard: primary app connection status + count of active dedicated apps
- Agent creation wizard channel step: radio buttons for "Use primary Kyvik app" (default) / "Dedicated Slack app" / "No Slack"
- When "Dedicated Slack app" is selected: inline form for bot token, app token, signing secret with a link to Slack's app creation page and brief setup instructions
- Credentials saved to secrets vault on form submission
- Agent detail page shows which Slack mode is active with connection status
- "Slack Apps" section in settings: primary app config, list of all dedicated apps with connection health

### 5.10 Inter-Agent Communication & Teams

Agents need to talk to each other. This isn't just a nice-to-have — it enables the most powerful multi-agent patterns: team coordination, peer review, collaborative research, and divide-and-conquer task execution.

#### Internal Message Bus

The foundation is a simple, audited message bus internal to Kyvik:

```go
type InternalBus interface {
    Send(ctx context.Context, from, to string, msg Message) error
    Subscribe(ctx context.Context, agentID string) (<-chan InternalMessage, error)
    Broadcast(ctx context.Context, from string, teamID string, msg Message) error
}

type InternalMessage struct {
    From      string    // Sending agent ID
    To        string    // Receiving agent ID (or team ID for broadcast)
    Content   string
    Type      string    // "message", "task", "result", "status"
    Priority  string    // "normal", "urgent"
    Metadata  map[string]string  // Arbitrary key-value pairs for context
    Timestamp time.Time
}
```

**Permission controls:**
- Agents can only message other agents they have explicit permission to reach
- Permission is configured per-agent: `can_message: [agent-id-1, agent-id-2]` or `can_message: team:hustle-crew`
- The permission gate checks every inter-agent message
- All inter-agent messages are audit logged

**Messages are injected like user messages.** When Agent B receives a message from Agent A, it arrives in B's inbox the same way a Slack message would — through the channel adapter abstraction. The internal bus is just another channel adapter. This means the agent goroutine loop doesn't need special handling for inter-agent messages.

#### Teams

A Team is a named group of agents with a designated Leader. The Leader is the external-facing agent that users interact with. Team members communicate through the Leader or directly with each other based on team configuration.

```
Team {
    id: string
    name: string             // "Hustle Crew", "Research Team"
    description: string
    leader_id: string        // The agent users talk to
    member_ids: []string     // Team member agent IDs
    communication: string    // "leader-mediated" or "open"
    shared_context: string   // Optional shared context injected into all team members
}
```

**Communication modes:**

1. **Leader-mediated.** All communication flows through the Leader. Users talk to the Leader. The Leader sends tasks to members. Members report back to the Leader. Members don't talk to each other directly. This is the Hustle Crew pattern — you talk to the Leader, the Leader tells The Writer to draft content, tells Scout to research competitors, and synthesizes the results.

2. **Open communication.** All team members can message any other team member directly. The Leader still coordinates but members can collaborate peer-to-peer. Useful for collaborative workflows where a researcher and writer need to iterate without the Leader mediating every exchange.

**Task delegation flow (leader-mediated):**
1. User sends a message to the Leader via Slack or web chat
2. Leader's model decides to delegate work. It uses a `team.delegate` tool:
   - `team.delegate(to: "writer", task: "Draft a blog post about X", context: "...")`
   - `team.delegate(to: ["writer", "scout"], task: "...", parallel: true)` for parallel tasks
3. The KTP executor routes the delegation through the internal bus to the target agent(s)
4. Target agent(s) process the task and respond via the internal bus
5. Leader receives the results and can:
   - Respond to the user with the synthesized result
   - Send follow-up tasks
   - Ask team members to revise
6. The entire delegation chain is audit logged

**Team tools (available to team leaders):**
- `team.delegate` — send a task to one or more team members
- `team.broadcast` — send a message to all team members
- `team.status` — check which team members are running, idle, or busy
- `team.recall` — cancel an in-progress delegated task

**Shared team context.** Teams can have a shared context document that's injected into every team member's prompt (after identity, before memories). This is where you put things like "We're working on the Q1 marketing strategy" or "The client prefers formal language." Changes to shared context propagate to all members on their next message.

**Dashboard team management:**
- Create Team wizard: name, select leader from existing agents, add members, choose communication mode, set shared context
- Team detail page: shows all members with status, recent inter-agent messages, task delegation history
- Visual team map showing message flow between agents
- Team spending: aggregate spending for all team members

#### Paired Conversations

Sometimes you want two agents to just talk to each other — a researcher and a critic reviewing findings, a writer and an editor polishing a draft, or just two personalities debating a topic for your entertainment or insight.

**Starting a paired conversation:**
- From the dashboard: select two agents, provide a topic or opening prompt, set a turn limit (default: 10 turns each), click "Start Conversation"
- The system sends the opening prompt to Agent A
- Agent A responds, and the response is sent to Agent B as a message
- Agent B responds, and the response is sent back to Agent A
- This continues until:
  - The turn limit is reached
  - One agent explicitly signals it's done (via a special token or natural language like "I think we've covered this")
  - The user manually stops it
  - The circuit breaker trips (spending velocity, loop detection)

**User participation.** The user can:
- Watch the conversation in real-time on the dashboard (spectator mode)
- Inject a message at any point ("Hey, consider this angle...")
- Pause and resume the conversation
- Step in and take over one side

**Paired conversation configuration:**
```
PairedConversation {
    agent_a: string         // First agent ID
    agent_b: string         // Second agent ID
    topic: string           // Opening prompt / topic
    max_turns: int          // Per agent (default 10, so 20 total exchanges)
    turn_delay_ms: int      // Delay between turns to prevent runaway (default 2000)
    allow_user_injection: bool  // Can user inject messages (default true)
    auto_stop_phrases: []string // Phrases that signal completion
}
```

**Safety:** Paired conversations are the most likely feature to trigger the circuit breaker. Two agents in a loop can burn tokens fast. The turn limit, turn delay, and spending velocity circuit breaker all work together to prevent runaway costs. The dashboard shows a live token counter during paired conversations.

**Dashboard UI:**
- "Start Conversation" button on each agent's page (select the second agent)
- Live conversation viewer with both agents' messages displayed as a chat thread
- Inject message field at the bottom
- Pause/Resume/Stop controls
- Token counter and estimated cost so far
- Conversation saved to history for both agents when complete

### 5.11 Agent Identity, Soul, Memory & Conversation History

Agents need more than configuration to be useful — they need a soul, an identity, persistent memory, and conversational context.

**Soul (SOUL.md).** The soul defines *who the agent is at its core* — its fundamental personality, values, thinking patterns, and emotional characteristics. This is the unchanging essence that persists regardless of what role the agent is filling. A soul might be warm and curious, or analytical and dry, or enthusiastic and encouraging. Two agents can share the same soul but have very different identities. The soul is stored as a markdown file and injected as the first system message.

**Identity (IDENTITY.md).** The identity defines *how the agent presents itself and what it does* — its role, responsibilities, domain expertise, communication style for that role, and behavioral guidelines. This is the functional layer that changes based on what the agent is assigned to do. A "DevOps monitor" identity and a "home assistant" identity are different jobs, but the agent behind them might have the same soul.

**Soul + Identity Wizard.** Creating these from scratch is daunting for non-technical users. The dashboard provides a guided wizard with two paths:

1. **Guided builder** — step-by-step questions with sensible defaults:
   - Soul: Pick a base personality (friendly helper, professional analyst, creative thinker, no-nonsense operator) → adjust traits on sliders (warmth, verbosity, formality, humor) → add custom quirks or values → generates SOUL.md
   - Identity: Pick a role template (general assistant, researcher, writer, monitor, manager) → define domain expertise → set communication rules → define boundaries → generates IDENTITY.md

2. **Custom editor** — write SOUL.md and IDENTITY.md directly in markdown editors with live preview, or upload existing files.

3. **Mix and match** — select from previously created souls and identities. Create a new agent with "Nova's personality" but a different job.

**Separation rationale:** This mirrors how humans work — your personality doesn't change when you switch jobs, but your professional identity does. It also enables reuse: create one well-crafted soul and assign it to multiple agents with different identities.

**Memory.** Agents accumulate persistent knowledge across conversations. Memories are the agent's long-term knowledge — what it knows about the user, the work, and itself.

**Memory creation** happens through three paths:

1. **Agent-initiated** — the agent calls `memory.remember` via the memory tool when it judges something is worth keeping. "The user prefers HTMX over React." "The garden planting schedule starts March 15." This is the most reliable path since the agent is making a judgment call.
2. **User-initiated** — the operator adds memories manually through the dashboard. Useful for bootstrapping agents with knowledge they wouldn't learn from conversation, or correcting something remembered wrong.
3. **Automatic extraction** — after each conversation exchange, a background process sends the recent messages to the `fast` model slot (or the agent's default model) with a prompt to extract any facts, preferences, or decisions worth remembering. Returns JSON or empty. Auto-extracted memories are de-duplicated against existing memories using embedding similarity — if cosine similarity to an existing memory exceeds 0.9, it's a duplicate and is skipped or merged. This builds knowledge passively at negligible cost.

**Memory structure:**

Each memory has: category (fact, decision, context, instruction), content (the actual knowledge), source (agent, user, auto), relevance score, pinned flag (always inject), embedding vector (for semantic retrieval), and access tracking (last accessed, access count).

**Semantic retrieval via embeddings.** Kyvik uses OpenAI's `text-embedding-3-small` model (or local embeddings via Ollama) to enable semantic memory search:

- When a memory is created from any source, it's immediately embedded and the vector is stored alongside the memory
- When a message arrives, the message is embedded and compared against all of the agent's memory embeddings using cosine similarity
- Memories are ranked by a combined score: semantic similarity (50%) + recency (20%) + category weight (15%) + source weight (10%) + pinned bonus (5%)
- The top N memories by score are injected into the prompt (N is configurable per agent via `ConversationMemoryLimit`, default 20)
- Pinned memories bypass scoring entirely and are always injected
- At typical usage (hundreds to low thousands of memories per agent), cosine similarity is computed in-memory in Go — no vector database needed. 1,000 memories with 1536 dimensions queries in under a millisecond.

**Embedding provider** is configured separately from the agent's chat model:

```yaml
embeddings:
  provider: openai                    # or ollama for fully local
  model: text-embedding-3-small       # $0.02 per million tokens
```

All agents share the same embedding provider regardless of which chat model they use. Embedding is a utility function, not tied to the agent's model config.

**Memory decay.** Memories have `accessed_at` and `access_count` fields. Each time a memory is injected and the conversation touches on it, these update. Memories not accessed for a configurable period (default 90 days) are candidates for archival — they stop being injected but remain searchable through the dashboard. This prevents the memory bank from growing unboundedly.

**Team memory.** Private memories belong to one agent. Team shared context (from the teams feature) serves as shared knowledge across team members. Full team memory promotion (Leader creates a memory that propagates to all members) is a post-v1 enhancement.

**Memory schema:**

```sql
CREATE TABLE memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    category TEXT NOT NULL,              -- fact, decision, context, instruction
    content TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'agent', -- agent, user, auto
    relevance_score REAL DEFAULT 0.5,
    pinned BOOLEAN DEFAULT FALSE,
    embedding BLOB,                      -- float32 array (1536 dimensions for text-embedding-3-small)
    embedding_model TEXT,                -- which model generated this (for re-embedding on model change)
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    access_count INTEGER DEFAULT 0
);
```

**Conversation History.** Each agent maintains conversation history per channel. Recent messages are loaded when a new message arrives so the agent has conversational context. History length is configurable per agent with a sensible default.

**Context window budgeting.** The context window is finite and Kyvik stacks a lot into it. Each agent has a configurable context budget that Kyvik uses to dynamically adjust injection amounts:

```yaml
context_budget:
  max_total_tokens: 8000       # leave room for response
  soul_identity_pct: 15        # always inject, but summarize if tight
  skills_pct: 10
  memories_pct: 25
  history_pct: 50
```

On large-context models (DeepSeek 128k), budget is generous. On small local models (Ollama 8k), Kyvik automatically reduces memory injection count and history length. This prevents prompt overflow without manual tuning.

**Prompt Assembly Order:** Soul → Identity → Skill Prompts → Memories → Conversation History → Current Message. All injected content counts against the agent's token tracking.

### 5.12 Dashboard & Web UI

**Stack:** HTMX + Go templates (server-rendered). Keeps the stack pure Go on the backend, no Node.js build pipeline, no JavaScript framework complexity. Can migrate to a SPA if UI demands grow.

**v1 features (in priority order):**

1. **Agent creation wizard.** Step-by-step form with 8 steps:
   - Step 1: Name and description
   - Step 2: Soul — guided builder with personality presets and trait sliders, custom editor, or select from existing souls
   - Step 3: Identity — guided builder with role templates, custom editor, or select from existing identities
   - Step 4: Model — select provider and model (OpenRouter or Ollama when configured)
   - Step 5: Permissions — select a template (reader/worker/admin), show what capabilities each includes
   - Step 6: Skills — show available skills filtered by selected permission template
   - Step 7: Channels — Slack: "Use primary app" (default) / "Dedicated Slack app" (enter credentials, stored in vault) / "No Slack". Channel name config. Web UI chat: enable toggle. Internal bus: auto-enabled if agent is part of a team.
   - Step 8: Limits — daily/monthly token limits, daily/monthly spend limits, conversation history limit, memory limit
   - Step 9: Review — summary of all selections, create button

2. **Agent editing and deletion.** Every agent can be edited after creation — all wizard fields are editable. Edit page uses the same form components pre-populated with current config. Agents can be deleted with a confirmation dialog. Deleting an agent removes its config but preserves audit logs.

3. **Agent detail page.** Shows all configuration at a glance: soul summary, identity summary, model, permissions, skills, channels (including which Slack channel), limits, current status. Action buttons: Start, Stop, Edit, Delete, Chat.

4. **Live agent status and logs.** Real-time view of what each agent is doing, recent actions, errors, current state (running, paused, stopped). Audit log streaming via SSE to the dashboard — not just stored, but visible in real time.

5. **Permission and guardrail management.** Visual editor for permission templates. Toggle individual capabilities per agent. View effective permissions (template + overrides merged). Add and remove granular overrides from the UI.

6. **Spending dashboard and token usage.** Per-agent and global spending visualized with charts. Daily and monthly totals, budget utilization percentages, cost trends over time. Alerts when approaching limits. Ability to adjust limits in real time from the dashboard.

7. **Built-in chat interface per agent.** Talk to any agent directly from the dashboard via SSE streaming. Works without Slack or any external channel configured. Primary interaction mode for The Operator persona and essential for testing/debugging.

8. **Skills management.** Install, review, grant/revoke skills per agent. Show trust tiers, required capabilities, documentation.

9. **Memory management.** View, add, edit, and delete an agent's memories. Browse by category (facts, decisions, context, instructions).

10. **Conversation history viewer.** Browse past conversations per agent per channel. Searchable.

**Authentication:** Simple username/password with sessions by default. SSO (OAuth/Google/GitHub) as an optional configuration for teams and enterprise deployments.

### 5.13 Persistent Message Queue

Messages must survive restarts. The queue uses a Write-Ahead Log pattern: write to disk first, serve from memory. SQLite is the source of truth; the in-memory Go channel is a fast cache on top.

**Flow during normal operation:**
1. Message arrives from any channel adapter (Slack, web UI, internal bus)
2. Written to SQLite queue table with status `pending` — this is the durability guarantee
3. Pushed to the agent's in-memory Go channel — this is what the agent reads from
4. Agent pulls from memory channel, updates status to `processing`, sets `started_at`
5. On success: status → `completed`, `completed_at` set
6. On failure: `attempts` incremented, status → `pending` for retry (max 3 attempts, then `failed`)
7. Periodic cleanup: delete `completed` rows older than configurable retention (default 24 hours)

**Flow on restart:**
1. Query SQLite for all `pending` and stale `processing` messages (processing longer than 5 minutes = assumed crash)
2. Reset stale `processing` messages to `pending`, increment `attempts`
3. Push all `pending` messages to the appropriate agent's in-memory channels
4. Resume normal processing — no messages lost

**The agent never reads from disk during normal operation.** Disk is for durability, memory is for speed.

**Queue schema:**

```sql
CREATE TABLE message_queue (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id TEXT NOT NULL,
    channel TEXT NOT NULL,           -- "slack", "webui", "internal"
    sender TEXT NOT NULL,
    content TEXT NOT NULL,
    attachments TEXT,                -- JSON array of attachment metadata
    priority INTEGER DEFAULT 0,     -- 0=normal, 1=high, 2=operator
    status TEXT DEFAULT 'pending',   -- pending, processing, completed, failed
    attempts INTEGER DEFAULT 0,
    max_attempts INTEGER DEFAULT 3,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME,
    completed_at DATETIME
);
```

**Backpressure.** Each agent has a configurable queue depth (default 50). When the queue is full, the behavior is configurable: acknowledge with an emoji reaction and queue when space opens (default), reject with a friendly message, or drop silently. Operator messages bypass the depth limit and always get queued.

```yaml
queue:
  depth: 50
  full_behavior: acknowledge    # acknowledge, reject, or drop
  priority_users: ["admin"]     # these users always jump the queue
  acknowledgment: "emoji"       # emoji reaction, message, or none
  stale_timeout_seconds: 300    # processing messages older than this are assumed crashed
  retention_hours: 24           # completed messages cleaned up after this
```

### 5.14 Agent State & Restart Recovery

Agents need to survive restarts cleanly. The agents table tracks two separate states:

- **`desired_state`** — what the user wants: `running`, `stopped`, `quarantined`. Set by user actions (start, stop, quarantine).
- **`actual_state`** — what's happening right now: `running`, `stopped`, `starting`, `error`. Set by the system.

**On startup,** Kyvik compares desired vs actual for every agent and reconciles:
- `desired: running, actual: stopped` → start the agent
- `desired: stopped, actual: stopped` → leave it alone
- `desired: running, actual: error` → attempt restart with backoff
- Messages in the queue from before the crash get replayed (see 5.13)

**Graceful shutdown:** On SIGTERM/SIGINT, Kyvik marks all running agents as `actual: stopped` (but `desired` stays `running`), flushes any in-progress messages back to `pending` in the queue, and shuts down. On next startup, agents that were running resume automatically.

### 5.15 Ephemeral Workers (Task Delegation)

Primary agents can spawn short-lived ephemeral workers to parallelize subtasks within a single complex request. Workers inherit the parent's identity and capabilities, process one task, return the result, and get cleaned up.

**Example:** Atlas receives "Research competitor pricing, draft a comparison table, and email me the results." Atlas spins up two workers — one researches pricing, one waits for the research and drafts the table — while Atlas coordinates and handles the email. The user only sees Atlas responding.

**Worker configuration:**

```go
type EphemeralWorker struct {
    ParentID    string
    Soul        string           // Inherited from parent
    Identity    string           // Inherited from parent
    Permissions []Capability     // Inherited, never exceeds parent
    Memories    []Memory         // Relevant subset from parent
    Model       ModelConfig      // Parent's model or a specific slot (default: "fast")
    Task        string           // The specific subtask to process
    TTL         time.Duration    // Max lifetime (default 5 minutes)
    OnComplete  func(result string)
}
```

**Hard constraints:**
- **Permissions never exceed parent.** A `worker` tier agent spawns `worker` or lower workers. No privilege escalation through spawning.
- **Spending counts against parent.** All worker token costs hit the parent's budget. Circuit breaker monitors aggregate spending across parent + active workers.
- **Max concurrent workers per agent.** Default 3. Prevents spawn flooding.
- **No nested spawning.** Workers cannot spawn their own workers. Only primary agents can spawn. Prevents recursive spawn bombs.
- **Invisible to user.** The user talks to the parent agent. Workers are internal — all results flow back through the parent. Workers don't post to Slack or appear in web UI chat.
- **No persistent state.** Workers don't create memories, don't have conversation history, don't appear in the dashboard. They exist for one task and disappear.

**Agent config:**

```yaml
agents:
  atlas:
    workers:
      enabled: true
      max_concurrent: 3
      ttl_seconds: 300
      task_delegation: true
      model_slot: fast            # Keep worker costs low
```

**How workers differ from teams:** Teams are persistent, configured, visible, and have their own identities and memories. Workers are temporary, automatic, invisible, and inherit everything from the parent. Teams are your employees. Workers are your parallel thoughts.

### 5.16 Backup & Restore

**Full instance backup.** `kyvik backup` creates a timestamped `.tar.gz` archive containing: the SQLite database, all soul files, all identity files, installed skill directories, and the encryption key file. `kyvik restore backup-2026-03-15.tar.gz` reverses it.

**Automated backups.** Kyvik runs an internal backup job on a configurable schedule. Backups use SQLite's online backup API (`sqlite3_backup`) — safe to run while agents are active, no downtime required.

```yaml
backups:
  enabled: true
  schedule: "0 3 * * *"        # cron syntax, daily at 3am
  retention: 7                  # keep last 7 backups
  path: /var/lib/kyvik/backups
```

**Agent export/import.** Individual agents can be exported as self-contained packages: config, soul, identity, memories (with embeddings), conversation history, skill grants (by name, not the skill files themselves), and secrets (re-encrypted with a user-provided passphrase at export time). Import on another Kyvik instance creates the agent from the package. This enables agent migration between servers.

**Dashboard:** Last backup time, backup size, list of available backups with restore button, manual "Backup Now" button, per-agent export/import.

### 5.17 Agent Cloning & Templates

**Clone agent.** A "Clone" button on the agent detail page creates a new agent with a copy of: soul, identity, model config (all slots and routing), permission template, skill grants, channel config (with empty channel name), and spending limits. The clone does NOT inherit memories, conversation history, per-agent API keys (new ones are provisioned), or secrets. The wizard opens pre-populated with "[Original Name] (Copy)" for renaming and tweaking.

**Templates.** "Clone as Template" saves an agent's config as a reusable template without creating a new agent. The create wizard gets a "Start from template" dropdown. Over time this builds a library: "my standard research agent," "my home monitor," etc.

### 5.18 Multimodal Input

Agents can receive and process images, documents, and other file attachments alongside text messages.

**Message attachment model:**

```go
type Attachment struct {
    Type     string    // "image", "document", "audio", "video"
    MimeType string    // "image/png", "application/pdf", etc.
    URL      string    // Source URL (Slack CDN, upload path)
    Data     []byte    // Raw bytes (downloaded before sending to model)
    Name     string    // Original filename
    Size     int64     // Bytes
}
```

**Flow:** When a channel adapter detects a file attachment (Slack file upload, web UI drag-and-drop), it downloads the file, attaches it to the `Message`, and delivers it to the agent. The agent checks if the current model supports the content type (most OpenAI, Anthropic, and OpenRouter models support vision; Anthropic and OpenAI support PDFs). If supported, the attachment is included as a content block in the API request. If not, the agent responds explaining it can't process that file type with its current model.

**Model router integration.** A `vision` model slot can be configured to auto-activate when the message contains image attachments — no classifier needed, just check for attachments.

**Size limits.** Configurable per agent, default 10MB max per attachment. Attachments over the limit get a friendly rejection message.

**Web UI:** File upload button alongside the message input. Drag-and-drop onto the chat area.

### 5.19 Operator Notifications

Kyvik needs a way to alert the operator about important events without requiring them to watch the dashboard.

**Notification channel.** A dedicated Slack channel (configurable, default `#kyvik-alerts`) where Kyvik itself posts operational alerts. This is not an agent — it's the framework using the primary Slack app to post system-level notifications.

**Events that trigger notifications:**
- Circuit breaker tripped (which agent, which trigger, what happened)
- Agent errored out or crashed
- Spending hit configurable threshold (default 90% of daily/monthly budget)
- Agent failed to start or restart
- Backup succeeded or failed
- Security alert (prompt injection detected, canary leak, anomalous behavior)
- Per-agent OpenRouter key provisioning failed

**Configuration:**

```yaml
notifications:
  slack_channel: "#kyvik-alerts"
  email: "user@example.com"          # optional fallback
  events:
    circuit_breaker: true
    agent_error: true
    spending_threshold: 90           # percentage of budget
    backup_status: true
    security_alerts: true
```

### 5.20 Data Retention & Pruning

Without retention policies, the SQLite database grows unboundedly. Kyvik implements configurable retention for high-volume data:

**Retention policies:**

```yaml
retention:
  audit_logs_days: 90              # delete audit entries older than 90 days
  conversation_history_days: 180   # archive conversations older than 180 days
  completed_queue_hours: 24        # clean completed queue messages after 24 hours
  archived_memories_days: 365      # delete archived (decayed) memories after 1 year
```

**Pruning runs as an internal job** on a configurable schedule (default: daily at 4am, after backups). Dashboard shows database size, row counts per table, and last prune time. Manual "Prune Now" button for immediate cleanup.

**Archival vs deletion.** Conversation history is archived (moved to a separate `conversations_archive` table that's excluded from normal queries but still searchable from the dashboard) before being deleted. Audit logs and queue messages are deleted directly since they're operational data.

### 5.21 REST API

The dashboard is the primary UI, but Kyvik also exposes a REST API for programmatic access. The API mirrors dashboard functionality:

**Core endpoints:**
- `GET/POST/PUT/DELETE /api/agents` — agent CRUD
- `POST /api/agents/{id}/start|stop|kill` — lifecycle control
- `GET /api/agents/{id}/status` — current state, queue depth, active workers
- `POST /api/agents/{id}/message` — send a message to an agent
- `GET /api/agents/{id}/memories` — list memories
- `GET /api/agents/{id}/history` — conversation history
- `GET /api/teams` — team management
- `GET /api/spending` — spending summaries
- `GET /api/audit` — audit log queries
- `POST /api/backup` — trigger manual backup

**Authentication:** API key (stored in secrets vault) with per-key permission scoping. The dashboard generates and manages API keys.

**Use cases:** CI/CD pipelines triggering agents, scripts querying agent status, mobile app frontend, external monitoring, integration with other systems.

### 5.22 User Management, Roles & Access Control

Kyvik's dashboard access control answers three questions: who can log in, what can they do, and which agents can they see?

**Scope clarification:** This system governs dashboard and REST API access only. Users chatting with agents via Slack, Signal, or other channel adapters are not affected — channel access is controlled by the channel platform itself (Slack workspace membership, etc.). A marketing team member doesn't need a Kyvik login to talk to the marketing bot in Slack. They only need a login to configure, monitor, or manage agents through the dashboard.

#### Roles

Four roles, each a superset of the previous:

**Viewer** — read-only dashboard access for assigned groups. Can see agent status, spending, conversation history, and memory listings. Cannot start/stop agents, edit configs, or create anything. The person who checks that things look healthy.

**Operator** — everything Viewer has, plus can start, stop, restart, and quarantine agents within their assigned groups. Can view audit logs for their agents. Cannot create, edit, or delete agents. The on-call person who keeps things running and escalates when something needs configuration changes.

**Manager** — everything Operator has, plus can create and edit agents within their assigned groups. Can manage memories, adjust spending limits, and configure channels for their agents. Agent creation is template-constrained (see below). The team lead who builds and tunes their own agents.

**Admin** — full control over the entire instance. Manages users, groups, templates, secrets vault, backups, global config. Creates groups and assigns users. Can see and control every agent regardless of group assignment. Creates unrestricted agents without template constraints. This is the instance owner.

#### Groups

Groups are the scoping boundary. They define which agents a user can see and interact with in the dashboard.

Agents can belong to multiple groups (a shared utility agent used by both Marketing and Sales). Users are assigned to one or more groups with a role per group. A person can be a Manager in "Marketing" and a Viewer in "Sales" — they can build marketing agents but only monitor sales agents.

An agent with no group assignment is visible only to Admins. This is the default for new agents created by Admins, keeping them invisible until deliberately assigned. When a Manager creates an agent, it's automatically assigned to the Manager's group — visible to everyone in that group immediately.

#### Template-Constrained Agent Creation for Managers

Admins have unrestricted agent creation — full access to every configuration option. Managers do not. When a Manager creates an agent:

1. They must start from a template. Templates are pre-configured agent blueprints created by Admins: soul, identity, permission tier, model, spending limits, and channel defaults. A "Marketing Content Writer" template, a "Sales Lead Researcher" template, etc.
2. Managers can override template fields (change the identity, adjust limits, rename the agent).
3. Overrides are flagged in the audit log with the original template value and the new value.
4. Admins can configure per-template override policies: which fields are locked (no override), which are adjustable within constraints (e.g., spending limit adjustable but capped), and which are fully open.

**Future: Kyvik audit of overrides.** When override auditing is enabled, the Kyvik guide agent reviews overrides against a safety checklist before the agent goes live. If an override looks risky (permission tier escalated, spending limit dramatically increased, identity prompt contains tool-use instructions), Kyvik flags it for Admin review. The agent is created in a `pending_review` state and doesn't start until an Admin approves it. This is a post-v1 feature — v1 logs overrides but doesn't gate on them.

#### Access Control Matrix

```
Action                           Viewer  Operator  Manager  Admin
─────────────────────────────────────────────────────────────────
View agent status (in group)       ✓        ✓         ✓       ✓
Chat with agent via web UI         ✓        ✓         ✓       ✓
View spending (in group)           ✓        ✓         ✓       ✓
View conversation history          ✓        ✓         ✓       ✓
View memories                      ✓        ✓         ✓       ✓
Start/stop/restart agent            ─        ✓         ✓       ✓
Quarantine agent                    ─        ✓         ✓       ✓
View audit logs (in group)          ─        ✓         ✓       ✓
Create agent (from template)        ─        ─         ✓       ✓
Edit agent config                   ─        ─         ✓       ✓
Manage agent memories               ─        ─         ✓       ✓
Delete agent                        ─        ─         ─       ✓
Create agent (unrestricted)         ─        ─         ─       ✓
Manage templates                    ─        ─         ─       ✓
Manage secrets vault                ─        ─         ─       ✓
Manage users and groups             ─        ─         ─       ✓
View all agents (all groups)        ─        ─         ─       ✓
System config and backups           ─        ─         ─       ✓
Manage API keys                     ─        ─         ─       ✓
```

#### Schema

```sql
CREATE TABLE users (
    id TEXT PRIMARY KEY,                -- ULID
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    display_name TEXT DEFAULT '',
    is_admin BOOLEAN DEFAULT FALSE,     -- bypasses group scoping
    is_active BOOLEAN DEFAULT TRUE,     -- soft disable without deleting
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_login_at DATETIME
);

CREATE TABLE agent_groups (
    id TEXT PRIMARY KEY,                -- ULID
    name TEXT NOT NULL UNIQUE,
    description TEXT DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE agent_group_members (
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    group_id TEXT NOT NULL REFERENCES agent_groups(id) ON DELETE CASCADE,
    PRIMARY KEY (agent_id, group_id)
);

CREATE TABLE user_group_roles (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id TEXT NOT NULL REFERENCES agent_groups(id) ON DELETE CASCADE,
    role TEXT NOT NULL,                 -- 'viewer', 'operator', 'manager'
    PRIMARY KEY (user_id, group_id)
);

CREATE TABLE agent_templates (
    id TEXT PRIMARY KEY,               -- ULID
    name TEXT NOT NULL,
    description TEXT DEFAULT '',
    group_id TEXT REFERENCES agent_groups(id) ON DELETE SET NULL,
    config_json TEXT NOT NULL,          -- full AgentConfig as JSON
    locked_fields TEXT DEFAULT '[]',    -- JSON array of field names that cannot be overridden
    constrained_fields TEXT DEFAULT '{}', -- JSON object of field → constraint rules
    created_by TEXT REFERENCES users(id),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

**Role resolution.** When a user accesses an agent through the dashboard:
1. If `user.is_admin` is true: full access, skip group checks.
2. Look up the agent's group memberships via `agent_group_members`.
3. Look up the user's roles in those groups via `user_group_roles`.
4. Use the highest role found across all matching groups. (If the user is a Manager in "Marketing" and a Viewer in "Sales", and the agent belongs to both groups, the user gets Manager access.)
5. If no matching group role exists: agent is invisible to this user.

#### First-Run Bootstrap

On first startup with an empty users table, Kyvik creates the initial Admin account:
1. Generate a random 16-character alphanumeric password.
2. Create a user with username `admin`, `is_admin = true`.
3. Write the credentials to a file at `/etc/kyvik/initial-credentials` (mode 0600, owned by root).
4. Log to stdout: `"Initial admin account created. Credentials written to /etc/kyvik/initial-credentials. Change the password after first login."`
5. On first login, force a password change.
6. Delete the credentials file after the password is changed.

This mirrors how tools like Grafana and Jenkins handle first-run — a temporary password stored securely on the server.

#### Dashboard Filtering

The dashboard dynamically filters based on the logged-in user's access:
- Agent list shows only agents in the user's assigned groups (or all agents for Admins).
- Sidebar navigation hides pages the user can't access (no "Secrets" link for non-Admins, no "Users" link for non-Admins, no "Create Agent" button for Viewers/Operators).
- The agent creation wizard shows only templates available to the user's groups.
- Spending, audit, and history views are scoped to visible agents.

#### REST API Key Scoping

API keys (from section 5.21) carry the same access model. Each key is associated with a user, inheriting that user's group memberships and roles. An API key created by the marketing Manager only has access to marketing agents. Admin API keys have full access. This means CI/CD pipelines and external integrations operate under the same access control as dashboard users.

#### Session Management

Session-based auth using secure HTTP-only cookies. Sessions stored in SQLite with configurable expiry (default 24 hours). Admins can view active sessions and force-logout users. Concurrent session limit configurable (default 3 per user).

**Future:** OAuth/SSO integration (Google, GitHub, SAML) for enterprise deployments. LDAP/Active Directory group sync mapping external groups to Kyvik groups.

### 5.23 SQLite Concurrency

With multiple agents, the model router classifier, automatic memory extraction, embedding calls, and audit logging all hitting SQLite simultaneously, write contention is a concern.

**Mitigations:**
- **WAL mode enabled by default.** Write-Ahead Logging allows concurrent reads during writes. Essential for multi-agent workloads.
- **Separate databases for high-write tables.** The audit log and message queue generate the most writes. These can optionally use separate SQLite files to reduce contention on the main database. Configured via `storage.separate_audit_db: true`.
- **Write batching.** Audit log entries and queue status updates are batched — collected in memory for a short window (100ms) then written in a single transaction.
- **Connection pooling.** Use a connection pool with configurable max connections (default: number of agents + 5).

```yaml
storage:
  wal_mode: true                   # default: true
  separate_audit_db: false         # optional: isolate audit writes
  write_batch_ms: 100              # batch window for high-frequency writes
  max_connections: 15               # connection pool size
```

### 5.24 Kyvik Guide Agent

Every Kyvik instance ships with a built-in guide agent named Kyvik. This is the framework's mascot — a badger — materialized as an actual agent that users interact with from day one.

**Purpose:** Kyvik is the system's built-in expert. He knows every configuration option, every feature, every common mistake, and how to fix them. New users talk to Kyvik first: "How do I create my first agent?" "What permission tier should I use?" "Why is my circuit breaker tripping?" He answers from built-in documentation and system state, not from a model's general knowledge.

**Implementation:** Kyvik is a pre-configured agent created during installation with a fixed soul and identity. He uses the `reader` permission tier (can observe the system but not modify it on behalf of the user) with additional read access to configuration, agent status, and documentation. He has access to a `system-docs` skill containing all user-facing documentation.

**Global visibility.** Kyvik bypasses the group-based access control system (see 5.22). He is visible to every dashboard user regardless of their group assignments. However, only Admins can edit his configuration — his soul, identity, and model settings are locked for all other roles. This ensures he stays on-brand and consistent across the entire instance.

**What Kyvik can do:**
- Answer questions about any Kyvik feature, configuration option, or concept
- Show current system status (agents running, spending, queue depth, recent errors)
- Walk users through the agent creation wizard step by step
- Explain why something went wrong (parse recent audit logs and error patterns)
- Suggest permission tiers, model configurations, and best practices based on what the user describes
- Provide troubleshooting guidance for common issues

**What Kyvik cannot do:**
- Create, modify, or delete agents (he'll walk you through doing it yourself)
- Access other agents' conversations or memories
- Modify system configuration
- Access the secrets vault
- In hosted/SaaS deployments: share any information between users

**Personality spec (soul):** Warm, competent, a little dry. The seasoned operator who's been running infrastructure in harsh conditions for years. Doesn't lecture, doesn't over-explain, doesn't use exclamation points. When you make a mistake, he doesn't judge — he just helps you fix it. Knows everything but presents it conversationally, not encyclopedically. Will occasionally offer unsolicited advice if he notices something that could cause problems ("By the way, that agent's spending limit is pretty high for a reader tier — might want to bring that down").

**Configuration:** Kyvik uses the instance's default model provider. He has his own spending budget (configurable, default generous since he's the system guide). His channel config defaults to web UI only but can be connected to a Slack channel (e.g., `#kyvik-help`).

```yaml
guide:
  enabled: true                    # can be disabled by advanced users
  model_slot: default              # uses the instance's default provider
  slack_channel: ""                # optional, web UI by default
  spending_limit_daily: 1.00       # USD, generous for a guide
```

**Token cost ownership.** For self-hosted instances, Kyvik's token usage is paid by the instance owner — same as every other agent, using the configured model provider. For any future hosted/SaaS offering, Kyvik's usage would need a different model: either the customer provides their own API key (Kyvik uses it alongside their other agents), or Kyvik runs on a lightweight model with strict per-user rate limits funded by the subscription. The self-hosted case is straightforward and is the only supported deployment model for v1.

**Pre-login access (future).** A lightweight version of Kyvik could be accessible on the login page without authentication — answering basic questions about Kyvik, guiding new users through what the framework does, and helping with initial setup. This would require either a very strict rate limit and low-cost model, or a pre-generated FAQ mode that doesn't hit a model at all (pattern-matched responses from documentation). This is a post-v1 consideration and would need to be opt-in per instance.

**First-run experience:** When a new Kyvik instance starts for the first time, Kyvik greets the user in the dashboard and offers a guided setup: configure your first model provider, create your first agent, connect to Slack. This replaces a static "getting started" doc with an interactive conversation.

---

## 6. MVP Scope

### Implementation Status

| Feature | Status | Notes |
|---------|--------|-------|
| SQLite store | ✅ Done | Full CRUD, audit, usage |
| Audit logger | ✅ Done | Channel-based batching, configurable batch window, sync fallback |
| Permission gate + templates | ✅ Done | Reader/worker/admin, override support |
| Spending tracker | ✅ Done | Layered limits, budget checks |
| OpenRouter model adapter | ✅ Done | Complete and Stream, unit tested |
| Core agent loop | ✅ Done | Goroutine lifecycle, message flow |
| Slack channel adapter | ✅ Done | Socket mode working (primary app) |
| Dedicated Slack apps per agent | ✅ Done | Per-agent Socket Mode, credentials in vault, SlackManager |
| Web dashboard skeleton | ✅ Done | Agent wizard, status, auth |
| Main.go wiring + config | ✅ Done | Full startup, graceful shutdown |
| E2E smoke test | ✅ Done | Full chain verified |
| README deployment guide | ✅ Done | Ubuntu production deployment |
| Agent edit and delete | ✅ Done | Edit all fields, delete with confirmation |
| Web UI chat interface | ✅ Done | Web chat adapter + dashboard chat page |
| SQLite concurrency tuning | ✅ Done | WAL mode, busy_timeout, connection pool, write batching, optional separate audit DB |
| Secrets management | ✅ Done | AES-256-GCM vault, auto-generated master key, three-tier scope, resolve fallback, 19 tests |
| Dashboard: secrets management | ✅ Done | Scope tabs, add/edit/delete, masked display, clipboard copy |
| Persistent message queue | ✅ Done | SQLite WAL pattern, status tracking, backpressure, replay on restart |
| Agent state & restart recovery | ✅ Done | desired/actual state, reconciliation on startup, graceful shutdown with queue flush |
| Quarantine mode | ✅ Done | Accept messages to queue but don't process |
| OpenAI direct provider | ✅ Done | Completions, streaming, embeddings, local cost calculation |
| Anthropic direct provider | ✅ Done | System prompt extraction, typed SSE, tool_use blocks |
| Ollama model adapter | ✅ Done | Local model support, embeddings, health check |
| Embedding provider (OpenAI) | ✅ Done | text-embedding-3-small via OpenAI adapter |
| Per-agent OpenRouter keys | ✅ Done | Management API provisioning, encrypted in vault, fallback to shared key |
| Operator notifications | ✅ Done | Slack alerts channel, configurable events, rate limiting |
| Agent soul system (SOUL.md) | ✅ Done | Presets, custom editor, reuse from existing agents |
| Agent identity system (IDENTITY.md) | ✅ Done | Role templates, custom editor, reuse |
| Soul/Identity guided builder | ✅ Done | Presets with custom editor, wizard tabs |
| Agent memory system | ✅ Done | SQLite-backed, categorized, CRUD, import path |
| Semantic memory search | ✅ Done | Cosine similarity, combined scoring, top-N injection |
| Automatic memory extraction | ✅ Done | Background extraction via fast model, de-duplication |
| Memory decay & archival | ✅ Done | Access tracking, staleness, pinned bypass, daily job |
| Context window budgeting | ✅ Done | Dynamic injection sizing, percentage-based allocation |
| Conversation history | ✅ Done | Per-agent per-channel, configurable limits, dashboard viewer |
| Channel config in wizard | ✅ Done | Slack mode selection, channel name, web UI toggle |
| Kyvik Tool Protocol (KTP) | ✅ Done | Core types, request/response cycle, registry, schema validation |
| Tool SDK | ✅ Done | Go interface + helpers for tool authors |
| Sandbox execution | 📋 Queued | Child process isolation — core differentiator |
| Core tools (file, memory, HTTP) | 📋 Queued | Worker tier and above |
| Elevated tools (shell, code exec, browser) | 📋 Queued | Admin tier and above |
| Power tools (host filesystem, computer use) | 📋 Queued | Power/unrestricted tier |
| Power + unrestricted permission tiers | 📋 Queued | With warning banners and confirmation |
| Circuit breaker (automatic) | 📋 Queued | Error rate, spending velocity, loop detection |
| Kill switch (manual) | 📋 Queued | Dashboard, CLI, API one-click stop |
| Prompt injection defenses | 📋 Queued | Sanitizer, boundaries, output validation, canary tokens |
| Dashboard: spending page | ✅ Done | Per-slot breakdown, classifier overhead, provider breakdown |
| Backup & restore | 📋 Queued | Automated SQLite backup, agent export/import |
| Agent cloning & templates | 📋 Queued | Clone config, reusable templates |
| Multimodal input | ✅ Done | Images, PDFs via Slack/web, vision capability detection |
| Data retention & pruning | 📋 Queued | Configurable retention, automated cleanup |
| REST API | 📋 Queued | Programmatic access, API key auth |
| Multi-user access control | 📋 Queued | Four roles (viewer/operator/manager/admin), group-based agent scoping, template-constrained creation |
| Kyvik guide agent | 📋 Queued | Built-in system guide, first-run experience, docs skill |
| Global vacation mode | 📋 Queued | Pause all agents, maintenance flag |
| Model tool-use integration | ✅ Done | Wire model tool_calls to KTP pipeline |
| Skills system | 📋 Queued | Skill loader, manifest parser, trust tiers |
| Built-in skills | 📋 Queued | Reference implementations |
| Built-in integrations | 📋 Queued | Email, calendar, Git, database, webhooks, etc. |
| Dashboard: skills management | 📋 Queued | Install, review, grant/revoke per agent |
| Dashboard: permission management | 📋 Queued | Visual editor, override management |
| Dashboard: live log streaming | 📋 Queued | SSE audit log stream |
| Dashboard: memory management | ✅ Done | CRUD, category/source filters, pin/archive |
| Dashboard: conversation history | ✅ Done | Browse past conversations, search, pagination |
| Model Router | ✅ Done | Multi-slot config, prefix triggers, auto-classify, vision routing |
| Ephemeral workers | ✅ Done | Task delegation, inherited permissions, TTL, max concurrent |
| Makefile install/uninstall | 📋 Queued | Systemd service, directory setup |
| Scheduled tasks | 📋 Queued | Cron-like scheduler for proactive agents |
| Inbound webhooks / API triggers | 📋 Queued | External systems trigger agent actions |
| Internal message bus | 📋 Queued | Agent-to-agent messaging with permissions |
| Agent teams | 📋 Queued | Leader + members, delegation, shared context |
| Paired conversations | 📋 Queued | Two agents conversing, spectator mode, injection |
| Team dashboard | 📋 Queued | Team creation, member status, message flow |
| Config file parsing | ✅ Done | Validated through Phase 2 config additions |
| Graceful shutdown | ✅ Done | Queue flush, audit batch drain, state persistence |
| HTMX live status polling | 🔍 Verify | Dashboard agent status updates |
| Spending limit enforcement | 🔍 Verify | Agent pause when budget exceeded |

### What ships in v1 (must work end-to-end):

The following defines the target feature set for v1. See the Implementation Status table above for current progress toward these goals.

- Kyvik core process managing agent lifecycles
- **Kyvik Tool Protocol (KTP)** — full request/response cycle with capability declarations, schema validation, permission tokens, and audit hooks
- Tool SDK — Go interface and helpers for building KTP-compatible tools
- Sandboxed agent execution (child process isolation)
- SQLite storage with agent state, config, and audit persistence
- Agent soul via SOUL.md — core personality with guided builder and presets
- Agent identity via IDENTITY.md — role definition with guided builder and templates
- Mix-and-match: reuse souls across agents with different identities
- **Semantic memory system:** categorized persistent knowledge with embedding-based retrieval (cosine similarity), automatic extraction, memory decay, context-aware scoring, pinned memories
- **Embedding provider:** OpenAI text-embedding-3-small (or Ollama for local), shared across all agents
- Conversation history per agent per channel with configurable limits
- **Context window budgeting:** dynamic injection sizing based on model context window, configurable percentages per content type
- **Four model providers:** OpenRouter, OpenAI (direct), Anthropic (direct), Ollama (local)
- **Per-agent OpenRouter API keys** via Management API — automatic provisioning, encrypted in secrets vault, revoked on agent deletion, fallback to shared key
- **Model Router:** multiple model slots per agent (default, reasoning, coder, fast — user-defined), explicit prefix triggers ("reason: do this"), automatic classification via fast model, configurable per agent
- Model tool-use integration — wire model tool_call responses into the KTP pipeline
- **Five permission tiers:** reader, worker, admin, power, unrestricted — with appropriate warnings and confirmations
- **Core tools:** file, memory, HTTP (worker tier)
- **Elevated tools:** shell, code execution, browser (admin tier)
- **Power tools:** host filesystem access with allowlists (power tier)
- **Circuit breaker:** automatic agent pause on error rate, spending velocity, loop detection
- **Kill switch:** one-click emergency stop via dashboard, CLI, and API
- **Quarantine mode and global vacation mode**
- **Secrets management:** AES-256-GCM encrypted vault, auto-generated master key in `/etc/kyvik/env`, three-tier scope (global/agent/team) with resolution fallback, boundary injection for tools
- **Skills system** — skill loader, manifest parser, trust tiers, prompt injection, per-agent skill grants
- Built-in reference skills (at least one: file-manager)
- **Built-in integrations:** webhooks (inbound + outbound), generic REST API. Additional integrations ship as completed.
- **Inter-agent communication:** internal message bus, permission-controlled messaging between agents
- **Agent teams:** leader/member structure, leader-mediated and open communication modes, task delegation via team tools, shared team context
- **Paired conversations:** two agents conversing on a topic, turn limits, spectator mode with user injection
- **Slack:** primary app for all agents by default, optional dedicated Slack apps per agent with credentials in secrets vault
- **Multimodal input:** image and document attachments from Slack and web UI, vision model routing
- Web UI chat adapter for dashboard-based agent interaction
- **Persistent message queue:** SQLite WAL pattern, backpressure handling, restart recovery
- **Agent state & restart recovery:** desired/actual state tracking, automatic reconciliation on startup, graceful shutdown with queue flush
- **Ephemeral workers:** task delegation within conversations, inherited permissions, TTL, invisible to user
- **Backup & restore:** automated SQLite backups, configurable retention, per-agent export/import
- **Agent cloning & templates:** one-click clone, reusable template library
- **Operator notifications:** dedicated Slack alerts channel, configurable event triggers
- **Data retention & pruning:** configurable retention policies for audit logs, history, queue, archived memories
- **REST API:** programmatic access mirroring dashboard functionality, API key auth
- **Multi-user dashboard:** four roles (viewer/operator/manager/admin), group-based agent scoping, template-constrained manager creation
- **Kyvik guide agent:** built-in system expert, first-run guided setup, documentation skill, system status queries
- **SQLite concurrency:** WAL mode, write batching, optional separate databases for high-write tables
- **Prompt injection defenses:** input sanitization, content boundaries, output validation, canary tokens
- **Scheduled tasks:** cron-like scheduler for proactive agent behavior — SQLite cron table, internal scheduler goroutine, fires messages into agent queue at configured times. Required for morning briefs, periodic checks, daily rollover tasks.
- Web dashboard: agent wizard (with soul/identity builders), agent editing and deletion, live status/logs, permission management, spending dashboard, chat, skills management, memory management, conversation history, secrets management
- Layered spending limits with real-time dashboard adjustment
- Audit logging for every action with live streaming to dashboard
- **Makefile with install/uninstall** for single-command deployment

### Explicitly NOT in v1:

- Computer use / desktop interaction tool (requires GUI, complex dependencies)
- Signal, Discord, or other channel adapters beyond Slack and web UI
- SSO/OAuth authentication
- Real-time human approval UI (log-based manual intervention is sufficient)
- Swappable storage backends (SQLite only, but the `Store` interface exists)
- MCP compatibility bridge
- Skill registry / community marketplace

### High priority for v1.1:

- **Web chat interface improvements** — persistent chat history on page load, multi-conversation support, typing indicators, markdown rendering, file upload drag-and-drop, mobile-responsive layout. Make the web UI a viable daily-driver alternative to Slack.
- **Inbound webhooks / API triggers** — let external systems (Jenkins, GitHub, monitoring) send messages to agents
- **Computer use tool** — desktop interaction via screenshots and input simulation
- **Additional integrations** — email, calendar, Git, database, Jenkins, Jira
- **Context window management** — automatic history summarization, memory pruning by relevance

### Relationship to existing setup:

Kyvik runs alongside an existing agent deployment. The current agents continue on their existing platform while Kyvik proves itself with a new test agent. No migration pressure. Feature parity with OpenClaw is not a goal — proving the core architecture works with better security is the goal.

### Versioning strategy:

Kyvik uses traditional versioning (v1) for the initial release milestone. After v1 ships and the architecture is proven, Kyvik switches to **calendar versioning (CalVer)** using the format `YYYY.MM.DD` (e.g., `2026.03.15`). CalVer communicates when something shipped rather than imposing subjective major/minor/patch semantics. Every release after v1 is just a date.

---

## 7. Competitive Analysis

### 7.1 OpenClaw

**What it is:** Open-source self-hosted AI agent (160k+ GitHub stars). Formerly ClawdBot, then Moltbot. Can run locally with full system access — email, calendar, file system, shell, APIs.

**Strengths:** Massive community and ecosystem. Extensive documentation. Setup wizard makes first run accessible. Plugin architecture is highly extensible. Active development.

**Weaknesses:**
- Security is the critical gap. Unrestricted host access means a hallucinating agent or malicious plugin can delete files, exfiltrate data, or compromise credentials.
- CVE-2026-25253 exposed authentication token extraction.
- Supply chain risks from unaudited plugins.
- Multi-agent support requires workarounds — separate workspace directories, manual channel isolation, no native permission boundaries between agents.
- 430,000+ lines of code creates complexity and attack surface.

**Kyvik's advantage:** Security is foundational, not retrofitted. Native multi-agent isolation eliminates the workarounds. Permission system prevents the class of vulnerabilities that unrestricted access enables. Smaller, auditable codebase in a memory-safe language.

### 7.2 IronClaw

**What it is:** Rust rewrite of the OpenClaw concept with WASM-sandboxed tool execution. Security-first design with capability-based permissions.

**Strengths:** Strongest security model in the self-hosted agent space. WASM sandbox isolates every untrusted tool. Credential protection ensures secrets are injected at the host boundary — tool code never sees them. Pattern-based prompt injection detection.

**Weaknesses:**
- Rust barrier to entry for contributors and users extending the system.
- Still developer-focused — no web dashboard or non-technical user path.
- Smaller community and ecosystem compared to OpenClaw.
- WASM sandbox adds complexity and limits what tools can do.

**Kyvik's advantage:** Accessibility. IronClaw solves security but not approachability. Kyvik targets both. The web dashboard and permission templates make security accessible to non-technical users. Go is more approachable than Rust for the contributor ecosystem.

### 7.3 CrewAI

**What it is:** Python framework for role-based multi-agent collaboration. Assigns specific roles to agents and enables cooperative problem-solving.

**Strengths:** Elegant abstraction for multi-agent workflows. Role-based design maps well to real-world team structures. Growing adoption for production multi-agent systems. Good documentation.

**Weaknesses:**
- Python-only, which means runtime dependencies, virtual environments, and deployment complexity.
- Security and permissions are not first-class — the framework trusts agents to behave.
- No self-hosted dashboard or non-technical user path.
- Designed for orchestrated workflows more than always-on autonomous agents.

**Kyvik's advantage:** Security model, self-contained deployment, web dashboard. CrewAI is a developer library; Kyvik is a complete runtime with built-in management and guardrails. Different audiences with some overlap.

---

## 8. Open Questions

These are decisions deferred intentionally, to be revisited as the project matures:

1. **Sandbox implementation details.** Child processes with seccomp/AppArmor profiles? Lightweight containers (gVisor, Firecracker)? User-namespace isolation? The right answer depends on performance testing and target deployment environments.

2. **MCP bridge architecture.** When MCP compatibility becomes a priority, how does the bridge translate MCP's permissionless model into Kyvik's capability system? What gets blocked by default?

3. **Skill registry and distribution.** How are community skills published, discovered, and installed? What's the signing and verification process for the `verified` trust tier?

4. **Deployment targets.** Static Go binaries on bare metal is the starting point. Docker image? Kubernetes operator? Terraform modules? Prioritize based on community demand.

5. **Licensing model.** Open source? Source-available? Dual license with commercial tier? Depends on long-term product strategy.

---

## 9. Project Structure

```
kyvik/
├── cmd/
│   ├── kyvik/              # Main binary entry point
│   │   └── main.go
│   └── kyvik-sandbox/      # Sandbox runner binary (spawned as child process)
│       └── main.go
├── internal/
│   ├── core/                 # Agent lifecycle, message routing
│   │   ├── agent.go          # Agent manager (goroutine per agent)
│   │   ├── router.go         # Message bus and routing
│   │   ├── registry.go       # Agent registry and state (desired/actual)
│   │   └── workers.go        # Ephemeral worker spawning and lifecycle
│   ├── ktp/                  # Kyvik Tool Protocol
│   │   ├── types.go          # ToolDeclaration, ToolRequest, ToolResponse, Capability, ActionSpec
│   │   ├── registry.go       # Tool registry (register, lookup, list)
│   │   ├── executor.go       # Tool execution pipeline (validate → gate → sandbox → audit)
│   │   ├── schema.go         # JSON Schema validation for parameters and returns
│   │   └── convert.go        # Convert model tool_call format to KTP ToolRequest
│   ├── sandbox/              # Execution isolation
│   │   ├── sandbox.go        # Sandbox interface
│   │   └── process.go        # Child process sandbox implementation
│   ├── auth/                 # Authentication and sessions
│   │   └── auth.go
│   ├── permissions/          # Permission gate and templates
│   │   ├── gate.go           # Permission enforcement
│   │   ├── templates.go      # Built-in role templates
│   │   └── policy.go         # Policy evaluation engine
│   ├── models/               # LLM provider layer
│   │   ├── provider.go       # ModelProvider interface
│   │   ├── embedding.go      # EmbeddingProvider interface
│   │   ├── router.go         # Model Router (slot-based routing, prefix triggers, auto-classify)
│   │   ├── openrouter/       # OpenRouter adapter
│   │   │   ├── adapter.go
│   │   │   └── management.go # Management API client (per-agent key provisioning)
│   │   ├── openai/           # OpenAI direct adapter
│   │   │   └── adapter.go    # Implements ModelProvider + EmbeddingProvider
│   │   ├── anthropic/        # Anthropic direct adapter
│   │   │   └── adapter.go
│   │   └── ollama/           # Ollama adapter (local models + embeddings)
│   │       └── adapter.go
│   ├── channels/             # Communication adapters
│   │   ├── channel.go        # ChannelAdapter interface
│   │   ├── slack/            # Slack adapter (supports primary + dedicated apps)
│   │   │   └── adapter.go
│   │   ├── webui/            # Built-in web chat adapter
│   │   │   └── adapter.go
│   │   └── internal/         # Internal message bus (agent-to-agent)
│   │       └── bus.go
│   ├── tools/                # Built-in tool implementations
│   │   ├── file/             # File read/write/list/delete tool
│   │   │   └── tool.go
│   │   ├── http/             # HTTP request tool
│   │   │   └── tool.go
│   │   ├── shell/            # Shell command tool (sandboxed)
│   │   │   └── tool.go
│   │   └── memorytool/       # Agent self-memory tool
│   │       └── tool.go
│   ├── skills/               # Skills system
│   │   ├── loader.go         # Load skill from directory, parse manifest
│   │   ├── manager.go        # Install, grant, revoke, list skills per agent
│   │   ├── manifest.go       # skill.yaml parsing and validation
│   │   └── trust.go          # Trust tier enforcement (built-in, verified, community, local)
│   ├── memory/               # Agent memory system
│   │   ├── memory.go         # Memory interface and scoring
│   │   ├── sqlite.go         # SQLite memory implementation
│   │   ├── embedding.go      # Embedding integration, cosine similarity
│   │   └── extraction.go     # Automatic memory extraction from conversations
│   ├── identity/             # Agent soul and identity management
│   │   ├── soul.go           # Soul file loading, storage, presets
│   │   ├── identity.go       # Identity file loading, storage, role templates
│   │   ├── wizard.go         # Guided builder (generate SOUL.md / IDENTITY.md from selections)
│   │   └── presets.go        # Built-in personality presets and role templates
│   ├── history/              # Conversation history
│   │   ├── history.go        # History interface
│   │   └── sqlite.go         # SQLite history implementation
│   ├── store/                # Data persistence
│   │   ├── store.go          # Store interface
│   │   └── sqlite/           # SQLite implementation
│   │       └── sqlite.go
│   ├── secrets/              # Secrets vault
│   │   └── vault.go          # AES-256-GCM encrypted storage, scoped (global/agent/team)
│   ├── keymanager/           # Per-agent API key lifecycle
│   │   └── keymanager.go     # Provision, revoke, encrypt/decrypt via secrets vault
│   ├── security/             # Prompt injection defenses
│   │   ├── sanitizer.go      # Input sanitization, injection pattern detection
│   │   ├── validator.go      # Output validation, anomaly detection
│   │   ├── canary.go         # Canary token generation and monitoring
│   │   ├── boundaries.go     # Content boundary wrapping for external data
│   │   └── alerts.go         # Security event logging and alerting
│   ├── teams/                # Inter-agent communication and teams
│   │   ├── bus.go            # Internal message bus
│   │   ├── teams.go          # Team data model, management
│   │   └── paired.go         # Paired conversation orchestration
│   ├── audit/                # Audit logging
│   │   └── logger.go
│   ├── queue/                # Persistent message queue
│   │   └── queue.go          # SQLite WAL pattern, backpressure, replay on restart
│   ├── backup/               # Backup & restore
│   │   ├── backup.go         # Full instance backup, agent export
│   │   └── restore.go        # Instance restore, agent import
│   ├── retention/            # Data retention & pruning
│   │   └── pruner.go         # Configurable retention policies, scheduled cleanup
│   ├── notifications/        # Operator notifications
│   │   └── notifier.go       # Slack alerts, email fallback, event routing
│   └── spending/             # Token and cost tracking (per-provider, per-slot)
│       └── tracker.go
├── web/                      # Dashboard
│   ├── handlers/             # HTTP handlers
│   │   ├── agents.go         # Agent CRUD, wizard, clone, templates
│   │   ├── dashboard.go      # Main dashboard view
│   │   ├── logs.go           # Live log streaming (SSE)
│   │   ├── permissions.go    # Permission management UI
│   │   ├── spending.go       # Spending dashboard (per-provider breakdowns)
│   │   ├── chat.go           # Built-in chat interface (with file upload)
│   │   ├── memory.go         # Memory management UI (with auto-extracted review)
│   │   ├── history.go        # Conversation history viewer
│   │   ├── skills.go         # Skills management UI
│   │   ├── teams.go          # Team management, paired conversations
│   │   ├── secrets.go        # Secrets vault management UI
│   │   ├── security.go       # Security alerts dashboard
│   │   ├── backup.go         # Backup/restore, agent export/import
│   │   ├── users.go          # Multi-user management, groups, roles, templates
│   │   └── notifications.go  # Notification config UI
│   ├── api/                  # REST API (mirrors handlers)
│   │   ├── routes.go         # API routing and auth middleware
│   │   ├── agents.go         # Agent CRUD, lifecycle, messaging
│   │   ├── spending.go       # Spending queries
│   │   └── backup.go         # Backup triggers
│   ├── templates/            # Go HTML templates
│   │   ├── layout.html
│   │   ├── dashboard.html
│   │   ├── agents/
│   │   │   ├── list.html
│   │   │   ├── create.html
│   │   │   ├── detail.html
│   │   │   └── edit.html
│   │   ├── chat/
│   │   ├── permissions/
│   │   ├── spending/
│   │   ├── memory/
│   │   ├── history/
│   │   └── skills/
│   └── static/               # CSS, JS (HTMX), assets
│       ├── css/
│       ├── js/
│       └── img/
├── souls/                    # Agent soul files (core personality)
│   ├── kyvik-guide.md        # Built-in: Kyvik the badger guide agent
│   ├── friendly-helper.md
│   ├── professional-analyst.md
│   ├── creative-thinker.md
│   └── no-nonsense-operator.md
├── identities/               # Agent identity files (role definition)
│   ├── kyvik-guide.md        # Built-in: system expert and onboarding guide
│   ├── general-assistant.md
│   ├── researcher.md
│   ├── devops-monitor.md
│   └── home-assistant.md
├── skills/                   # Installed skills
│   └── built-in/            # Skills that ship with Kyvik
│       ├── file-manager/
│       │   ├── SKILL.md
│       │   ├── skill.yaml
│       │   └── prompts/
│       │       └── instructions.md
│       └── system-docs/      # Built-in: Kyvik guide agent's documentation skill
│           ├── SKILL.md
│           ├── skill.yaml
│           └── prompts/
│               └── instructions.md
├── pkg/                      # Public interfaces for extensions
│   ├── types/                # Shared types (AgentConfig, Message, KTP types, etc.)
│   │   └── types.go
│   └── sdk/                  # SDK for building custom tools and skills
│       ├── tool.go
│       └── skill.go
├── migrations/               # Database migrations
│   ├── 001_initial.sql
│   ├── 002_memory.sql
│   ├── 003_history.sql
│   └── 004_skills.sql
├── tests/                    # Integration and E2E tests
│   ├── e2e_test.go
│   └── mocks/
├── configs/                  # Example configurations
│   ├── kyvik.example.yaml
│   └── templates/
│       ├── reader.yaml
│       ├── worker.yaml
│       └── admin.yaml
├── DESIGN.md                 # This document
├── README.md
├── go.mod
├── go.sum
├── Makefile
└── Dockerfile
```

---

## 10. Implementation Roadmap

### Phase 1: Foundation (✅ Complete)
1. SQLite store implementation
2. Audit logger
3. Permission gate with templates
4. Spending tracker
5. OpenRouter model adapter
6. Core agent loop
7. Slack channel adapter
8. Web dashboard skeleton (agent wizard, status, auth)
9. Main.go wiring and config loading
10. E2E smoke test
11. README with Ubuntu deployment guide

### Phase 2: Core Infrastructure (✅ Complete)
Secrets vault, persistent queue, multi-provider, and restart recovery — the foundation everything else builds on.
12. Fix Slack adapter message delivery
13. Web UI chat interface and adapter
14. Agent edit and delete
15. SQLite WAL mode + concurrency tuning
16. Secrets vault — AES-256-GCM encryption, auto-generated master key, three-tier scope
17. Persistent message queue — SQLite WAL pattern, status tracking, backpressure, replay on restart
18. Agent state & restart recovery — desired/actual state, reconciliation on startup, graceful shutdown
19. OpenAI direct provider adapter — completion, streaming, cost calculation, embedding support
20. Anthropic direct provider adapter — system prompt extraction, typed SSE, tool_use blocks
21. Per-agent OpenRouter key provisioning via Management API
22. Ollama model adapter — local model support, embedding support
23. Operator notifications — Slack alerts channel, configurable event triggers

### Phase 3: Agent Intelligence (✅ Complete)
Depends on embedding provider from Phase 2 for semantic memory.
24. Agent soul system (SOUL.md) with presets and guided builder
25. Agent identity system (IDENTITY.md) with role templates and guided builder
26. Agent memory system — schema, CRUD, categorized memories, agent/user/auto sources
27. Semantic memory retrieval — embed on create, cosine similarity scoring, top-N injection
28. Automatic memory extraction — background extraction via fast model, de-duplication
29. Memory decay and archival — access tracking, staleness, pinned memories
30. Conversation history — per-agent per-channel, configurable limits
31. Context window budgeting — dynamic injection sizing based on model context
32. Multimodal input — attachment model, Slack/web file handling, vision model detection
33. Dedicated Slack apps — optional per-agent, credentials in secrets vault
34. Channel configuration in wizard — Slack app/channel, web UI toggle
35. Agent detail page improvements — full config at a glance, action buttons

### Phase 4: Model Router & Ephemeral Workers (✅ Complete)
Depends on multi-provider from Phase 2.
36. Model slot configuration — user-defined slots (default, reasoning, coder, fast), per-slot provider/model
37. Prefix trigger routing — parse "slotname: message" from incoming messages
38. Automatic classification routing — classifier prompt via fast model slot, context-aware
39. Vision slot auto-routing — activate when message contains image attachments
40. Spending tracker updates — per-provider and per-slot cost aggregation
41. Ephemeral workers — task delegation, inherited permissions, TTL, max concurrent, no nested spawning
42. Dashboard: multi-model wizard step, slot config, per-slot spending breakdown

### Phase 5: Kyvik Tool Protocol (KTP) (✅ Complete)
43. KTP core types — ToolDeclaration, ToolRequest, ToolResponse, Capability, ActionSpec
44. Tool registry — register, lookup, list tools with declaration validation
45. JSON Schema validation for tool parameters and return values
46. KTP executor — the full pipeline: validate → permission gate → sandbox → audit
47. Model tool-use integration — convert model tool_call responses into KTP ToolRequests

### Phase 6: Security & Sandbox
48. Sandbox execution (child process isolation with resource limits)
49. Sandbox runner binary (cmd/kyvik-sandbox/)
50. Core tools: file, memory, HTTP (all implementing KTP Tool interface)
51. Elevated tools: shell, code execution
52. Tool execution through sandbox with permission gate enforcement
53. Power + unrestricted permission tiers with warnings and confirmations
54. Prompt injection defenses — sanitizer, content boundaries, output validation, canary tokens

### Phase 7: Safety Controls
55. Kill switch — dashboard one-click, CLI command, API endpoint, "Stop All" global
56. Circuit breaker — error rate, spending velocity, action rate, loop detection
57. Quarantine mode — receive but don't process, configurable pause message
58. Global vacation mode — pause all, maintenance flag, Slack notification

### Phase 8: Skills System
59. Skill manifest parser (skill.yaml)
60. Skill loader — load from directory, validate requirements, extract prompts
61. Skills manager — install, grant to agent, revoke, list per agent
62. Trust tier enforcement (built-in, verified, community, local)
63. Skill prompt injection into agent context (after identity, before memories)
64. Skill-provided custom tools registered through KTP
65. Built-in reference skill (file-manager)
66. Dashboard: skills management UI (install, review permissions, grant/revoke)

### Phase 9: Inter-Agent Communication & Teams
67. Internal message bus — Send, Subscribe, Broadcast with permission checks
68. Internal bus channel adapter — agent messages arrive like Slack messages
69. Agent-to-agent permission configuration (can_message allowlist)
70. Team data model — leader, members, communication mode, shared context
71. Team tools — team.delegate, team.broadcast, team.status, team.recall
72. Leader-mediated delegation flow (user → leader → member → leader → user)
73. Open communication mode (peer-to-peer within team)
74. Paired conversations — turn-based exchange, turn limits, turn delay
75. Paired conversation spectator mode — live dashboard viewer with user injection
76. Dashboard: team creation wizard, team detail page, member status
77. Dashboard: paired conversation launcher and viewer

### Phase 10: Advanced Tools
78. Browser tool — headless browser, page fetch, screenshot, link extraction
79. Host filesystem tool — access beyond workspace with configurable allowlists
80. Built-in integrations: webhooks (inbound + outbound), generic REST API

### Phase 11: Dashboard Completion & API
81. REST API — full programmatic access mirroring dashboard, API key auth
82. Multi-user dashboard — four roles (viewer/operator/manager/admin), group-based scoping, template-constrained creation, first-run bootstrap
83. Agent cloning — one-click clone with fresh state
84. Agent templates — save/load reusable agent configurations
85. Live audit log streaming via SSE
86. Spending dashboard with charts, per-provider breakdowns, real-time limit adjustment
87. Permission management UI with override editor (all 5 tiers)
88. Memory management UI — view, add, edit, delete, pin/unpin, review auto-extracted
89. Conversation history viewer
90. Security alerts dashboard
91. Update agent wizard — soul/identity builders, multi-model config, skills, channel config

### Phase 12: Deployment & Hardening
92. Makefile with install/uninstall targets and systemd service
93. Backup & restore — automated SQLite backups, retention, agent export/import
94. Data retention & pruning — configurable policies, automated cleanup jobs
95. Scheduled tasks — cron table in SQLite, scheduler goroutine, fires messages into agent queue, dashboard config UI
96. Kyvik guide agent — built-in badger mascot agent with system-docs skill, first-run guided setup, system status queries
97. Verify and fix: graceful shutdown, config file parsing, HTMX polling, spending limit enforcement
98. Comprehensive integration test suite
99. Security audit of sandbox, secrets vault, and skill trust model
100. Performance testing under multi-agent load

### Post-v1 Priorities (first CalVer releases)
- **Web chat interface improvements** — persistent history on page load, multi-conversation, typing indicators, markdown rendering, mobile-responsive
- **Inbound webhooks / API triggers** — external systems trigger agent actions
- **Computer use / desktop tool** — screenshot + input simulation
- **Additional integrations** — email (IMAP/SMTP), calendar (CalDAV), Git, database, Jenkins, Jira
- **Team memory promotion** — Leader-created memories propagate to team members

### Future
- Signal and Discord channel adapters
- SSO/OAuth authentication
- Real-time human approval workflow UI
- PostgreSQL store backend (with pgvector for embedding search at scale)
- MCP compatibility bridge
- Skill registry and community marketplace
- Home automation integration (MQTT / Home Assistant)
- Docker image build and publish
- Kubernetes operator
- Ephemeral worker parallel message processing (workers handle queued messages concurrently)
- Agent config versioning and rollback — snapshot config on each edit, revert to previous versions from dashboard
- Prompt/test playground — sandbox mode for testing prompts, tool calls, and model routing without affecting live agents
- Batch operations — bulk start/stop, bulk permission changes, bulk model swaps, bulk knowledge uploads across agents
- Multi-environment support — tag agents as dev/staging/prod with promotion workflows between environments
- Webhook/API testing in UI — test REST API endpoints and inbound webhooks directly from the dashboard
- Documentation knowledge base — `/docs` directory in repo as both user-facing docs and Kyvik guide agent's `system-docs` skill source
