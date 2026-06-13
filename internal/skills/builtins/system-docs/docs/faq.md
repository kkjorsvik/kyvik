# Frequently Asked Questions

## General

**What is Kyvik?**
Kyvik is a security-first, multi-agent AI framework. It manages AI agent lifecycles with built-in guardrails, sandboxed execution, and a web dashboard, all deployable as a single binary.

**What does the name mean?**
Kyvik is named after the badger (Norwegian: grevling). Like a badger, it's compact, capable, and security-minded.

**Do I need Docker to run Kyvik?**
No. Kyvik compiles to a single binary. Docker is available as a deployment option but not required.

## Agents

**How many agents can I run?**
There's no hard limit. Each agent runs as a goroutine, which is lightweight. The practical limit depends on your LLM provider rate limits and budget.

**Can agents talk to each other?**
Yes, through teams. See [Teams](teams.md) for communication modes and delegation patterns.

**What happens if an agent crashes?**
The agent moves to `error` state. The Kyvik runtime continues running — other agents are unaffected. Check the audit log for the error details and restart the agent.

## Permissions

**What tier should I use for a new agent?**
Start with `reader` for information retrieval or `writer` for agents that need to create content or make API calls. Only escalate to `admin` or higher when the agent genuinely needs shell/code execution or host filesystem access. See [Permissions](permissions.md).

**Can I give an agent access to one specific tool without changing its tier?**
Permission overrides let you grant or deny specific capabilities beyond the base tier. However, the tier sets the floor — you can't use overrides to bypass the minimum tier requirement for a tool.

## Models

**Which LLM provider should I use?**
OpenRouter gives access to many models through one API key. Anthropic, OpenAI, and Ollama are available for direct access. You can configure multiple providers and use model slots to route different types of requests. See [Models](models.md).

**Can I use local models?**
Yes. Configure an Ollama provider pointing to your local Ollama instance. No API key needed.

## Security

**Is everything really audited?**
Yes. Every tool call, permission decision, state change, and spending event is logged to the audit trail. Even `unrestricted` tier agents are fully audited.

**What if an agent tries to do something malicious?**
Multiple layers protect against this: the permission gate denies unauthorized actions, the sandbox limits resource access, the circuit breaker catches abnormal behavior patterns, and the audit trail records everything. See [Security](security.md).

**How are secrets stored?**
Encrypted with AES-256-GCM using a master key from the `KYVIK_MASTER_KEY` environment variable. Secrets resolve through a scope cascade: agent-specific, then team-level, then global.

## Spending

**What happens when an agent hits its budget?**
The agent is paused — no further LLM calls are made. An operator must raise the limit or wait for the period to reset. See [Spending](spending.md).

**Can I set different budgets for different agents?**
Yes. Per-agent spending limits override the global defaults. Set them in the agent configuration.

## Skills

**What's the difference between a skill and a tool?**
A tool is a runtime capability (file access, HTTP requests, shell execution). A skill is a knowledge/behavior package (prompts and documentation) that extends what an agent knows or how it behaves. Skills may require specific tools to function.

**Can any agent use any skill?**
Only if the agent's tier meets the skill's requirements. Prompt-only skills (like system-docs) work with any tier. Skills that require tools (like file-manager) need the appropriate tier. See [Skills](skills.md).
