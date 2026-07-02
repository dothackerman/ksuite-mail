---
title: "feat: Domain-search fixture probe"
date: "2026-07-01"
type: "feat"
artifact_contract: "ce-unified-plan/v1"
artifact_readiness: "implementation-ready"
product_contract_source: "issue"
execution: "code"
origin: "https://github.com/dothackerman/ksuite-mail/issues/18"
---

## Goal Capsule

| Field | Value |
|---|---|
| Objective | Extend the fixed daemon-side IMAP provider probe to validate domain-header search behavior against controlled fixtures for `From`, `To`, `Cc`, and `Bcc` headers, and report safe, structured counts/booleans for fixture-aware results. |
| Authority | GitHub Issue #18, Requirements/Architecture/UAT sources listed below. |
| Execution profile | Standard Go implementation with deterministic fake-source tests and CLI/daemon contract coverage. |
| Stop conditions | Stop if implementation requires exposing message subjects, bodies, raw headers, attachment names, raw provider text, credentials, raw IMAP command capabilities, or a raw IMAP command surface through the CLI or response payload. |
| Tail ownership | LFG owns implementation, review fixes, local gates, PR creation, and CI follow-up unless a product/security conflict appears. |

---

## Product Contract

The public command `ksuite-mail probe imap --account <account-ref> --json` still uses the fixed daemon-side checklist and account-scoped credential resolution.

For `policy = "domain"` accounts, the probe should assert fixture-driven behavior for:

- `UID SEARCH HEADER From <domain>`
- `UID SEARCH HEADER To <domain>`
- `UID SEARCH HEADER Cc <domain>`
- `UID SEARCH HEADER Bcc <domain>` when available

It must report safe fixture outcomes with counts and booleans, and return `inconclusive` for missing fixture coverage.

---

## Problem Frame

Issue #16 delivered initial folder-state probes. Issue #18 requires extending that work so domain-header probing is fixture-driven and explicitly records safe search-facet results. The work must remain diagnostic-only and boundary-safe:

- no raw IMAP execution path in the CLI,
- no credential leakage,
- no message content leakage,
- no raw provider responses in JSON.

## Requirements

- R1. Probe `UID SEARCH HEADER` for `From`, `To`, and `Cc` on configured domains with fixture results and no content leakage.
- R2. Probe `UID SEARCH HEADER Bcc` from sent mail context when available and safe.
- R3. Emit safe structured facts that include matched counts and boolean outcomes for each fixture-driven header/domain slice.
- R4. Return `inconclusive` with stable codes when fixture coverage is missing for applicable domain-policy checks.
- R5. Keep the non-matching fixture invisible in probe output and maintain current call ordering (`capability -> folders -> select -> search`).
- R6. Extend UAT scenario coverage for domain-search fixture behavior.

## Scope Boundaries

- In scope: provider probe logic, fake adapter fixtures, deterministic provider-probe tests, daemon/CLI boundary tests where needed, UAT docs.
- Out of scope: changing CLI argument grammar, adding raw IMAP features, live Infomaniak network probing in CI.

## Open Questions

- None beyond `Bcc` availability handling, resolved below as: only run/record `Bcc` fixture checks where sent-folder context is present; report omission as fixture-based inconclusive state.

## Implementation Units

### U1. Add Structured Domain-Header Probe Facts

- **Goal:** make `domain_header_search` output machine-usable, fixture-safe facts.
- **Requirements:** R1, R2, R3, R4.
- **Files:** `internal/api/api.go`, `internal/providerprobe/imap.go`, `internal/api/api_test.go`.
- **Approach:**
  - Extend `api.ProbeFacts` with a structured domain-header payload (domain + header result counts and non-matching visibility booleans).
  - Populate a `domain_search`-typed fact set in `probeDomainHeaders` while keeping existing `Detail` text stable and non-sensitive.
  - Keep `remote_failed`/`permission_denied`/`fixture_required`/`inconclusive` codes stable.
- **Verification:** add unit serialization/assertion tests for the new fact payload.

### U2. Expand Fixture Probe Coverage and Ordering Tests

- **Goal:** prove fixture-driven matching/nonmatching behavior and call-order invariants.
- **Requirements:** R1, R2, R4, R5.
- **Files:** `internal/providerprobe/imap_test.go`, `internal/daemon/daemon_test.go`, `cmd/ksuite-mail/probe_test.go` if necessary.
- **Approach:**
  - Add matrix cases for per-header fixture matches, missing fixtures by header/domain, and non-matching fixture visibility.
  - Assert the probe checks all required domain headers and sent-folder `Bcc` routing where applicable.
  - Keep leak checks in place to prevent private content text and raw provider text in response JSON.
- **Verification:** failing raw text assertions continue to reject fixture/provider leakage.

### U3. UAT Documentation Update

- **Goal:** record fixture expectations and outcomes for Issue #18.
- **Requirements:** R6.
- **Files:** `docs/uat/infomaniak-imap-probe.md`.
- **Approach:** clarify UAT-IMAP-PROBE-004 expectations for structured counts/booleans and non-matching visibility.
- **Verification:** scenario outcome checklist remains explicit and no raw artifacts are committed.

## Verification Contract

| Gate | Applies To | Done Signal |
|---|---|---|
| `make fmt` | U1-U3 | Go formatting passes. |
| `make test` | U1-U3 | All tests pass, including provider probe matrix and daemon/CLI checks. |
| `make test-e2e` | U2 | Hermetic e2e tests pass without credentials. |
| `make lint` | U1-U3 | `go vet` and `golangci-lint` pass. |
| `make gate` | Whole slice | Full local pre-PR gate passes. |
