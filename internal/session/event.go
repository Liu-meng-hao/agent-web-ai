// Package session 负责会话事件溯源：只追加的事件流、崩溃恢复、还原成消息。
package session

import (
	"encoding/json"
	"fmt"
	"time"
)

// SchemaVersion 是事件结构版本。
const SchemaVersion = 1

// EventType 是事件类型。
type EventType string

const (
	TypeSessionStarted   EventType = "session.started"
	TypeTurnStarted      EventType = "turn.started"
	TypeAssistantMessage EventType = "assistant.message"
	TypeToolCall         EventType = "tool.call"
	TypeToolResult       EventType = "tool.result"
	TypeTurnFinished     EventType = "turn.finished"
	TypeSessionRecovered EventType = "session.recovered"
	TypeSessionEnded     EventType = "session.ended"
	TypeSummary          EventType = "summary.created"
	TypeTruncated        EventType = "session.truncated"
)

// Event 是事件流中的一行。
type Event struct {
	V       int             `json:"v"`
	Seq     int64           `json:"seq"`
	TS      time.Time       `json:"ts"`
	Type    EventType       `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// New 构造一个事件；seq 与时间戳由 Store 负责填。
func New(t EventType, payload any) (Event, error) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return Event{}, fmt.Errorf("编码事件 payload 失败: %w", err)
		}
		raw = b
	}
	return Event{V: SchemaVersion, Type: t, Payload: raw}, nil
}

// Decode 把 payload 解到 v。
func (e Event) Decode(v any) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("事件 %s 没有 payload", e.Type)
	}
	return json.Unmarshal(e.Payload, v)
}

// Validate 校验必填字段。
func (e Event) Validate() error {
	if e.V != SchemaVersion {
		return fmt.Errorf("事件版本 %d 不受支持（期望 %d）", e.V, SchemaVersion)
	}
	if e.Seq <= 0 {
		return fmt.Errorf("事件 seq 必须为正数，当前 %d", e.Seq)
	}
	if e.Type == "" {
		return fmt.Errorf("事件缺少 type")
	}
	return nil
}

// SessionStarted 是会话开始时写入的元信息快照。
type SessionStarted struct {
	Cwd      string `json:"cwd"`
	Model    string `json:"model"`
	Shell    string `json:"shell"`
	Approval string `json:"approval"`
}

// TurnStarted 记录一轮用户输入。
type TurnStarted struct {
	TurnID   string `json:"turn_id"`
	UserText string `json:"user_text"`
}

// AssistantMessage 记录模型的一次完整输出（含工具调用，不含增量）。
type AssistantMessage struct {
	TurnID    string         `json:"turn_id"`
	Step      int            `json:"step"`
	Text      string         `json:"text,omitempty"`
	ToolCalls []ToolCallInfo `json:"tool_calls,omitempty"`
}

// ToolCallInfo 是 assistant 消息里的工具调用。
type ToolCallInfo struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// ToolCall 记录一次即将执行的工具调用（先写它，崩溃才能识别中断）。
type ToolCall struct {
	TurnID string          `json:"turn_id"`
	Step   int             `json:"step"`
	CallID string          `json:"call_id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args,omitempty"`
}

// ToolResult 记录一次工具结果。
type ToolResult struct {
	CallID     string `json:"call_id"`
	Name       string `json:"name,omitempty"`
	Status     string `json:"status"` // ok | fail | denied
	Content    string `json:"content"`
	DurationMS int64  `json:"duration_ms"`
}

// TurnFinished 记录一轮结束原因。
type TurnFinished struct {
	TurnID string `json:"turn_id"`
	Stop   string `json:"stop"`
}

// SessionRecovered 记录一次崩溃恢复。
type SessionRecovered struct {
	InterruptedCalls []string `json:"interrupted_calls,omitempty"`
	TruncatedBytes   int64    `json:"truncated_bytes,omitempty"`
}

// SessionEnded 记录会话结束原因。
type SessionEnded struct {
	Reason string `json:"reason"`
}

// Summary 记录一次 L1 摘要。Covered 是已被摘要替代的消息数，
// 恢复时据此丢掉对应的历史前缀，保证上下文与压缩时等价。
type Summary struct {
	Text    string    `json:"text"`
	Covered int       `json:"covered"`
	At      time.Time `json:"at"`
}

// Truncated 记录一次没有摘要的硬截断。
//
// 必须落盘：否则重启后只会按最后一条摘要的 covered 恢复，
// 被硬截掉的消息会原样回来，再压一遍。
type Truncated struct {
	Covered int       `json:"covered"`
	At      time.Time `json:"at"`
}
