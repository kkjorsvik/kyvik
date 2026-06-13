# Security

## Design Principle

Kyvik is security-first: deny by default, audit everything, sandbox all execution.

## Sandboxing

Every tool call runs in a sandbox with resource limits based on the agent's permission tier:

| Tier | Memory | CPU | Timeout | Network | Max Output |
|------|--------|-----|---------|---------|------------|
| reader | 256 MB | 10% | 10s | No | 1 MB |
| writer | 512 MB | 25% | 30s | No | 1 MB |
| admin | 1 GB | 50% | 120s | Yes | 1 MB |
| power | 1 GB | 50% | 300s | Yes | 5 MB |
| unrestricted | 2 GB | 100% | 300s | Yes | 1 MB |

Sandboxes are isolated child processes. An agent cannot escape its sandbox or access resources beyond its tier.

## Permission Gate

Every tool call passes through the permission gate before execution:

1. Check the agent's tier against the tool's minimum tier
2. Check capability triplets: `(tool, action, resource)`
3. Apply any per-agent overrides
4. Deny if any check fails
5. Log the decision to the audit trail

See [Permissions](permissions.md) for tier details.

## Circuit Breaker

The circuit breaker automatically quarantines agents exhibiting abnormal behavior:

| Trigger | Default Threshold |
|---------|-------------------|
| Errors in window | 5 errors in 10 minutes |
| Action rate | 30 tool calls per minute |
| Destructive actions | 5 destructive calls per session |
| Identical messages | 3 identical consecutive messages |
| Spending velocity | 50% of daily budget in 5 minutes |

When any trigger fires, the agent is quarantined. An operator must review and explicitly restart the agent.

## Secrets Management

Secrets are stored encrypted with **AES-256-GCM**:

- Master key from `KYVIK_MASTER_KEY` environment variable (base64-encoded, 32 bytes)
- Each secret encrypted with a random 12-byte nonce
- Storage format: `nonce (12 bytes) || ciphertext`

### Scope Cascade

Secrets resolve through a cascade:

1. `agent:<agentID>` — agent-specific secret
2. `team:<teamID>` — team-level secret (if agent is in a team)
3. `global` — system-wide secret

The first match wins. This lets you set a default API key globally and override it for specific agents or teams.

## Audit Trail

Every action is logged:

- Tool calls (with arguments and results)
- Permission decisions (grants and denials)
- State changes (start, stop, quarantine)
- Spending events
- Configuration changes

Audit logs are immutable and queryable through the dashboard and API.

## Prompt Injection Defense

Kyvik includes prompt injection detection for incoming messages. Suspicious messages are flagged and can trigger quarantine depending on configuration. The circuit breaker's identical-message detection also catches simple injection loops.

## Allowlists

Several tools enforce allowlists for defense in depth:

- **http** — host allowlist limits which domains agents can reach
- **shell** — command allowlist limits which commands can be executed
- **hostfs** — path allowlist limits which host directories are accessible

Allowlists are configured per-agent or globally. The `unrestricted` tier bypasses allowlists but is still audited.
