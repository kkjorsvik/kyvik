# Contributing to Kyvik

Thanks for your interest in Kyvik! This project is in **pre-alpha**, so
interfaces and internals are still moving. Contributions, bug reports, and
design feedback are all welcome.

By participating you agree to abide by our [Code of Conduct](CODE_OF_CONDUCT.md).

## Getting Started

Kyvik is written in Go and backed by PostgreSQL. It builds two binaries: the
main server (`cmd/kyvik`) and the sandbox runner (`cmd/kyvik-sandbox`).

**Prerequisites:**

- Go (see `go.mod` for the required version)
- PostgreSQL (the only supported database)
- `golangci-lint` for linting
- Docker (optional — `make docker-build` / `make docker-run`)

**Setup:**

```bash
git clone https://github.com/kkjorsvik/kyvik.git
cd kyvik

# Bring up PostgreSQL (a docker-compose.yml is provided)
docker compose up -d db

# Copy the example config and set secrets via environment variables
cp configs/kyvik.example.yaml kyvik.yaml

make dev      # run with `go run` (no build artifact)
# or
make build && make run
```

Schema migrations are embedded and applied automatically at startup.

## Development Workflow

```bash
make test     # run all tests
make lint     # run golangci-lint
make build    # build ./build/kyvik
```

Run a single package's tests:

```bash
go test ./internal/store/... -v -run TestName
```

## Pull Request Guidelines

Before opening a PR, make sure:

- [ ] `make test` passes
- [ ] `make lint` passes
- [ ] New behavior has test coverage
- [ ] You've considered the security implications (this is a security-first
      project — see the design principles below)
- [ ] Commits are focused and have clear messages

Open PRs against the default branch. Keep changes scoped; large or
architecture-changing PRs are easier to review with a short design note or a
linked issue first.

## Design Principles

When implementing features, follow these priorities (from `DESIGN.md`):

1. **Security first** — deny-by-default, audit everything, sandbox all execution
2. **Accessible** — sensible defaults, usable by non-experts via the dashboard
3. **Multi-agent native** — each agent isolated with its own identity,
   permissions, and sandbox
4. **Go-native simplicity** — self-contained binaries, no runtime dependencies

`DESIGN.md` is the source of truth for architecture; `CLAUDE.md` has a concise
map of the codebase layout and key patterns.

## Reporting Bugs & Requesting Features

Use the GitHub issue templates. For **security vulnerabilities**, do not open a
public issue — follow [`SECURITY.md`](SECURITY.md) instead.
