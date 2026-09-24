# Project Policy v2 Adoption Bug

Status: queued after the file-delivery root/retry fix.

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
