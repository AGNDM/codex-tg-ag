# Interrupted turn routing bug brief

## Problem

An implicit Telegram-origin `interrupted` snapshot can leave the bridge's stored
thread active even when the snapshot has no evidence that the turn can still
accept input. The terminal gate deliberately preserves the live panel during the
ADR-012 recovery window, but input routing can mistake that preserved UI state for
permission to call `turn/steer`. After the window expires, stale top-level thread
metadata, SQLite fallback, or removal of the defer marker during terminal logging
can restore the old active identity and repeat the cycle.

The absence of tool output is a useful reproduction shape, not proof that a turn
is dead. A legitimate active turn may have no tool output.

### Additional reproduction: asynchronous tool result is orphaned

On 2026-09-22, a Telegram-origin Lead turn started a bounded code-mode query and
received `Script running with cell ID ...`. Before the operator sent another
message, App Server marked the turn `interrupted`. The tool cell could still
finish, or an individual tool item could already appear `completed`, while the
turn no longer resumed model execution and never produced a final answer.

Treat this as a distinct unresolved lifecycle case. A running or completed tool
item is not itself proof that the model turn can still resume, but the bridge must
not silently present a healthy active run when the tool continuation has been
orphaned. Investigation must correlate the App Server turn transition, tool/cell
completion, and delivery of the tool result back to the model. The expected UX is
either continued execution through a final answer or a prompt terminal error that
states the tool continuation was lost; indefinite active/interrupted oscillation
is invalid.

### Confirmed repair-isolation defect

The App Server request client currently races an internal response timer against
the caller context deadline. The same `thread/read` deadline can therefore return
either `context.DeadlineExceeded` or an ordinary `request timeout` error. The
daemon exempts only context cancellation/deadline errors from automatic repair;
the ordinary timeout requests a repair that closes both the shared poll and live
App Server sessions. Closing live can interrupt unrelated active turns owned by
other Lead Agents.

The historical `repair.last_reason=thread_read` state proves this repair path has
been requested, but does not by itself prove that every observed interruption was
caused by it. Incident-level attribution requires a matching timeline of the
failed read, repair request, live close/generation change, and turn transition.

The follow-up fix should first make request timeouts consistently wrap
`context.DeadlineExceeded`, then isolate automatic poll and live repair by role
and expected generation. Poll repair must not close live, invalidate approvals,
or disturb active turns. Explicit operator `/repair` may remain a full repair.
Transport EOF/process exit must be surfaced as a typed transport failure so a
dead poll process does not degrade into endless timeouts. ADR-012's 90-second
terminal gate is not the cause and should remain unchanged until the session
repair defect is fixed and re-evaluated.

## Goal

For one turn identity, keep the first implicit `interrupted` deadline fixed. While
the snapshot remains ambiguous, preserve the live Telegram panel but do not submit
operator input. Once the deadline expires, persist the turn as terminal, clear its
old active identity, and prevent metadata fallback from making it steerable again.
Allow steering only after App Server provides explicit active or waiting evidence
for that same turn.

## Non-goals

- Do not remove ADR-012's transient interrupted recovery window.
- Do not infer liveness from the presence or absence of tools or tool output.
- Do not queue hidden operator input or automatically replay rejected input.
- Do not start a parallel turn while the old turn is still ambiguous.
- Do not change App Server transport or add another runtime/state store.

## UX / Operator Flow

- During the recovery window, refresh with `thread/read` before routing input.
- If the same turn explicitly recovers to active or waiting, use its normal input
  route.
- If it remains implicitly `interrupted`, attempt `turn/steer` once for the
  selected explicit target and let App Server decide whether that exact turn is
  still active. Do not call `turn/start` after an uncertain steer result.
- After the recovery deadline confirms `interrupted`, a new operator message may
  start one new turn. Replies and armed steer state must not target the old turn.

## Domain Model

The existing per-thread-and-turn terminal-gate state remains the lifecycle record.
Its first-seen time and expiry are monotonic for that turn. Terminal logging must
not clear an accepted or still-relevant gate record. Unknown or incomplete reads
do not constitute recovery; explicit same-turn active/waiting evidence does.

UI visibility and input eligibility are separate decisions:

- deferred interrupted: panel remains live, input is temporarily ineligible;
- explicitly recovered: panel and input return to active behavior;
- accepted terminal: old active identity is cleared and input may create a new
  turn;
- explicit stop: terminal immediately, as today.

## Architecture

Keep the change inside `internal/daemon` and the existing App Server normalization
boundary:

1. Expose one terminal-gate input disposition used by armed, reply, and bound
   active-turn routes before any `TurnSteer` call.
2. When an interrupted decision becomes terminal, normalize only the matching old
   `ActiveTurnID` and status before persistence. Preserve a different, explicitly
   newer active turn.
3. Prevent `mergeThreadMetadata` from restoring an active ID contradicted by the
   accepted latest-turn terminal evidence.
4. Make terminal diagnostics idempotent and keep the fixed gate deadline instead
   of deleting it and reopening the grace window.

## TDD sequence

1. A service test expects zero steer, zero start, and an explicit not-submitted
   response when input arrives during the interrupted grace window.
2. A gate test expects `interrupted -> inconclusive read -> interrupted` to retain
   the original deadline.
3. An expiry service test refreshes twice through terminal logging, expects the
   stale active identity to remain cleared, then starts one new turn without
   steering the old one.

Existing tests continue to cover active recovery, waiting, explicit stop,
active-but-not-steerable, and no-active-steer fallback behavior.

## Live validation

Use a dedicated test topic/thread. During a controlled implicit interrupted state,
send input inside the grace window and verify through Telegram readback that the
bridge attempts one steer without starting a parallel turn. After expiry, send a
new task and verify one new turn ID and one `New run` only after an idle re-read,
with no steer call for the old turn. Also verify a normal
long-running turn still accepts steering. Correlate sanitized lifecycle logs and
SQLite gate state. If a controlled implicit interrupted state cannot be produced,
record that exact live-QA blocker.

## Acceptance Criteria

- [ ] Deferred interrupted UI state permits one `TurnSteer` attempt only for the
      selected target and never grants `TurnStart` eligibility by itself.
- [ ] The first interrupted deadline cannot be extended by polling, input,
      diagnostics, or an inconclusive read.
- [ ] Expiry clears only the matching stale active turn and remains terminal across
      repeated refreshes.
- [ ] Explicit same-turn active/waiting evidence within the window restores normal
      routing without requiring tool output.
- [ ] After confirmed interruption, one new operator message can start exactly one
      new turn.
- [ ] ADR-012, the regression map, validation notes, and tests describe the final
      contract.
