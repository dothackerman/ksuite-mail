# ROADMAP

Product and implementation roadmap for `ksuite-mail`.

## Product Intent

`ksuite-mail` is a local mail gateway for safe personal AI agent access to Infomaniak K-Mail.

The human user mainly operates setup, `doctor`, and diagnostics. The agent is the primary consumer of mail lookup commands, but agents must never receive credentials, raw IMAP access, or non-policy-approved private mail.

## First-Version Scope

Read-only mail operations:

- `init`
- `doctor`
- `inbox`
- `list`
- `search`
- `show`
- `thread`
- `context`

Out of scope:

- sending email
- drafting
- replying
- attachment download
- generic IMAP proxying
- K-Calendar
- K-Drive

## Mail Policy

Supported first-version policies:

- `full`: expose all messages from the configured account/folders.
- `domain`: expose only messages whose `From`, `To`, `Cc`, or available `Bcc` headers match configured domains.

For `domain` accounts:

- Run server-side `UID SEARCH HEADER` before any `FETCH`.
- Fetch only matching UIDs.
- Re-validate exact domains locally before cache writes or responses.
- Body text, embedded previous replies, signatures, attachment contents, `Sender`, `Reply-To`, and transport headers are not policy-match inputs.
- Embedded previous replies means older thread content copied inside a newer email body, for example the text below "On DATE, NAME wrote:".

## Refresh And Cache

- No background mail refresh in v1.
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

## Provider Probe

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

Behavior checks must use a controlled test account or folder with known fixture messages when a boolean answer depends on message content or folder state. Without controlled fixtures, the probe should report that result as inconclusive rather than treating generic mailbox behavior as proven.

## Implementation Slices

Every implementation slice should reference an existing GitHub issue. If no issue exists yet, this roadmap must document the next product or technical step before implementation starts.

| Order | Issue | Slice | Next step |
|---|---|---|---|
| 1 | [#1](https://github.com/dothackerman/ksuite-mail/issues/1) | Bootstrap secure local init and deployment boundary. | In implementation. Do not expand scope without a follow-up issue. |
| 2 | [#2](https://github.com/dothackerman/ksuite-mail/issues/2) | Build daemon skeleton, Unix socket API, doctor, and credential boundary. | Implement after issue #1 lands. |
| 3 | [#3](https://github.com/dothackerman/ksuite-mail/issues/3) | Implement local cache and read-only command surface against fake/test mail adapters. | Keep live Infomaniak behavior behind fake/test adapters until issue #4 proves provider behavior. |
| 4 | [#4](https://github.com/dothackerman/ksuite-mail/issues/4) | Probe Infomaniak IMAP through the fixed sanitized daemon checklist. | Run against controlled fixtures and update architecture/NFRs if Infomaniak behavior diverges from generic IMAP assumptions. |

## Future Work

| Future item | Existing issue | Next step |
|---|---|---|
| MCP/OpenClaw adapter around the stable CLI/API. | None. | Create an issue after the CLI/API contract is stable. |
| Sending, drafting, and replying with explicit human confirmation. | None. | Make product decisions for confirmation UX, audit logs, failure handling, and rollback limits before implementation. |
| Attachment metadata and content retrieval policy. | None. | Decide whether agents need attachment names, metadata, or contents, then define privacy rules before implementation. |
| Broader provider support. | None. | Revisit only after the Infomaniak-first implementation is reliable and provider-probe results are documented. |
