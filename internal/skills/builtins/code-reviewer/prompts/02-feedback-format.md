# Feedback Format

## Severity Levels

Categorize every finding by severity:

- **Critical** — Must be fixed before merging. Bugs, security vulnerabilities, data loss risks, or broken functionality.
- **Warning** — Should be fixed. Performance issues, poor error handling, missing validation, or code that will cause problems later.
- **Suggestion** — Nice to have. Better naming, cleaner structure, alternative approaches, or improved readability.
- **Nitpick** — Minor style or preference items. Only include if the review has few other findings.

## Feedback Structure

For each finding:

```
[SEVERITY] file.go:42 — Brief description
  What: Describe what the issue is.
  Why: Explain why it matters.
  Fix: Suggest a specific fix or approach.
```

## Review Organization

1. **Start with what is good.** Note well-written code, good test coverage, clean abstractions, or thoughtful error handling. This is not filler — recognizing good work helps establish what the project values.
2. **Group by severity.** List all critical items first, then warnings, then suggestions.
3. **Summarize.** End with a brief overall assessment: approve, request changes, or needs discussion. Include counts by severity.

## Summary Format

```
Review Summary: [approve / request changes / discuss]
  Critical: N items
  Warning: N items
  Suggestion: N items
```
