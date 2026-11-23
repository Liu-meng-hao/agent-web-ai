package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agent/internal/llm"
	"agent/internal/security"
	"agent/internal/tools"
)

// scriptedProvider 按脚本逐轮返回事件，用来测多步工具循环。
type scriptedProvider struct {
	steps [][]llm.Event
	next  int
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	ch := make(chan llm.Event, 8)
	step := p.steps[p.next]
	p.next++
	go func() {
		defer close(ch)
		for _, ev := range step {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func newTestAgent(t *testing.T, prov llm.Provider, approver Approver) *Agent {
	t.Helper()

	dir := t.TempDir()
	jail, err := security.NewJail(dir)
	if err != nil {
		t.Fatal(err)
	}
	shell := security.DetectShell("bash", "")
	reg := tools.NewRegistry()
	if err := reg.Register(tools.NewShell()); err != nil {
		t.Fatal(err)
	}

	a := New(Options{
		Provider: prov,
		Model:    "scripted",
		Registry: reg,
		Policy:   security.NewPolicy("standard"),
		Approver: approver,
		MaxSteps: 6,
		Call: &tools.CallContext{
			Cwd:    dir,
			Jail:   jail,
			Runner: security.NewRunner(shell),
		},
	})
	return a
}

func TestLoopEmptyResponse(t *testing.T) {
	cases := []struct {
		name     string
		finish   string
		wantStop string
		wantWord string
	}{
		{"length", "length", "length", "max_tokens"},
		{"stop", "stop", "empty", "空内容"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prov := &scriptedProvider{steps: [][]llm.Event{{
				{Type: llm.EventReasoning, Text: "thinking"},
				{Type: llm.EventDone, FinishReason: tc.finish},
			}}}
			a := newTestAgent(t, prov, nil)

			var notices []string
			stop := ""
			a.opts.Emit = func(ev Event) {
				switch ev.Type {
				case EventNotice:
					notices = append(notices, ev.Text)
				case EventTurnFinished:
					stop = ev.Stop
				}
			}
			if err := a.RunTurn(context.Background(), "hi"); err != nil {
				t.Fatal(err)
			}
			if stop != tc.wantStop {
				t.Fatalf("stop = %q, want %q", stop, tc.wantStop)
			}
			joined := strings.Join(notices, "\n")
			if !strings.Contains(joined, tc.wantWord) {
				t.Fatalf("缺少提示 %q: %q", tc.wantWord, joined)
			}
			// 空回复不能写进历史，否则下一轮会带上一条空 assistant 消息。
			if a.conv.Len() != 1 {
				t.Fatalf("对话应只保留用户消息，实际 %d 条", a.conv.Len())
			}
		})
	}
}

func TestLoopExecutesToolAndContinues(t *testing.T) {
	call := llm.ToolCall{ID: "c1", Name: "shell", Args: json.RawMessage(`{"command":"echo hi"}`)}
	prov := &scriptedProvider{steps: [][]llm.Event{
		{
			{Type: llm.EventTool, ToolCall: &call},
			{Type: llm.EventDone, FinishReason: "tool_calls"},
		},
		{
			{Type: llm.EventText, Text: "done"},
			{Type: llm.EventDone, FinishReason: "stop"},
		},
	}}

	a := newTestAgent(t, prov, AllowAll{})

	var requested, results int
	var lastResult string
	a.opts.Emit = func(ev Event) {
		switch ev.Type {
		case EventToolRequested:
			requested++
			if ev.Risk != security.RiskExec {
				t.Errorf("risk = %v, want exec", ev.Risk)
			}
		case EventToolResult:
			results++
			if ev.Result != nil {
				lastResult = ev.Result.Content
			}
		}
	}

	if err := a.RunTurn(context.Background(), "跑一下 echo"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if requested != 1 || results != 1 {
		t.Fatalf("requested=%d results=%d", requested, results)
	}
	if !strings.Contains(lastResult, "hi") {
		t.Fatalf("工具结果未回灌: %q", lastResult)
	}

	// user → assistant(tool_call) → tool → assistant
	msgs := a.Conversation().Messages()
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4", len(msgs))
	}
	if msgs[2].Role != llm.RoleTool || msgs[2].ToolCallID != "c1" {
		t.Fatalf("tool 消息缺少 tool_call_id: %+v", msgs[2])
	}
	if !strings.Contains(msgs[2].Content, "<untrusted_content") {
		t.Fatalf("工具结果未加不可信标记: %q", msgs[2].Content)
	}
}

func TestLoopDeniedToolIsNotExecuted(t *testing.T) {
	call := llm.ToolCall{ID: "c1", Name: "shell", Args: json.RawMessage(`{"command":"echo should-not-run"}`)}
	prov := &scriptedProvider{steps: [][]llm.Event{
		{
			{Type: llm.EventTool, ToolCall: &call},
			{Type: llm.EventDone, FinishReason: "tool_calls"},
		},
		{
			{Type: llm.EventText, Text: "好的，我换个方案"},
			{Type: llm.EventDone, FinishReason: "stop"},
		},
	}}

	a := newTestAgent(t, prov, DenyAll{})

	var denied bool
	var executed bool
	a.opts.Emit = func(ev Event) {
		if ev.Type == EventToolResult {
			if ev.Decision == Deny {
				denied = true
			}
			if ev.Result != nil && strings.Contains(ev.Result.Content, "should-not-run") {
				executed = true
			}
		}
	}

	if err := a.RunTurn(context.Background(), "删点东西"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if !denied {
		t.Fatal("应产生拒绝事件")
	}
	if executed {
		t.Fatal("被拒绝的工具不应执行")
	}

	msgs := a.Conversation().Messages()
	if len(msgs) < 3 || msgs[2].Role != llm.RoleTool {
		t.Fatalf("缺少 tool 结果消息: %+v", msgs)
	}
	if !strings.Contains(msgs[2].Content, "拒绝") {
		t.Fatalf("拒绝原因未回灌: %q", msgs[2].Content)
	}
}

func TestLoopMaxStepsKeepsGeneratedPage(t *testing.T) {
	call := llm.ToolCall{ID: "page-1", Name: "artifact_write", Args: json.RawMessage(`{}`)}
	prov := &scriptedProvider{steps: [][]llm.Event{
		{{Type: llm.EventTool, ToolCall: &call}, {Type: llm.EventDone, FinishReason: "tool_calls"}},
		{{Type: llm.EventTool, ToolCall: &call}, {Type: llm.EventDone, FinishReason: "tool_calls"}},
	}}

	reg := tools.NewRegistry()
	if err := reg.Register(tools.Define("artifact_write", "test", tools.RiskWrite,
		func(ctx context.Context, in struct{}, tc *tools.CallContext) (tools.Result, error) {
			return tools.Result{Content: "页面已保存，预览地址：http://localhost/page"}, nil
		})); err != nil {
		t.Fatal(err)
	}
	a := New(Options{
		Provider: prov,
		Model:    "scripted",
		Registry: reg,
		Policy:   security.NewPolicy("yolo"),
		MaxSteps: 2,
	})

	var notice, stop string
	a.opts.Emit = func(ev Event) {
		switch ev.Type {
		case EventNotice:
			notice += ev.Text
		case EventTurnFinished:
			stop = ev.Stop
		}
	}
	if err := a.RunTurn(context.Background(), "生成页面"); err != nil {
		t.Fatalf("页面已落盘时不应返回 ErrMaxSteps: %v", err)
	}
	if stop != "max_steps" {
		t.Fatalf("stop = %q, want max_steps", stop)
	}
	if !strings.Contains(notice, "页面已保存") {
		t.Fatalf("缺少保留页面的提示: %q", notice)
	}
}
