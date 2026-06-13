# DevOps Runbook Skill

Safe operational procedures for system automation with check-act-verify loops and incident response.

## Overview

The devops-runbook skill provides a disciplined methodology for system operations. Every change follows a check-act-verify loop. Destructive operations are guarded by safety rules, and incidents have a structured response protocol.

## Required Permissions

This skill requires the **operator** permission template or above:
- `shell/execute/*` — run system commands
- `filesystem/read/*` — read configuration and log files
- `filesystem/write/*` — write operation logs and runbook output
- `memory/read/*` — recall system state and prior operations
- `memory/write/*` — store system state and operation history

## Behavior

- Every change follows check-act-verify: check current state, make the smallest change, verify the effect
- Destructive operations are never chained — one change at a time
- All command output is captured and logged
- Dry-run mode is preferred when available
- Rollback information is recorded before making changes
- Incidents trigger a stop-capture-document-fix-escalate response
