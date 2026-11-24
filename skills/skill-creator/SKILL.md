---
name: skill-creator
description: 指导创建、修改和校验本项目的 SKILL.md，覆盖触发条件、正文结构、资源拆分与验证流程
when_to_use: 用户要新增技能、改写现有技能、检查技能格式或排查技能不生效时
version: 0.1.0
allowed-tools: [fs_read, fs_list, shell, skill_read_file]
license: Apache-2.0
source: https://github.com/anthropics/skills/tree/main/skills/skill-creator
---

# Skill Creator

把“写技能”当成写一份可执行的判断手册，而不是写一篇介绍文章。技能正文只提供流程、判断标准和完成标准；真正的执行仍由 `fs_read`、`shell`、`artifact_write` 等常规工具完成。

## 先判断该不该建技能

值得建：

- 同一套流程会在多个任务里重复出现；
- 流程里有需要判断的分支，光靠一句提示说不清；
- 需要把长文档、脚本或检查清单从主提示里拆出去。

不值得建：

- 一次性的临时做法；
- 项目约定（应该写进 system prompt 或 README）；
- 能用代码、校验器或模板强制的事情。

## 本项目格式

- 目录：`skills/<name>/SKILL.md`，一个目录一个技能。
- 只扫描一层；以 `.` 或 `_` 开头的目录会被跳过。
- `SKILL.md` 必须是普通文件，不能是符号链接；单文件不超过 256 KiB。
- front matter 必须以整行 `---` 开始和结束。
- `name`：小写字母、数字、连字符，`^[a-z0-9][a-z0-9-]{0,63}$`；省略时用目录名归一化。
- `description`：必填，1–200 字，是模型判断“要不要加载”的唯一依据。
- `when_to_use`：可选，但强烈建议写，会出现在 system prompt 的技能目录里。
- 正文：不能为空；加载时超过 8000 token 会被截断，完整内容要用 `skill_read_file` 分段读。
- `allowed-tools`：只是展示和建议，不会强制限制工具。
- 修改后执行 `/skill reload`，或重启进程；没有 reload 不会自动发现。

## 写 description

description 只回答“什么时候用”，不要概括流程。模型看到流程摘要时，往往会直接照摘要做，而不去读正文。

好的写法：

```yaml
description: 把数据或调研结果做成一页可在浏览器打开的精美 HTML 报告
when_to_use: 用户要求生成数据报告、周报、数据看板、单页分析或可视化页面时
```

要点：

- 用中文，写用户会说的触发词：报告、周报、看板、落地页、重构、报错、测试失败。
- 包含症状和场景，不要只写“处理 XX”。
- 不超过 200 字；超过会被拒绝加载。
- 不要把“先读 A，再写 B”写进 description。

## 正文结构

建议按下面的顺序写，没内容的小节可以删：

```markdown
# 技能名

一句话说明这个技能解决什么问题。

## 适用与不适用

- 适用：……
- 不适用：……（避免模型过度触发）

## 前置检查

需要先确认哪些信息？缺信息时先问，不要猜。

## 步骤

1. 先做什么，用什么工具，看到什么算完成。
2. 判断分支：如果 A 则……；如果 B 则……。
3. 收尾：输出什么、写到哪里、如何验证。

## 判断标准

- 什么算通过，什么算失败。
- 哪些情况必须停下来问用户。

## 常见错误

- 错误做法 → 为什么错 → 正确做法。

## 完成标准

- [ ] 可验证的条件 1
- [ ] 可验证的条件 2
```

写作要求：

- 步骤用动词开头，写清工具名（必须是本项目真实存在的工具）。
- 每个关键步骤给出“怎么判断做完了”，不要只写“检查一下”。
- 需要分支时写条件，不要写“视情况而定”。
- 不要写密钥、绝对路径、个人环境信息。
- 不要写“你可以考虑”“建议尽量”这类软约束；要写可执行的规则。
- 正文控制在 8000 token 以内；超出的长文放 `references/`。

## 资源怎么拆

- `references/`：超过 100 行的参考资料、检查清单、API 文档。
- `scripts/`：确实需要重复执行的脚本；正文里用 `shell` 调用，并说明依赖。
- `assets/`：模板、示例数据。
- 正文只留“什么时候读哪个文件”，不要把长文复制进来。
- 读取资源用 `skill_read_file(name, path, offset, limit)`，默认 200 行，上限 500 行。

## 验证

1. 运行 `go run ./cmd/agent skill list`，确认技能出现且没有 warning。
2. 运行 `go run ./cmd/agent skill show <name>`，确认正文完整。
3. 在 TUI 里执行 `/skill reload`，再执行 `/skill <name>` 查看。
4. 用一句真实用户请求测试：模型是否能从 description 判断出该加载这个技能。
5. 如果改了正文，确认没有被 8000 token 截断；截断时把长内容移到 `references/`。

## 常见错误

| 现象 | 原因 | 修正 |
|---|---|---|
| 技能不出现 | 目录以 `.`/`_` 开头，或没有 `SKILL.md` | 改成普通目录名，补 `SKILL.md` |
| 加载时报 name 不合法 | 用了大写、下划线、中文或空格 | 改成小写字母/数字/连字符 |
| 加载时报 description 为空 | front matter 缺字段或拼错 | 补 `description`，注意是 200 字以内 |
| 技能不触发 | description 太泛，或没写 `when_to_use` | 加入用户会说的具体触发词 |
| 正文被截断 | 正文超过 8000 token | 拆到 `references/`，正文只留索引 |
| 改了没生效 | 没有 reload | 执行 `/skill reload` 或重启 |

## 完成标准

- [ ] 目录名和 `name` 一致，且符合正则
- [ ] `description` 是“何时用”，≤200 字
- [ ] `when_to_use` 写了具体触发场景
- [ ] 正文有步骤、判断标准、完成标准
- [ ] 引用的工具都是本项目真实工具
- [ ] 长内容已拆到 `references/`
- [ ] `agent skill list` / `agent skill show` 无 warning
- [ ] `/skill reload` 后能看到并加载

---

改编自 Anthropic 的 `skill-creator`（Apache-2.0），已按本项目的 front matter 规则、工具名和 8000 token 预算重写。原始条款见 `skills/licenses/Apache-2.0.txt`。
