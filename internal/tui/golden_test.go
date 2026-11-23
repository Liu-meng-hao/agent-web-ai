package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"agent/internal/agent"
	"agent/internal/llm"
	"agent/internal/tools"
)

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain 去掉 ANSI 颜色，保证 golden 与终端能力无关。
func plain(s string) string { return ansiRE.ReplaceAllString(s, "") }

func newTestModel() *model {
	ag := agent.New(agent.Options{
		Provider: llm.Mock{},
		Model:    "mock-1",
		Registry: tools.NewRegistry(),
	})
	return newModel(Options{
		Agent:    ag,
		Model:    "mock-1",
		Approver: NewApprover(),
	})
}

// requireGolden 比对 testdata 下的 golden 文件；UPDATE_GOLDEN=1 时重写。
func requireGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("缺少 golden 文件 %s（可用 UPDATE_GOLDEN=1 go test ./internal/tui 生成）: %v", path, err)
	}
	if got != string(want) {
		// Windows 上 CRLF 检出会让 golden 全部失败；比较前统一成 LF。
		wantLF := strings.ReplaceAll(string(want), "\r\n", "\n")
		gotLF := strings.ReplaceAll(got, "\r\n", "\n")
		if gotLF != wantLF {
			t.Fatalf("%s 与 golden 不一致\n--- got ---\n%s\n--- want ---\n%s", name, got, string(want))
		}
	}
}

func TestViewLayoutGolden(t *testing.T) {
	cases := []struct {
		name string
		w, h int
	}{
		{"view_80.golden", 80, 30},
		{"view_120.golden", 120, 30},
	}
	for _, c := range cases {
		m := newTestModel()
		m.Update(tea.WindowSizeMsg{Width: c.w, Height: c.h})
		m.blocks = []block{
			{kind: blockText, speaker: "you", text: "帮我做一个销售周报页面"},
			{kind: blockText, speaker: "agent", text: "好的，先写页面骨架。"},
			{kind: blockTool, tool: "artifact_write", preview: "slug=sales-weekly", status: "ok",
				result: "已写入 index.html（v1）", duration: 12 * time.Millisecond},
			{kind: blockNotice, text: "上下文压缩完成：覆盖 12 条消息"},
		}
		requireGolden(t, c.name, plain(m.View()))
	}
}

func TestUsageHeader(t *testing.T) {
	m := newTestModel()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.tokensIn, m.tokensOut, m.cost, m.currency = 12_300, 4_500, 0.0123, "CNY"
	view := plain(m.View())
	if !strings.Contains(view, "12.3k in / 4.5k out") {
		t.Fatalf("头部缺少 token 统计:\n%s", view)
	}
	if !strings.Contains(view, "CNY 0.0123") {
		t.Fatalf("头部缺少成本:\n%s", view)
	}
}

func TestSendInteractionGolden(t *testing.T) {
	m := newTestModel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.ta.SetValue("你好")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("发送后应启动一轮对话")
	}
	if !m.streaming {
		t.Fatal("发送后应进入 streaming 状态")
	}
	view := plain(m.View())
	if !strings.Contains(view, "you › 你好") {
		t.Fatalf("发送内容未渲染:\n%s", view)
	}
	requireGolden(t, "send.golden", view)
}

func TestApprovalInteractionGolden(t *testing.T) {
	m := newTestModel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(approvalMsg{req: agent.ApprovalRequest{
		Tool:    "shell",
		Risk:    tools.RiskExec,
		Preview: "echo hi",
	}})
	if !m.overlay {
		t.Fatal("审批请求应打开弹层")
	}
	view := plain(m.View())
	if !strings.Contains(view, "需要审批") || !strings.Contains(view, "[y] 允许一次") {
		t.Fatalf("审批弹层内容缺失:\n%s", view)
	}
	requireGolden(t, "approval.golden", view)

	// 按 y 后弹层关闭
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if m.overlay {
		t.Fatal("选择后弹层应关闭")
	}
}

func TestInterruptQuits(t *testing.T) {
	m := newTestModel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C 应返回退出命令")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("Ctrl+C 应退出程序，实际 %T", cmd())
	}
	if m.ctx.Err() == nil {
		t.Fatal("Ctrl+C 应取消当前上下文")
	}
}

func TestLogCommand(t *testing.T) {
	m := newTestModel()
	m.logTail = func(n int) ([]string, error) {
		return []string{`{"level":"WARN","msg":"测试日志"}`, "second"}, nil
	}
	m.command("/log")
	found := false
	for _, b := range m.blocks {
		if b.kind == blockNotice && strings.Contains(b.text, "测试日志") {
			found = true
		}
	}
	if !found {
		t.Fatal("/log 未渲染日志行")
	}
}
