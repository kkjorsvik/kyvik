# AGENTS.md

Operational guide for coding agents in this repository.
Kyvik is a Go codebase with security-first defaults and pre-alpha scope.

## 1) Repository Snapshot

- Module: `github.com/kkjorsvik/kyvik`
- Go version: `1.24`
- Main binaries: `cmd/kyvik` and `cmd/kyvik-sandbox`
- Key docs: `README.md`, `DESIGN.md`, `CLAUDE.md`

## 2) Build / Run / Lint / Test Commands

Use Make targets first; they reflect project defaults.

### Make targets

- Build server: `make build`
- Build sandbox runner: `make build-sandbox`
- Build both binaries: `make build-all`
- Run app (build + execute): `make run`
- Dev run (no artifact): `make dev`
- Run all tests: `make test`
- Lint: `make lint`
- Apply local migration: `make migrate`
- Clean artifacts: `make clean`

### Direct Go commands

- All tests, verbose: `go test ./... -v`
- Package-scoped tests: `go test ./internal/security/... -v`
- Single test (name contains): `go test ./internal/store/... -v -run TestName`
- Single test (exact match): `go test ./internal/store/... -v -run '^TestName$'`
- Subtests/pattern: `go test ./internal/router/... -v -run 'TestRoute_.*'`
- Disable cache for retries: `go test ./internal/skills/... -v -count=1`
- Race check: `go test ./... -race`
- Coverage check: `go test ./... -cover`

### Lint

- Primary: `golangci-lint run ./...`
- Equivalent: `make lint`

## 3) Runtime / Config Notes

- Default (and only supported) storage is PostgreSQL.
- Migrations are in `migrations/`.
- Config path is `kyvik.yaml`; fallback is `configs/kyvik.example.yaml`.
- Common secrets via env vars:
  - `KYVIK_OPENROUTER_API_KEY`
  - `KYVIK_OPENAI_API_KEY`
  - `KYVIK_ANTHROPIC_API_KEY`
  - `KYVIK_SLACK_BOT_TOKEN`
  - `KYVIK_SLACK_APP_TOKEN`
  - `KYVIK_MASTER_KEY`

## 4) Go Code Style Guidelines

Follow patterns already used in `internal/`, `pkg/`, `web/`, and `cmd/`.

### Formatting and structure

- Run `gofmt` on every changed Go file.
- Keep functions focused; prefer early returns over deep nesting.
- Break long calls/struct literals into readable multiline blocks.
- Keep comments concise and meaningful; avoid redundant comments.

### Imports

- Keep import groups in this order:
  1. standard library
  2. third-party packages
  3. local module imports (`github.com/kkjorsvik/kyvik/...`)
- Do not manually align imports; let `gofmt` handle formatting.
- Use blank imports only when required (for example, SQL driver registration in tests).

### Types and interfaces

- Prefer concrete structs plus constructors for wiring subsystems.
- Keep interfaces close to where they are consumed.
- Use compile-time interface assertions when useful:
  - `var _ SomeInterface = (*SomeType)(nil)`
- Use `any` only for truly dynamic payloads (tool params, generic maps).

### Naming

- Exported identifiers: `PascalCase` with doc comments.
- Unexported identifiers: `camelCase`.
- Use clear receiver names aligned with package conventions (`q`, `m`, `h`, etc.).
- Test names should be descriptive; `TestXxx_Yyy` is preferred.

### Error handling

- Never silently ignore errors unless explicitly intentional and documented.
- Wrap returned errors with context using `%w`.
- Reuse sentinel errors from `pkg/types/errors.go` when semantics match.
- In binaries, `log.Fatalf` is acceptable for startup/fatal conditions.
- In libraries/internal packages, return errors rather than logging and continuing.

### Logging

- Prefer structured logging (`log/slog`) in core/internal services.
- Add stable context fields such as `agent_id`, `id`, or `trigger`.
- Never log secrets, tokens, raw API keys, or master keys.

### Context, concurrency, lifecycle

- Accept `context.Context` as first parameter for I/O and long-running operations.
- Propagate cancellation/timeouts downstream.
- Protect shared mutable state with mutexes or channels.
- Keep goroutine lifecycle explicit; expose stop/shutdown behavior.

### Security expectations

- Preserve deny-by-default permission behavior.
- Keep sandbox boundaries and permission gates intact.
- Audit sensitive actions and permission decisions.
- Validate untrusted input; avoid command/path injection opportunities.

## 5) Testing Guidelines

- Favor table-driven tests for multi-case logic.
- Use `t.Helper()` in shared test helpers.
- Keep tests deterministic; avoid sleep-based timing dependencies.
- Prefer in-memory DB setup for unit/integration tests where possible.
- Validate both behavior and error semantics.

Suggested pre-merge flow:

1. Run targeted package tests for touched code.
2. Run single-test commands for changed logic paths.
3. Run `make test` if changes are broad or cross-cutting.
4. Run `make lint`.

## 6) Web/UI Conventions

- UI is server-rendered (Go templates + HTMX), no JS framework.
- Handlers: `web/handlers/`; templates: `web/templates/`; static: `web/static/`.
- Return correct content type headers and HTMX-friendly fragments where expected.

## 7) Workspace and Git Hygiene

- Do not revert unrelated working tree changes.
- Keep edits tightly scoped to the requested task.
- Avoid destructive git operations unless explicitly requested.
- Prefer small, reviewable diffs over broad refactors.

## 8) Cursor / Copilot Rules

Checked for external instruction files:

- `.cursor/rules/`
- `.cursorrules`
- `.github/copilot-instructions.md`

Current status in this repository:

- No Cursor rules files found.
- No Copilot instructions file found.

If these files are added later, treat them as repository policy and update this document.
