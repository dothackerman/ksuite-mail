---
title: "feat: Refresh-strategy compatibility probe"
date: "2026-07-01"
type: "feat"
artifact_contract: "ce-unified-plan/v1"
artifact_readiness: "implementation-ready"
product_contract_source: "ce-plan-bootstrap"
execution: "code"
origin: "https://github.com/dothackerman/ksuite-mail/issues/17"
---

## Goal Capsule

| Field | Value |
|---|---|
| Objective | Extend the fixed daemon-side IMAP provider probe so it returns machine-readable refresh-strategy facts (`MODSEQ`/`HIGHESTMODSEQ` vs UID-range fallback) without parsing human-readable detail text. |
| Authority | GitHub issue #17 and the linked requirements/architecture sections are authoritative. |
| Execution profile | Narrow Go implementation in the provider probe and API contract, with coverage in fake adapter, daemon/CLI contract tests, and UAT docs. |
| Stop conditions | Stop if the probe starts exposing credentials, message subjects/bodies, raw IMAP command/error text, or arbitrary provider content as part of facts. |
| Tail ownership | LFG owns implementation, review fixes, local gates, PR creation, and CI follow-up unless a genuine product/security conflict appears. |

---

## Product Contract

### Summary

`ksuite-mail probe imap --account <account-ref> --json` currently reports fixed IMAP diagnostics for the selected account. It now also needs to report whether a refresh can rely on `CONDSTORE`/`HIGHESTMODSEQ` or should use UID-range refresh behavior through safe, typed facts.

### Problem Frame

Issue #16 added folder-state facts and UID behavior checks. Issue #17 narrows the remaining gap: choosing refresh strategy should be machine-readable and safe (no raw text parsing) so that refresh code can make a stable decision.

### Requirements

- R1. Report `CONDSTORE` and `HIGHESTMODSEQ` support as safe structured facts, or `inconclusive` when prerequisites are missing.
- R2. Report UID range behavior as structured facts that can be consumed directly.
- R3. Add a structured strategy outcome (`modseq` or `uid_range`) with no reliance on free-form detail parsing.
- R4. Ensure missing fixtures/conditions produce `inconclusive` for strategy decision, not false support/unsupported.
- R5. Keep existing redaction/privacy and fixed-checklist boundaries.
- R6. Add tests for supported/unsupported/inconclusive refresh-strategy behavior through fake behavior and daemon path.

### Sources

- `docs/requirements.md`: FR-014, FR-006, FR-007.
- `docs/non-functional-requirements.md`: NFR-SEC-002, NFR-SEC-005, NFR-ERR-002, NFR-REL-003.
- `docs/architecture-arc42.md`: ADR-007, ADR-006, sections around UID search/range and HIGHESTMODSEQ decisions.
- `docs/uat/infomaniak-imap-probe.md`: UAT-IMAP-PROBE-003.

## Implementation Units

### U1. Structured Refresh Strategy Fact

- **Goal:** Add a new fixed-checklist result for refresh strategy compatibility and emit stable fields.
- **Files:** `internal/api/api.go`, `internal/providerprobe/imap.go`.
- **Approach:**
  - Add structured fields in `api.ProbeFacts`:
    - `condstore_supported`
    - `highestmodseq_available`
    - `uid_range_supported`
    - `refresh_strategy`
  - Extend `RunIMAP` to append a `refresh_strategy` check after UID behavior is known.
  - `refresh_strategy` must resolve to:
    - `modseq` when CONDSTORE/HIGHESTMODSEQ evidence is sufficient.
    - `uid_range` when UID range behavior is supported.
    - `inconclusive` when either `CAPABILITY`, folder selection, or UID-range prerequisites are missing.
  - Keep `uid_behavior` check with `status+code` and add facts to avoid parsing text.

### U2. Probe Fact Coverage and Guards

- **Goal:** Ensure strategy depends only on bounded safe checks and remains `inconclusive` on missing fixtures.
- **Files:** `internal/providerprobe/imap.go`.
- **Approach:**
  - Compute strategy from existing checks using structured results from capability list and `folder_selection`/`uid_behavior` outcomes.
  - Never assert `unsupported` unless behavior is proven; use `inconclusive` for missing fixture conditions.
  - Preserve existing order and failure short-circuit behavior.

### U3. Verification and Regression Tests

- **Goal:** Cover refresh strategy cases with hermetic tests.
- **Files:** `internal/providerprobe/imap_test.go`, `internal/daemon/daemon_test.go`, `internal/api/api_test.go`.
- **Approach:**
  - Unit test strategy outcomes:
    - modseq-supported,
    - uid-range fallback,
    - inconclusive when CONDSTORE missing fixture state,
    - inconclusive when UID fixtures missing.
  - Verify `uid_behavior` and `refresh_strategy` facts include typed booleans and strategy string.
  - Verify CLI daemon response includes the new check with safe facts and no leaked raw text.

### U4. UAT Clarification

- **Goal:** Update UAT-IMAP-PROBE-003 to call out structured strategy compatibility.
- **Files:** `docs/uat/infomaniak-imap-probe.md`.
- **Approach:**
  - Add explicit success criteria for the strategy decision and structured fields so the manual live-probe path can record the refresh choice without inspecting details.

## Verification Contract

- `make fmt`: gofmt/goimports clean for new fields and tests.
- `make test`: unit/integration pass, including provider probe and daemon coverage.
- `make lint`: vet and lints pass.
- `make test-e2e`: keep hermetic coverage; no live IMAP credentials required.
- `make gate`: local pre-PR gate pass, or blockers documented.
