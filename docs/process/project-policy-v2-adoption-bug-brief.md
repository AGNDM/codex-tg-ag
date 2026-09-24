# Project Policy v2 Adoption Bug

Status: implemented locally; CI and live Telegram validation pending.

## Symptom

Some Project-bound Agents still receive the full legacy Lead policy reminder
and protocol-v1 file-delivery instructions with a per-turn nonce. Adopted
Projects should receive only the short `project-v1` / `file=v2` marker and use
nonce-free protocol v2 from their root `AGENTS.md`.

## Investigation Target

- Determine whether affected Project roots lack the exact regular-file
  `Codex-TG Project Agent Policy: project-v1` marker or whether policy detection
  is using a stale or incorrect cwd.
- Verify Project creation and binding preserve a root `AGENTS.md` suitable for
  every Agent working in that Project.
- Keep legacy protocol v1 for genuinely unadopted Projects and already-created
  turns.

## Acceptance

- Adopted Projects receive the compact per-turn marker rather than the full
  policy text.
- New turns in those Projects use file protocol v2 without a nonce.
- Legacy Projects and in-flight v1 turns remain compatible.
- Tests cover at least two different bound Projects so the result is not tied
  to Axiom's Project root.

## Decision

Add an explicit `/agent policy install` command. It resolves only the current
Lead's bound Codex Project root through App Server and creates a complete,
public-safe `AGENTS.md` template with exclusive-create semantics. It never
overwrites or appends to an existing file, symlink, directory, or other entry;
existing Project rules require a manual merge.

`/agent project` reports whether the selected Project uses legacy prompts or
has adopted `project-v1`. `/agent policy` reports the same status. Installation
affects new turns only; a running protocol-v1 turn keeps its frozen nonce.
