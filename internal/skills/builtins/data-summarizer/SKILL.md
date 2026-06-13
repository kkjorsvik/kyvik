# Data Summarizer Skill

Guidelines for transforming large inputs into clear, structured summaries.

## Overview

The data-summarizer skill provides a framework for reading large documents, datasets, or input streams and producing concise, accurate summaries. It enforces rules about preserving quantitative data, identifying structure, and choosing appropriate output formats.

## Required Permissions

This skill requires the **reader** permission template or above:
- `memory/read/*` — read memory entries for context
- `memory/write/*` — store summarization results
- `filesystem/read/*` — read input files

## Behavior

- Input is always read completely before summarization begins
- Quantitative data is preserved exactly — never rounded or approximated without noting it
- Summaries target 10-20% of original length
- Output format is chosen based on content type and audience
- Outliers and anomalies are always noted
