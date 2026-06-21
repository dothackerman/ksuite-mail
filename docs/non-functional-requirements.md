# Non-Functional Requirements

## Configuration

### NFR-CFG-001 Format

Use TOML for non-secret configuration.

Example:

```toml
[mail]
default_limit = 25
cache_ttl = "90d"

[[mail.accounts]]
id = "rs_info"
email = "info@regenerativ.ch"
host = "mail.infomaniak.com"
port = 993
tls = true
username = "info@regenerativ.ch"
password_ref = { source = "file", provider = "local", id = "/ksuite-mail/rs_info/password" }
policy = "full"
folders = ["INBOX", "Sent"]

[[mail.accounts]]
id = "og_private_rs"
email = "private@example.com"
host = "mail.infomaniak.com"
port = 993
tls = true
username = "private@example.com"
password_ref = { source = "file", provider = "local", id = "/ksuite-mail/og_private/password" }
policy = "domain"
domains = ["regenerativ.ch"]
folders = ["INBOX", "Sent"]
```

### NFR-CFG-002 Location

Hardened Linux deployment:

```text
/etc/ksuite-mail/config.toml
```

Suggested ownership:

```text
/etc/ksuite-mail/config.toml  root:ksuite-mail  640
```

### NFR-CFG-003 Validation

`ksuite-mail doctor` must validate:

- TOML syntax.
- required account fields.
- unique account ids.
- supported policies.
- domain list presence for `domain` policy.
- folder list presence.
- unknown keys as warnings or errors.
- cache TTL parse validity.

### NFR-CFG-004 OpenClaw Independence

The config format may use a SecretRef-like structure but must not depend on OpenClaw runtime internals.

## Credentials

### NFR-SEC-001 No Plaintext Passwords In Config

Mailbox passwords must not appear in `config.toml`.

Use credential references:

```toml
password_ref = { source = "file", provider = "local", id = "/ksuite-mail/rs_info/password" }
```

### NFR-SEC-002 Daemon-Side Resolution

Only `ksuite-maild` resolves credentials.

`ksuite-mail` must never receive or print credentials.

### NFR-SEC-003 Dedicated Service User

Useful local production deployment should run:

```text
ksuite-maild
  User=ksuite-mail
```

Credential file:

```text
/etc/ksuite-mail/secrets.json  ksuite-mail:root  600
```

Agent/user processes must not be able to read the credential file.

### NFR-SEC-004 Secret Backends

Acceptable first local options:

- `0600` file readable only by `ksuite-mail`.
- systemd credentials.
- daemon-side exec resolver.
- OS keyring only if the service-user boundary is understood.

Same-user SecretRef, keyring, `pass`, or unlocked vault access is hygiene, not a hard security boundary.

### NFR-SEC-005 No Credential Leakage

Never print secrets in:

- logs
- errors
- doctor output
- JSON responses
- panic traces

### NFR-SEC-006 App Passwords

Prefer Infomaniak app passwords if supported.

## Privacy Boundary

### NFR-PRV-001 Domain-Scoped Accounts

For private scoped accounts:

- no non-matching headers downloaded
- no non-matching bodies downloaded
- no non-matching attachments downloaded
- no non-matching rows in cache
- no non-matching content in logs
- policy matches are based only on `From`, `To`, `Cc`, and `Bcc`
- body text is never a policy-match input

### NFR-PRV-002 Search Before Fetch

`UID SEARCH` must happen before any `FETCH` for domain-scoped accounts.

Tests must fail if code fetches all/recent messages and filters locally.

### NFR-PRV-003 Local Validation After Fetch

Server-side search is the first gate.

Local exact-domain parsing is the second gate before cache writes or responses.

### NFR-PRV-004 Bcc Limits

`Bcc` must be searched and validated when the provider exposes it, mainly for sent mail.

Received mail often does not expose useful `Bcc` header data. Missing `Bcc` data must not be treated as a privacy failure by itself.

### NFR-PRV-005 Auditability

The daemon should record policy decisions without storing private non-matching content.

Useful fields:

- timestamp
- account id
- folder
- command
- result count
- visible reason
- denied reason

## Performance And Cache

### NFR-PERF-001 Cache Ownership

The daemon owns the cache.

```text
/var/lib/ksuite-mail/mail.db  ksuite-mail:ksuite-mail  600
```

Agents and normal users must not read the DB directly.

### NFR-PERF-002 Cache Engine

Use SQLite + FTS5.

Suggested tables:

```text
messages(
  id, account_id, folder, uidvalidity, uid,
  message_id, thread_key,
  subject, from_text, to_text, cc_text,
  bcc_text,
  date, flags, has_attachments,
  snippet, body_text,
  visible_reason,
  content_hash,
  first_loaded_at,
  last_loaded_or_verified_at,
  updated_at
)

messages_fts(subject, from_text, to_text, cc_text, bcc_text, snippet, body_text)

folder_state(
  account_id, folder,
  uidvalidity, uidnext,
  highestmodseq,
  last_seen_uid,
  last_refresh_attempted_at,
  last_successful_refresh_at
)
```

### NFR-PERF-003 Incremental Refresh State

Store refresh state per account and folder:

- account id
- folder
- UIDVALIDITY
- UIDNEXT
- last seen UID
- HIGHESTMODSEQ when supported
- last refresh attempted timestamp
- last successful refresh timestamp

The daemon must use IMAP UID state as the primary remote freshness mechanism.

When `UIDVALIDITY` changes, cached rows for that account/folder must be invalidated and rebuilt through policy-approved fetches.

When `HIGHESTMODSEQ` is supported, use it to reduce flag/state refresh work. Without it, refresh bounded UID ranges and retained cached UID metadata as needed.

### NFR-PERF-004 On-Demand Refresh

The daemon must not perform first-version background mail refresh.

Commands that need mail data trigger an on-demand refresh before querying the local cache.

Refresh must:

- select or examine configured folders read-only when possible
- fetch only new or changed remote data
- avoid refetching cached message bodies when IMAP state indicates they are unchanged
- use `BODY.PEEK` or equivalent read-only fetch behavior to avoid marking messages as read
- write only policy-approved content to SQLite

For domain-scoped accounts, the refresh path must search server-side before fetch for each configured domain and allowed address header.

### NFR-PERF-005 Query Latency Targets

Targets for cached data:

- list/search brief results: under 200 ms for normal local mailbox sizes.
- show cached preview: under 100 ms.
- context pack from cache: under 200 ms.

Targets for uncached IMAP access:

- command should return bounded partial results where possible.
- no command should fetch unbounded message bodies by default.

### NFR-PERF-006 Retention

Default cache TTL is 90 days.

TTL is based on `last_loaded_or_verified_at`, not on the email sent or received date.

Implications:

- old emails may remain cached if recently verified against the remote mailbox
- recent emails may expire if not loaded or verified within the TTL window
- expired rows may be removed from the cache
- a command requesting data outside retained cache may trigger remote refresh if the message is still visible and policy-approved

Remote deletion handling:

- messages observed as deleted or expunged remotely must be removed locally
- moved messages are represented as deletion from the old configured folder plus add/update in the new configured folder when that folder is refreshed

### NFR-PERF-007 Context Efficiency

Default responses must be compact.

Required flags:

- `--limit`
- `--fields`
- `--brief`
- `--preview`
- `--max-chars`
- `--since`

Avoid by default:

- full bodies in search results
- full threads
- quoted histories
- attachment contents

Unread/seen state is not a first-version agent requirement.

### NFR-PERF-008 Deterministic Context Packing

`context` must be deterministic.

It may include:

- selected headers
- latest message excerpt
- short thread timeline
- visible reason

It must not call an LLM.

## Errors And Offline Behavior

### NFR-ERR-001 Offline Local Results

When remote mail cannot be reached, read commands must return locally cached policy-approved results when available.

Responses must clearly indicate that remote refresh failed and returned data is local-only or potentially stale.

### NFR-ERR-002 Safe Error Shape

JSON responses should use a stable envelope:

```json
{
  "status": "ok_stale",
  "results": [],
  "refresh": {
    "attempted": true,
    "remote_ok": false,
    "last_successful_refresh_at": "2026-06-21T10:00:00Z"
  },
  "warnings": [
    {
      "code": "remote_unreachable",
      "message": "Returned local cached results only."
    }
  ]
}
```

Allowed high-level statuses:

- `ok`
- `ok_stale`
- `partial`
- `error`

Errors must be specific enough for the CLI to print useful diagnostics, but must not include credentials, email bodies, subjects, attachment names, or raw provider text that could contain private mail content.

## Operations

### NFR-OPS-000 Init Command

`sudo ksuite-mail init` should prepare a local deployment.

Responsibilities:

- create the `ksuite-mail` service user if missing
- create required directories with hardened ownership and modes
- create or validate `/etc/ksuite-mail/config.toml`
- create or validate the daemon-readable secrets file
- prompt for credentials through an interactive TTY
- install or print systemd service/socket units
- configure socket group access using an existing local group by default

It must not create or configure an agent-specific OS user.

### NFR-OPS-001 Linux Service Boundary

Use systemd service/socket units.

Files:

```text
/usr/local/bin/ksuite-mail
/usr/local/bin/ksuite-maild
/etc/ksuite-mail/config.toml
/etc/ksuite-mail/secrets.json
/var/lib/ksuite-mail/mail.db
/run/ksuite-mail/ksuite-mail.sock
```

### NFR-OPS-002 Socket Permissions

Suggested model:

```text
/run/ksuite-mail/ksuite-mail.sock  ksuite-mail:<access-group>  660
```

For a single-user local deployment, `<access-group>` may be the existing local user's primary group.

A dedicated group such as `mailagents` is optional and useful only when multiple local users or agent runtimes need socket access.

The socket access group must not own or read the daemon credential file or SQLite cache.

### NFR-OPS-003 Systemd Hardening

Use systemd hardening as defense-in-depth:

- `NoNewPrivileges=true`
- `PrivateTmp=true`
- `ProtectSystem=strict`
- `ProtectHome=true`
- restricted `ReadWritePaths`
- restrictive `UMask`

### NFR-OPS-004 Debuggability

First IPC format:

```text
HTTP/JSON over Unix domain socket
```

Reason:

- inspectable
- curl-debuggable
- easy for agents
- enough for local throughput

Reserve protobuf/gRPC for later if required by:

- generated schemas
- multiple non-CLI clients
- streaming
- higher-throughput internal calls

## Reliability

### NFR-REL-001 IMAP Library Isolation

Use `go-imap/v2/imapclient` behind a narrow adapter because v2 is beta.

The rest of the daemon must not depend directly on go-imap types.

### NFR-REL-002 Fallback Stack

Fallback order if Go IMAP blocks:

1. Node + ImapFlow.
2. Python + IMAPClient.
3. Rust.

### NFR-REL-003 Provider Variance

Infomaniak IMAP behavior must be tested for:

- `UID SEARCH`
- header search
- UID range searches
- `EXAMINE` or read-only `SELECT`
- `BODY.PEEK`
- folder naming
- sent folder naming
- UIDVALIDITY
- UIDNEXT
- CONDSTORE/HIGHESTMODSEQ support

The local cache schema may remain generic IMAP-based, but optimizations must not assume unsupported Infomaniak capabilities.
