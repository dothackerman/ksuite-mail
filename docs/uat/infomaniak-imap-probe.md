# Infomaniak IMAP Provider Probe UAT

These scenarios verify that the provider probe works through the real local CLI and daemon path while preserving the credential and mail-content boundaries.

## Command Path

Use the public CLI while the real daemon is running:

```bash
ksuite-mail probe imap --account <account-ref> --json
```

The CLI must talk to the daemon over the Unix socket. The daemon must resolve credentials internally, run the fixed provider probe checklist, and return sanitized structured diagnostics.

`<account-ref>` is the id of an existing account already registered in daemon configuration. Probing must not introduce test-only account registration, credential passing, or raw mailbox selection features.

The account reference is mandatory. The command must not infer a default account, even when only one account is configured.

The CLI is only the view over the daemon response. It must not own provider probing logic, credential resolution, folder discovery, fixture evaluation, or IMAP behavior decisions.

## Required Fixture Coverage

The useful fixture set contains:

- a message whose `From` matches a configured domain
- a message whose `To` matches a configured domain
- a message whose `Cc` matches a configured domain
- a sent message with available `Bcc` matching a configured domain, if Infomaniak exposes that header
- a non-matching message that must remain invisible
- enough UID spacing to test UID range search behavior

If a fixture is missing, affected checks must return `inconclusive`; they must not claim provider support or lack of support.

## Scenarios

### UAT-IMAP-PROBE-001 Fixed CLI Entry Point

Expected behavior:

- `ksuite-mail probe imap --account <account-ref> --json` reaches the daemon over the Unix socket.
- `ksuite-mail doctor --json` remains a setup and health diagnostic command and does not run the live provider probe.
- The CLI does not accept arbitrary raw IMAP commands.
- The response is compact JSON.
- The response is scoped to the selected existing account reference.
- The response contains no credentials, subjects, bodies, raw headers, attachment names, raw provider errors, or arbitrary IMAP responses.

Outcome:

- `passed`, `failed`, `inconclusive`, or `not_run`
- bug issue when failed

### UAT-IMAP-PROBE-002 Capability And Folder Diagnostics

Expected behavior:

- The probe reports sanitized `CAPABILITY` diagnostics.
- The probe reports sanitized `LIST` diagnostics.
- The probe reports read-only folder selection behavior through `EXAMINE` or read-only `SELECT`.
- Folder names may appear only as operational diagnostics.
- Provider folder listing may be broader than the configured folder list for diagnostics such as Sent folder discovery, but message-level checks must stay bounded to daemon-selected diagnostic targets for the explicit account reference.

Outcome:

- `passed`, `failed`, `inconclusive`, or `not_run`
- bug issue when failed

### UAT-IMAP-PROBE-003 UID State And Range Behavior

Expected behavior:

- The probe reports `UIDVALIDITY`.
- The probe reports `UIDNEXT`.
- The probe reports UID range search behavior using safe counts and booleans.
- The probe reports `CONDSTORE` / `HIGHESTMODSEQ` support when available.

Outcome:

- `passed`, `failed`, `inconclusive`, or `not_run`
- bug issue when failed

### UAT-IMAP-PROBE-004 Domain Header Search Behavior

Expected behavior:

- The probe checks `UID SEARCH HEADER From <domain>`.
- The probe checks `UID SEARCH HEADER To <domain>`.
- The probe checks `UID SEARCH HEADER Cc <domain>`.
- The probe checks `UID SEARCH HEADER Bcc <domain>` when available.
- Matching behavior is reported through safe counts, booleans, and `inconclusive` where fixtures are missing.
- The non-matching fixture remains invisible.

Outcome:

- `passed`, `failed`, `inconclusive`, or `not_run`
- bug issue when failed

### UAT-IMAP-PROBE-005 BODY.PEEK Read State

Expected behavior:

- The probe verifies whether `BODY.PEEK` avoids marking mail as seen.
- The result is reported as a safe boolean or `inconclusive`.
- No message content is returned.

Outcome:

- `passed`, `failed`, `inconclusive`, or `not_run`
- bug issue when failed

### UAT-IMAP-PROBE-006 Sent Folder And Bcc Availability

Expected behavior:

- The probe identifies the correct Sent folder behavior for configured accounts.
- The probe confirms or marks inconclusive whether `Bcc` is available in sent mail.
- No sent message content is returned.

Outcome:

- `passed`, `failed`, `inconclusive`, or `not_run`
- bug issue when failed

## Run Summary Template

Raw artifacts stay local under `.uat-runs/<run-id>/`.

```md
## Run

- Date:
- Commit:
- Daemon mode:
- CLI command shape: `ksuite-mail probe imap --account <account-ref> --json`
- Raw artifacts: `.uat-runs/<run-id>/`

## Scenario Outcomes

| Scenario | Outcome | Bug issue | Notes |
|---|---|---|---|
| UAT-IMAP-PROBE-001 | not_run | | |
| UAT-IMAP-PROBE-002 | not_run | | |
| UAT-IMAP-PROBE-003 | not_run | | |
| UAT-IMAP-PROBE-004 | not_run | | |
| UAT-IMAP-PROBE-005 | not_run | | |
| UAT-IMAP-PROBE-006 | not_run | | |

## Privacy Check

- [ ] No credentials
- [ ] No email addresses unless public test fixture addresses
- [ ] No subjects or bodies
- [ ] No raw headers
- [ ] No attachment names
- [ ] No raw provider errors or arbitrary IMAP responses
```
