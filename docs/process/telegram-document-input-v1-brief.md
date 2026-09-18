# Telegram Document Input v1

## Goal

Let an operator attach one Telegram document to one Codex turn. A document with
a caption starts or steers exactly one turn. A text reply to a previously sent
document does the same. The daemon downloads the document into the bound
Project before handing Codex a prompt containing the operator text and a safe
Project-relative path.

## User Contract

- A document with a non-empty caption is one input: document plus caption.
- A document without a caption does not start a Codex turn and receives a short
  instruction to reply to that document with the task.
- A non-empty text reply to a document is one input: replied document plus text.
- Download, size, routing, or persistence failure produces a direct error and
  never starts or steers a turn.
- Stored documents live under a bridge-owned directory inside the bound
  Project. Paths cannot escape that Project, and unsafe filenames are reduced
  to safe basenames.

## Scope

- Telegram `document` metadata and `caption` parsing.
- Bot API `getFile` and authenticated file download.
- A conservative configurable-or-constant maximum document size.
- Project-local persistence and a prompt that names the saved relative path.
- Existing routing precedence and active-turn steering remain authoritative.

## Out Of Scope

- Photos, albums, video, voice, and App Server `localImage` / `localAudio`.
- Multiple attachments in one turn.
- Time-window merging of unrelated Telegram updates.
- Content extraction, MIME conversion, virus scanning, and background cleanup.
- Files sent outside a route that resolves to a bound Project directory.

## Minimal Tests

1. Telegram decoding and dispatch preserves document metadata and caption.
2. A captioned document downloads safely and sends exactly one prompt containing
   the caption and Project-relative path.
3. A text reply to a document sends exactly one equivalent prompt; a bare
   document starts no turn.
4. Oversized files, download errors, and unsafe routing do not call App Server.

## Validation

- Run focused package tests first, then `go test ./...` and
  `go build -buildvcs=false ./...` through GitHub Actions if the Azure host is
  resource constrained.
- Live Telegram validation remains required before declaring the feature fully
  deployed: captioned document, bare document plus reply, and one rejected
  oversized document.
