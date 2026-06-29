# ksuite-mail

ksuite-mail is a local mail gateway for controlled, read-only access to Infomaniak K-Mail through a daemon-owned credential and cache boundary.

## Language

**UAT Scenario**:
A named user acceptance test case that describes a user-visible behavior, its preconditions, the command path to exercise, and the safe evidence needed to judge the outcome.
_Avoid_: ad hoc manual test, transcript

**UAT Run**:
One execution of one or more UAT scenarios against a specific local setup at a specific time.
_Avoid_: test log, raw output

**Raw UAT Artifact**:
Local-only evidence captured during a UAT run, such as CLI JSON, daemon logs, screenshots, or scratch notes, before sanitization and summary.
_Avoid_: checked-in evidence, public transcript

**Account Reference**:
The stable configured account id used by user-facing commands to select an existing daemon-side mailbox account.
_Avoid_: mailbox credential, test account handle

**Probe Target**:
An existing configured account selected by account reference for a provider probe run.
_Avoid_: test-only account, raw mailbox selector

**Provider Probe**:
A fixed daemon-side compatibility diagnostic that checks live IMAP provider behavior for an explicit probe target and returns only sanitized structured facts.
_Avoid_: doctor check, raw IMAP runner

**Bug Issue**:
A tracker issue opened for a failed UAT scenario, containing enough sanitized reproduction and analysis context to fix the defect.
_Avoid_: failure note, loose TODO
