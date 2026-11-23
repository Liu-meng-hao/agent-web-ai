package agent

import (
	"strings"

	"agent/internal/llm"
)

// Conversation 是一段对话历史。
type Conversation struct {
	system   string
	summary  string
	covered  int
	messages []llm.Message
}

// SetSummary 设置较早对话的摘要与已压缩的消息数。
func (c *Conversation) SetSummary(summary string, covered int) {
	c.summary = summary
	c.covered = covered
}

// SummaryInfo 返回摘要与已压缩消息数。
func (c *Conversation) SummaryInfo() (string, int) { return c.summary, c.covered }

// Covered 返回已被摘要替代的消息数。
func (c *Conversation) Covered() int { return c.covered }

// AddCovered 在硬截断（没有摘要）时也要推进覆盖计数，保证恢复后口径一致。
func (c *Conversation) AddCovered(n int) { c.covered += n }

// DropOldest 移除最早 n 条消息。
func (c *Conversation) DropOldest(n int) {
	if n <= 0 {
		return
	}
	if n > len(c.messages) {
		n = len(c.messages)
	}
	c.messages = append([]llm.Message(nil), c.messages[n:]...)
}

// clearSummary 清空摘要状态。
func (c *Conversation) clearSummary() {
	c.summary = ""
	c.covered = 0
}

// NewConversation 创建带 system prompt 的对话。
func NewConversation(system string) *Conversation {
	return &Conversation{system: system}
}

// Append 追加一条消息。
func (c *Conversation) Append(m llm.Message) {
	c.messages = append(c.messages, m)
}

// Messages 返回历史副本。
func (c *Conversation) Messages() []llm.Message {
	return append([]llm.Message(nil), c.messages...)
}

// Len 返回历史消息数。
func (c *Conversation) Len() int { return len(c.messages) }

// Restore 把恢复出来的消息追加进对话。
func (c *Conversation) Restore(msgs []llm.Message) {
	c.messages = append(c.messages, msgs...)
}

// WithSystem 返回请求用的完整消息列表（system 在前）。
func (c *Conversation) WithSystem() []llm.Message { return c.WithSystemExtra("") }

// WithSystemExtra 把临时上下文（记忆召回）并入 system 消息。
//
// 合并而不是追加第二条 system：部分 OpenAI 兼容端点只接受一条 system，
// 连续两条会被静默忽略或直接报错。
func (c *Conversation) WithSystemExtra(extra string) []llm.Message {
	var parts []string
	if c.system != "" {
		parts = append(parts, c.system)
	}
	if c.summary != "" {
		parts = append(parts, "【较早对话摘要】\n"+c.summary)
	}
	if extra != "" {
		parts = append(parts, extra)
	}
	system := strings.Join(parts, "\n\n")
	out := make([]llm.Message, 0, len(c.messages)+1)
	out = append(out, llm.Message{Role: llm.RoleSystem, Content: system})
	out = append(out, c.messages...)
	return out
}
