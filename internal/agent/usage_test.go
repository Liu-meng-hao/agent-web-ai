package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent/internal/llm"
	"agent/internal/session"
	"agent/internal/telemetry"
	"agent/internal/tools"
)

// usageProvider 每次都先报一次用量，再给出一段文本。
type usageProvider struct {
	in, out int
}

func (usageProvider) Name() string { return "fake" }

func (p usageProvider) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	ch := make(chan llm.Event, 4)
	go func() {
		defer close(ch)
		ch <- llm.Event{Type: llm.EventUsage, Usage: &llm.Usage{InputTokens: p.in, OutputTokens: p.out}}
		ch <- llm.Event{Type: llm.EventText, Text: "完成"}
		ch <- llm.Event{Type: llm.EventDone, FinishReason: "stop"}
	}()
	return ch, nil
}

func TestRunTurnRecordsUsageAndTrace(t *testing.T) {
	pricing := &telemetry.Pricing{Currency: "CNY", Models: map[string]telemetry.Price{
		"fake-model": {Input: 1, Output: 2},
	}}
	meter := telemetry.NewMeter(pricing)
	traceDir := t.TempDir()

	var usageEvents []Event
	a := New(Options{
		Provider: usageProvider{in: 500_000, out: 250_000},
		Model:    "fake-model",
		Registry: tools.NewRegistry(),
		MaxSteps: 3,
		Meter:    meter,
		TraceDir: traceDir,
		Emit: func(ev Event) {
			if ev.Type == EventUsage {
				usageEvents = append(usageEvents, ev)
			}
		},
	})

	if err := a.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	if len(usageEvents) != 1 {
		t.Fatalf("应收到 1 个用量事件，实际 %d", len(usageEvents))
	}
	ev := usageEvents[0]
	if ev.TokensIn != 500_000 || ev.TokensOut != 250_000 {
		t.Fatalf("事件用量错误: %+v", ev)
	}
	if ev.Cost != 1.0 { // 0.5*1 + 0.25*2
		t.Fatalf("事件成本错误: %v", ev.Cost)
	}

	snap := meter.Snapshot()
	if snap.In != 500_000 || snap.Out != 250_000 || snap.Cost != 1.0 {
		t.Fatalf("累计快照错误: %+v", snap)
	}

	files, err := os.ReadDir(traceDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("应写 1 份 trace，实际 %d", len(files))
	}
	raw, err := os.ReadFile(filepath.Join(traceDir, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var tr telemetry.TurnTrace
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.TokensIn != 500_000 || tr.TokensOut != 250_000 || tr.Cost != 1.0 || tr.Stop != "stop" {
		t.Fatalf("trace 内容错误: %+v", tr)
	}
}

// usageRecorder 模拟 session.Store 的用量落盘接口。
type usageRecorder struct {
	in, out int
	cost    float64
	events  []session.Event
}

func (r *usageRecorder) Record(ev session.Event) error {
	r.events = append(r.events, ev)
	return nil
}
func (r *usageRecorder) Sync() error { return nil }
func (r *usageRecorder) AddUsage(in, out int, cost float64) error {
	r.in += in
	r.out += out
	r.cost += cost
	return nil
}

func TestRunTurnReportsUsageToRecorder(t *testing.T) {
	pricing := &telemetry.Pricing{Currency: "CNY", Models: map[string]telemetry.Price{
		"fake-model": {Input: 1, Output: 1},
	}}
	rec := &usageRecorder{}
	a := New(Options{
		Provider: usageProvider{in: 1_000_000, out: 0},
		Model:    "fake-model",
		Registry: tools.NewRegistry(),
		MaxSteps: 2,
		Meter:    telemetry.NewMeter(pricing),
		Recorder: rec,
	})
	if err := a.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if rec.in != 1_000_000 || rec.cost != 1.0 {
		t.Fatalf("Recorder 用量错误: %+v", rec)
	}
}

// TestCompactRecordsSummaryUsage 覆盖摘要调用的漏计：压缩本身也是真实请求。
func TestCompactRecordsSummaryUsage(t *testing.T) {
	pricing := &telemetry.Pricing{Currency: "CNY", Models: map[string]telemetry.Price{
		"sum-model": {Input: 1, Output: 1},
	}}
	meter := telemetry.NewMeter(pricing)
	rec := &usageRecorder{}
	sum := &scriptedProvider{steps: [][]llm.Event{{
		{Type: llm.EventUsage, Usage: &llm.Usage{InputTokens: 4_000, OutputTokens: 200}},
		{Type: llm.EventText, Text: "## 目标\n无"},
		{Type: llm.EventDone, FinishReason: "stop"},
	}}}
	a := New(Options{
		Summarizer:      sum,
		SummarizerModel: "sum-model",
		Meter:           meter,
		Recorder:        rec,
		MinKeep:         2,
	})
	for i := 0; i < 10; i++ {
		a.Conversation().Append(llm.Message{Role: llm.RoleUser, Content: "较早的对话内容 config/agent.yaml 4318"})
	}
	if _, err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := meter.Snapshot()
	if snap.In != 4_000 || snap.Out != 200 {
		t.Fatalf("摘要用量未计入 meter: %+v", snap)
	}
	if rec.in != 4_000 || rec.out != 200 {
		t.Fatalf("摘要用量未写入会话 meta: %+v", rec)
	}
}

// TestRunTurnTraceIncludesSummaryUsage 覆盖 trace 里的 summary_* 字段。
func TestRunTurnTraceIncludesSummaryUsage(t *testing.T) {
	meter := telemetry.NewMeter(&telemetry.Pricing{
		Currency: "CNY",
		Models:   map[string]telemetry.Price{"sum-model": {Input: 1, Output: 1}},
	})
	traceDir := t.TempDir()
	sum := &scriptedProvider{steps: [][]llm.Event{{
		{Type: llm.EventUsage, Usage: &llm.Usage{InputTokens: 1_000, OutputTokens: 100}},
		{Type: llm.EventText, Text: "## 目标\n压缩完成"},
		{Type: llm.EventDone, FinishReason: "stop"},
	}}}
	a := New(Options{
		Provider:        usageProvider{in: 10, out: 5},
		Model:           "main-model",
		Summarizer:      sum,
		SummarizerModel: "sum-model",
		Meter:           meter,
		TraceDir:        traceDir,
		MinKeep:         2,
		MaxSteps:        1,
		Registry:        tools.NewRegistry(),
		Budget:          Budget{Window: 400},
	})
	for i := 0; i < 20; i++ {
		a.Conversation().Append(llm.Message{Role: llm.RoleUser, Content: "较早的对话内容需要被压缩 config/agent.yaml 4318"})
	}
	if err := a.RunTurn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}

	files, err := os.ReadDir(traceDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("应写 1 份 trace，实际 %d", len(files))
	}
	raw, err := os.ReadFile(filepath.Join(traceDir, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var tr telemetry.TurnTrace
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.SummaryTokensIn != 1_000 || tr.SummaryTokensOut != 100 {
		t.Fatalf("trace 缺少摘要用量: %+v", tr)
	}
	if tr.TokensIn != 10 || tr.TokensOut != 5 {
		t.Fatalf("主对话用量被污染: %+v", tr)
	}
}
