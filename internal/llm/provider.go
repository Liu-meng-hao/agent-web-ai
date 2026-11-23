// Package llm 定义与供应商无关的流式接口。
//
// 约定：Stream 立即返回事件通道，所有错误都以 Event{Type: EventError} 送达，
// 通道关闭即表示本次生成结束。调用方只需 range 通道。
package llm

import (
	"context"
	"encoding/json"
	"errors"
)

// Role 是消息角色。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 是模型发起的一次工具调用。
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"arguments,omitempty"`
}

// ToolSchema 是暴露给模型的工具声明。
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Message 是上下文中的一条消息。
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Request 是一次生成请求。
type Request struct {
	Model       string
	Messages    []Message
	Tools       []ToolSchema
	Temperature float64
	MaxTokens   int
}

// Usage 是 token 用量（部分供应商流式不返回，可能为 nil）。
type Usage struct {
	InputTokens  int
	OutputTokens int
	// ReasoningTokens 是输出中用于思考的 token，已包含在 OutputTokens 内。
	ReasoningTokens int
}

// EventType 区分流式事件。
type EventType string

const (
	EventText      EventType = "text"
	EventReasoning EventType = "reasoning"
	EventTool      EventType = "tool_call"
	EventUsage     EventType = "usage"
	EventDone      EventType = "done"
	EventError     EventType = "error"
)

// Event 是流式输出的一帧。
type Event struct {
	Type         EventType
	Text         string
	ToolCall     *ToolCall
	Usage        *Usage
	FinishReason string
	Err          error
}

// Provider 是所有模型实现的统一接口。
type Provider interface {
	// Name 返回供应商短名，用于日志与状态栏。
	Name() string
	// Stream 发起一次流式生成；返回的通道关闭即结束。
	Stream(ctx context.Context, req Request) (<-chan Event, error)
}

// ErrNoAPIKey 表示缺少鉴权信息。
var ErrNoAPIKey = errors.New("缺少 API key：请设置 AGENT_API_KEY，或在 config/agent.yaml 的 models.*.api_key 中填写")
