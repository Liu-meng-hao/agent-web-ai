# 第三方技能来源与许可

本目录下部分技能改编自开源项目。每个技能都保留了自己的 `license` / `source` front matter 字段，完整许可条款见 `skills/licenses/`。

| 本地技能 | 上游项目 | 上游路径 | 许可 |
|---|---|---|---|
| `skill-creator` | [anthropics/skills](https://github.com/anthropics/skills) | `skills/skill-creator` | Apache-2.0 |
| `frontend-design` | [anthropics/skills](https://github.com/anthropics/skills) | `skills/frontend-design` | Apache-2.0 |
| `systematic-debugging` | [obra/superpowers](https://github.com/obra/superpowers) | `skills/systematic-debugging` | MIT |
| `test-driven-development` | [obra/superpowers](https://github.com/obra/superpowers) | `skills/test-driven-development` | MIT |
| `verification-before-completion` | [obra/superpowers](https://github.com/obra/superpowers) | `skills/verification-before-completion` | MIT |

## 修改说明

以上技能均不是原样复制，而是按本项目的约束重写：

- front matter 改为本项目支持的字段，`description` 控制在 200 字以内；
- 工具名改为 `fs_read` / `fs_list` / `shell` / `artifact_write` / `skill_read_file`；
- 删除或替换了不适用于本项目的脚本、堆栈和外部依赖；
- 正文压缩到 8000 token 预算以内，并按中文表达习惯重写；
- 补充了本项目的日志路径、测试命令和设计系统路径。

## 许可全文

- Apache-2.0：`skills/licenses/Apache-2.0.txt`
  - 上游版权声明：Copyright 2026 Anthropic, PBC.
- MIT：`skills/licenses/MIT.txt`
  - 上游版权声明：Copyright (c) 2025 Jesse Vincent

## 重新获取

本目录中的版本是 2026-11-23 从上游 `main` 分支获取后改编的：

- `anthropics/skills`：`e4bb5d960d87126ff3b3a8af1a104b7db67560ba`
- `obra/superpowers`：`22f0b463e7a5e579904dd6211a520321035526f7`

需要跟进时，请重新阅读上游 `SKILL.md`，不要直接覆盖本地改写版；先对比差异，再决定是否合并。
