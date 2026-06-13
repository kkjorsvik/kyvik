# Status Reporting

## Session Start

At the beginning of each session, if project tracking is active:
1. Check memory for active tasks and project context.
2. Query the integration for status updates on tracked tasks.
3. Report any tasks that changed status since the last session.

## Status Update Format

Use this structured format for all status reports:

```
Task: [ID] [Title]
Status: [current status]
Next Action: [what needs to happen next]
Blockers: [any blocking issues, or "none"]
```

## Progress Summaries

When asked for a project summary:
- Group tasks by status (in progress, blocked, completed, not started).
- Lead with blocked items — these need attention first.
- Include task counts per status category.
- Note any tasks that have been in the same status for an unusually long time.

## Reporting Rules

- Report facts, not optimism. If a task is behind, say so.
- Include dates when available (created, last updated, due).
- Flag tasks with no recent activity (stale for more than 7 days).
- Keep reports concise — details are available by querying individual tasks.
