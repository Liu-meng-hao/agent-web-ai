package telemetry

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// TokensPerUnit 是单价对应的 token 数：pricing.yaml 里填“每百万 token”的价格。
const TokensPerUnit = 1_000_000

// Price 是单个模型的单价（货币单位见 Pricing.Currency）。
type Price struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
}

// Pricing 是 config/pricing.yaml 的结构。
type Pricing struct {
	Currency string           `yaml:"currency"`
	Models   map[string]Price `yaml:"models"`
}

// LoadPricing 读取单价配置；文件不存在时返回空配置（成本按 0 计）。
func LoadPricing(path string) (*Pricing, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Pricing{Currency: "CNY", Models: map[string]Price{}}, nil
		}
		return nil, fmt.Errorf("读取单价配置 %s 失败: %w", path, err)
	}
	var p Pricing
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("解析单价配置 %s 失败: %w", path, err)
	}
	if p.Currency == "" {
		p.Currency = "CNY"
	}
	if p.Models == nil {
		p.Models = map[string]Price{}
	}
	return &p, nil
}

// Cost 按模型计算一次用量的费用；未配置的模型返回 0。
func (p *Pricing) Cost(model string, in, out int) float64 {
	if p == nil {
		return 0
	}
	price, ok := p.Models[model]
	if !ok {
		return 0
	}
	return (float64(in)*price.Input + float64(out)*price.Output) / TokensPerUnit
}

// Snapshot 是一份累计用量快照。
type Snapshot struct {
	In       int
	Out      int
	Cost     float64
	Currency string
}

// Meter 累计一个进程内的 token 与成本。
//
// 并发安全：TUI 事件循环、turn loop 与后台摘要都可能上报用量。
type Meter struct {
	mu      sync.Mutex
	pricing *Pricing
	in      int
	out     int
	cost    float64
}

// NewMeter 构造记账器；pricing 可以为 nil（成本恒为 0）。
func NewMeter(pricing *Pricing) *Meter {
	return &Meter{pricing: pricing}
}

// Add 累计一次用量并返回最新快照。
func (m *Meter) Add(model string, in, out int) Snapshot {
	if m == nil {
		return Snapshot{Currency: "CNY"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.in += in
	m.out += out
	if m.pricing != nil {
		m.cost += m.pricing.Cost(model, in, out)
	}
	return m.snapshotLocked()
}

// Seed 在恢复会话时把历史用量注入记账器。
func (m *Meter) Seed(in, out int, cost float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.in += in
	m.out += out
	m.cost += cost
}

// Reset 清零累计用量。
//
// 必须原地清零而不是替换 Meter：Agent 持有的是同一个指针，
// 换对象会让运行中的 agent 继续往旧记账器里累加，界面不会归零。
func (m *Meter) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.in, m.out, m.cost = 0, 0, 0
}

// Snapshot 返回当前累计用量。
func (m *Meter) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{Currency: "CNY"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Meter) snapshotLocked() Snapshot {
	currency := "CNY"
	if m.pricing != nil && m.pricing.Currency != "" {
		currency = m.pricing.Currency
	}
	return Snapshot{In: m.in, Out: m.out, Cost: m.cost, Currency: currency}
}
