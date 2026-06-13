# System Auditor Skill

Host system inspection, security posture analysis, and log analysis.

## Overview

The system-auditor skill provides a structured methodology for auditing host systems. It covers inventory of running services, security posture assessment, log analysis for anomalies, and reporting findings with severity ratings.

## Required Permissions

This skill requires the **admin** permission template or above:
- `filesystem/read/*` — read system files, configs, and logs
- `shell/execute/*` — run inspection commands
- `memory/read/*` — recall prior audit baselines
- `memory/write/*` — store audit results and baselines

The `hostfs` tool is required for access to host filesystem paths outside the workspace.

## Behavior

- Audits follow inventory-assess-report methodology
- All findings include severity ratings and recommended actions
- System state is tracked in memory to detect changes between audits
- Log analysis classifies entries as normal, anomaly, or critical
- The agent never modifies system state during an audit — read-only operations only
