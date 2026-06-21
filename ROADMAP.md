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
- Body text, quoted history, signatures, attachment contents, `Sender`, `Reply-To`, and transport headers are not policy-match inputs.

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

## Implementation Slices

1. Bootstrap secure local init and deployment boundary.
2. Build daemon skeleton, Unix socket API, doctor, and credential boundary.
3. Implement local cache and read-only command surface against fake/test mail adapters.
4. Probe Infomaniak IMAP through the fixed sanitized daemon checklist.

## Future Work

- MCP/OpenClaw adapter around the stable CLI/API.
- Sending, drafting, and replying with explicit human confirmation.
- Attachment metadata and content retrieval policy, if needed.
- Broader provider support only after the Infomaniak-first implementation is reliable.

