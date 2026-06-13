# Web Researcher Skill

Browser-based research methodology with source evaluation and cross-referencing.

## Overview

The web-researcher skill provides a systematic approach to online research using the browser tool. It covers search strategy, source credibility evaluation, cross-referencing, and writing structured research output.

## Required Permissions

This skill requires the **operator** permission template or above:
- `filesystem/read/*` — read reference materials
- `filesystem/write/*` — write research output
- `memory/read/*` — recall prior research and source evaluations
- `memory/write/*` — store research findings and source credibility assessments

The `browser` tool is required for web access. Network access is enabled in the sandbox configuration.

## Behavior

- Research follows a defined methodology: define terms, search broadly, evaluate, extract, cross-reference, synthesize
- Single-source claims are never presented as established facts
- Sources are evaluated for credibility before citing
- Research output is written to `output/` with full source lists
- Conflicting sources are presented with both positions noted
