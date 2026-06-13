# System Docs Skill

Provides comprehensive Kyvik framework documentation to agents. This is a prompt-only skill — no tools or capabilities required — so any agent regardless of permission tier can use it.

## What It Provides

Behavioral instructions that tell an agent how to answer user questions from documentation rather than general knowledge, plus a reference documentation library covering:

- **Getting Started** — first-run flow, agent creation, configuration
- **Agents** — lifecycle, states, message processing, concurrency
- **Permissions** — six tiers, tool requirements, overrides, templates
- **Models** — providers, model slots, routing (prefix, vision, auto-classification)
- **Tools** — all built-in tools, tier requirements, sandbox limits
- **Skills** — trust tiers, installation, granting, creating skills
- **Teams** — communication modes, delegation, paired conversations
- **Spending** — limits, tracking, velocity triggers, notifications
- **Security** — sandboxing, circuit breaker, secrets, audit, allowlists
- **Troubleshooting** — common issues in problem/cause/fix format
- **REST API** — authentication, roles, rate limits, key endpoints
- **FAQ** — frequently asked questions with cross-references

## Architecture

This skill has two layers:

1. **`prompts/instructions.md`** — behavioral instructions injected into the agent's context. Tells the agent how to use its documentation knowledge: answer from docs, don't dump sections, reference specific topics, acknowledge gaps.

2. **`docs/`** — twelve reference documents with accurate values from the codebase. These serve as the authoritative source and are structured for future dynamic access when skill-directory reading is wired.

## Required Permissions

None. This is a prompt-only skill with no `required_tools` or `required_capabilities`.
