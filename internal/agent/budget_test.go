package agent

import (
	"testing"

	"agent/internal/llm"
)

func TestEstimateTokensMixesCJKAndASCII(t *testing.T) {
	// 每条消息固定 4 token 开销；ASCII 4 字符 1 token，CJK 1 字符 1 token。
	msgs := []llm.Message{{Role: llm.RoleUser, Content: "abcd"}}
	if got := EstimateTokens(msgs); got != 5 {
		t.Fatalf("ASCII 估算 = %d, want 5", got)
	}
	msgs = []llm.Message{{Role: llm.RoleUser, Content: "中文"}}
	if got := EstimateTokens(msgs); got != 6 {
		t.Fatalf("CJK 估算 = %d, want 6", got)
	}
}

func TestBudgetLimit(t *testing.T) {
	b := Budget{Window: 100000, OutputReserve: 8000, Safety: 0.1, CompactAt: 0.75}
	// (100000-8000)*0.9*0.75 = 62100
	if got := b.Limit(); got != 62100 {
		t.Fatalf("Limit = %d, want 62100", got)
	}
	if got := (Budget{}).Limit(); got != 0 {
		t.Fatalf("Window=0 应关闭预算，得到 %d", got)
	}
}

func TestBudgetLimitClampsBadConfig(t *testing.T) {
	// Reserve 大于窗口时退回整窗；非法 Safety/CompactAt 用默认值。
	b := Budget{Window: 1000, OutputReserve: 2000, Safety: 1.5, CompactAt: 2}
	// 1000*0.9*0.75 = 675
	if got := b.Limit(); got != 675 {
		t.Fatalf("Limit = %d, want 675", got)
	}
}
