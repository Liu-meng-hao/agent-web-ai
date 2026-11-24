---
name: test-driven-development
description: 写实现前先写会失败的测试，用红-绿-重构循环保证改动可验证
when_to_use: 新增功能、修 bug、重构或改变行为，准备先写实现代码时
version: 0.1.0
allowed-tools: [fs_read, fs_list, shell, skill_read_file]
license: MIT
source: https://github.com/obra/superpowers/tree/main/skills/test-driven-development
---

# Test-Driven Development

## 铁律

**没有先看到失败的测试，不写生产代码。**

如果已经先写了实现，删掉重来。不要留作“参考”，也不要边写测试边改实现——那还是先写代码。

## 红-绿-重构

### 红：写一个会失败的测试

- 一个测试只验证一个行为。
- 测试名说清“应该发生什么”，不要写 `Test1`。
- 调真实代码，不要 mock 被测对象本身。
- Go 里优先用表驱动测试，把输入、期望、错误分开。

```go
func TestParseSkillRejectsBadName(t *testing.T) {
    _, err := parseSkill(t.TempDir(), "Bad Name", "---\ndescription: x\n---\n正文", 8000)
    if err == nil || !strings.Contains(err.Error(), "name") {
        t.Fatalf("err = %v, want name error", err)
    }
}
```

### 验证红：亲眼看它失败

运行测试，确认：

- 它确实失败，而不是编译错误或环境错误；
- 失败原因就是“功能还没实现”；
- 失败信息能看懂。

如果测试直接通过，说明它测的是已有行为，改测试。

### 绿：写最小实现

- 只写让当前测试通过的最少代码。
- 不加未测试的参数、选项、抽象层。
- 不顺手重构其他代码。

### 验证绿：亲眼看它通过

- 当前测试通过；
- 相关测试不回归；
- 输出干净，没有 warning 或跳过。

**“相关测试”指整个项目测试集，不只是你改的那个文件。** 交付前跑完整命令：

```bash
go test ./...
```

### 重构：在绿的前提下清理

- 去重、改名字、抽小函数；
- 保持测试绿；
- 不改变行为。

## 本项目的测试习惯

- 用 `t.TempDir()` 隔离文件系统，不要写死路径。
- 用 `t.Helper()` 标记测试辅助函数。
- 错误信息写清 `got` / `want`，便于定位。
- 表驱动测试用 `t.Run(name, func(t *testing.T){...})`。
- TUI 有 golden 测试；只有确认视觉变化是有意的，才用 `UPDATE_GOLDEN=1` 更新。
- 并发/锁相关测试用 `-race`：`go test -race ./internal/session ./internal/memory`。
- 外部依赖（LLM、MCP、shell）用接口或 mock 隔离；不要为了测试把生产代码改成到处 if。

## 常见借口

| 借口 | 现实 |
|---|---|
| “太简单了，不用测” | 简单代码也会坏，测试只要几十秒。 |
| “先写完再补测试” | 补的测试一写就过，证明不了能抓住 bug。 |
| “我手动测过了” | 手动测试没有记录、不能重跑，覆盖了哪些用例说不清。 |
| “删掉写了几小时的代码太浪费” | 沉没成本已经发生；留不可信的代码才是浪费。 |
| “先探索一下再补测试” | 可以探索，但探索代码要丢掉，正式实现仍从测试开始。 |
| “测试太难写，说明设计有问题” | 对，先简化接口，而不是放弃测试。 |
| “TDD 太慢” | TDD 把调试时间前移；跳过它只是把成本推到线上。 |

## 红旗

- 先写实现，后补测试；
- 测试一写就通过；
- 说不清测试为什么失败；
- 一次加多个测试、多个实现，无法隔离；
- “就这一次，跳过 TDD”；
- 为了让测试通过而改测试断言。

## 完成清单

- [ ] 每个新行为都有测试
- [ ] 每个测试都先看到失败
- [ ] 失败原因正确（缺功能，不是拼写/环境）
- [ ] 实现是最小可用代码
- [ ] `go test ./...` 全绿
- [ ] 没有为了通过而弱化断言
- [ ] 边界和错误路径有覆盖

---

改编自 Jesse Vincent 的 `superpowers:test-driven-development`（MIT），已按 Go、本项目测试命令和 TUI golden 测试重写。原始条款见 `skills/licenses/MIT.txt`。
