# Tools

## Overview

Kyvik provides tools through the Kyvik Tool Protocol (KTP). Every tool call passes through the permission gate and is logged to the audit trail. Tools are sandboxed according to the agent's tier.

## Built-In Tools

### file (min: reader)
Workspace file operations. Actions: `read`, `write`, `list`, `delete`, `mkdir`, `stat`.
- Confined to the agent's workspace directory
- Power tier can access host paths via allowlists
- Unrestricted tier has full filesystem access

### memory (min: reader)
Persistent key-value storage. Actions: `remember`, `recall`, `forget`, `list`.
- Runs in-process (no sandbox overhead)
- Categories: `fact`, `decision`, `context`, `instruction`
- Memories persist across conversations

### http (min: writer)
HTTP client. Actions: `fetch` (GET only), `request` (GET/POST/PUT/PATCH/DELETE/HEAD).
- HTTPS only, SSRF protection enabled
- Host allowlist enforced — agents can only reach approved domains
- Unrestricted tier bypasses allowlists

### github (min: writer)
GitHub integration. Actions: `get_repo`, `list_issues`, `create_issue`, `comment_issue`.
- Requires a `github:token` secret in the vault
- Authentication handled automatically via vault

### rest_api (min: writer)
Pre-configured REST API endpoints. Actions: `list_endpoints`, `call`.
- Endpoints defined in configuration with URL templates
- Supports response caching and rate limiting
- Authentication via vault-backed secrets

### shell (min: admin)
Shell command execution. Action: `exec`.
- Command allowlist enforced — only pre-approved commands allowed
- Blocked commands: `mkfs`, `shutdown`, `reboot`, `systemctl`, `fdisk`, `iptables`, `mount`, and others
- Default timeout 30s, maximum 300s
- Unrestricted tier bypasses allowlists

### code (min: admin)
Code execution in sandbox. Actions: `run`, `run_file`.
- Supported languages: `python3`, `bash`, `go`
- Default timeout 60s, maximum 600s
- Sandboxed with resource limits based on agent tier

### browser (min: admin)
Headless browser. Actions: `fetch_page`, `screenshot`, `extract_links`, `search_web`.
- Search uses DuckDuckGo HTML
- SSRF protection enabled

### hostfs (min: power)
Host filesystem access. Actions: `read`, `write`, `list`, `stat`, `delete`, `mkdir`.
- Absolute paths only
- Path allowlist enforced
- Default max read/write: 10 MB

### Team Tools (min: writer)

| Tool | Action | Description |
|------|--------|-------------|
| `team:delegate` | delegate | Leader sends a task to a specific team member |
| `team:broadcast` | broadcast | Send message to all team members |
| `team:status` | status | Query team member operational state |
| `team:recall` | recall | Leader sends urgent recall to a member |

See [Teams](teams.md) for details on team communication.

## Sandbox Resource Limits

Tool execution is sandboxed with limits based on agent tier:

| Tier | Memory | CPU | Timeout | Network |
|------|--------|-----|---------|---------|
| reader | 256 MB | 10% | 10s | No |
| writer | 512 MB | 25% | 30s | No |
| admin | 1 GB | 50% | 120s | Yes |
| power | 1 GB | 50% | 300s | Yes |
| unrestricted | 2 GB | 100% | 300s | Yes |

See [Security](security.md) for more on sandboxing.
