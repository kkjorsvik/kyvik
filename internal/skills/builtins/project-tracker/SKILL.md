# Project Tracker Skill

Task management conventions for agents using external integrations (Linear, Notion, Jira, etc.).

## Overview

The project-tracker skill provides a task lifecycle methodology for agents that manage work items through API integrations. It covers task creation, status tracking, progress reporting, and session-start status checks.

## Required Permissions

This skill requires the **writer** permission template or above:
- `memory/read/*` — read project context from memory
- `memory/write/*` — store task state and project context

The `rest_api` tool is required for communicating with external project management services.

## Behavior

- Tasks follow a defined lifecycle: receive, check, create/update, track, report
- Memory stores project context between sessions
- Status reports use a structured format: task, status, next action
- Session start includes checking active tasks for updates
