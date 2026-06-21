# AGENTS.md

Guidance for coding agents implementing this repository.

## Project Intent

`ksuite-mail` is a local, read-only mail gateway for safe personal AI agent access to Infomaniak K-Mail.

The human user is mainly the operator for setup, `doctor`, and diagnostics. The agent is the primary consumer of mail lookup commands, but agents must never receive credentials, raw IMAP access, or non-policy-approved private mail.

## Working From Issues

Issues should identify the narrow documentation slices needed for the task.

Use this split:

- User intent: cite `UC-*`, `FR-*`, and `NFR-*` sections from `docs/requirements.md` and `docs/non-functional-requirements.md`.
- Technical intent: cite `ARCH-*` and `ADR-*` sections from `docs/architecture-arc42.md`.

Read the issue-linked sections first. Do not read or reinterpret every requirement and architecture chapter for a narrow slice unless the issue is missing references, the linked sections conflict, or the implementation touches a cross-cutting boundary.

If an issue lacks precise references, add a comment proposing the needed `UC-*`/`FR-*`/`NFR-*` and `ARCH-*`/`ADR-*` sections before widening behavior.

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

Do not combine unrelated issues on one branch. If an issue uncovers prerequisite work, open or update a separate issue and branch unless the prerequisite is tiny and clearly part of the original slice.

## Security Boundaries

- Keep credentials daemon-side only.
- The CLI must not read, print, log, or receive mailbox credentials.
- The daemon owns IMAP access, policy enforcement, and the SQLite cache.
- Agents and normal users must not read the credential file or SQLite cache directly.
- Do not expose raw IMAP commands through the CLI or daemon API.
- Do not log email bodies, subjects, attachment names, credentials, or raw provider errors that may contain private content.

## First-Version Scope

Implement read-only mail operations only:

- `init`
- `doctor`
- `inbox`
- `list`
- `search`
- `show`
- `thread`
- `context`

Sending, drafting, replying, attachment download, generic IMAP proxying, K-Calendar, and K-Drive are out of scope.

## Mail Policy

Supported first-version policies:

- `full`: expose all messages from the configured account/folders.
- `domain`: expose only messages whose `From`, `To`, `Cc`, or available `Bcc` headers match configured domains.

For `domain` accounts:

- Run server-side `UID SEARCH HEADER` before any `FETCH`.
- Fetch only matching UIDs.
- Re-validate exact domains locally before writing cache rows or returning responses.
- Body text, quoted history, signatures, attachment contents, `Sender`, `Reply-To`, and transport headers are not policy-match inputs.

## Refresh And Cache

- Do not implement background mail refresh in v1.
- Relevant commands trigger bounded on-demand refresh before cache queries.
- Use IMAP UID state as the remote freshness mechanism: `UIDVALIDITY`, `UIDNEXT`, UID ranges, and `HIGHESTMODSEQ`/CONDSTORE when supported.
- Use `BODY.PEEK` or read-only fetch behavior so read-only access does not mark messages as seen.
- Store only policy-approved content in SQLite.
- Cache TTL is based on `last_loaded_or_verified_at`, not message date.
- Hashes may be stored for local diagnostics or dedupe after policy-approved fetches; do not rely on remote body hashes as the primary freshness mechanism.

## Setup Boundary

`sudo ksuite-mail init` may create local users, files, directories, and systemd units. It must not mutate remote mailbox content.

Expected local boundary:

- service user: `ksuite-mail`
- socket access: existing local user group by default, optional dedicated group for multi-user deployments
- no agent-specific OS user created by this project

## Probe Rules

Provider probing must be a fixed checklist, not an arbitrary IMAP command runner.

Allowed probe areas:

- `CAPABILITY`
- `LIST`
- `EXAMINE` or read-only `SELECT`
- `UIDVALIDITY`
- `UIDNEXT`
- `UID SEARCH HEADER`
- UID range search
- `BODY.PEEK`
- `CONDSTORE` / `HIGHESTMODSEQ`
- Sent folder naming and `Bcc` availability

Probe output must be sanitized and should report capabilities, booleans, counts, and safe diagnostics only.

## Implementation Standards

- Use Go for production code.
- Keep `go-imap/v2/imapclient` behind a narrow adapter.
- Prefer small, auditable interfaces over broad abstractions.
- Add tests for policy boundaries, search-before-fetch ordering, credential redaction, structured stale results, and cache invalidation.
- Keep JSON responses compact and stable for agent consumption.

## Quality Gates

The repository should expose these canonical commands once Go code exists:

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

- `make fmt`: run modern Go formatting, at minimum `gofmt` or `go fmt ./...`; add `goimports` when imports need automatic grouping.
- `make lint`: run `go vet ./...` and `golangci-lint run ./...`.
- `make test`: run normal unit and integration tests with `go test ./...`.
- `make test-e2e`: run hermetic end-to-end tests. Use fake IMAP servers, fixtures, or local test daemons; do not require real Infomaniak credentials.
- `make vuln`: run Go dependency and vulnerability checks. Use `go mod verify` plus `govulncheck ./...`. Go has no exact built-in `npm audit` equivalent; `govulncheck` is the intended vulnerability scanner.
- `make security`: run Semgrep locally when available, for example `semgrep scan --config auto`.
- `make gate`: run the full local pre-PR gate: formatting check, lint, unit/integration tests, vulnerability checks, security checks, and hermetic e2e tests.

If a command cannot run in the current environment, document the blocker in the PR and keep the Makefile target behavior explicit.

## Git Hooks

Add and maintain project-local hook scripts when the Go module is introduced.

Expected behavior:

- pre-commit: run formatter and linter checks before allowing a commit.
- pre-push: run tests before pushing.

The hooks should call the canonical Makefile targets instead of duplicating command logic.

Recommended mapping:

```bash
pre-commit -> make fmt && make lint
pre-push   -> make test
```

If hermetic e2e tests are fast and reliable, pre-push may also run `make test-e2e`. Live Infomaniak probing must not run automatically from hooks.

## Pull Request CI

Every PR should run GitHub Actions with:

- formatting check
- Go linting
- `go test ./...`
- hermetic e2e tests
- Go module verification and vulnerability scan: `go mod verify` and `govulncheck ./...`
- Semgrep security scan

Do not run live Infomaniak mailbox probes in normal PR CI. Keep live provider probing as an explicit local/manual diagnostic through the fixed sanitized probe path.

## Commit Hygiene

Group changes semantically.

Prefer small iterative commits that each leave the branch in a coherent state:

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
