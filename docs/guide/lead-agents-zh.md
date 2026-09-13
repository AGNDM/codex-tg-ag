# Telegram 大 Agent 使用手册

这套服务运行在 Azure 上，通过 `@agndm_bot` 把 Telegram topic 映射到持久的 Codex thread。每个 topic 最多有一个大 Agent；你只和大 Agent沟通，Luna 子代理由大 Agent 在内部调用。

## 当前默认配置

| Agent | 默认模型 | 默认推理强度 | 默认项目 | 当前职责由谁决定 |
|---|---|---|---|---|
| Axiom | `gpt-5.6-sol` | `medium` | 尚未绑定 Codex Project | 你在 Axiom topic 中布置 |
| Gnome | `gpt-5.6-sol` | `medium` | 尚未绑定 Codex Project | 你在 Gnome topic 中布置 |
| Dreamer | `gpt-5.6-sol` | `medium` | 尚未绑定 Codex Project | 你在 Dreamer topic 中布置 |

默认政策：大 Agent 负责与你讨论、规划、汇报并请求澄清；日常执行可以委派给 `gpt-5.6-luna` 子代理，但不会让你直接与 Luna 对话。push、merge、deploy、付费、删除数据、生产变更等关键动作必须先在 Telegram 中与你讨论。低风险、例行的工具审批可以自动处理。

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

登记当前负责的项目：

```text
/agent project
/agent project Codex项目名称或ID
```

这会更新控制面中的项目标签，不会自动移动 Git 仓库或改变文件路径。

切换当前 Lead 的模型：

```text
/agent model sol
/agent model sol high
/agent model astra
/agent model astra low
```

`sol` 对应 `gpt-5.6-sol`，省略推理强度时使用 `medium`。`astra` 对应 `gpt-6-astra`，省略时使用 `low`。可选推理强度为 `low`、`medium`、`high`、`xhigh`、`max`、`ultra`。Lead 不能切换成 Luna；Luna 只用于内部子代理执行。

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

额度建议：

- 大部分日常交涉：Lead 使用 Sol `medium`。
- 复杂规划或疑难问题：临时使用 `/agent model astra low`。
- 问题解决后：切回 `/agent model sol medium`。
- 重复、机械、可并行工作：让 Lead 委派 Luna，不要直接创建 Luna Lead。

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
