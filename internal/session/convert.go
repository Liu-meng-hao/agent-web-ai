package session

import (
	"fmt"
	"strings"

	"agent/internal/llm"
)

const interruptedNotice = "上一次执行被中断，命令可能已产生副作用。请先确认当前状态，再决定是否重试。"

// ToMessages 把事件流还原成对话消息。
//
// 只还原模型需要的消息；system prompt 不复用旧值，由调用方按当前配置重新生成。
// 对没有结果的工具调用补一条合成消息，避免模型误以为命令没执行过。
func ToMessages(events []Event) []llm.Message {
	var out []llm.Message

	for _, e := range events {
		switch e.Type {
		case TypeTurnStarted:
			var p TurnStarted
			if e.Decode(&p) == nil {
				out = append(out, llm.Message{Role: llm.RoleUser, Content: p.UserText})
			}

		case TypeAssistantMessage:
			var p AssistantMessage
			if e.Decode(&p) != nil {
				continue
			}
			msg := llm.Message{Role: llm.RoleAssistant, Content: p.Text}
			for _, c := range p.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args})
			}
			out = append(out, msg)

		case TypeToolResult:
			var p ToolResult
			if e.Decode(&p) != nil {
				continue
			}
			out = append(out, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: p.CallID,
				Content:    toolContent(p),
			})
		}
	}

	for _, id := range Interrupted(events) {
		out = append(out, llm.Message{Role: llm.RoleTool, ToolCallID: id, Content: interruptedNotice})
	}
	return out
}

// toolContent 与 loop 写入时的包装保持一致，保证恢复后的上下文与崩溃前等价。
func toolContent(p ToolResult) string {
	if p.Status == "denied" || p.Status == "interrupted" {
		return p.Content
	}
	name := p.Name
	if name == "" {
		name = "tool"
	}
	return fmt.Sprintf("<untrusted_content source=%q>\n%s\n</untrusted_content>", name, p.Content)
}

// BuildRestore 还原会话，并应用最后一次摘要：
// 返回的消息已经丢掉被摘要覆盖的前缀，调用方还需把 Summary 注入 system。
func BuildRestore(events []Event) (msgs []llm.Message, summary string, covered int) {
	for _, e := range events {
		switch e.Type {
		case TypeSummary:
			var p Summary
			if e.Decode(&p) == nil {
				summary = p.Text
				if p.Covered > covered {
					covered = p.Covered
				}
			}
		case TypeTruncated:
			// 硬截断没有摘要，但覆盖计数更大；取最大值才不会把被截掉的消息还回来。
			var p Truncated
			if e.Decode(&p) == nil && p.Covered > covered {
				covered = p.Covered
			}
		}
	}
	msgs = ToMessages(events)
	if covered < 0 {
		covered = 0
	}
	if covered > len(msgs) {
		covered = len(msgs)
	}
	return msgs[covered:], summary, covered
}

// Render 把事件流渲染成纯文本（replay 用，不调用模型）。
func Render(events []Event) string {
	var b strings.Builder
	for _, e := range events {
		switch e.Type {
		case TypeTurnStarted:
			var p TurnStarted
			if e.Decode(&p) == nil {
				fmt.Fprintf(&b, "\n[you] %s\n", p.UserText)
			}
		case TypeAssistantMessage:
			var p AssistantMessage
			if e.Decode(&p) == nil {
				if strings.TrimSpace(p.Text) != "" {
					fmt.Fprintf(&b, "[agent] %s\n", p.Text)
				}
				for _, c := range p.ToolCalls {
					fmt.Fprintf(&b, "[tool_call] %s %s %s\n", c.Name, c.ID, string(c.Args))
				}
			}
		case TypeToolResult:
			var p ToolResult
			if e.Decode(&p) == nil {
				fmt.Fprintf(&b, "[tool_result] %s %s (%dms)\n", p.Name, p.Status, p.DurationMS)
				for _, line := range strings.Split(strings.TrimSpace(p.Content), "\n") {
					fmt.Fprintf(&b, "    %s\n", line)
				}
			}
		case TypeTurnFinished:
			var p TurnFinished
			if e.Decode(&p) == nil {
				fmt.Fprintf(&b, "[turn finished] %s\n", p.Stop)
			}
		case TypeSummary:
			var p Summary
			if e.Decode(&p) == nil {
				fmt.Fprintf(&b, "[summary] 覆盖 %d 条消息\n%s\n", p.Covered, p.Text)
			}
		case TypeTruncated:
			var p Truncated
			if e.Decode(&p) == nil {
				fmt.Fprintf(&b, "[truncated] 硬截断至第 %d 条消息（无摘要）\n", p.Covered)
			}
		}
	}
	return b.String()
}
