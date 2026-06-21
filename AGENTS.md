# AGENTS.md

Guidance for coding agents implementing this repository.

## Project Intent

`ksuite-mail` is a local, read-only mail gateway for safe personal AI agent access to Infomaniak K-Mail.

The human user is mainly the operator for setup, `doctor`, and diagnostics. The agent is the primary consumer of mail lookup commands, but agents must never receive credentials, raw IMAP access, or non-policy-approved private mail.

## Source Of Truth

Read these documents before implementation:

- `docs/requirements.md`
- `docs/non-functional-requirements.md`
- `docs/architecture-arc42.md`

If code and docs disagree, stop and update the docs or ask for a product decision before widening behavior.

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

- Do not implement background mail sync in v1.
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

