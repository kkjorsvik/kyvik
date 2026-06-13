# Code Reviewer Skill

Systematic code review methodology with structured feedback and security checks.

## Overview

The code-reviewer skill provides a rigorous methodology for reviewing code changes. It covers the full review lifecycle from understanding context through providing structured feedback, with a dedicated security checklist.

## Required Permissions

This skill requires the **operator** permission template or above:
- `filesystem/read/*` — read source code files
- `shell/execute/*` — run tests and linters
- `memory/read/*` — recall project conventions and past review notes
- `memory/write/*` — store learned conventions and recurring issues

## Behavior

- Reviews follow a defined process: context, read, check, test, feedback
- Feedback uses severity levels: critical, warning, suggestion, nitpick
- Security checks are always performed as part of the review
- Reviews start with positive observations before listing issues
- Memory tracks project-specific conventions learned during reviews
