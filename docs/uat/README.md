# User Acceptance Testing

User acceptance testing verifies that a completed slice works through the same local user-facing path an operator or implementation agent would use. UAT complements automated tests; it does not replace hermetic unit, integration, e2e, security, or gate checks.

## Protocol

1. Confirm the slice's UAT scenarios are checked in before implementation is considered complete.
2. Run normal automated quality gates first unless the scenario explicitly requires checking a known failing build.
3. Start the real local daemon using the intended local configuration for the scenario.
4. Exercise the scenario through the public CLI, not by calling daemon internals or reading daemon-owned files directly.
5. Use normal product selectors and configuration. Do not add a feature solely to make the scenario testable.
6. Store raw run artifacts only under `.uat-runs/`.
7. Record the outcome for each scenario as `passed`, `failed`, `inconclusive`, or `not_run`.
8. For each `failed` scenario, open or link a bug issue before treating the UAT run as documented.
9. Keep checked-in documentation to the scenario protocol, reusable templates, and sanitized summaries that affect product or architecture decisions.

## Outcome Vocabulary

`passed`: The scenario satisfied its expected behavior through the public CLI path, and all evidence needed for review is sanitized.

`failed`: The scenario produced an incorrect, unsafe, or unusable result. A failed scenario must link to a bug issue.

`inconclusive`: The scenario could not prove the behavior because a required fixture, environment condition, provider behavior, or prerequisite was missing.

`not_run`: The scenario was intentionally skipped for this run. The reason must be stated.

## Evidence Rules

Raw run artifacts must not be committed. They belong under `.uat-runs/`, which is ignored by git.

Checked-in UAT docs and tracker comments may include:

- scenario name
- timestamp
- local software version or commit
- sanitized command shape
- sanitized daemon mode
- safe result codes
- capability names
- folder names when operationally required
- counts
- booleans
- outcome vocabulary
- links to bug issues

Checked-in UAT docs and tracker comments must not include:

- mailbox credentials
- email addresses unless they are public test fixture addresses
- message subjects
- message bodies
- raw message headers
- attachment names
- raw provider errors
- arbitrary IMAP responses
- daemon-owned secret file contents
- daemon-owned cache contents

## Failed Scenario Bug Issue

A bug issue for failed UAT must contain enough sanitized information to reproduce, analyze, and fix the problem without exposing email content.

Use this structure:

```md
## Scenario

<UAT scenario id and name>

## Environment

- Commit:
- OS:
- Daemon mode:
- Config shape:
- Fixture state:

## Reproduction

1. <sanitized setup step>
2. <sanitized command shape>
3. <sanitized observation step>

## Expected

<safe expected behavior>

## Observed

<safe observed behavior using result codes, counts, booleans, and sanitized messages>

## Raw Artifacts

Local-only path: .uat-runs/<run-id>/

## Privacy Check

- [ ] No credentials
- [ ] No email addresses unless public test fixture addresses
- [ ] No email subjects or bodies
- [ ] No raw headers
- [ ] No attachment names
- [ ] No raw provider errors
```
