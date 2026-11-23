package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"agent/internal/llm"
	"agent/internal/session"
)

type fakeSummaryRecorder struct {
	text      string
	covered   int
	calls     int
	truncated int
}

func (r *fakeSummaryRecorder) Record(session.Event) error { return nil }
func (r *fakeSummaryRecorder) Sync() error                { return nil }
func (r *fakeSummaryRecorder) SaveSummary(text string, covered int) error {
	r.text = text
	r.covered = covered
	r.calls++
	return nil
}
func (r *fakeSummaryRecorder) SaveTruncated(covered int) error {
	r.truncated = covered
	return nil
}

// failingProvider 模拟摘要模型故障。
type failingProvider struct{ calls int }

func (p *failingProvider) Name() string { return "failing" }

func (p *failingProvider) Stream(context.Context, llm.Request) (<-chan llm.Event, error) {
	p.calls++
	ch := make(chan llm.Event, 1)
	ch <- llm.Event{Type: llm.EventError, Err: errors.New("摘要模型不可用")}
	close(ch)
	return ch, nil
}

func summaryEvents(text string) [][]llm.Event {
	return [][]llm.Event{{
		{Type: llm.EventText, Text: text},
		{Type: llm.EventDone, FinishReason: "stop"},
	}}
}

func TestCompactDropsOldestAndPersists(t *testing.T) {
	// chunk 里的实体必须出现在摘要中，否则会触发一次重试并最终判失败。
	const summary = "## 目标\n用 shell 执行 cat 读取 config/agent.yaml 的 port 配置，确认端口是 4318"

	prov := &scriptedProvider{steps: append(summaryEvents(summary), summaryEvents(summary)...)}
	rec := &fakeSummaryRecorder{}
	a := New(Options{
		Provider:        prov,
		Model:           "scripted",
		Summarizer:      prov,
		SummarizerModel: "scripted",
		MinKeep:         3,
		Recorder:        rec,
	})

	call1 := llm.ToolCall{ID: "c1", Name: "shell", Args: json.RawMessage(`{"command":"cat config/agent.yaml"}`)}
	call2 := llm.ToolCall{ID: "c2", Name: "shell", Args: json.RawMessage(`{"command":"cat config/agent.yaml"}`)}
	a.Restore([]llm.Message{
		{Role: llm.RoleUser, Content: "查看 config/agent.yaml"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call1}},
		{Role: llm.RoleTool, ToolCallID: "c1", Content: "port: 4318"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call2}},
		{Role: llm.RoleTool, ToolCallID: "c2", Content: "port: 4318"},
		{Role: llm.RoleAssistant, Content: "端口是 4318"},
		{Role: llm.RoleUser, Content: "继续"},
	})

	// Keep=3 → split=4，正好落在 tool 消息上，必须向后推到 5。
	got, err := a.Compact(context.Background())
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !strings.Contains(got, "已压缩") {
		t.Fatalf("提示异常: %q", got)
	}

	msgs := a.Conversation().Messages()
	if len(msgs) != 2 {
		t.Fatalf("剩余消息 = %d, want 2", len(msgs))
	}
	if msgs[0].Role == llm.RoleTool {
		t.Fatalf("切点落在 tool 消息上，剩余首条是孤立工具结果: %+v", msgs[0])
	}

	sum, covered := a.Conversation().SummaryInfo()
	if !strings.Contains(sum, "config/agent.yaml") {
		t.Fatalf("摘要未保留关键实体: %q", sum)
	}
	if covered != 5 {
		t.Fatalf("covered = %d, want 5", covered)
	}
	if rec.calls != 1 || rec.covered != 5 || !strings.Contains(rec.text, "4318") {
		t.Fatalf("摘要未正确持久化: calls=%d covered=%d text=%q", rec.calls, rec.covered, rec.text)
	}
}

func TestCompactWithoutHistory(t *testing.T) {
	a := New(Options{Summarizer: &scriptedProvider{}, SummarizerModel: "scripted"})
	a.Restore([]llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "hello"},
	})
	got, err := a.Compact(context.Background())
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got != "没有可压缩的历史" {
		t.Fatalf("提示异常: %q", got)
	}
}

func TestMaybeCompactLeavesShortConversationAlone(t *testing.T) {
	prov := &scriptedProvider{}
	a := New(Options{
		Budget:          Budget{Window: 100000, OutputReserve: 1000, Safety: 0.1, CompactAt: 0.75},
		Summarizer:      prov,
		SummarizerModel: "scripted",
	})
	a.Restore([]llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "hello"},
	})
	if err := a.maybeCompact(context.Background(), 0); err != nil {
		t.Fatalf("maybeCompact: %v", err)
	}
	if prov.next != 0 {
		t.Fatalf("未超预算时不应调用摘要模型，实际调用 %d 次", prov.next)
	}
}

// longHistory 造一段超过最小保留条数的历史，内容含可校验实体。
func longHistory() []llm.Message {
	filler := strings.Repeat("请检查 config/agent.yaml 的 4318 端口配置是否正确。", 3)
	var msgs []llm.Message
	for i := 0; i < 3; i++ {
		msgs = append(msgs,
			llm.Message{Role: llm.RoleUser, Content: filler},
			llm.Message{Role: llm.RoleAssistant, Content: "我来读取文件"},
		)
	}
	return append(msgs, llm.Message{Role: llm.RoleUser, Content: "继续"})
}

// 覆盖率偏低只告警，不再重试、不再判失败。
func TestSummaryAcceptsLowEntityCoverage(t *testing.T) {
	prov := &scriptedProvider{steps: summaryEvents("已经检查完毕，没有发现问题")}
	a := New(Options{
		Summarizer:      prov,
		SummarizerModel: "scripted",
		MinKeep:         3,
	})
	a.Restore(longHistory())

	var warned bool
	a.opts.Emit = func(ev Event) {
		if ev.Type == EventNotice && strings.Contains(ev.Text, "保真度") {
			warned = true
		}
	}

	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatalf("覆盖率低不应失败: %v", err)
	}
	if prov.next != 1 {
		t.Fatalf("不应重试摘要，实际调用 %d 次", prov.next)
	}
	if !warned {
		t.Fatal("覆盖率偏低应产生可见告警")
	}
	if sum, _ := a.Conversation().SummaryInfo(); sum == "" {
		t.Fatal("摘要未被接受")
	}
}

// 摘要失败且确实要溢出时硬截断，并写入 truncated 事件。
func TestHardTruncateRecordsEvent(t *testing.T) {
	sum := &failingProvider{}
	rec := &fakeSummaryRecorder{}
	a := New(Options{
		Budget:          Budget{Window: 200, OutputReserve: 0, Safety: 0.1, CompactAt: 0.75},
		Summarizer:      sum,
		SummarizerModel: "failing",
		MinKeep:         3,
		Recorder:        rec,
	})
	a.Restore(longHistory())

	var noticed bool
	a.opts.Emit = func(ev Event) {
		if ev.Type == EventNotice && strings.Contains(ev.Text, "硬截断") {
			noticed = true
		}
	}

	if err := a.maybeCompact(context.Background(), 0); err != nil {
		t.Fatalf("硬截断后不应再返回错误: %v", err)
	}
	if rec.truncated == 0 {
		t.Fatal("硬截断必须写 session.truncated，否则重启会还原被截消息")
	}
	if !noticed {
		t.Fatal("硬截断应有可见提示")
	}
	if _, covered := a.Conversation().SummaryInfo(); covered == 0 {
		t.Fatal("硬截断也要推进 covered")
	}
}

// 摘要失败后本轮不再重试，跨轮做退避。
func TestCompactFailureBacksOff(t *testing.T) {
	main := &scriptedProvider{steps: [][]llm.Event{
		{{Type: llm.EventText, Text: "ok"}, {Type: llm.EventDone, FinishReason: "stop"}},
		{{Type: llm.EventText, Text: "ok"}, {Type: llm.EventDone, FinishReason: "stop"}},
	}}
	sum := &failingProvider{}
	a := New(Options{
		Provider:        main,
		Model:           "scripted",
		Budget:          Budget{Window: 1000, OutputReserve: 0, Safety: 0.1, CompactAt: 0.75},
		Summarizer:      sum,
		SummarizerModel: "failing",
		MinKeep:         3,
		MaxSteps:        1,
	})
	// 约 700 token：超过 limit(675) 但不到硬截断阈值(877)。
	big := strings.Repeat("x", 2700)
	a.Restore([]llm.Message{
		{Role: llm.RoleUser, Content: big},
		{Role: llm.RoleAssistant, Content: "ok"},
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "ok"},
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "ok"},
	})

	if err := a.RunTurn(context.Background(), "再来"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if err := a.RunTurn(context.Background(), "再来"); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if sum.calls != 1 {
		t.Fatalf("失败后应退避，摘要模型只应被调用 1 次，实际 %d 次", sum.calls)
	}
}

// 工具 schema / 召回块必须计入预算。
func TestMaybeCompactCountsOverhead(t *testing.T) {
	prov := &scriptedProvider{steps: summaryEvents("## 目标\n无")}
	a := New(Options{
		Budget:          Budget{Window: 100000, OutputReserve: 0, Safety: 0.1, CompactAt: 0.75},
		Summarizer:      prov,
		SummarizerModel: "scripted",
		MinKeep:         3,
	})
	a.Restore([]llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "hello"},
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "hello"},
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "hello"},
	})
	// 消息本身远低于 limit，但加上固定开销后超了。
	if err := a.maybeCompact(context.Background(), 70000); err != nil {
		t.Fatalf("maybeCompact: %v", err)
	}
	if prov.next != 1 {
		t.Fatalf("固定开销未计入预算，摘要调用 %d 次", prov.next)
	}
}

func TestBuildSummaryInputHonorsBudget(t *testing.T) {
	chunk := make([]llm.Message, 0, 120)
	for i := 0; i < 119; i++ {
		chunk = append(chunk, llm.Message{Role: llm.RoleTool, Content: strings.Repeat("x", 3000)})
	}
	chunk = append(chunk, llm.Message{Role: llm.RoleUser, Content: "最新"})

	out := buildSummaryInput("", chunk)
	if got := estimateText(out); got > summaryInputBudget+300 {
		t.Fatalf("摘要输入 %d token，超出预算 %d", got, summaryInputBudget)
	}
	if !strings.Contains(out, "最新") {
		t.Fatal("最新的用户消息被丢掉了")
	}
	if !strings.Contains(out, "已省略") {
		t.Fatal("超预算时应标注省略条数")
	}
}

// 工具输出里的普通英文词不应被当成必须保留的实体。
func TestEntitiesIgnoreToolOutput(t *testing.T) {
	chunk := []llm.Message{
		{Role: llm.RoleTool, Content: "exit_code duration stdout stderr a.txt Preparing modules"},
		{Role: llm.RoleUser, Content: "检查 config/agent.yaml"},
	}
	got := entitiesFrom(chunk)
	if len(got) != 1 || got[0] != "config/agent.yaml" {
		t.Fatalf("实体抽取异常: %v", got)
	}
}
