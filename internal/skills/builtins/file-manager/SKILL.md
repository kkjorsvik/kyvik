# File Manager Skill

Provides organized file operations with workspace conventions and safety checks.

## Overview

The file-manager skill teaches agents how to work with their workspace filesystem
in a structured, predictable way. It enforces conventions for input/output directories
and adds safety checks before destructive operations.

## Required Permissions

This skill requires the **worker** permission template or above:
- `filesystem/read/*` — read files anywhere in the workspace
- `filesystem/write/*` — create and modify files in the workspace

Agents with the **reader** template cannot use this skill (no write access).

## Workspace Conventions

| Directory  | Purpose                                    |
|------------|--------------------------------------------|
| `input/`   | Source files provided by the user (read-only by convention) |
| `output/`  | Results produced by the agent              |
| `scratch/` | Temporary working files (auto-cleaned)     |

## Behavior

- Input files are never modified or deleted
- Output is always written to `output/` with clear naming
- Existing files are checked before overwrite — agent confirms or uses a new name
- Operations are reported with paths and sizes
