# AGENTS.md

Operational guidance for coding agents working in this repository.

This file is not the product roadmap and not the architecture source of truth. Treat the assigned GitHub issue as the task. Use the issue's linked documentation sections for context.

## Issue-First Workflow

Start every task from the GitHub issue.

Each issue should identify narrow documentation references:

- User intent: `UC-*`, `FR-*`, and `NFR-*` sections from `docs/requirements.md` and `docs/non-functional-requirements.md`.
- Technical intent: `ARCH-*` and `ADR-*` sections from `docs/architecture-arc42.md`.

Read the issue-linked sections first. Do not reread or reinterpret the full requirements and architecture for a narrow slice unless:

- the issue is missing references
- linked sections conflict
- implementation touches a cross-cutting security, cache, IPC, or deployment boundary

If references are missing or insufficient, comment on the issue with the sections you believe apply before widening behavior.

If code and docs disagree, stop and update the docs or ask for a product decision before implementation continues.

## Branches

Each issue gets its own branch.

Branch naming:

```text
issue-<id>-<issue-title-as-kebab-case>
```

Examples:

```text
issue-1-bootstrap-secure-local-init-and-deployment-boundary
issue-4-probe-infomaniak-imap-through-fixed-sanitized-daemon-checklist
```

Do not combine unrelated issues on one branch. If prerequisite work appears, open or update a separate issue unless the prerequisite is tiny and clearly part of the current slice.

## Security Rules

These rules apply to every implementation slice:

- Keep credentials daemon-side only.
- The CLI must not read, print, log, or receive mailbox credentials.
- Agents and normal users must not read the credential file or SQLite cache directly.
- Do not expose raw IMAP commands through the CLI or daemon API.
- Do not log email bodies, subjects, attachment names, credentials, or raw provider errors that may contain private content.
- Live Infomaniak probing must stay behind the fixed sanitized daemon path. It must not run in PR CI, git hooks, or normal tests.

## Coding Standards

- Use Go for production code.
- Keep `go-imap/v2/imapclient` behind a narrow adapter.
- Prefer small, auditable interfaces over broad abstractions.
- Keep JSON responses compact and stable for agent consumption.
- Add tests for policy boundaries, search-before-fetch ordering, credential redaction, structured stale results, and cache invalidation when touching those areas.

## Quality Gates

Use the canonical Makefile targets:

```bash
make fmt
make lint
make test
make test-e2e
make vuln
make security
make gate
```

Expected meanings:

- `make fmt`: run Go formatting. Use `gofmt` or `go fmt ./...`; use `goimports` for import grouping when available.
- `make lint`: run `go vet ./...` and `golangci-lint run ./...`.
- `make test`: run normal unit and integration tests with `go test ./...`.
- `make test-e2e`: run hermetic end-to-end tests with fake IMAP servers, fixtures, or local test daemons. Do not require real Infomaniak credentials.
- `make vuln`: run Go module verification and vulnerability checks with `go mod verify` and `govulncheck ./...`.
- `make security`: run Semgrep, normally `semgrep scan --config auto --error`.
- `make gate`: run the full local pre-PR gate.

Go has no exact built-in `npm audit` equivalent. Use `govulncheck` for Go vulnerability analysis and Dependabot for dependency update PRs.

If a command cannot run in the current environment, document the blocker in the PR and keep the Makefile target behavior explicit.

## Git Hooks

Project-local hooks live in `.githooks/`.

Install them with:

```bash
make install-hooks
```

Expected behavior:

- pre-commit runs formatting and lint checks.
- pre-push runs tests.

Hooks call Makefile targets. Do not duplicate command logic in hook scripts.

## Pull Request CI

Every PR should run GitHub Actions with:

- formatting check
- Go linting
- `go test ./...`
- hermetic e2e tests
- `go mod verify`
- `govulncheck ./...`
- Semgrep security scan

Keep PR CI hermetic. Do not require real mailbox credentials or live Infomaniak access.

## Commit Hygiene

Group changes semantically.

Prefer small iterative commits that each leave the branch coherent:

- docs-only decisions in their own commit
- setup/build plumbing in its own commit
- daemon behavior in its own commit
- cache or policy logic in its own commit
- tests with the behavior they validate, unless separating them improves review clarity

Push iteratively while working so remote state reflects meaningful progress.

## PR Workflow

When an issue implementation is complete:

1. Run `make gate`.
2. Push the issue branch.
3. Open a draft PR.
4. Perform a self-review of the diff, docs, tests, logs, and security boundaries.
5. Fix issues found during self-review with follow-up commits on the same branch.
6. Mark the PR ready for review only after self-review passes and the quality gate is green or documented with a concrete blocker.

