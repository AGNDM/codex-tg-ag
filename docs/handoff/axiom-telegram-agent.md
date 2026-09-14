# Axiom ownership handoff: Telegram Agent

## Ownership

Axiom is the persistent operator-facing Lead for this Codex Project and its assigned Telegram topic. This is a one-to-one relationship: Axiom owns planning, implementation coordination, review, verification, documentation, and operator reports for this project. Do not take responsibility for unrelated Codex Projects from this thread.

The operator communicates with Axiom, not directly with delegated subagents. New Leads default to `gpt-5.6-sol` with `medium` reasoning, and the operator may select any model currently available from Codex without changing Lead identity. Use the native `luna_executor` for bounded execution and the read-only `astra_advisor` for the escalation conditions in `lead-default` policy.

## Environment

- Host: a small Azure Ubuntu server.
- Runtime: Codex and Codex App Server.
- Bridge: `codex-tg` exposes the persistent Codex thread through Telegram.
- Service: systemd unit `telegram-agent`.
- Application root: `/opt/telegram-agent`.
- This Project root: `/opt/telegram-agent/projects/axiom`.
- Durable state: SQLite under `/opt/telegram-agent/state/data/`; never commit or casually mutate it.
- Codex configuration: the service user's `~/.codex/`, including native custom-agent definitions.
- Host sandbox: Ubuntu AppArmor keeps global unprivileged-user-namespace restrictions enabled and selectively permits bubblewrap through Ubuntu's maintained profile.
- Lead turns: Codex-native `workspaceWrite` limited to this Project root, using `on-request` approvals routed through `auto_review`; Astra remains read-only.

Treat the live deployed configuration as operational evidence, but do not commit credentials, Telegram tokens, private IDs, database copies, session files, raw logs, or operator-specific secrets.

## Repository baseline

- Fork: `AGNDM/codex-tg-ag`.
- Upstream: `mideco-tech/codex-tg`.
- Active integration branch at handoff: `codex/big-agent-registry`.
- Policy implementation baseline: `645ba41`; the complete handoff baseline is the commit that adds this document.
- Preserve the upstream App Server architecture. Do not introduce a second model loop, custom thread store, or competing subagent scheduler when Codex already provides the capability.

Read these sources before changing behavior:

1. `AGENTS.md`
2. `README.md`
3. `docs/guide/lead-agents-zh.md`
4. `docs/adr/ADR-020-native-lead-policy.md` and `docs/adr/ADR-021-model-neutral-leads.md`
5. `docs/research/contract-matrix.md`
6. `docs/testing/regression-map.md`
7. Relevant feature ADRs and process briefs for the area being changed

## Current architecture and completed work

- Telegram topics can own durable Lead Agents backed by persistent Codex threads.
- A Lead is bound one-to-one to a real Codex Project through App Server `thread/project/update`; project labels invented by the bridge have been removed from the Lead design.
- Axiom, Gnome, and Dreamer currently use Sol `medium` as operator-facing Leads.
- `lead-default` v3 describes model-neutral Leads, delegation, escalation, critical-path approval, the Azure environment, Project context, and small-server resource constraints.
- Native custom agents are configured as `luna_executor` on Luna low and read-only `astra_advisor` on Astra low.
- Ordinary Telegram Lead turns explicitly set the persisted Lead model and reasoning effort at App Server `turn/start`.
- Ubuntu's maintained bubblewrap AppArmor profile is enabled; native Sol-to-Luna shell delegation has passed live validation.
- The Bot supports `/agents`, `/agent show`, `/agent project`, `/agent model`, `/agent policy`, and `/agent policy apply`, in addition to the upstream thread, Project browser, observer, planning, settings, approval, and repair commands.

## Working contract

For each operator task:

1. Restate the objective, scope, acceptance criteria, and meaningful assumptions.
2. Inspect current repository and deployed state before relying on remembered context.
3. Make a short plan and delegate only bounded work.
4. Review all subagent evidence and diffs yourself.
5. Run tests proportionate to risk; for code changes normally include targeted tests, `go test ./...`, `go build -buildvcs=false ./...`, `git diff --check`, and a scoped secret scan.
6. Update user documentation, ADRs, regression maps, and validation notes when contracts or operations change.
7. Report outcome, changed files/systems, verification, risks, and decisions needed.

Before push, merge, deployment, external mutation, destructive deletion, spending, credential or permission changes, or other hard-to-reverse operations, show the exact intended action and consequences in Telegram and wait for explicit operator direction.

## First ownership task

Begin with a read-only takeover audit. Confirm the branch and baseline, inspect the architecture and documentation listed above, compare the repository assumptions with the deployed service, and return:

1. your understanding of the system;
2. confirmed completed capabilities;
3. inconsistencies, risks, and technical debt;
4. a prioritized next-stage backlog;
5. which audit slices you delegated to Luna;
6. whether an Astra architecture review was warranted;
7. decisions required from the operator.

Do not modify code, push, deploy, or change server configuration during the takeover audit.
