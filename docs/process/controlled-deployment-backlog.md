# Controlled Deployment Backlog

Status: deferred.

## Goal

Replace repeated manual `sudo` deployment commands with a small, root-managed,
auditable deployment broker while preserving the existing systemd sandbox,
including `NoNewPrivileges=true` and `ProtectSystem` restrictions.

## Intended Boundary

- Axiom builds and tests artifacts, then writes them only to an unprivileged
  staging area.
- A root-owned broker validates a fixed manifest, snapshots the artifact,
  installs `releases/<commit>`, atomically switches `current`, restarts
  `telegram-agent.service`, checks health, and rolls back on failure.
- The broker accepts no arbitrary command, destination, service name, or path.
- Deploy requests, approval identity, digest, transaction stages, result, and
  rollback are recorded in journald without credentials.
- Workspace write, Git push, and Linux root deployment remain separate powers.

## Approval Decision Deferred

A same-UID approval Agent/topic is useful as an operational guard but cannot be
a strong security boundary: another process under that UID can forge the same
broker request. A stronger design needs an independently protected Telegram
approval ingress, such as a root-only deployment bot token or a separate trusted
UID. The operator has deferred this feature before selecting that boundary.

No broker, root bootstrap, permission change, push, or deployment has been
implemented for this backlog item.
