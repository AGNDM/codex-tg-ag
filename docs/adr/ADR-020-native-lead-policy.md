# ADR-020: Native Codex Lead Policy

## Decision

Lead orchestration uses Codex's native subagent tools and custom-agent configuration. The Telegram daemon does not implement a second scheduler or model loop.

The daemon embeds a versioned `lead-default` policy, records `policy_id` and `policy_version` for each durable lead, and exposes `/agent policy` plus `/agent policy apply`. Applying a policy starts a normal turn in the lead's persistent App Server thread. If an outdated lead receives a normal task first, the daemon injects the full policy with that task and records the upgrade after App Server accepts the turn. Current-policy lead turns receive a compact reminder so the critical delegation and approval rules survive long context and compaction.

Policy version 2 also gives every lead a durable environment model: it runs on a small Azure Linux server, receives operator input through its Telegram topic, is backed by Codex and Codex App Server, and is bound one-to-one to a real Codex Project. The bridge remains a control and presentation layer rather than a competing agent runtime. Leads should conserve host resources and re-read deployed configuration and repository documentation before changing infrastructure assumptions.

Codex custom agents provide the model-specific workers:

- `luna_executor`: `gpt-5.6-luna`, low reasoning, bounded execution.
- `astra_advisor`: `gpt-6-astra`, low reasoning, read-only expert advice.

The global Codex `[agents]` configuration enables native multi-agent tools, caps concurrent subagents at two, and defaults unspecified subagents to Luna low. The Sol lead remains the operator-facing agent and reviews all subagent results.

Every Telegram-started lead turn sends the App Server `turn/start` top-level `model` and `reasoning_effort` overrides, including ordinary turns outside collaboration mode. This makes the persisted Sol lead setting effective instead of relying on the thread's previous model.

Lead turns also send Codex-native `sandboxPolicy: workspaceWrite`, restricted to the bound Project root, with `approvalPolicy: on-request` and `approvalsReviewer: auto_review`. This prevents a prior read-only CLI or audit turn from leaving sticky thread permissions that silently block later implementation. Ordinary non-Lead threads keep their existing permissions, and `astra_advisor` remains read-only through its custom-agent definition.

Custom roles are standalone TOML files discovered by Codex under `~/.codex/agents/` (or project-local `.codex/agents/`) using their `name` field. They are not registered in a daemon-owned role table.

## Consequences

- Codex remains responsible for spawning, waiting, steering, and consolidating subagent work.
- Policy changes are auditable and existing leads can report whether an update is pending.
- Project `AGENTS.md` continues to define project-specific work rules. It does not replace the operator-level lead policy.
- Applying a policy consumes one normal lead turn because the instruction becomes part of the durable thread history.
- Runtime reminders add a small token cost to Telegram-originated lead messages.
- Leads can edit their bound Project locally; automatic review may approve low-risk requests, while the Lead Policy still requires explicit operator direction for push, merge, deployment, credentials, permissions, destructive actions, and other critical paths.
- A daemon rollback refuses to overwrite a newer or foreign policy version.
