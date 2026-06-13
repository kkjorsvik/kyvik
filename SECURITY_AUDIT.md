# Security Audit Report

**Date:** 2026-02-23
**Scope:** Code-level security review of Kyvik's most sensitive subsystems
**Status:** Pre-v1 audit — all HIGH/MEDIUM findings fixed

## Summary

Kyvik's security-first design is reflected in strong implementations across encryption, authentication, session management, SQL injection prevention, XSS protection, SSRF defense, and audit logging. This audit focused on 7 areas and found 4 actionable vulnerabilities (1 HIGH, 3 MEDIUM) which have been fixed, plus 6 LOW-severity items — 3 of which (L3, L5, L6) have since been fixed, with 3 remaining as documented known limitations (L1, L2, L4).

## Findings

### H1 — Skill prompts can override agent identity (HIGH) — FIXED

**Location:** `internal/skills/manager.go:184`

**Problem:** Community and local skill prompt content was injected raw into the system prompt via `PromptContentForAgent()`. A malicious skill could contain "Ignore all previous instructions..." and override the agent's identity and security constraints.

**Impact:** An attacker who publishes a malicious community skill could take full control of any agent that grants it, bypassing permission boundaries.

**Fix:** Untrusted skill content (community and local tiers) is now wrapped in content boundaries using `security.WrapExternalContent()`, which marks the content as external data and prevents it from being interpreted as instructions. Built-in and verified skills remain unwrapped as they are code-reviewed.

---

### M1 — Canary token not stripped from leaked response (MEDIUM) — FIXED

**Location:** `internal/security/defense.go:126-153`

**Problem:** When `ValidateResponse()` detected a canary token leak in a model response, it logged a security event but returned the response unchanged, allowing the canary value to reach end users.

**Impact:** Canary tokens leaked to users could be used to craft targeted attacks against the system prompt, or reveal internal security mechanisms.

**Fix:** After detecting a canary leak, the canary value is now stripped from the response and replaced with `[REDACTED]` before returning.

---

### M2 — Permission template loading silently fails (MEDIUM) — FIXED

**Location:** `internal/permissions/store_gate.go:266-308`

**Problem:** `loadTemplatesFromDir()` silently skipped files that failed to read, parse, or had missing names. An operator deploying a misconfigured template would have no indication it was ignored.

**Impact:** Security policies could silently fail to load, leaving agents with default (deny-all) permissions when operators expected specific capabilities to be available.

**Fix:** Added `slog.Warn` logging for each failure path: read errors, YAML parse errors, and missing name fields.

---

### M3 — Webhook transform template not validated at save time (MEDIUM) — FIXED

**Location:** `web/handlers/webhooks.go:81`, `web/handlers/outbound_webhooks.go`

**Problem:** Inbound webhook `TransformTemplate` and outbound webhook `PayloadTemplate` fields were saved without validating Go template syntax. Invalid templates would fail silently at execution time, causing webhooks to drop payloads.

**Impact:** Operators could save broken templates that silently discard all incoming webhook payloads, leading to missed alerts and events with no error feedback.

**Fix:** Template syntax is now validated using `text/template.Parse()` before saving. Invalid templates return HTTP 400 with a descriptive error message.

---

### L1 — No OS-level filesystem isolation in sandbox (LOW) — Documented

**Location:** `internal/sandbox/`

The process sandbox uses `AllowedPaths`/`DeniedPaths` for application-level path filtering, but does not enforce OS-level filesystem isolation (chroot, namespaces, seccomp). For production deployments with untrusted agents, container-level isolation (Docker, Podman) is recommended.

---

### L2 — Network isolation is application-level only (LOW) — Documented

**Location:** `internal/sandbox/`

Network restrictions (allowed hosts, HTTPS-only) are enforced in the HTTP client layer. Agents executing arbitrary code could bypass these by using raw sockets. Container-level network policies are recommended for production.

---

### L3 — Permission gate not transactional (LOW) — FIXED

**Location:** `internal/permissions/store_gate.go:65-111`

The `Check()` method reads overrides and agent config in separate queries. A narrow TOCTOU window exists if overrides are modified between queries. The risk is minimal because permission changes are infrequent admin operations.

**Fix:** Added `GetAgentWithOverrides()` method to `PermissionStore` that reads both the agent config and permission overrides inside a single read-only database transaction. `Check()` and `GetAgentCapabilities()` now use this transactional method, eliminating the TOCTOU window.

---

### L4 — extractResource defaults to wildcard (LOW) — Documented (by design)

**Location:** `internal/permissions/store_gate.go:207-216`

When a tool call has no recognizable resource parameter, `extractResource()` defaults to `"*"`. This is by design — it enables tool calls without explicit resource parameters to be matched against wildcard permission patterns.

---

### L5 — Secrets injected as env vars in sandbox (LOW) — FIXED

**Location:** `internal/sandbox/manager.go`

Sandbox processes previously received secrets via environment variables, which are visible in `/proc/*/environ` on Linux.

**Fix:** Secrets are now served on-demand via a per-execution Unix domain socket (`SecretsServer`). The parent process creates the socket at `{workspace}/tmp/.kyvik-secrets.sock` with 0600 permissions before spawning the sandbox binary. The sandbox binary connects to the socket to resolve secrets, so they never appear in environment variables. Backward compatibility is maintained via env var fallback when the socket is not configured.

---

### L6 — Skill sandbox config declared but not enforced (LOW) — FIXED

**Location:** `pkg/types/types.go:563-569`

`SkillSandboxConfig` declares network and filesystem constraints in skill manifests, but these were not enforced at runtime. Skills inherited the agent's sandbox configuration.

**Fix:** Skill sandbox constraints are now enforced using intersection semantics (skills can only restrict, never expand agent capabilities). `SkillSandboxConfig` is carried on `ToolRequest` and applied at three layers: the executor applies network/host constraints to sandbox overrides, the sandbox manager computes host intersection and passes path restrictions as env vars, and the file tool enforces read/write path restrictions on every operation.

## Strong Areas (No Issues Found)

| Area | Implementation | Notes |
|------|---------------|-------|
| **Encryption** | AES-256-GCM | Proper nonce handling, authenticated encryption |
| **API key hashing** | bcrypt | Constant-time comparison |
| **Session management** | HMAC-SHA256 | HttpOnly, Secure, SameSite=Strict cookies |
| **SQL injection** | All parameterized queries | Both SQLite and PostgreSQL stores |
| **XSS prevention** | `html/template` | Auto-escaping in all web templates |
| **SSRF protection** | Private IP blocking | HTTPS-only, redirect limits, host validation |
| **Webhook signatures** | HMAC-SHA256 | Constant-time comparison via `hmac.Equal()` |
| **Rate limiting** | Token bucket | Per-agent and per-endpoint |
| **Audit logging** | Comprehensive | Every permission check, tool call, and admin action logged |
| **Prompt injection** | Multi-layer defense | Sanitization, content boundaries, identity reinforcement, canary tokens |

## Recommendations for Future Hardening

1. **Container sandbox backend** — Implement the container sandbox to provide OS-level isolation for untrusted agents
2. ~~**Skill sandbox enforcement**~~ — Done: `SkillSandboxConfig` constraints enforced via intersection semantics (fixes L6)
3. ~~**Transactional permission checks**~~ — Done: `GetAgentWithOverrides()` uses read-only transactions (fixes L3)
4. ~~**Secrets API**~~ — Done: Unix socket `SecretsServer` replaces env var injection (fixes L5)
5. ~~**Content Security Policy**~~ — Done: `SecurityHeaders` middleware adds CSP and security headers to all responses
6. **Nonce-based CSP** — Migrate from `'unsafe-inline'` to nonce-based CSP by updating all 37 templates (follow-up to Rec #5)
