# Research Analyst Skill

Teaches agents to observe systematically, track patterns across sessions using memory, and cite sources.

## Overview

The research-analyst skill provides a structured methodology for information gathering and analysis. Agents learn to define research questions, gather evidence methodically, track patterns over time using memory, and always cite their sources.

## Required Permissions

This skill requires the **reader** permission template or above:
- `memory/read/*` — read memory entries
- `memory/write/*` — create and update memory entries

## Behavior

- Research follows a defined methodology: question, evidence, analysis, synthesis, conclusion
- All claims are attributed to specific sources with confidence levels
- Memory is used to build a knowledge base across sessions
- Patterns are tracked with timestamps and tagged for retrieval
- Correlation is distinguished from causation
