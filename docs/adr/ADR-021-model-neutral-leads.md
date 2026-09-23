# ADR-021: Model-Neutral Lead Agents

- Status: accepted
- Supersedes: ADR-020's Sol/Astra Lead model restriction
- Related: `docs/process/big-agent-registry-brief.md`

## Context

The first Lead Agent slice limited operator-facing Leads to Sol or Astra and used Luna only through a native custom-agent role. That restriction is unnecessary because Codex App Server already provides the authoritative model catalog and every Lead turn carries an explicit persisted model and reasoning effort.

The Lead's durable identity is its Telegram topic, Codex thread, and bound Codex Project. Its model is a configurable execution setting, not part of that identity.

## Decision

- New Leads default to `gpt-6-sol` with `medium` reasoning. Existing Leads retain their persisted model until the operator changes it.
- The operator may change a Lead to any non-hidden model returned by App Server `model/list`.
- `/agent model` accepts a full model ID. The convenience aliases `sol`, `luna`, and `astra` resolve to `gpt-6-sol`, `gpt-6-luna`, and `gpt-6-astra`; explicit GPT-5.6 model IDs remain valid when App Server advertises them.
- When reasoning effort is omitted, the daemon stores the selected model's advertised default. An explicitly supplied effort must be supported by that model when the catalog provides supported values.
- A model change applies to new turns and preserves the Lead's topic, thread, Project, history, policy, and authority.
- Native custom-agent names such as `luna_executor` and `astra_advisor` remain delegation roles. They are not model IDs and selecting the same underlying model does not import their role instructions.
- `lead-default` version 4 records the GPT-6 Sol creation default while preserving model-neutral Lead authority, delegation, escalation, review, and critical-path approval rules.

## Consequences

- Luna-class and other available Codex models can be operator-facing Leads when explicitly selected.
- App Server remains the source of truth for model availability and reasoning compatibility.
- A catalog read failure or invalid model/effort leaves the persisted Lead setting unchanged.
- Existing Leads retain their configured model during the policy upgrade.
- Live Telegram validation is required before deployment because this changes a public command and the policy injected into persistent Lead threads.
