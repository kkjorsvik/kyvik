# Skills

## What Is a Skill?

A skill is a packaged bundle of prompts, documentation, and capability declarations that extends what an agent knows or can do. Skills are loaded at agent startup and their prompt content is injected into the agent's context.

## Skill Structure

A skill directory contains:

```
my-skill/
  skill.yaml         # Manifest: name, version, requirements
  SKILL.md            # Human-readable documentation
  prompts/            # Prompt files injected into agent context
    instructions.md
  docs/               # Reference documentation (optional)
```

The `skill.yaml` manifest declares:
- `name`, `version`, `description`, `author`, `license`
- `required_tools` — tools the agent must have access to
- `required_capabilities` — capability triplets the agent must have
- `sandbox` — optional sandbox constraints (network, paths)

## Trust Tiers

Skills have a trust tier that controls how they're handled:

| Tier | Approval | Warning | Source |
|------|----------|---------|--------|
| `builtin` | None needed | None | Ships with Kyvik binary |
| `verified` | None needed | None | Verified by Kyvik project |
| `community` | Required | "Has not been reviewed... may modify agent behavior" | Community contributed |
| `local` | None needed | Informational | Created locally by operator |

## Installing Skills

- **Built-in skills** are installed automatically on startup
- **Community skills** require explicit approval before use
- **Local skills** are placed in the skills directory and loaded at startup

## Granting Skills to Agents

Skills are assigned to agents in their configuration. When an agent starts, the skill loader:

1. Reads the manifest
2. Checks that the agent's tier meets all `required_tools` and `required_capabilities`
3. If requirements are met, injects prompt content into the agent's context
4. If not, the skill is skipped with a warning

## Prompt-Only Skills

Skills with no `required_tools` or `required_capabilities` are prompt-only. They work with any agent regardless of tier. The system-docs skill is an example — it provides documentation knowledge without needing any tool access.

## Creating a Skill

1. Create a directory with the skill structure above
2. Write a `skill.yaml` manifest
3. Add prompt files in `prompts/` — these get injected into agent context
4. Add `SKILL.md` for human documentation
5. Place in the skills directory or package for distribution
