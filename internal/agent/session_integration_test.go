package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agent/internal/llm"
	"agent/internal/security"
	"agent/internal/session"
	"agent/internal/tools"
)

// TestSessionRoundTrip 覆盖「跑两轮 → 关闭 → 重开 → 还原消息」。
func TestSessionRoundTrip(t *testing.T) {
	root := t.TempDir()
	store, err := session.Create(root, session.Meta{Cwd: "D:/x", Model: "scripted"})
	if err != nil {
		t.Fatal(err)
	}

	call := llm.ToolCall{ID: "c1", Name: "shell", Args: json.RawMessage(`{"command":"echo hi"}`)}
	prov := &scriptedProvider{steps: [][]llm.Event{
		{{Type: llm.EventTool, ToolCall: &call}, {Type: llm.EventDone, FinishReason: "tool_calls"}},
		{{Type: llm.EventText, Text: "第一轮完成"}, {Type: llm.EventDone, FinishReason: "stop"}},
		{{Type: llm.EventText, Text: "第二轮完成"}, {Type: llm.EventDone, FinishReason: "stop"}},
	}}

	a := newTestAgent(t, prov, AllowAll{})
	a.SetRecorder(store)

	if err := a.RunTurn(context.Background(), "看看目录"); err != nil {
		t.Fatal(err)
	}
	if err := a.RunTurn(context.Background(), "继续"); err != nil {
		t.Fatal(err)
	}
	dir := store.Dir()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	rec, err := session.Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Events) < 8 {
		t.Fatalf("事件数 = %d, 事件流不完整", len(rec.Events))
	}
	if len(rec.Interrupted) != 0 {
		t.Fatalf("不应有中断调用: %v", rec.Interrupted)
	}

	msgs := session.ToMessages(rec.Events)
	if len(msgs) != 6 {
		t.Fatalf("消息数 = %d, want 6: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != llm.RoleUser || msgs[0].Content != "看看目录" {
		t.Fatalf("首条消息异常: %+v", msgs[0])
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].ID != "c1" {
		t.Fatalf("工具调用丢失: %+v", msgs[1])
	}
	if msgs[2].Role != llm.RoleTool || !strings.Contains(msgs[2].Content, "hi") {
		t.Fatalf("工具结果异常: %+v", msgs[2])
	}
	if msgs[5].Content != "第二轮完成" {
		t.Fatalf("末条消息异常: %+v", msgs[5])
	}
}

// TestSessionRecoveryMarksInterruptedCall 模拟崩溃：tool.call 之后没有 tool.result。
func TestSessionRecoveryMarksInterruptedCall(t *testing.T) {
	root := t.TempDir()
	store, err := session.Create(root, session.Meta{Cwd: "D:/x", Model: "scripted"})
	if err != nil {
		t.Fatal(err)
	}

	ev, _ := session.New(session.TypeTurnStarted, session.TurnStarted{TurnID: "t1", UserText: "删库"})
	_ = store.Append(ev)
	ev, _ = session.New(session.TypeAssistantMessage, session.AssistantMessage{
		TurnID: "t1", Step: 1,
		ToolCalls: []session.ToolCallInfo{{ID: "c9", Name: "shell", Args: json.RawMessage(`{"command":"rm -rf build"}`)}},
	})
	_ = store.Append(ev)
	ev, _ = session.New(session.TypeToolCall, session.ToolCall{TurnID: "t1", Step: 1, CallID: "c9", Name: "shell"})
	_ = store.Append(ev)

	dir := store.Dir()
	_ = store.Close()

	rec, err := session.Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Interrupted) != 1 || rec.Interrupted[0] != "c9" {
		t.Fatalf("interrupted = %v", rec.Interrupted)
	}

	msgs := session.ToMessages(rec.Events)
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleTool || last.ToolCallID != "c9" {
		t.Fatalf("缺少合成的中断消息: %+v", last)
	}
	if !strings.Contains(last.Content, "可能已产生副作用") {
		t.Fatalf("合成消息缺少风险提示: %q", last.Content)
	}
}

// 保证测试辅助函数与生产代码使用同一套策略。
var _ = security.NewPolicy
var _ = tools.NewShell
