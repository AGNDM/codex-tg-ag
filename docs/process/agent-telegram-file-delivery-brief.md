# Agent To Telegram File Delivery

Status: planning.

## Goal

Let an Agent explicitly deliver a regular file from its bound Project to the
Telegram chat/topic that originated the turn.

## Capability Finding

The installed Codex App Server declares `dynamicTools` on `thread/start` and
emits `item/tool/call` requests. Its declared protocol does not provide a tool
registration field on `turn/start` or `thread/resume`. This slice does not rely
on unverified restore tricks, replace an existing Lead thread, or change its
durable identity.

## Proposed First Slice

- Each eligible Telegram turn receives a random delivery nonce in its Agent
  instructions. An Agent requests delivery with one top-level, standalone,
  machine-readable control block in its completed final response. The block
  contains a protocol version, the matching turn nonce, and up to three file
  entries. Unknown or missing fields make the request invalid.
- The bridge parses only that complete block; it never
  interprets natural-language requests, Markdown links, tool output, or
  `fileChange` events as delivery instructions.
- Each request contains one Project-relative path and an optional short
  caption. Multiple files require multiple explicit entries with a small cap.
- The bridge removes valid control blocks from the rendered final text, sends
  each accepted file through the existing in-memory `SendDocumentData` path,
  and records delivery state so snapshot replay cannot resend it. An invalid
  dedicated block remains visible as a short delivery validation error rather
  than disappearing silently.
- The destination comes from the saved Telegram origin of that exact turn. A
  request cannot provide chat, topic, user, thread, or turn identifiers.
- Only Telegram-origin turns can deliver files in this slice. Foreign GUI/CLI
  observations do not inherit a Telegram destination.
- Only a completed final can activate delivery. Partial or transient
  `interrupted` snapshots never activate it, and turns completed before the
  feature was enabled are never backfilled.
- A sent document is routed back to the producing thread/turn so replies to the
  document retain normal routing precedence.

## File Boundary

- Resolve only beneath the producing thread's Project cwd using an anchored
  filesystem root.
- Reject absolute paths, traversal, symlink escape, directories, devices,
  sockets, FIFOs, and files above 50 MB, matching the official cloud Bot API
  `sendDocument` limit.
- Reject bridge/runtime and common credential paths such as `.git`, `.env`,
  `.codex`, `.codex-tg`, session databases, and configured state directories.
- Read a bounded number of bytes from the validated handle. Filename rules are
  an accidental-disclosure guard, not isolation from a malicious same-UID
  Agent.
- Send as a Telegram document without a MIME allowlist. Caption uses the shared
  identity header plus the optional Agent caption, and delivery is silent.

## Delivery Semantics

- The first accepted final freezes the normalized directive set for that turn.
  Later final changes cannot append deliveries and are recorded as conflicts.
- Before any upload, atomically claim each `(thread, turn, directive index)` in
  SQLite. Concurrent live and polling paths therefore share one delivery path.
- Delivery state is `sending`, `sent`, `failed`, or `unknown`. A successful send
  stores the Telegram message id and message route.
- A daemon restart finds stale `sending` records and changes them to `unknown`;
  it never retries them automatically because Telegram may already have
  accepted the upload.
- Definite validation or Bot API failure becomes `failed`. A timeout after an
  upload may be `unknown`. Final Card/details show the bridge result; the Agent
  should say that it submitted a delivery, not claim that Telegram received it.
- Retrying requires a new explicit user request and turn. The operator is warned
  that retrying an `unknown` delivery may produce a duplicate.

## Future Native Tool

For newly created threads, a native dynamic tool such as
`telegram_send_file({path, caption?})` could provide immediate structured
success or failure to the Agent. This slice keeps one delivery entry point.
Dynamic tools or an MCP hot-reload path require separate capability validation
and must later call the same delivery service rather than creating parallel
security and deduplication rules.

## Minimal TDD

1. One valid directive sends exact bytes to the saved origin topic, removes the
   control block from visible final text, and saves the reply route.
2. Concurrent live/poll claims, replay of the same final, a changed later final,
   and restart recovery from `sending` do not send again.
3. Traversal, symlink escape, non-regular files, sensitive paths, and oversized
   files never call the sender.
4. A directive cannot choose another destination, and a non-Telegram turn
   cannot deliver.
5. Definite failure and unknown timeout produce clear, stable delivery state.
6. Nonce mismatch, quoted examples, ordinary code fences, Markdown links, paths
   in prose, and `fileChange` events never trigger delivery.

Live validation creates a file containing a random nonce, asks the Agent to
send it, downloads it from Telegram, verifies the nonce, and replies to the
document to confirm thread routing.
