---
title: "feat: Read-only BODY.PEEK read-state probe"
date: "2026-07-01"
type: "feat"
artifact_contract: "ce-unified-plan/v1"
artifact_readiness: "implementation-ready"
product_contract_source: "https://github.com/dothackerman/ksuite-mail/issues/19"
execution: "code"
---

## Goal Capsule

| Field | Value |
|---|---|
| Objective | Extend the fixed daemon-side IMAP provider probe so BODY.PEEK checks can prove whether message seen state changes on read, and return a safe structured boolean/inconclusive result. |
| Authority | GitHub issue #19 and the cited requirements/architecture/UAT sections are authoritative. |
| Execution profile | Go implementation in `internal/providerprobe`, `internal/mail`, `internal/imapadapter`, `internal/mailfake`, and `internal/daemon` test stubs plus UAT updates. |
| Stop conditions | Stop if implementation would return message bodies, subjects, raw provider responses, raw headers, attachment names, credentials, or private provider text. |

---

## Product Contract

The command `ksuite-mail probe imap --account <account-ref> --json` remains daemon-side and fixed-checklist-only.

The `read_state` check must report:

- `passed` + `read_state_preserved=true` when BODY.PEEK does not alter seen state for a bounded fixture message.
- `failed` with a safe code when BODY.PEEK marks seen state.
- `inconclusive` when fixture availability prevents a valid behavior check.

The response shape must stay content-free and include only safe structured data.

## Requirements

- R1. `BODY.PEEK` preview behavior remains part of the fixed probe checklist and is bound to a daemon-selected configured folder.
- R2. The probe reports seen-preservation behavior as a safe boolean fact (`read_state_preserved`) or `inconclusive` when a fixture is unavailable.
- R3. `probe` checks must stay schema-safe and not include private text or raw server content.
- R4. `mail.Source` supports read-state verification behavior for the probe path without leaking raw fetch details.
- R5. Tests cover seen preserved, seen changed, fixture-missing, and sanitized-error scenarios.
- R6. UAT scenario #005 reflects read-state safety and explicit boolean reporting.

## Scope Boundaries

- In scope: `read_state` probe logic, adapter contract extension, fake source/test harness updates, and UAT documentation wording.
- Deferred: live Infomaniak raw network probe execution and any CLI-side diagnostics behavior not already required by issue #19.
- Out of scope: changing provider behavior, adding raw IMAP command APIs, or exposing message bodies/headers/flags in CLI output.

## Sources

- `docs/requirements.md`: UC-011, FR-011, FR-012, FR-014
- `docs/non-functional-requirements.md`: NFR-REL-003, NFR-SEC-002, NFR-SEC-005, NFR-ERR-002
- `docs/architecture-arc42.md`: ARCH-RUN-007, ARCH-CON-007, ARCH-CON-005, ADR-005, ADR-007
- `docs/uat/infomaniak-imap-probe.md`: UAT-IMAP-PROBE-005

## Implementation Units

### U1. Extend Probe Behavior for Read-State Safety

- Implement read-state check as a fixed boolean/inconclusive fact path.
- Keep the check as a safe probe check (`read_state`) with structured facts only.
- On safe read that preserves seen, report `body_peek_preserves_seen` and `read_state_preserved=true`.
- On changed seen state, report `failed` with code `body_marked_seen` and `read_state_preserved=false`.

### U2. Source Contract for Read-State-Checked Preview

- Extend `mail.Source` with a bounded read-state preview method for probe-only behavior.
- Implement the new method in `imapadapter.Source` using `SELECT/EXAMINE` + flag-aware `FETCH` calls.
- Update `mailfake.Adapter` and probe stubs to provide deterministic seen behavior for tests.

### U3. Test Coverage

- Update `internal/providerprobe/imap_test.go` for:
  - safe-pass with `read_state_preserved=true`,
  - safe-fail with `read_state_preserved=false` and `body_marked_seen`,
  - fixture-missing fixture case remains `inconclusive`,
  - sanitized leak cases keep asserting no raw provider text.
- Update any adapter and endpoint tests that exercise read-state behavior to compile and verify structured facts.

### U4. UAT Documentation

- Update `docs/uat/infomaniak-imap-probe.md` scenario `UAT-IMAP-PROBE-005` to explicitly require structured boolean/inconclusive reporting.

## Verification Contract

- `go test ./...` (or focused package tests for changed units) validates:
  - source interface changes,
  - probe read-state fact behavior,
  - safe output/no leak guarantees.
- Keep no live Infomaniak mailbox interactions in normal CI/test execution.

## Definition of Done

- `read_state` check reports stable boolean safety evidence through `read_state_preserved` facts.
- `body_marked_seen` failure is implemented with no raw content/protocol leakage.
- UAT scenario text captures issue #19 acceptance semantics.
- The probe remains daemon-only and command-only through `ksuite-mail probe imap --account <account-ref> --json`.
