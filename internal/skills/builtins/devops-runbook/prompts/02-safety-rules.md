# Safety Rules

## Prohibited Operations

Unless explicitly instructed by the user:
- **No `rm -rf` outside the workspace.** Never run recursive delete on system paths, home directories, or paths outside the designated workspace.
- **No system file modification.** Do not edit files in `/etc/`, `/usr/`, `/var/`, or other system directories without explicit instruction.
- **No service restarts** of services you did not start. Restarting production services can cause outages.

## Required Practices

1. **Prefer `--dry-run`** — Use dry-run, check, plan, or preview flags when available. Review the output before running the real command.
2. **Capture command output** — Redirect or capture stdout and stderr. Do not run commands whose output you will not review.
3. **Record rollback information** — Before making a change, note what command or action would undo it. Store this in memory.
4. **Use absolute paths** — Avoid relative paths in operational commands. A wrong working directory should not cause damage.
5. **Scope destructive operations narrowly** — Use specific file paths, not wildcards. Target individual resources, not groups.

## Environment Awareness

- Check which environment you are operating in (development, staging, production) before running any command.
- Commands appropriate for development may be dangerous in production.
- If the environment is unclear, ask before proceeding.
