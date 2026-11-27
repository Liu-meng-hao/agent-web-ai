---
name: verification-before-completion
description: 在任何对外声称完成、修复、通过或汇报状态之前，先运行验证命令并核对本次输出的证据
when_to_use: 准备说已完成、已修复、测试通过，或提交代码、结束任务、汇报任何进展之前（不限于跑测试）
version: 0.1.0
allowed-tools: [shell, fs_read, fs_list]
license: MIT
source: https://github.com/obra/superpowers/tree/main/skills/verification-before-completion
---

# Verification Before Completion

## 铁律

**没有本次运行产生的新鲜证据，不做完成声明。**

“应该可以”“刚才跑过”“我很有信心”都不是证据。

## 五步闸门

在说出任何状态之前：

1. **确定命令**：哪条命令能证明这句话？
2. **完整运行**：现在、完整地跑，不用上次结果，不跳过失败用例。
3. **读输出**：看退出码、失败数、warning、跳过项。
4. **判断**：输出是否真的支持结论？不支持就按实际状态说。
5. **再声明**：带着命令和结果说，不裸说“完成”。

## 常见声明与所需证据

| 声明 | 需要 | 不够 |
|---|---|---|
| 测试通过 | 完整测试命令输出，0 失败 | 上次跑过、单文件通过、应该能过 |
| 构建通过 | 构建命令退出码 0 | 只跑了 lint |
| 修复生效 | 原始症状的复现测试通过 | 改了代码、看起来像好了 |
| 回归测试有效 | 先失败再通过（红-绿） | 测试只通过过一次 |
| 需求完成 | 逐条对照需求清单 | 测试通过 |
| 页面可用 | 预览地址打开，375px 与桌面都不溢出 | 自检无报错 |

## 本项目常用验证

```bash
go test ./...
go vet ./...
go build ./...
```

涉及 TUI 布局时：

```bash
go test ./internal/tui
```

只有确认视觉变化是有意的，才更新 golden：

```bash
UPDATE_GOLDEN=1 go test ./internal/tui
```

涉及配置、目录、端口时：

```bash
go run ./cmd/agent doctor
```

涉及页面时：拿到 `artifact_write` 返回的预览地址并实际打开；自检问题要逐条修复，不能只说“已生成”。

## 红旗

- “应该可以了”“看起来没问题”“大概率通过”；
- 在运行验证前说“完成”“搞定”“修好了”；
- 用上一次的输出证明这一次；
- 只跑了单个测试文件，就说“测试全过”；
- 信任子 agent 或工具返回的“success”，不独立核对；
- 累了、想收尾了，就跳过验证。

## 汇报格式

有证据的汇报：

```text
验证：go test ./... → ok（0 失败）
验证：go build ./... → exit 0
未覆盖：没有在真实终端里验证 80x24 的 TUI 布局
```

没有证据就说没有证据，不要包装成完成。

## 完成清单

- [ ] 每个“通过/完成”都有对应命令和输出
- [ ] 跑的是完整测试集，不是单文件
- [ ] 看了退出码和失败数
- [ ] 原始症状/需求逐条核对
- [ ] 未覆盖的部分明确列出
- [ ] 没有用“应该/可能/似乎”代替证据

---

改编自 Jesse Vincent 的 `superpowers:verification-before-completion`（MIT），已按本项目的 Go 命令、TUI golden 测试和页面预览流程重写。原始条款见 `skills/licenses/MIT.txt`。
