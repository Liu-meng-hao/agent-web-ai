# terminal-agent（Go）

终端里的 AI agent：**长记忆落文件、带摘要、可接 MCP 与 Skill、能执行 shell、能产出可直接在浏览器打开的页面**。
交互全部发生在 TUI，浏览器只用来承载 agent 生成的成品页。

设计文档见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)（需求映射、模块划分、接口、里程碑、决策记录）。

## 快速开始

要求：Go 1.24+。

```bash
# 1. 配置
cp config/agent.yaml config/agent.local.yaml   # 可选：改成本地文件
setx AGENT_API_KEY "sk-..."                    # 或临时 $env:AGENT_API_KEY="sk-..."

# 2. 体检（不需要 key，mock 也能跑）
mkdir .tmp
@"
models:
  main:
    provider: mock
    model: mock-1
"@ | Set-Content .tmp/mock.yaml
Copy-Item config/pricing.yaml .tmp/pricing.yaml   # 可选：让 doctor 的单价检查通过
go run ./cmd/agent --config .tmp/mock.yaml doctor

# 3. 冒烟（不需要 key，走 mock provider）
go run ./cmd/agent run --config .tmp/mock.yaml "自检"

# 4. 真实模型
go run ./cmd/agent run "用一句话解释 MCP"
go run ./cmd/agent chat

# 5. 离线验证工具 + 审批链路（mock 收到 "shell:" 前缀会真的调 shell）
go run ./cmd/agent run --config .tmp/mock.yaml --yes "shell: echo hi"
go run ./cmd/agent run --config .tmp/mock.yaml "shell: echo hi"   # 不带 --yes 会被拒绝
```

构建：

```bash
make build          # -> bin/agent(.exe)
make test
```

> 沙箱或 CI 环境里用户目录不可写时，Makefile 已把 `GOPATH` / `GOCACHE` 重定向到工作区 `./.gopath`、`./.gocache`。

## 命令

| 命令 | 说明 |
|---|---|
| `agent run "<prompt>"` | 一次性执行，流式打印到 stdout，适合脚本 |
| `agent run --yes "<prompt>"` | 同上，自动批准工具调用（会执行命令，慎用） |
| `agent` / `agent chat` | 进入 TUI；Windows 默认弹出独立终端窗口 |
| `agent chat --no-window` | 在当前终端进入 TUI，不弹新窗口 |
| `agent chat --resume <id>` | 恢复指定会话（支持 id 前缀） |
| `agent chat --continue` | 恢复最近一个会话 |
| `agent sessions` | 列出会话（id、标题、时间、状态、事件数） |
| `agent sessions rm <id>` | 删除会话（需输入 yes 确认） |
| `agent replay <id>` | 只回放事件流，不调用模型（`--from`/`--to` 限 seq） |
| `agent memory list` | 列出活跃记忆 |
| `agent memory search <词>` | 检索记忆 |
| `agent memory show` | 打印 `MEMORY.md` |
| `agent memory rebuild` | 重新导入手工区并重写 `MEMORY.md` |
| `agent mcp` / `agent mcp tools` | MCP server 状态 / 已发现的工具 |
| `agent skill list` / `agent skill show <name>` | 技能目录 / 技能正文 |
| `agent preview [slug] [--open]` | 启动本地预览服务（只监听回环地址） |
| `agent doctor [--mcp]` | 检查 Go 版本、配置、API key、目录权限、单价覆盖、日志/trace 体积、技能、预览端口、MCP |
| `agent version` | 版本信息 |

全局参数：`--config <path>`（默认 `config/agent.yaml`）。

## 目录

```text
cmd/agent/          入口
internal/cli/       子命令、装配、生命周期
internal/config/    yaml 加载、${ENV} 展开、校验
internal/llm/       Provider 接口、OpenAI 兼容 SSE（含 usage）、mock
internal/agent/     turn loop、上下文预算、压缩、审批闸门
internal/session/   事件溯源、崩溃恢复、回放
internal/memory/    facts.jsonl、BM25、摘要、视图渲染
internal/tools/     registry、shell、fs、artifact、memory/skill 工具
internal/mcp/       MCP 客户端（stdio / streamable HTTP）
internal/skills/    SKILL.md 发现与渐进加载
internal/artifact/  页面写入、自检、预览服务、设计系统 embed
internal/telemetry/ JSON 日志、turn trace、token/成本记账
internal/security/  路径 jail、审批策略、受限执行器
internal/tui/       Bubble Tea 界面
config/             agent.yaml、pricing.yaml、mcp.example.yaml
docs/               架构文档
```

## 里程碑

| 里程碑 | 状态 |
|---|---|
| M0 骨架 | ✅ 已完成 |
| M1 工具循环（registry / shell / fs / jail / 审批 / TUI 工具卡片） | ✅ 已完成 |
| M1.1 修补（Schema 描述、错误渲染、env 白名单、敏感文件、untrusted 标记） | ✅ 已完成 |
| M2 会话（事件溯源 / 崩溃恢复 / resume / replay） | ✅ 已完成 |
| M3 长记忆（facts / BM25 / write·search·forget / rebuild / 召回注入） | ✅ 已完成 |
| M3.5 记忆修正（memory_update + diff 确认） | 未做 |
| M4 摘要（L1 预算与压缩 / 持久化 / 恢复 / `/compact`） | ✅ 已完成 |
| M5 MCP（stdio / streamable HTTP / 状态栏 / `/mcp`） | ✅ 已完成 |
| M6 Skill（loader / catalog / `skill_load` / `skill_read_file`） | ✅ 已完成 |
| M7 页面（artifact 工具 / 预览服务 / 设计系统 / `web-report`） | ✅ 已完成 |
| M8 打磨（doctor / 成本 / 日志 / golden 测试 / README） | ✅ 已完成 |

已知简化：工具串行执行、`fs_write` 未做、命令黑名单强制审批未做、TUI 内 `/resume <id>` 暂由 CLI 参数承担、`facts.jsonl` 只追加不压缩、L2 日摘要未做、页面无历史快照。

## 会话

每次 `run`/`chat` 都会写一份事件流到 `data/sessions/<日期>/<id>/`（`events.jsonl` + `meta.json` + `lock`）。

```bash
agent sessions                       # 列出会话
agent chat --continue                # 接着最近一次会话聊
agent chat --resume 01M7VJ28         # 按 id 前缀恢复
agent replay 01M7VJ28                # 不调用模型，回放整段对话
agent replay 01M7VJ28 --from 10 --to 20
```

崩溃恢复：若进程在工具执行中被杀，重启打开该会话时会截断半行、把坏字节存进 `events.bad`、并给未完成的工具调用补一条「可能已产生副作用」的合成消息。

## 长期记忆

- **写入**：模型调用 `memory_write`（text/key/scope/confidence），同 key 覆盖旧值，无 key 按文本相似度去重。
- **召回**：每轮按用户输入做 BM25 + 时间衰减检索，合并进 system prompt；`session` 作用域只在本会话可见。
- **手工修正**：直接编辑 `data/memory/MEMORY.md` 的 manual 区，然后 `agent memory rebuild`；运行中的 TUI 里用 `/memory rebuild`。
- **删除**：`memory_forget(id)` 只置 `archived`，不物理删除。

## 技能与页面

- 把带 front matter 的 `SKILL.md` 放进 `skills/<name>/` 即生效，无需改代码；TUI 里 `/skill reload` 立即重扫。
- `artifact_write` 支持 create / replace / patch，写后自检（标签配平、`<html lang>`、外链、占位文案），连续两次阻塞失败才标记 `needs_review`。
- 预览服务懒启动，只监听 `127.0.0.1`；TUI 里 `/preview` 看地址、`ctrl+o` 打开浏览器。

## 可观测性与成本

- 日志：`slog` JSON 追加到 `data/logs/agent.log`，超过 5MB 轮转为 `agent.log.1`；TUI 里 `/log` 从文件尾部读取最近 200 行。
- trace：每轮一份 `data/logs/turns/<ulid>.json`（步数、消息数、工具耗时、token、成本、摘要开销、结束原因与错误），按 `session.keep_days` 自动清理。
- 成本：`config/pricing.yaml` 按模型配置每百万 token 的输入/输出单价，TUI 顶部实时累计；请求通过 `stream_options.include_usage` 获取用量，端点不支持该字段时自动去掉重试一次，不返回 usage 时记为 0。未配置单价的模型启动时会提示，成本按 0 显示。
- 推理模型：`reasoning_content` 不显示原文，TUI 状态栏显示“思考中”和推理 token；`max_tokens` 同时包含思考与正文，生成大页面时建议先写骨架再分块 `patch`，输出达到上限会明确提示。
- 会话 `meta.json` 里累计 `tokens` 与 `cost`，恢复会话后继续累加。

## 配置要点

- `models.*.api_key` 支持 `${AGENT_API_KEY}` 形式的变量引用。
- `shell.default`：`bash`（默认）/ `pwsh` / `cmd`，找不到时按此顺序回退；`shell.bash_path` 可指定 Git Bash。
- `agent.approval`：`readonly` / `standard` / `yolo`。
- `preview`：页面预览服务，懒启动（首次写页面时）。
- `config/pricing.yaml`：模型单价；缺失时成本显示为 0，不影响对话。
