# ADR-022: Bound background thread polling and compact snapshot storage

## Status

Accepted.

## Context

The observer loop stores a `next_poll_after` deadline, but previously read every
tracked thread on every observer tick. Bound idle threads therefore received a
full `thread/read` with turns every five seconds. The returned history was kept
both in `threads.raw_json` and again inside the compact snapshot. A terminal
gate state that had already accepted an interrupted turn after its grace window
was also rewritten on every later observation of the same turn.

Production diagnosis found repeated App Server session replacement and an
`initialize` timeout alongside a 149 MiB state database, multi-megabyte compact
snapshots, and one accepted interrupted turn observed more than 9,000 times.
These facts establish polling and write amplification. They do not, by
themselves, prove that amplification was the sole cause of the initialize
timeout.

## Decision

- The observer checks `next_poll_after` before a full thread read.
- Missing, malformed, or expired deadlines remain immediately pollable.
- Catch-up conditions, including newer indexed thread metadata, bypass a future
  deadline so completion and approval recovery are not delayed.
- An accepted `grace_expired` terminal state is a stable tombstone for the same
  interrupted turn. A later non-interrupted snapshot still clears it through
  the existing recovery path.
- Compact snapshots omit `Thread.Raw`. Full history remains in
  `threads.raw_json`, and older-panel Details reconstruction uses its existing
  fallback to that record.

## Consequences

Idle bound threads use their existing 30-second deadline instead of being read
every observer tick. Active and deferred turns retain their shorter deadlines,
while explicit hot polling remains independently bounded. Repeated terminal
observations stop generating redundant state writes and expiry logs. Compact
snapshots no longer duplicate the full raw thread history.

Metadata-only polling, arbitrary output truncation, database vacuum policy, and
App Server reconnect redesign are intentionally deferred. They require separate
compatibility work for approval/reply recovery, completion delivery, and Details
exports.
