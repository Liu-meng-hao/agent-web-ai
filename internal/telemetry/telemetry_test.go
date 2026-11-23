package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPricingCost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pricing.yaml")
	raw := "currency: CNY\nmodels:\n  deepseek-chat:\n    input: 2.0\n    output: 8.0\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPricing(path)
	if err != nil {
		t.Fatal(err)
	}
	// 100 万输入 + 100 万输出 = 2 + 8 = 10 元
	if got := p.Cost("deepseek-chat", 1_000_000, 1_000_000); got != 10 {
		t.Fatalf("成本计算错误: %v", got)
	}
	if got := p.Cost("unknown", 1_000_000, 1_000_000); got != 0 {
		t.Fatalf("未配置模型应按 0 计: %v", got)
	}
}

func TestLoadPricingMissingFile(t *testing.T) {
	p, err := LoadPricing(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("缺文件不应报错: %v", err)
	}
	if p.Currency != "CNY" || len(p.Models) != 0 {
		t.Fatalf("应返回空配置: %+v", p)
	}
}

func TestMeterAccumulates(t *testing.T) {
	p := &Pricing{Currency: "CNY", Models: map[string]Price{"m": {Input: 1, Output: 2}}}
	m := NewMeter(p)

	snap := m.Add("m", 500_000, 250_000)
	if snap.In != 500_000 || snap.Out != 250_000 {
		t.Fatalf("快照用量错误: %+v", snap)
	}
	// 0.5*1 + 0.25*2 = 1.0
	if snap.Cost != 1.0 {
		t.Fatalf("成本错误: %v", snap.Cost)
	}

	m.Seed(100, 50, 0.5)
	snap = m.Snapshot()
	if snap.In != 500_100 || snap.Out != 250_050 || snap.Cost != 1.5 {
		t.Fatalf("Seed 后快照错误: %+v", snap)
	}
}

func TestMeterReset(t *testing.T) {
	p := &Pricing{Currency: "CNY", Models: map[string]Price{"m": {Input: 1, Output: 1}}}
	m := NewMeter(p)
	m.Add("m", 1_000_000, 1_000_000)
	m.Reset()
	snap := m.Snapshot()
	if snap.In != 0 || snap.Out != 0 || snap.Cost != 0 {
		t.Fatalf("Reset 后应清零: %+v", snap)
	}
}

func TestNilMeterIsSafe(t *testing.T) {
	var m *Meter
	snap := m.Add("m", 1, 1)
	if snap.In != 0 || snap.Cost != 0 {
		t.Fatalf("nil 记账器应返回空快照: %+v", snap)
	}
}

func TestLogWarnAndTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "agent.log")
	LogWarn(path, "第一条")
	LogWarn(path, "第二条")

	lines, err := Tail(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("应读到 2 行，实际 %d", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &rec); err != nil {
		t.Fatalf("日志不是 JSON: %v (%s)", err, lines[1])
	}
	if rec["msg"] != "第二条" || rec["level"] != "WARN" {
		t.Fatalf("日志字段错误: %+v", rec)
	}

	// 只保留最后 n 行
	lines, _ = Tail(path, 1)
	if len(lines) != 1 {
		t.Fatalf("应只返回 1 行，实际 %d", len(lines))
	}
}

func TestTailMissingFile(t *testing.T) {
	lines, err := Tail(filepath.Join(t.TempDir(), "none.log"), 10)
	if err != nil || len(lines) != 0 {
		t.Fatalf("缺文件应返回空: %v %v", lines, err)
	}
}

func TestWriteTurn(t *testing.T) {
	dir := t.TempDir()
	tr := TurnTrace{TurnID: "01ABC", Model: "m", Steps: 2, TokensIn: 10, TokensOut: 3}
	if err := WriteTurn(dir, tr); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "01ABC.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got TurnTrace
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.TurnID != "01ABC" || got.TokensIn != 10 || got.Steps != 2 {
		t.Fatalf("trace 内容错误: %+v", got)
	}
}
