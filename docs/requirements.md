# Requirements

## How To Reference Requirements

Implementation issues should reference the narrow sections needed for the slice:

- `UC-*` sections describe user-visible intent.
- `FR-*` sections describe product behavior.
- `NFR-*` sections in `docs/non-functional-requirements.md` describe security, operations, reliability, performance, and error constraints.

Coding agents should not need to read every requirement for a narrow implementation issue.

## Scope

Build a standalone local CLI and daemon for controlled read-only access to Infomaniak K-Mail.

Primary executable:

- `ksuite-mail`: thin CLI client, no credentials, agent/script/user facing.
- `ksuite-maild`: local daemon, owns IMAP access, credentials, policy enforcement, and cache.

Out of scope for first version:

- Sending email.
- Attachment content download.
- K-Calendar.
- K-Drive.
- Generic raw IMAP proxying.
- OpenClaw-specific runtime dependency.

## Users

### Local Human User

Uses the CLI for setup, `doctor`, and diagnostics when agent access falls short.

The human user normally reads mail in a traditional mail client. This project exists to provide safe mail access to local personal AI agents.

### Local Agent

Primary consumer of the mail lookup commands. Uses the CLI for policy-approved mail search, context retrieval, and meeting/workflow support.

### Optional Future MCP/OpenClaw Adapter

Wraps the standalone CLI/API without changing the core tool.

## Use Cases

### UC-001 List Recent Mail

Actor: local user or agent.

Command:

```bash
ksuite-mail inbox --brief --limit 20 --json
```

Result:

- Returns compact policy-approved messages.
- Does not include full bodies.
- Does not include attachment contents.
- Includes stable message ids.
- Attempts an on-demand remote refresh before returning.
- If remote mail is unavailable, returns locally cached messages with structured stale-result metadata.

### UC-002 Search Mail

Actor: local user or agent.

Command:

```bash
ksuite-mail search "OpenRouter credits" --account all --limit 10 --json
```

Result:

- Searches only policy-approved cached content.
- Returns id, date, account, folder, from, to, subject, snippet, flags, and visible reason.
- Does not expose raw IMAP identifiers as public ids.
- Attempts an on-demand remote refresh before searching the local cache.
- If remote mail is unavailable, searches locally cached messages and reports the refresh failure without leaking mail content.

### UC-003 Preview Message

Actor: local user or agent.

Command:

```bash
ksuite-mail show msg_abc123 --preview --json
```

Result:

- Returns headers and a bounded body preview.
- Re-checks the policy boundary before returning content.
- Omits embedded previous replies unless explicitly requested and allowed by policy.

### UC-004 Retrieve Bounded Message Body

Actor: local user or agent.

Command:

```bash
ksuite-mail show msg_abc123 --max-chars 4000 --json
```

Result:

- Returns a bounded body.
- Respects policy and output budget.
- Does not return attachment contents.

### UC-005 Retrieve Thread Summary

Actor: local user or agent.

Command:

```bash
ksuite-mail thread msg_abc123 --brief --json
```

Result:

- Returns compact thread timeline.
- Includes only policy-approved messages.
- Omits repeated embedded previous replies by default.

### UC-006 Build Agent Context Pack

Actor: local agent.

Command:

```bash
ksuite-mail context msg_abc123 --budget 1200 --json
```

Result:

- Deterministically packs the smallest useful context.
- Includes relevant headers, latest body excerpt, and short thread timeline.
- Does not use an LLM summary.
- Does not exceed the requested budget except for documented metadata overhead.

### UC-007 Diagnose Setup

Actor: local user.

Command:

```bash
ksuite-mail doctor --json
```

Result:

- Checks daemon reachability.
- Checks config parse validity.
- Checks account definitions.
- Checks credential presence without printing credentials.
- Checks cache availability.
- Checks IMAP connectivity without broad fetching.

### UC-008 Initialize Local Service

Actor: local human user.

Command:

```bash
sudo ksuite-mail init
```

Result:

- Creates the dedicated `ksuite-mail` service user when missing.
- Creates required config, secret, cache, and runtime directories.
- Installs or prints systemd service/socket unit configuration.
- Prompts for mailbox credentials through an interactive TTY, never through command arguments.
- Stores credentials in a daemon-readable file that normal users and agents cannot read.
- Configures socket access for an existing local user group, with an optional dedicated access group if desired.
- Does not create or configure an agent-specific OS user.

### UC-009 Full Regenerativ Schweiz Mailbox Access

Actor: local user or agent.

Account policy:

```toml
policy = "full"
```

Result:

- Exposes all messages from configured RS mailboxes.
- Still hides credentials and raw IMAP access.
- Caches according to configured retention.

### UC-010 Scoped Private Mailbox Access

Actor: local user or agent.

Account policy:

```toml
policy = "domain"
domains = ["regenerativ.ch"]
```

Result:

- Exposes only messages where configured `From`, `To`, `Cc`, or `Bcc` headers match `regenerativ.ch`.
- Does not treat body text, embedded previous replies, signatures, or attachment contents as policy matches.
- Uses server-side IMAP `UID SEARCH` before any `FETCH`.
- Does not download, cache, index, or expose non-matching private messages.

### UC-011 Probe Infomaniak IMAP Behavior

Actor: local human user or implementation agent through the local CLI.

Command:

```bash
ksuite-mail probe imap --account <account-ref> --json
```

Result:

- The CLI talks to the daemon through the Unix socket.
- The account reference names an existing daemon-side account configuration and is mandatory.
- The daemon resolves credentials internally and probes Infomaniak IMAP behavior for that account.
- The probe returns sanitized capability, folder, extension, and behavior diagnostics.
- The probe does not expose credentials, raw IMAP command execution, message subjects, message bodies, raw headers, attachment names, or arbitrary provider text.

## Functional Requirements

### FR-001 Standalone Operation

The tool must work without OpenClaw.

OpenClaw, MCP, cron jobs, scripts, and other agents may call the CLI later as adapters.

### FR-002 Multi-Account Mail Surface

The daemon must expose one combined mail surface across configured accounts.

Each account must define:

- id
- email
- host
- port
- TLS mode
- username
- credential reference
- policy
- folders

### FR-003 Account Policies

Supported first-version policies:

- `full`: expose all messages from the account.
- `domain`: expose only messages matching configured domains in relevant address headers.

### FR-004 Domain Header Matching

For `policy = "domain"`, evaluate:

- `From`
- `To`
- `Cc`
- `Bcc` when available, mainly for sent mail.

Matching rules:

- `person@regenerativ.ch` matches `regenerativ.ch`.
- `person@fake-regenerativ.ch` does not match.
- Subdomains require explicit configuration.
- Mentioning a configured domain in the email body is not enough to make the message visible.
- `Sender`, `Reply-To`, `Delivered-To`, and other transport or convenience headers are not first-version policy match inputs.

### FR-005 Server-Side Filtering First

Domain-scoped accounts must run server-side IMAP search before fetching headers or bodies.

Preferred implementation:

- Run multiple simple `UID SEARCH HEADER <field> <domain>` calls.
- Union matching UIDs locally.
- Fetch only matching UIDs.
- Re-validate exact domains locally before caching.

### FR-006 On-Demand Remote Refresh

The daemon must not run a background mail refresh in the first version.

Mail is refreshed only when a relevant CLI/API command is triggered, such as:

- `inbox`
- `list`
- `search`
- `show`
- `thread`
- `context`

Refresh behavior:

- Use IMAP UID state to identify new or changed remote data.
- Store local refresh state per account and folder.
- Write policy-approved fetched mail to the local cache.
- Reuse cached messages on later commands when remote state indicates they do not need to be fetched again.
- Do not use remote body hash comparison as the primary freshness mechanism.

Content hashes may be stored for local integrity, deduplication, or diagnostics after a message has already been policy-approved and fetched.

### FR-007 Stable Message IDs

The CLI must expose stable opaque ids.

Internal mapping may include:

- account id
- folder
- UIDVALIDITY
- UID

### FR-008 JSON Output

Agent-facing commands must support structured JSON.

Compact JSON is the default for agent use.

JSONL may be added for streaming or batch use.

### FR-009 Structured Refresh Status

JSON responses that depend on remote mail refresh must include structured refresh metadata.

Required metadata:

- whether a remote refresh was attempted
- whether remote refresh succeeded
- timestamp of the last successful refresh
- safe warning/error codes when remote refresh failed
- whether returned results are local-only or potentially stale

Remote errors must not leak email addresses, subjects, bodies, attachment names, credentials, or raw provider messages that could contain private data.

### FR-010 Progressive Disclosure

Default commands must return minimal information.

More content requires explicit flags:

- `--preview`
- `--max-chars`
- `--thread-brief`
- `--include-embedded-replies`
- `--fields`

Embedded previous replies means older thread content copied inside a newer email body, for example the text below "On DATE, NAME wrote:". First-version `domain` accounts should not expose embedded previous replies unless a later product decision explicitly accepts that privacy risk.

Message flags may be returned as diagnostic metadata when available, but first-version agent workflows must not depend on unread/seen state.

### FR-011 No Raw IMAP Escape Hatch

The CLI must not expose raw IMAP commands to agents.

The daemon owns IMAP sessions and policy enforcement.

### FR-012 Read-Only First Version

First version must support read-only mail operations only:

- list
- inbox
- search
- show
- thread
- context
- doctor
- probe

Setup operations such as `init` may create local users, files, directories, and systemd units, but must not mutate remote mailbox content.

Send/draft/reply must be separate later work with human confirmation.

### FR-013 Remote Deletion And Move Handling

When a configured folder is refreshed:

- Messages observed as deleted or expunged remotely must be deleted locally.
- Messages moved out of a configured folder must be treated as deleted from that folder.
- Messages moved into another configured folder must be added or updated when that folder is refreshed.
- Messages moved into non-configured folders are outside the first-version visible mail surface.

### FR-014 Fixed Provider Probe

Provider probing must be a fixed checklist, not an arbitrary IMAP command runner.

Provider probing is exposed through `ksuite-mail probe imap --account <account-ref> --json`, not through `doctor`. `doctor` remains focused on local setup and health diagnostics.

The account reference is mandatory and must refer to an existing configured account. The probe must not infer a default account and must not introduce test-only account registration, credential passing, or raw mailbox selection.

The probe may check:

- `CAPABILITY`
- `LIST`
- `EXAMINE` or read-only `SELECT`
- `UIDVALIDITY`
- `UIDNEXT`
- `UID SEARCH HEADER`
- UID range search
- `BODY.PEEK`
- `CONDSTORE` / `HIGHESTMODSEQ`
- Sent folder naming
- `Bcc` availability in sent mail

Probe output must use safe structured diagnostics such as booleans, counts, capability names, folder names, and redacted error codes.

Behavior checks must use a controlled test account or folder when the answer depends on message content or folder state.

Domain-header search behavior checks apply to `domain` policy accounts with configured domains. For `full` policy accounts, those checks must return `not_applicable` because full-policy access does not depend on domain-scoped filtering.

Minimum useful fixture coverage:

- a message whose `From` matches a configured domain
- a message whose `To` matches a configured domain
- a message whose `Cc` matches a configured domain
- a sent message with available `Bcc` matching a configured domain, if Infomaniak exposes that header
- a non-matching message that must remain invisible
- enough UID spacing to test UID range search behavior

When fixtures are missing, the probe must return an `inconclusive` result for affected checks instead of reporting provider behavior as supported or unsupported.
