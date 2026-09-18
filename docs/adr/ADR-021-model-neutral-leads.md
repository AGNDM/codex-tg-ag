# ADR-021: Model-Neutral Lead Agents

- Status: accepted
- Supersedes: ADR-020's Sol/Astra Lead model restriction
- Related: `docs/process/big-agent-registry-brief.md`

## Context

The first Lead Agent slice limited operator-facing Leads to Sol or Astra and used Luna only through a native custom-agent role. That restriction is unnecessary because Codex App Server already provides the authoritative model catalog and every Lead turn carries an explicit persisted model and reasoning effort.

The Lead's durable identity is its Telegram topic, Codex thread, and bound Codex Project. Its model is a configurable execution setting, not part of that identity.

## Decision

- New Leads continue to default to `gpt-5.6-sol` with `medium` reasoning.
- The operator may change a Lead to any non-hidden model returned by App Server `model/list`.
- `/agent model` accepts a full model ID and the convenience aliases `sol`, `luna`, and `astra`.
- When reasoning effort is omitted, the daemon stores the selected model's advertised default. An explicitly supplied effort must be supported by that model when the catalog provides supported values.
- A model change applies to new turns and preserves the Lead's topic, thread, Project, history, policy, and authority.
- Native custom-agent names such as `luna_executor` and `astra_advisor` remain delegation roles. They are not model IDs and selecting the same underlying model does not import their role instructions.
- `lead-default` version 3 uses model-neutral Lead wording while preserving delegation, escalation, review, and critical-path approval rules.

## Consequences

- Luna-class and other available Codex models can be operator-facing Leads when explicitly selected.
- App Server remains the source of truth for model availability and reasoning compatibility.
- A catalog read failure or invalid model/effort leaves the persisted Lead setting unchanged.
- Existing Leads retain their configured model during the policy upgrade.
- Live Telegram validation is required before deployment because this changes a public command and the policy injected into persistent Lead threads.
