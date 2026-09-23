# Telegram 大 Agent 使用手册

这套服务运行在 Azure 上，通过 `@agndm_bot` 把 Telegram topic 映射到持久的 Codex thread。每个 topic 最多有一个大 Agent；你只和大 Agent沟通，Luna 子代理由大 Agent 在内部调用。

## 当前默认配置

| Agent | 默认模型 | 默认推理强度 | 默认项目 | 当前职责由谁决定 |
|---|---|---|---|---|
| Axiom | 持久配置 | `medium` | Axiom Codex Project | 你在 Axiom topic 中布置 |
| Gnome | 持久配置 | `medium` | Gnome Codex Project | 你在 Gnome topic 中布置 |
| Dreamer | 持久配置 | `medium` | Dreamer Codex Project | 你在 Dreamer topic 中布置 |

Axiom 的 Project 已指定为本仓库的长期开发与维护 Project；其接管范围、服务器环境、验证流程和首个审计任务见 [`docs/handoff/axiom-telegram-agent.md`](../handoff/axiom-telegram-agent.md)。Gnome 与 Dreamer 保持各自独立 Project，不参与本仓库的日常所有权。

默认政策 `lead-default v4`：大 Agent 负责与你讨论、规划、复核和汇报。它知道自己运行在资源有限的 Azure Linux 小服务器上，由 Codex、Codex App Server 和 `codex-tg` Telegram bridge 组成；Telegram 是交互与控制层，Codex 才是 turn、工具、sandbox、审批和原生子代理的运行时。每个 Lead 的持久 thread 一对一绑定真实 Codex Project，并以 Project roots、thread 历史、`AGENTS.md` 和仓库文档作为稳定上下文。

Lead 通过 Codex 原生 custom agent `luna_executor`（Luna low）处理范围明确的日常执行，通过只读的 `astra_advisor`（Astra low）临时咨询架构权衡、冲突约束、两次认真尝试后仍未解决的问题和高风险审查。新 Lead 默认使用 GPT-6 Sol medium，但你可以切换为 Codex 当前提供的任意模型；Lead 仍是唯一与你对话并整合子代理结果的角色。为适应小服务器，通常一次只运行一个子代理，确有独立性和时间收益时才并发两个。push、merge、deploy、付费、删除数据、生产变更等关键动作必须先在 Telegram 中与你讨论。

Lead 的 Telegram turn 默认使用 Codex 原生 `workspaceWrite`，可写范围限定为其绑定的 Project root，并使用 `on-request + auto_review` 处理低风险审批。因此 Lead 和 Luna 可以在 Project 内编辑、运行测试和创建本地 commit；Astra 始终只读。Git push、merge 和部署仍不属于自动许可，必须先向你说明具体动作并获得批准。

## 日常使用

直接在某个 Agent 的 topic 中发送普通消息即可。Bot 会恢复该 topic 对应的持久 Codex thread，因此上下文不会因为服务重启而丢失。

示例：

```text
检查项目现状，先给我计划；不要部署。
```

查看三个 Agent：

```text
/agents
```

查看当前 topic 的 Agent：

```text
/agent show
```

查看或升级当前 Lead Policy：

```text
/agent policy
/agent policy apply
```

`apply` 会在该 Lead 的持久 thread 中启动一次正常 turn，把最新版政策写入长期上下文。之后每次 Telegram 发起的 Lead turn 都会附带一段很短的政策提醒，以抵抗长上下文和 compaction 后的遗忘。

如果 Policy 已升级但尚未手动 `apply`，下一条普通任务会把完整新 Policy 与该任务一起注入，并自动登记新版本，不需要先执行单独命令。

状态含义：

- `initializing`：首次初始化尚未同步完成。
- `working`：正在执行。
- `waiting`：等待审批或你的回答。
- `idle`：上一轮已经完成，可以接新任务。
- `attention`：上一轮失败或被中断，建议查看最后回复或 `/status`。

## 创建与配置 Agent

在一个尚未绑定 Agent 的 topic 中创建：

```text
/agent create 自定义名称
```

名称最长 40 个字符，不能与现有 Agent 重复。topic 名和 Agent 名可以不同，但保持一致更容易识别。

查看或切换当前 Lead 负责的真实 Codex Project：

```text
/agent project
/agent project Codex项目名称或ID
```

这里的项目来自 Codex App Server 的 Project 列表，不是 Bot 自造的标签。绑定时，Bot 会调用 Codex 的 `thread/project/update`，让现有主 thread 进入目标 Project，同时保留该 thread 的历史上下文；之后新 turn 使用该 Project 的首个 root。一个 Codex Project 同时只能绑定一个 Lead。

切换 Project 不会复制、移动或删除仓库文件。它改变的是主 thread 的 Codex Project 归属和后续工作目录，因此切换前应先让当前 turn 完成，并确认目标 Project 正确。

切换当前 Lead 的模型：

```text
/agent model sol
/agent model sol high
/agent model luna
/agent model astra
/agent model astra low
/agent model 完整模型ID [推理强度]
```

`sol`、`luna`、`astra` 分别指向 `gpt-6-sol`、`gpt-6-luna`、`gpt-6-astra`，也可以直接使用 `/model` 当前列出的完整模型 ID。显式输入 `gpt-5.6-sol` 或 `gpt-5.6-luna` 时，只要 App Server 仍然公布该模型，旧模型仍可使用。切换时省略推理强度，会采用目标模型由 App Server 公布的默认值；显式指定的值必须受该模型支持。模型切换不会改变 Lead 的 topic、thread、Project、历史或权限。`luna_executor` 和 `astra_advisor` 是独立的委派角色名称，不等同于 Lead 模型 ID。

模型切换从下一个新 turn 生效。如果 Agent 当前正在执行，普通消息通常会 steer 到当前 turn，而不会中途替换模型。

## 规划、回复和停止

让当前绑定 thread 进入 Codex Plan Mode：

```text
/plan 请先分析并给出执行计划
```

指定 thread：

```text
/plan THREAD_ID 请制定计划
/reply --plan THREAD_ID 请制定计划
```

向指定 thread 发消息：

```text
/reply THREAD_ID 消息内容
```

停止当前 topic 的活跃 turn：

```text
/stop
```

或停止指定 thread：

```text
/stop THREAD_ID
```

## 审批与提问

优先使用 Bot 消息下面的批准、拒绝或选项按钮。也可以使用：

```text
/approve REQUEST_ID
/deny REQUEST_ID
```

Codex 提出文字问题时，直接回复对应的 `[Plan]` 或请求卡片即可把答案送回同一个 turn。

## 模型与推理强度

以下命令控制普通 Codex thread 和 Telegram 发起的 Plan turn 的全局默认值：

```text
/settings
/model
/effort
```

它们会显示 Telegram 按钮菜单，设置持久保存在 SQLite 中。LeadAgent 不会被全局 `/model` 降级；要调整当前 Lead，使用 `/agent model ...`。

额度和委派规则：

- 新建 Lead 默认使用 Sol `medium`；之后可按任务和成本需要切换为任意可用模型。
- 复杂规划或疑难问题：Lead 可临时调用只读 `astra_advisor`，不要求 Lead 自身使用 Sol。
- 重复、机械、范围明确或适合并行的工作：Lead 调用 `luna_executor`。
- 最多同时两个子代理；这台小服务器默认优先只开一个。
- 只有你希望当前 Lead 的后续新 turn 持续使用 Astra 时，才使用 `/agent model astra low` 切换 Lead 模型。

官方 OpenAI 模型指引建议从较低 reasoning effort 开始，只有在任务确实需要时再提高，以平衡响应速度与推理深度。

## Thread 与项目命令

| 命令 | 用途 |
|---|---|
| `/threads [数量或搜索词]` | 查看缓存的 Codex threads。 |
| `/agent project` | 从 Codex App Server 读取本机真实的 Codex Project。 |
| `/agent project <名称或ID>` | 将当前 topic 的 lead 与一个 Codex Project 一对一绑定，并保留原主 thread/context。 |
| `/projects` | 原 fork 的工作区/线程浏览器；它按 thread 的 cwd 展示，不是 lead 的 Codex Project 绑定来源。 |
| `/show THREAD_ID` | 显示指定 thread 卡片。 |
| `/bind THREAD_ID` | 把当前 chat/topic 绑定到已有 thread。不要用它覆盖 Lead topic 的固定绑定。 |
| `/new PROJECT PROMPT` | 在选定项目中创建 thread 并执行首个 prompt。 |
| `/newchat PROMPT` | 在 Codex Chats 目录中新建一次 Chat。 |
| `/newthread PROMPT` | 不指定 cwd 创建新 thread。 |

## 观察与界面命令

| 命令 | 用途 |
|---|---|
| `/start` | 检查 Bot 是否在线。 |
| `/help` | 显示命令列表。 |
| `/status` | 显示 daemon、App Server 和当前路由状态。 |
| `/context` 或 `/whereami` | 显示当前 topic 的 thread 上下文。 |
| `/observe all` | 把当前位置设为全局观察输出位置。 |
| `/observe off` | 关闭全局观察。 |
| `/panelmode per_run` | 每个新 turn 使用一组新状态卡片。 |
| `/panelmode stable` | 尽量复用同一组状态卡片。 |
| `/repair` | 请求后台重建 Codex App Server 会话。 |

兼容别名包括 `/models`、`/reasoning`、`/reasoning_effort`、`/plan_mode`、`/default` 和 `/default_mode`。一般使用菜单中显示的主命令即可。

## 推荐工作方式

1. 在每个 Agent topic 的第一条业务消息中说明长期职责、项目位置和禁止事项。
2. 要求 Agent 在开始前复述目标和验收条件。
3. 日常保持 Sol `medium`；只在难题期间升到 Astra `low` 或更高。
4. 看到 `waiting` 时回答问题或处理审批按钮。
5. 完成后让 Agent给出变更、测试、风险和下一步的简短总结。

## 故障排查

- Bot 没收到群组普通消息：确认 Bot 是管理员，或使用 `/command@agndm_bot`。
- 显示 `No bound thread`：进入正确的 Agent topic，或用 `/agent show` 检查绑定。
- App Server 未就绪：先 `/status`，再 `/repair`。
- Agent 长时间显示 `working`：用 `/context` 和 `/status` 查看；必要时 `/stop`。
- 服务重启不会删除 Agent、topic 或 thread 映射，它们保存在 Azure 的 SQLite 数据库中。
- 原生子代理验证不要使用 `codex exec --ephemeral`；当前 Codex 版本的 ephemeral thread 不会进入协作路由。Bot 的 Lead thread 和正常持久 Codex 会话不受这个限制。
