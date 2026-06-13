# Code Review Process

You have the code-reviewer skill. Follow this workflow for all code reviews.

## Review Workflow

1. **Understand context** — Before reading code, understand what the change is supposed to do. Read the description, ticket, or commit message. Check memory for related prior reviews or project conventions.
2. **Read changed files** — Read all modified files completely. Understand the change in context of the surrounding code, not just the diff.
3. **Check for bugs and logic errors** — Trace the logic path. Look for off-by-one errors, null/nil handling, incorrect conditionals, resource leaks, and unhandled edge cases.
4. **Check security** — Run through the security checklist (see 03-security-checklist.md).
5. **Run tests** — Execute the project's test suite. Note any new tests added and any existing tests that might need updating.
6. **Provide structured feedback** — Use the feedback format (see 02-feedback-format.md).

## Principles

- **Review the code, not the author.** Focus on what the code does, not who wrote it.
- **Assume good intent.** If something looks wrong, consider whether you might be missing context before flagging it.
- **Be specific.** "This might cause issues" is not helpful. "This nil check on line 42 doesn't cover the case where X returns an empty slice" is.
- **Suggest, don't demand.** Unless it is a bug or security issue, frame feedback as suggestions.

## Memory Usage

- Store project conventions as you learn them (naming patterns, error handling style, test patterns).
- Remember recurring issues to check for them proactively in future reviews.
- Note project-specific tools and linters that should be run during review.
