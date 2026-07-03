---
title: "fix: Validate domain read-state probe evidence"
date: "2026-07-03"
type: "fix"
artifact_contract: "ce-unified-plan/v1"
artifact_readiness: "implementation-ready"
product_contract_source: "https://github.com/dothackerman/ksuite-mail/issues/19"
execution: "code"
---

## Goal Capsule

| Field | Value |
|---|---|
| Objective | Refactor the domain-policy provider probe so `read_state` uses only same-folder UID evidence validated by `domain_header_search`, without changing public probe JSON. |
| Authority | GitHub issue #19, reviewer P1/P2 comments, UC-011, FR-014, NFR read-only fetch guidance, ARCH-RUN-007, ARCH-CON-007, and ADR-007. |
| Execution profile | Narrow Go fix in `internal/providerprobe` with focused fake-source tests. |
| Stop conditions | Stop if the fix exposes message content, raw headers, arbitrary IMAP responses, provider text, credentials, or changes the public JSON response shape. |

---

## Product Contract

The public command remains `ksuite-mail probe imap --account <account-ref> --json`.
The probe must keep returning safe structured facts only.
For domain-policy accounts, `read_state` may exercise `BODY.PEEK` only after the daemon has validated a bounded same-folder fixture UID through domain-header search evidence.

### Requirements

- R1. `probeDomainHeaders` keeps the public `domain_header_search` check unchanged while internally carrying validated UID evidence by folder, header, and domain.
- R2. `read_state` consumes validated same-folder evidence instead of issuing a new scoped domain-header search that could introduce an unvalidated UID before `BODY.PEEK`.
- R3. If domain-header search is overbroad, `read_state` remains `inconclusive` and does not call seen-state or preview methods.
- R4. Missing unrelated fixtures, such as Sent/Bcc evidence, do not block `read_state` when a safe same-folder INBOX fixture exists.
- R5. Tests prove the P1 unsafe re-search path and P2 missing unrelated fixture path without live Infomaniak access.

### Scope Boundaries

- In scope: internal probe result shape, same-folder UID evidence selection, and provider probe tests.
- Out of scope: public API schema changes, IMAP adapter protocol changes, UAT wording changes already covered by the issue-19 plan, and live provider probing.

---

## Planning Contract

- KTD1. Return an internal domain-header result from `probeDomainHeaders`.
  The result should contain the existing `api.ProbeCheck` plus private validated UID evidence, so `RunIMAP` can append the same public check while passing evidence to `read_state`.
- KTD2. Treat evidence as usable only when it is same-folder and the domain-header probe did not fail overbroad.
  Bcc evidence from Sent is still valid for the domain-header check but must not be used to preview the selected INBOX folder.
- KTD3. Let partial domain-header fixture absence remain a domain-header `fixture_required` result, but still allow read-state probing when at least one same-folder UID is validated.
  This addresses the P2 comment without weakening P1 safety.

### Assumptions

- The selected read-state folder is still the first configured probe folder from existing behavior.
- The public `api.ProbeFacts.DomainHeaderSearch` count-only JSON remains the compatibility boundary; raw UIDs stay internal.

---

## Implementation Units

### U1. Internal Domain Evidence Result

- **Goal:** Make `probeDomainHeaders` return a private result object containing the unchanged public check and same-folder validated UID evidence.
- **Requirements:** R1, R3, R4.
- **Dependencies:** None.
- **Files:** `internal/providerprobe/imap.go`, `internal/providerprobe/imap_test.go`.
- **Approach:** Introduce a small internal struct such as `domainHeaderProbeResult` with `check api.ProbeCheck` and a UID evidence collection keyed by folder/header/domain or equivalent typed entries. Append `result.check` to the public checklist. Populate evidence only from positive domain searches whose negative probe for the same folder/header/domain returned no UIDs. Keep Sent/Bcc evidence separate from the selected folder.
- **Patterns to follow:** Existing `probeCheckFacts`, `readStatePrerequisitesMet`, and count-only domain facts in `internal/providerprobe/imap.go`.
- **Test scenarios:** Domain probe public status, code, detail, and structured facts remain stable for existing matrix cases; overbroad negative search still returns `header_search_overbroad` and no read-state fetch occurs.
- **Verification:** Existing provider probe matrix tests still pass without public JSON fixture changes.

### U2. Read-State Uses Validated Evidence

- **Goal:** Remove the domain-policy read-state re-search path and select candidate UIDs only from validated same-folder evidence.
- **Requirements:** R2, R3, R4.
- **Dependencies:** U1.
- **Files:** `internal/providerprobe/imap.go`, `internal/providerprobe/imap_test.go`.
- **Approach:** Change `probeReadState` or its helper to accept optional validated evidence for domain accounts. Full-policy accounts can continue using bounded `ListUIDs`. Domain-policy accounts should sort and bound the evidence-derived UIDs, inspect seen state, and preview only a UID from that set. If no same-folder safe evidence exists, return `fixture_required`.
- **Execution note:** Start with regression tests that fail under the current re-search behavior.
- **Test scenarios:** A same-folder INBOX `From` fixture allows `read_state` to pass even when Sent/Bcc fixture evidence is missing; a scoped search that would return an extra UID is not called by `read_state`, and preview is only attempted for the UID already validated by `domain_header_search`; an overbroad domain search skips seen/preview calls.
- **Verification:** Focused `go test ./internal/providerprobe` demonstrates no preview occurs for unvalidated UIDs.

### U3. Regression Guard Shape

- **Goal:** Make the tests communicate the safety invariant reviewers care about.
- **Requirements:** R5.
- **Dependencies:** U1, U2.
- **Files:** `internal/providerprobe/imap_test.go`.
- **Approach:** Assert call sequences or call payloads rather than public JSON alone. The P1 regression should fail if `read_state` performs `SearchAllowed` after domain-header validation and then previews a UID that was not in the validated same-folder evidence. The P2 regression should fail if missing Sent/Bcc fixture evidence blocks a safe INBOX read-state probe.
- **Test scenarios:** Search call count/order distinguishes domain-header validation from read-state re-search; preview payload is the validated safe UID; no raw provider text appears in probe output.
- **Verification:** The focused test package fails before the implementation and passes after the refactor.

---

## Verification Contract

| Gate | Applies to | Done signal |
|---|---|---|
| `go test ./internal/providerprobe` | U1, U2, U3 | Regression tests pass and existing provider probe matrix stays green. |
| `go test ./...` | Whole branch | Interface and fake-source changes do not break other packages. |
| `make gate` | Pre-PR quality | Full repo quality gate passes or a concrete environment blocker is documented. |

---

## Definition of Done

- Public probe JSON remains unchanged for domain-header and read-state checks except for behavior intended by the reviewer comments.
- Domain-policy `read_state` never introduces a new UID through a second scoped header search before `BODY.PEEK`.
- Missing unrelated Sent/Bcc fixtures no longer block a safe same-folder read-state fixture.
- Focused tests prove P1 and P2 regressions, and no live provider credentials or mailbox access are required.
