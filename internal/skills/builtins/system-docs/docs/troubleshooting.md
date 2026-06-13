# Troubleshooting

Common issues in problem/cause/fix format.

---

## Agent won't start

**Problem:** Agent stays in `stopped` state after setting desired state to `running`.

**Cause:** Usually a configuration issue — invalid model configuration, missing provider credentials, or database error.

**Fix:** Check the logs for the agent's name. Verify the model provider is configured in `kyvik.yaml` and the API key environment variable is set.

---

## Agent quarantined unexpectedly

**Problem:** Agent moved to `quarantined` state without operator action.

**Cause:** The circuit breaker tripped. Possible triggers: too many errors (5 in 10 min), too many actions (30/min), destructive action limit (5/session), identical messages (3 in a row), or spending velocity (50% daily budget in 5 min).

**Fix:** Check the audit log for the quarantine event — it will say which trigger fired. Address the root cause (fix the error, adjust limits, or investigate the loop), then restart the agent.

---

## Tool call denied

**Problem:** Agent gets a permission denied error when calling a tool.

**Cause:** The agent's tier is below the tool's minimum requirement, or the specific capability isn't in the agent's template.

**Fix:** Check [Permissions](permissions.md) for tool tier requirements. Either upgrade the agent's tier or add a specific permission override.

---

## HTTP requests failing

**Problem:** Agent's `http` tool calls fail with "host not allowed".

**Cause:** The target domain isn't in the agent's HTTP allowlist.

**Fix:** Add the domain to the agent's HTTP allowlist in configuration. Remember that `unrestricted` tier bypasses allowlists, but prefer adding specific domains over escalating tiers.

---

## Model returning errors

**Problem:** Agent gets errors from the LLM provider.

**Cause:** Common causes: invalid API key, rate limiting from the provider, model name incorrect, or provider service outage.

**Fix:** Verify the API key environment variable is set correctly. Check if the model name matches what the provider expects. Check the provider's status page for outages. If rate limited, consider configuring multiple model slots for load distribution.

---

## Spending limit hit

**Problem:** Agent stops responding, audit shows spending limit reached.

**Cause:** The agent consumed its daily or monthly budget.

**Fix:** Either wait for the period to reset, increase the limit in the agent's config or global `kyvik.yaml`, or optimize the agent's prompts to reduce token usage. See [Spending](spending.md).

---

## Skill not loading

**Problem:** An agent doesn't have access to an assigned skill.

**Cause:** The skill's `required_tools` or `required_capabilities` exceed what the agent's tier provides.

**Fix:** Either upgrade the agent's tier to meet the skill's requirements, or use a prompt-only skill that has no requirements. See [Skills](skills.md).

---

## Team delegation not working

**Problem:** Leader's `team:delegate` calls fail or members don't receive tasks.

**Cause:** The leader agent must have at least `writer` tier. The target member must be in `running` state and part of the same team.

**Fix:** Verify both agents are running, both have `writer` tier or above, and both are members of the same team. Check the team's communication mode — in `leader-mediated` mode, only the leader can delegate.

---

## Database migration errors

**Problem:** Kyvik fails to start with database errors.

**Cause:** Schema mismatch — usually from a version upgrade that added new columns or tables.

**Fix:** For PostgreSQL, migrations run automatically at startup. For SQLite, run `make migrate` to apply the latest migration file. Back up your database before upgrading.

---

## Shell commands blocked

**Problem:** Agent's `shell` tool returns "command not allowed".

**Cause:** The command isn't in the shell allowlist. Some commands are permanently blocked for safety (`mkfs`, `shutdown`, `reboot`, `systemctl`, `fdisk`, `iptables`, `mount`).

**Fix:** Add the command to the shell allowlist in configuration. If it's a permanently blocked command, it cannot be enabled — this is by design for safety.
