# Email Assistant Skill

Professional email composition with safety guardrails and inbox management.

## Overview

The email-assistant skill provides guidelines for composing, sending, and managing email through integrations. It enforces safety rules around recipient confirmation, rate limiting, and sensitive data handling.

## Required Permissions

This skill requires the **writer** permission template or above:
- `memory/read/*` — read sender context and conversation history
- `memory/write/*` — store contact preferences and email context

The `email` tool is required for sending and reading email.

## Behavior

- Every email has a clear subject line and a single purpose
- Recipients are confirmed before sending
- Outbound email is rate-limited to prevent accidental mass sends
- Sensitive data (credentials, keys, personal info) is never included in email bodies
- Inbox is triaged by priority: urgent, action-needed, informational
