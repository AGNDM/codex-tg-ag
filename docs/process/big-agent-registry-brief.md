# Big Agent Registry Feature Brief

## Problem

An operator can bind Telegram chats and topics to Codex threads, but cannot manage a small roster of durable, named lead agents. The operator wants to talk only to Sol-class leads; each lead should retain its own thread, responsibilities, project context, and task queue while delegating routine work to Luna internally.

## Goal

Add a durable agent registry that binds a named lead agent to a Telegram topic and Codex thread, records its current project and policy, and makes that identity visible in Telegram. Leads use `gpt-5.6-sol` or stronger. Subagent activity remains an internal implementation detail unless the lead reports it.

## Non-goals

- Replacing Codex App Server as runtime authority.
- Building a second agent harness or model loop.
- Direct operator-to-Luna conversations.
- Automatic Git merge, push, deployment, spending, or destructive production changes.
- Supporting multiple human operators in the first slice.

## UX / Operator Flow

The operator opens a private Telegram forum topic for a lead, creates or binds the lead, assigns an existing or new project, and sends normal messages in that topic. Status and final messages carry the lead identity. Routine Codex/Guardian approvals may remain automatic. A lead must discuss business-critical actions in Telegram before proceeding.

Initial command surface:

- `/agents` lists durable leads and their project/thread state.
- `/agent create <name>` creates a Sol lead for the current topic.
- `/agent show` shows the lead bound to the current topic.
- `/agent project <project>` changes the lead's current registered project.

## Domain Model

`LeadAgent`: stable id, display name, Telegram chat/topic route, Codex thread id, model, reasoning effort, current project, status, policy, timestamps.

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

- [ ] Three named Sol-class lead agents can coexist with independent topic, thread, and project state.
- [ ] Restarting the daemon preserves all lead bindings.
- [ ] Normal messages resume the lead's durable Codex thread.
- [ ] Luna delegation is never exposed as a direct operator routing target.
- [ ] Existing project, thread, observer, and approval behavior remains compatible.
- [ ] Critical-path policy is present in each lead's instructions and produces a Telegram discussion before the action.
