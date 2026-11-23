---
name: example
description: 技能格式示例：复制本目录并改名后即可新增一个技能
when_to_use: 需要新建技能或不确定 front matter 怎么写时参考
version: 0.1.0
allowed-tools: [fs_read, skill_read_file]
---

# 示例技能

这是一个**模板**，不会被 agent 发现（以 `.` 开头的目录会被跳过）。

## 新增技能

1. 复制 `skills/.example` 为 `skills/<your-skill>`。
2. 修改 front matter 的 `name`，必须是小写字母、数字和连字符；建议与目录名一致。
3. 把 `description` 写成一句话，说明这个技能能做什么。
4. 在正文写清触发条件、步骤和完成标准。

## 写作建议

- 正文只写流程和判断标准，不要写密钥、绝对路径或与当前项目无关的内容。
- 需要引用的长文档放到 `references/`，运行时用 `skill_read_file` 按行读取。
- `allowed-tools` 只是建议，不会强制限制工具使用。
- 修改后需要重启 agent，或在 TUI 里执行 `/skill reload`。
