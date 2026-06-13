# Permissions

## Overview

Kyvik uses a **deny-by-default** permission model. Every tool call passes through a permission gate before execution. Agents can only do what their tier explicitly allows.

## The Six Tiers

Tiers are ordered by privilege level. Higher tiers include all capabilities of lower tiers.

| Tier | Level | What It Can Do |
|------|-------|----------------|
| `reader` | 0 | Read files, query data, use memory. No modifications. |
| `writer` | 1 | Create/modify files, make HTTP requests, use team tools. |
| `operator` | 2 | Writer capabilities plus broader coordination access. |
| `admin` | 3 | Full tool access: shell, code execution, browser. Network allowed in sandbox. |
| `power` | 4 | Admin plus host filesystem access via path allowlists. |
| `unrestricted` | 5 | No tool restrictions. HTTP allowlists and shell allowlists bypassed. Still audited. |

## Tool Tier Requirements

Each tool has a minimum tier to use it:

| Tool | Minimum Tier |
|------|-------------|
| `file` | reader |
| `memory` | reader |
| `http` | writer |
| `github` | writer |
| `rest_api` | writer |
| `team:delegate` | writer |
| `team:broadcast` | writer |
| `team:status` | writer |
| `team:recall` | writer |
| `shell` | admin |
| `code` | admin |
| `browser` | admin |
| `hostfs` | power |

## Permission Templates

Templates define capability grants as `(tool, action, resource)` triplets:

- **reader** — `filesystem:read:*`, `http:get:*`, `database:select:*`
- **worker** — adds `filesystem:write`, `http:post`, `database:insert/update`
- **admin** — `*:*:*` (all capabilities)
- **power** — `*:*:*` plus host filesystem via allowlists
- **unrestricted** — `*:*:*` with no allowlist enforcement

## Overrides

Permission overrides let operators grant or deny specific capabilities beyond the base template. Overrides are stored per-agent in the database and checked after the template but before execution.

## Common Mistakes

- **Starting with admin when writer suffices** — always use the lowest tier that works. Most agents only need `reader` or `writer`.
- **Forgetting allowlists** — `http` and `shell` tools enforce allowlists. An agent with writer tier still can't reach arbitrary URLs unless they're allowed.
- **Confusing agent tiers with dashboard roles** — agent permission tiers control what tools an agent can use. Dashboard roles (viewer, operator, manager, admin) control what humans can do in the web UI and API.
