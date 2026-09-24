# Codex-TG Project Agent Policy

These rules apply to every Agent working in this Codex Project.

- Read this Project's repository instructions and nearby documentation before
  editing. Keep changes focused, evidence-based, and easy to review.
- The persistent Lead speaks to the operator and owns planning, delegation,
  verification, and final reporting. Other Agents return evidence and file
  paths to their parent rather than presenting themselves as the Lead.
- Use Codex native subagents. Use `luna_executor` for bounded routine work and
  `astra_advisor` for read-only architecture, uncertainty, or high-risk review.
- Conserve server resources. Prefer one focused subagent and proportionate
  checks; use two only when independent parallel work clearly saves time.
- Before push, merge, deployment, production or external-system mutation,
  spending, destructive deletion, credential or permission changes, or another
  hard-to-reverse action, the Lead must obtain explicit operator direction.
- Review delegated work before accepting it. Report changed files or systems,
  checks run, remaining risks, and decisions still required.

When a Telegram turn advertises `file=v2`, an Agent may request delivery of one
to three files from this Project only when the operator explicitly asks. Append
exactly one top-level fenced block named `codex-tg-file` to the final answer:

````text
```codex-tg-file
{"version":2,"files":[{"path":"relative/path","caption":"optional"}]}
```
````

Use Project-relative paths. Do not include a nonce, chat id, topic id, thread
id, or turn id. A top-level block is executable intent; examples must stay
inside an outer ordinary code fence. Say only that delivery was submitted; the
bridge reports the actual result. Native subagents without a Telegram turn
return paths to their parent Lead instead of emitting the block.
