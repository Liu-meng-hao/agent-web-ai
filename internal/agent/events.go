package agent

import (
	"time"

	"agent/internal/llm"
	"agent/internal/tools"
)

// EventType 是 agent 向外广播的事件类型。
type EventType string

const (
	EventTurnStarted   EventType = "turn.started"
	EventText          EventType = "text"
	EventToolRequested EventType = "tool.requested"
	EventToolResult    EventType = "tool.result"
	EventTurnFinished  EventType = "turn.finished"
	EventError         EventType = "error"
	// EventReasoning 表示模型正在思考；Text 是增量片段，界面只做“思考中”提示，不展示原文。
	EventReasoning EventType = "reasoning"
	EventUsage     EventType = "usage"
	// EventNotice 是给用户看的非错误提示（压缩完成、记忆不可用等）。
	EventNotice EventType = "notice"
)

// Event 是一帧 agent 事件。TUI 与 headless 输出都消费它。
type Event struct {
	Type     EventType
	Step     int
	Text     string
	Call     *llm.ToolCall
	Result   *tools.Result
	Risk     tools.Risk
	Decision Decision
	Stop     string
	Duration time.Duration
	Err      error
	// 以下字段只在 EventUsage 时有意义，均为累计值。
	TokensIn  int
	TokensOut int
	// ReasoningTokens 是本轮输出中用于思考的 token，已包含在 TokensOut 内。
	ReasoningTokens int
	Cost            float64
}
