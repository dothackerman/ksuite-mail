# Architecture

arc42 structure for `ksuite-mail`.

## 1. Introduction And Goals

### 1.1 Requirements Overview

`ksuite-mail` provides controlled local read-only access to Infomaniak K-Mail for users, agents, scripts, and optional future MCP/OpenClaw adapters.

Core goals:

- expose a stable local CLI
- support multiple predefined mail accounts
- enforce full/domain account policies
- protect credentials from agent processes
- prevent non-matching private mail from being downloaded or cached
- refresh mail only on demand when relevant CLI/API commands run
- return compact JSON for context-efficient agent use

### 1.2 Quality Goals

1. Privacy boundary correctness.
2. Credential isolation.
3. Context-efficient query output.
4. Local query speed through daemon-owned cache.
5. Standalone operation independent of OpenClaw.

### 1.3 Stakeholders

| Stakeholder | Interest |
|---|---|
| OG | Safe local setup, diagnostics, and controlled agent access to mail. |
| Local agents | Primary consumers of compact policy-approved mail context. |
| Future adapters | Stable CLI/API contract. |
| Maintainer | Small auditable Linux service. |

## 2. Constraints

### 2.1 Technical Constraints

- Linux-first.
- Infomaniak K-Mail via IMAP.
- Go implementation.
- systemd service/socket deployment.
- SQLite + FTS5 cache.
- HTTP/JSON over Unix domain socket for first version.
- `go-imap/v2/imapclient` pinned behind adapter.
- No first-version background mail refresh; refresh is command-triggered.

### 2.2 Security Constraints

- Credentials must not be readable by agent/user processes.
- Raw IMAP access must stay inside `ksuite-maild`.
- Domain-scoped accounts must search server-side before fetch.
- No send-mail in first version.

### 2.3 Organizational Constraints

- Standalone tool first.
- MCP/OpenClaw wrappers later only as adapters.
- Documentation source of truth lives in this project folder.

## 3. Context And Scope

### 3.1 Business Context

```text
Local user / agent / script
  -> ksuite-mail CLI
  -> ksuite-maild
  -> Infomaniak IMAP
```

`ksuite-mail` enables:

- Safe agent access to policy-approved mail context.
- Meeting prep.
- Search across configured mailboxes.
- Scoped access to RS-related messages in a private mailbox.
- Human setup and diagnostics through `init` and `doctor`.

### 3.2 Technical Context

```text
ksuite-mail
  HTTP/JSON over UDS
ksuite-maild
  IMAP/TLS
Infomaniak K-Mail

ksuite-maild
  SQLite/FTS5
  TOML config
  daemon-side credentials
```

External systems:

- Infomaniak IMAP server.
- systemd.
- local filesystem.
- optional secret backend.
- optional future MCP/OpenClaw wrapper.

## 4. Solution Strategy

Use a split CLI/daemon architecture.

`ksuite-mail`:

- no secrets
- no IMAP sessions
- sends requests to daemon
- prints JSON or human-readable output

`ksuite-maild`:

- runs as dedicated Unix user
- owns credentials
- owns cache
- enforces policy
- talks to IMAP
- returns only policy-approved JSON

Security strategy:

- isolate dangerous capability in daemon
- expose narrow local socket
- test search-before-fetch
- never expose raw IMAP or credentials

Performance strategy:

- cache approved metadata/body text in SQLite
- use FTS5 for search
- refresh remote mail on demand before cache queries
- use IMAP UID state to avoid refetching unchanged messages
- default to compact outputs
- use progressive disclosure

## 5. Building Block View

### 5.1 Level 1

```text
+----------------------+       +----------------------+       +-------------------+
| User / Agent         | ----> | ksuite-mail          | ----> | ksuite-maild      |
| shell/OpenClaw/etc.  |       | thin CLI             | UDS   | daemon            |
+----------------------+       +----------------------+       +---------+---------+
                                                                  |       |
                                                                  |       |
                                                                  v       v
                                                        +-------------+ +--------------+
                                                        | IMAP client | | SQLite cache |
                                                        +-------------+ +--------------+
                                                                  |
                                                                  v
                                                        +---------------------------+
                                                        | Infomaniak K-Mail         |
                                                        +---------------------------+
```

### 5.2 Level 2 Components

| Component | Responsibility |
|---|---|
| CLI command parser | Parse user/agent commands and flags. |
| UDS HTTP client | Send local HTTP/JSON requests to daemon. |
| API handler | Validate daemon requests. |
| Policy engine | Apply full/domain account policy. |
| Refresh coordinator | Run command-triggered IMAP refresh and update cache state. |
| IMAP adapter | Hide `go-imap/v2` behind internal interface. |
| MIME parser | Parse headers and text bodies. |
| Cache repository | Read/write policy-approved SQLite rows. |
| FTS search | Search approved cached text. |
| Context packer | Build bounded deterministic agent context. |
| Credential resolver | Resolve secrets daemon-side only. |
| Audit logger | Record policy decisions without leaking content. |

### 5.3 Internal Interfaces

Mail source adapter:

```go
type MailSource interface {
    SelectFolder(ctx context.Context, account AccountPolicy, folder string) (FolderState, error)
    SearchAllowed(ctx context.Context, account AccountPolicy, folder string, uidRange UIDRange) ([]UID, error)
    FetchEnvelope(ctx context.Context, uids []UID) ([]Envelope, error)
    FetchBodyPreview(ctx context.Context, uid UID, maxBytes int) (BodyPreview, error)
}
```

Public local API:

```text
GET  /v1/health
POST /v1/list
POST /v1/search
POST /v1/show
POST /v1/thread
POST /v1/context
POST /v1/doctor
```

## 6. Runtime View

### 6.1 List Messages

```text
agent
  -> ksuite-mail inbox --brief --json
  -> POST /v1/list
  -> daemon checks request
  -> daemon attempts bounded on-demand refresh for configured folders
  -> daemon queries SQLite
  -> daemon returns compact JSON with refresh status
```

### 6.2 Domain-Scoped Refresh

```text
daemon
  -> connect IMAP over TLS
  -> examine/select configured folder read-only
  -> compare UIDVALIDITY/UIDNEXT/HIGHESTMODSEQ with folder_state
  -> UID SEARCH HEADER From regenerativ.ch
  -> UID SEARCH HEADER To regenerativ.ch
  -> UID SEARCH HEADER Cc regenerativ.ch
  -> UID SEARCH HEADER Bcc regenerativ.ch when available
  -> union UIDs
  -> FETCH only matching new/changed UIDs with BODY.PEEK/read-only behavior
  -> parse exact domains locally
  -> write approved rows to cache
  -> update last_successful_refresh_at and UID state
```

### 6.3 Show Message

```text
agent
  -> ksuite-mail show msg_abc123 --max-chars 4000 --json
  -> daemon resolves stable id
  -> daemon attempts bounded on-demand refresh for the message folder when useful
  -> daemon verifies policy/cache visibility
  -> daemon returns bounded body
```

### 6.4 Doctor

```text
user
  -> ksuite-mail doctor --json
  -> daemon validates config
  -> daemon checks credential presence
  -> daemon checks cache
  -> daemon checks narrow IMAP connectivity
  -> daemon returns diagnostics without secrets
```

### 6.5 Offline Or Remote Failure

```text
agent
  -> ksuite-mail search "query" --json
  -> daemon attempts remote refresh
  -> remote connection/auth/provider error occurs
  -> daemon queries local policy-approved cache
  -> daemon returns local results with safe structured refresh warning
```

Remote errors are classified for useful CLI diagnostics without returning credentials, raw provider text, subjects, bodies, attachment names, or non-approved mail content.

### 6.6 Init

```text
human
  -> sudo ksuite-mail init
  -> creates/validates ksuite-mail service user
  -> creates protected config, secret, cache, and runtime paths
  -> prompts for credentials through TTY
  -> installs or prints systemd service/socket units
```

`init` does not create an agent-specific OS user.

## 7. Deployment View

### 7.1 Linux Files

```text
/usr/local/bin/ksuite-mail          root:root          755
/usr/local/bin/ksuite-maild         root:root          755
/etc/ksuite-mail/config.toml        root:ksuite-mail   640
/etc/ksuite-mail/secrets.json       ksuite-mail:root   600
/var/lib/ksuite-mail/mail.db        ksuite-mail        600
/run/ksuite-mail/ksuite-mail.sock   ksuite-mail:<access-group> 660
```

### 7.2 Users And Groups

Single-user local deployment may use the existing local user's primary group for socket access:

```bash
sudo useradd --system --home /var/lib/ksuite-mail --shell /usr/sbin/nologin ksuite-mail
```

A dedicated socket access group is optional when multiple local users or agent runtimes need access:

```bash
sudo groupadd mailagents
sudo usermod -aG mailagents "$USER"
```

### 7.3 systemd Units

Service:

```ini
[Service]
User=ksuite-mail
Group=ksuite-mail
ExecStart=/usr/local/bin/ksuite-maild --config /etc/ksuite-mail/config.toml
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/ksuite-mail /run/ksuite-mail
UMask=0077
```

Socket:

```ini
[Socket]
ListenStream=/run/ksuite-mail/ksuite-mail.sock
SocketUser=ksuite-mail
SocketGroup=<access-group>
SocketMode=0660
```

## 8. Crosscutting Concepts

### 8.1 Policy Enforcement

Policy is enforced in the daemon, never in the CLI.

Domain policy has two gates:

1. server-side IMAP `UID SEARCH`
2. local exact-domain validation

Only `From`, `To`, `Cc`, and available `Bcc` headers are first-version policy match inputs.

Body text and quoted history are never policy match inputs.

### 8.2 Credential Boundary

Only the daemon resolves credentials.

The CLI and agents cannot read:

- `/etc/ksuite-mail/secrets.json`
- daemon keyring
- raw IMAP session state

### 8.3 Stable IDs

Public ids are opaque.

Internal mapping:

```text
account_id + folder + uidvalidity + uid
```

### 8.4 Cache Boundary

Cache contains only policy-approved content.

Agent reads cache only through daemon API.

Cached message rows are retained according to `last_loaded_or_verified_at`, not email date.

Remote deletions remove local rows when observed. Moves are represented as deletion from the old configured folder plus add/update in the new configured folder when that folder is refreshed.

### 8.5 Refresh Boundary

There is no background mail refresh in the first version.

Commands trigger bounded refresh before reading from the cache.

Refresh uses:

- UIDVALIDITY to detect invalidated UID mappings
- UIDNEXT and UID ranges to find new messages
- HIGHESTMODSEQ/CONDSTORE when supported to reduce state refresh work
- BODY.PEEK or read-only fetch behavior to avoid marking messages as read

Message content hashes may be stored locally after policy-approved fetches, but remote hash comparison is not the primary freshness mechanism.

### 8.6 IPC

First version:

```text
HTTP/JSON over Unix domain socket
```

Deferred:

```text
protobuf/gRPC
```

Use protobuf/gRPC only if stronger generated schemas, streaming, multiple non-CLI clients, or higher throughput justify the extra complexity.

## 9. Architecture Decisions

### ADR-001 Go For Production Implementation

Decision: use Go.

Rationale:

- good Linux daemon ergonomics
- simple static-ish deployment
- standard HTTP/UDS support
- better fit than Python for service boundary

### ADR-002 CLI/Daemon Split

Decision: split `ksuite-mail` and `ksuite-maild`.

Rationale:

- keeps credentials out of the CLI
- allows dedicated service user
- centralizes policy enforcement

### ADR-003 HTTP/JSON Over UDS First

Decision: use HTTP/JSON over Unix domain socket.

Rationale:

- inspectable
- curl-debuggable
- agent-friendly
- sufficient local performance

Consequence:

- schemas are less strict than protobuf.
- defer gRPC until requirements justify it.

### ADR-004 SQLite + FTS5 Cache

Decision: daemon-owned SQLite + FTS5 cache.

Rationale:

- fast local search
- simple deployment
- no external service
- strong file permission model

### ADR-005 go-imap/v2 Behind Adapter

Decision: use `go-imap/v2/imapclient` behind internal adapter.

Rationale:

- modern Go IMAP direction
- exposes needed UID/search/fetch primitives
- beta status requires isolation and pinning

Fallback:

1. Node + ImapFlow.
2. Python + IMAPClient.
3. Rust.

### ADR-006 Command-Triggered Refresh

Decision: do not run a background mail refresh in the first version.

Rationale:

- keeps daemon behavior easier to audit
- limits IMAP access to explicit local commands
- still improves latency through durable cache reuse
- avoids invisible background access to private mailboxes

Consequence:

- commands may spend time refreshing remote mail before returning
- offline commands return local cache with structured stale-result metadata

### ADR-007 Generic IMAP Schema With Infomaniak Probe

Decision: keep the local cache schema generic to IMAP concepts, but probe Infomaniak behavior before relying on provider-specific optimizations.

Required probe areas:

- UID SEARCH and header search
- UID range searches
- read-only folder selection
- BODY.PEEK behavior
- UIDVALIDITY and UIDNEXT
- CONDSTORE/HIGHESTMODSEQ
- Sent folder naming and Bcc availability

## 10. Quality Requirements

| Quality | Scenario |
|---|---|
| Privacy | Domain-scoped private account never fetches non-matching headers/bodies. |
| Security | Agent can query allowed mail but cannot read credentials. |
| Performance | Cached search returns compact results under 200 ms for normal local mailbox sizes. |
| Debuggability | Local API requests can be inspected as JSON. |
| Maintainability | IMAP library can be swapped by replacing one adapter. |
| Context efficiency | Default result sets avoid full bodies and quoted history. |
| Offline behavior | Remote failure returns local approved cache with safe stale-result metadata. |

## 11. Risks And Technical Debt

| Risk | Mitigation |
|---|---|
| Local filtering masquerades as privacy | Tests prove `SEARCH` before `FETCH`. |
| IMAP provider search quirks | Probe Infomaniak with test mailbox. |
| Incorrect cache freshness assumptions | Use UIDVALIDITY, UIDNEXT, and MODSEQ when available; treat hashes as local diagnostics only. |
| Unbounded refresh latency | Bound command-triggered refresh work and return local stale results on remote failure. |
| go-imap/v2 beta breakage | Pin version and isolate adapter. |
| Credential leakage through logs | Redaction and tests. |
| Sent folder localization | Folder discovery and config override. |
| Over-engineering IPC | Start with HTTP/JSON; defer gRPC. |
| Send-mail blast radius | Exclude from first version. |

## 12. Glossary

| Term | Meaning |
|---|---|
| CLI | Command-line interface. |
| Daemon | Long-running background service. |
| IMAP | Mail access protocol. |
| UDS | Unix domain socket. |
| IPC | Inter-process communication. |
| FTS5 | SQLite full-text search extension. |
| UID | IMAP unique message id inside a folder. |
| UIDVALIDITY | IMAP folder value used to determine UID stability. |
| SecretRef | Structured reference to a secret, not the secret value. |
| gRPC | RPC framework usually using protobuf. |
| protobuf | Typed binary schema/message format. |
