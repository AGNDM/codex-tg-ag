# Native Codex subagents

Install `agents-config.toml` as, or merge its `[agents]` table into, the service user's `~/.codex/config.toml`. Copy the two files under `agents/` to `~/.codex/agents/`.

Codex discovers each standalone agent file by its required `name`, `description`, and `developer_instructions` fields. No daemon-side role registration is needed. The global `[agents]` table only enables multi-agent tools, sets the concurrency cap, and chooses the fallback model for a spawn that does not name a custom agent.

After installation, restart the daemon so its App Server process reloads Codex configuration. Validate with a real native spawn: ask a lead to give `luna_executor` one read-only bounded task, wait for it, and report the custom agent name and model shown in the resulting subagent activity.
