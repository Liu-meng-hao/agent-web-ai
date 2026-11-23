package agent

import (
	"math"

	"agent/internal/llm"
)

// Budget 描述一次请求可用的上下文预算。
type Budget struct {
	Window        int
	OutputReserve int
	Safety        float64
	CompactAt     float64
}

// Limit 返回触发压缩的消息 token 上限；0 表示不启用预算控制。
func (b Budget) Limit() int {
	if b.Window <= 0 {
		return 0
	}
	usable := b.Window - b.OutputReserve
	if usable <= 0 {
		usable = b.Window
	}
	safety := b.Safety
	if safety <= 0 || safety >= 1 {
		safety = 0.1
	}
	at := b.CompactAt
	if at <= 0 || at > 1 {
		at = 0.75
	}
	return int(float64(usable) * (1 - safety) * at)
}

// EstimateTokens 粗略估算消息占用。
//
// 不引入 tiktoken：中日韩字符按 1 token、ASCII 按 4 字符 1 token 估算，
// 对"什么时候该压缩"这个判断已经足够，宁可略高估。
func EstimateTokens(msgs []llm.Message) int {
	total := 0
	for _, m := range msgs {
		total += 4 + estimateText(m.Content)
		for _, tc := range m.ToolCalls {
			total += 6 + estimateText(tc.Name) + estimateText(string(tc.Args))
		}
	}
	return total
}

// EstimateToolSchemas 估算工具声明占用。
//
// 工具 schema 每轮都随请求发送，几百到上千 token；不计入预算会让
// “估算没超、真实超窗”成为常态。
func EstimateToolSchemas(schemas []llm.ToolSchema) int {
	total := 0
	for _, s := range schemas {
		total += 8 + estimateText(s.Name) + estimateText(s.Description) + estimateText(string(s.Parameters))
	}
	return total
}

func estimateText(s string) int {
	if s == "" {
		return 0
	}
	ascii, other := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	return int(math.Ceil(float64(ascii)/4)) + other
}
