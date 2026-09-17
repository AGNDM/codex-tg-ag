# ADR-022: Agent-Requested Telegram File Delivery

Status: accepted for the first implementation slice.

## Context

An operator may ask a Telegram-bound Codex Lead to return a file from its bound
Project. App Server does not currently let the bridge add a dynamic tool to an
already-running persistent thread, and ordinary final text must not be treated
as an unrestricted local-file command.

## Decision

For Telegram-originated turns, the bridge adds a random per-turn nonce and a
small final-answer protocol instruction to the App Server input. The Agent may
request at most three deliveries in one standalone `codex-tg-file` JSON block.
The bridge removes that block from the visible Final Card and processes it only
after the turn is terminal.

Delivery is allowed only when all of these checks pass:

- the turn has a persisted Telegram chat/topic origin and the nonce matches;
- the protocol version and JSON fields are exact;
- each path is relative to the turn's Project root and cannot escape it;
- the opened target is a regular file and protected runtime, credential, Git,
  SQLite, session, and environment paths are rejected;
- each file is no larger than 50 MB, the Telegram cloud Bot API document limit.

The file body is streamed into Telegram multipart upload. SQLite atomically
freezes directives to the final-answer fingerprint and records one delivery row
per directive before network IO. A delivered row is never sent automatically a
second time. An interrupted or ambiguous upload is recorded as `unknown` and
requires a new explicit operator request rather than an automatic retry.

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
