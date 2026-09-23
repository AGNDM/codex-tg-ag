# GPT-6 Sol and Luna compatibility brief

## Goal

Make `gpt-6-sol` the default model for newly created Lead Agents and allow the
operator to select both `gpt-6-sol` and `gpt-6-luna` through the existing
Telegram model controls.

## Contract

- New Leads use `gpt-6-sol` with `medium` reasoning.
- Existing Leads retain their persisted model and reasoning effort.
- `/agent model sol` resolves to `gpt-6-sol`; `/agent model luna` resolves to
  `gpt-6-luna`; `/agent model astra` continues to resolve to `gpt-6-astra`.
- Full model IDs remain accepted when the non-hidden model appears in App Server
  `model/list`, including explicit GPT-5.6 IDs.
- Omitting effort uses the model catalog default. Explicit effort must be listed
  by App Server; GPT-6 Sol and Luna may therefore expose `none` through `max`.
- Changing a Lead model affects new turns only and does not change its topic,
  thread, Project, history, policy, permissions, or authority.

## Validation

- Unit tests cover the GPT-6 Sol creation default, GPT-6 aliases, `max`
  reasoning, and an explicit GPT-5.6 model ID.
- Required repository tests and build run in GitHub Actions when local Go is not
  available.
- Before deployment, verify the deployed Codex App Server advertises both GPT-6
  models through `/model`; code support cannot grant account/model availability.
