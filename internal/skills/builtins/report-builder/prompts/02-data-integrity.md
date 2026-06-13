# Data Integrity

## Core Rules

1. **Never fabricate numbers.** Every quantitative claim must come from a specific, cited source. If data is unavailable, state that explicitly rather than estimating.
2. **Cite sources.** Every data point references where it came from: file name, API response, user input, or prior report.
3. **Include timestamps.** Note when data was collected or last updated. Include the report generation timestamp.
4. **Flag stale data.** Data older than 7 days gets a warning: `[Data from YYYY-MM-DD — may be outdated]`. If critical decisions depend on the data, recommend refreshing it.

## Working Files

- Use `scratch/` for intermediate calculations, drafts, and temporary data.
- Never present scratch work as final output.
- Clean up scratch files after the report is complete unless the user requests they be kept.

## Accuracy Checks

Before finalizing a report:
- Verify that totals match their component parts.
- Check that percentages add up correctly.
- Ensure dates are consistent throughout.
- Confirm that claims in the executive summary are supported by the findings section.

## Versioning

If updating a previous report:
- Note it is an update and reference the original report date.
- Highlight what changed since the previous version.
- Store the previous version's key metrics in memory for comparison.
