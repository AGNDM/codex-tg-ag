# ADR-022: Agent-Requested Telegram File Delivery

Status: accepted for the first implementation slice.

## Context

An operator may ask a Telegram-bound Codex Lead to return a file from its bound
Project. App Server does not currently let the bridge add a dynamic tool to an
already-running persistent thread, and ordinary final text must not be treated
as an unrestricted local-file command.

## Decision

Projects that declare `Codex-TG Project Agent Policy: project-v1` as an exact
line in their root `AGENTS.md` use protocol v2. Its static instructions live in
that Project file and the bridge adds only a short capability marker. The Agent
may request at most three deliveries in one standalone `codex-tg-file` JSON block.
The bridge removes that block from the visible Final Card and processes it only
after the turn is completed.

Delivery is allowed only when all of these checks pass:

- the turn has a persisted Telegram chat/topic origin;
- the block version exactly matches the protocol frozen for that turn;
- legacy protocol v1 turns also match their persisted nonce;
- each path is relative to the Lead's bound Codex Project root and cannot escape
  it; legacy non-Lead routes use the producing thread cwd;
- the opened target is a regular file and protected runtime, credential, Git,
  SQLite, session, and environment paths are rejected;
- each file is no larger than 50 MB, the Telegram cloud Bot API document limit.

The file body is streamed into Telegram multipart upload. SQLite atomically
freezes directives to the final-answer fingerprint and records one delivery row
per directive before network IO. A delivered row is never sent automatically a
second time. An interrupted or ambiguous upload is recorded as `unknown` and
requires a new explicit operator request rather than an automatic retry.
If opening the local file fails, the bridge waits five seconds and tries that
open once more before recording a definite failure. Telegram upload itself is
never retried automatically because the first upload may already have arrived.

## Consequences

This keeps App Server authoritative for the turn and uses Telegram only as the
delivery adapter. It does not expose a general filesystem tool or accept paths
from foreign GUI/CLI turns. The 50 MB limit may be revisited only if the runtime
is deliberately moved to Telegram's Local Bot API and that operational contract
is documented and tested.

This is an accidental-disclosure guard, not isolation from a malicious process
running as the same Unix user: a same-UID process can race filesystem checks or
copy protected content into an allowed file. Upload remains synchronous with
Final Card processing and may delay observer updates for the duration of a large
transfer; a queue is deferred until live use shows that added complexity is
needed.

Protocol v1 remains available for Projects without the adoption marker and for
turns created before migration. Protocol v2 removes the nonce because routing,
authority, replay protection, and delivery identity already come from the saved
Telegram origin, App Server thread and turn, terminal status, final fingerprint,
and atomic claim. This accepts the narrow risk that a model could copy an old
v2 top-level block into another eligible Telegram turn and cause another delivery.
