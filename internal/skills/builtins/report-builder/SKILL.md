# Report Builder Skill

Structured report generation using file-manager conventions with data integrity guarantees.

## Overview

The report-builder skill provides templates and rules for generating structured reports. Reports are written to the `output/` directory with date-stamped filenames, following data integrity rules that prevent fabrication and ensure source citation.

## Required Permissions

This skill requires the **writer** permission template or above:
- `filesystem/read/*` — read source data and reference materials
- `filesystem/write/*` — write reports to output directory
- `memory/read/*` — read prior report context
- `memory/write/*` — store report metadata and templates

## Behavior

- Reports follow a standard template: title, executive summary, methodology, findings, analysis, recommendations
- All output goes to `output/` with date-stamped filenames
- Numbers are never fabricated — all data must come from a cited source
- Stale data (older than 7 days) is flagged with a warning
- Intermediate work uses `scratch/` directory
