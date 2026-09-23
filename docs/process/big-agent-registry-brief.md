# Big Agent Registry Feature Brief

## Problem

An operator can bind Telegram chats and topics to Codex threads, but cannot manage a small roster of durable, named lead agents. Each lead should retain its own thread, responsibilities, project context, and task queue while delegating bounded work internally.

## Goal

Add a durable agent registry that binds a named lead agent to a Telegram topic and Codex thread, records its current project and policy, and makes that identity visible in Telegram. New Leads default to `gpt-6-sol` with `medium` reasoning; the operator may select any model currently available from Codex. Subagent activity remains an internal implementation detail unless the lead reports it.

## Non-goals

- Replacing Codex App Server as runtime authority.
- Building a second agent harness or model loop.
- Direct operator routing to delegated custom-agent threads.
- Automatic Git merge, push, deployment, spending, or destructive production changes.
- Supporting multiple human operators in the first slice.

## UX / Operator Flow

The operator opens a private Telegram forum topic for a lead, creates or binds the lead, assigns an existing or new project, and sends normal messages in that topic. Status and final messages carry the lead identity. Routine Codex/Guardian approvals may remain automatic. A lead must discuss business-critical actions in Telegram before proceeding.

Initial command surface:

- `/agents` lists durable leads and their project/thread state.
- `/agent create <name>` creates a Sol medium Lead for the current topic.
- `/agent show` shows the lead bound to the current topic.
- `/agent project <project>` changes the lead's current registered Codex Project.
- `/agent model <model-id|sol|luna|astra> [effort]` selects any model advertised by App Server for future Lead turns.
- `/agent policy` reports the embedded policy and applied version.
- `/agent policy apply` injects the current policy into the persistent lead thread.

## Domain Model

`LeadAgent`: stable id, display name, Telegram chat/topic route, Codex thread id, model, reasoning effort, official Codex project id, status, policy id/version, timestamps.

The Codex `threadId` remains durable runtime identity. Telegram routes and the lead id are control-plane metadata stored in SQLite.

## Architecture

Add `internal/agents` as a cohesive registry service backed by `internal/storage`. Telegram command handlers call a small registry interface. Existing App Server lifecycle, observer, approval, and rendering paths remain authoritative and unchanged unless a vertical slice requires a narrow integration.

## Testing

- Storage migration and repository tests for create, lookup, topic uniqueness, and project update.
- Command parser/handler tests for `/agents` and `/agent show`.
- Regression checks from `docs/testing/regression-map.md`.
- `go test ./...` and `go build -buildvcs=false ./...`.
- Live Telegram topic readback before declaring Telegram-facing slices complete.

## Acceptance Criteria

- [ ] Three named lead agents can coexist with independent topic, thread, project, and model state.
- [ ] Restarting the daemon preserves all lead bindings.
- [ ] Normal messages resume the lead's durable Codex thread.
- [ ] Luna delegation is never exposed as a direct operator routing target.
- [ ] Existing project, thread, observer, and approval behavior remains compatible.
- [x] Critical-path and native delegation policy is versioned and can be injected into each persistent lead thread.
- [x] Current-policy lead turns receive a compact runtime reminder after long context or compaction.
- [x] Luna execution and Astra advice use Codex native custom agents rather than a daemon-owned scheduler.
