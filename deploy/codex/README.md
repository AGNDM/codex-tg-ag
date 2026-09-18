# Native Codex subagents

Install `agents-config.toml` as, or merge its `[agents]` table into, the service user's `~/.codex/config.toml`. Copy the two files under `agents/` to `~/.codex/agents/`.

Codex discovers each standalone agent file by its required `name`, `description`, and `developer_instructions` fields. No daemon-side role registration is needed. The global `[agents]` table only enables multi-agent tools, sets the concurrency cap, and chooses the fallback model for a spawn that does not name a custom agent.

After installation, restart the daemon so its App Server process reloads Codex configuration. Validate with a real native spawn: ask a lead to give `luna_executor` one read-only bounded task, wait for it, and report the custom agent name and model shown in the resulting subagent activity.

Use a normal persistent Codex session for this validation. Codex 0.154.0 does not register an `--ephemeral` exec thread with the collaboration router, so a native spawn from that special session shape fails with `no thread with id` even when custom-agent discovery is configured correctly.

## Ubuntu 24.04 bubblewrap

Ubuntu 24.04 enables AppArmor mediation of unprivileged user namespaces by default. If Codex reports that bubblewrap cannot create a user namespace while `kernel.unprivileged_userns_clone=1`, check `kernel.apparmor_restrict_unprivileged_userns` and the loaded AppArmor profiles.

Keep the global restriction enabled. Install Ubuntu's maintained `apparmor-profiles` package, copy its disabled-by-default `bwrap-userns-restrict` extra profile into `/etc/apparmor.d/`, and load it with `apparmor_parser`. That profile grants `/usr/bin/bwrap` the namespace operations it needs, then stacks child processes under `unpriv_bwrap` and denies capabilities there. This is a host-wide security-boundary change for callers of `/usr/bin/bwrap`; require explicit administrator approval before enabling it.

Validate both layers after loading the profile: first run a minimal read-only `bwrap --unshare-user` command, then ask a persistent lead to delegate one read-only shell task to `luna_executor`, wait, and independently verify the output. Restart the Telegram daemon afterward so the production App Server starts against the verified host policy.
