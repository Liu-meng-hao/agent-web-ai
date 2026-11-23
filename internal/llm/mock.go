package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Mock 是离线的确定性 provider，用于测试流式链路、TUI 与工具审批，
// 不需要网络和 API key。
//
// 约定：用户消息以 "shell:" 开头时，第一轮返回一次 shell 工具调用，
// 拿到工具结果后再给出总结——这样没有 API key 也能走通完整的
// 「模型 → 工具 → 审批 → 回灌 → 收尾」链路。
type Mock struct{}

// Name 返回供应商短名。
func (Mock) Name() string { return "mock" }

// Stream 按约定返回文本或工具调用。
func (Mock) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	text, call := mockReply(req)
	ch := make(chan Event, 8)

	go func() {
		defer close(ch)

		if call != nil {
			select {
			case <-ctx.Done():
				return
			case ch <- Event{Type: EventTool, ToolCall: call}:
			}
			select {
			case <-ctx.Done():
			case ch <- Event{Type: EventDone, FinishReason: "tool_calls"}:
			}
			return
		}

		for _, r := range text {
			select {
			case <-ctx.Done():
				return
			case ch <- Event{Type: EventText, Text: string(r)}:
			}
			time.Sleep(8 * time.Millisecond)
		}
		select {
		case <-ctx.Done():
		case ch <- Event{Type: EventDone, FinishReason: "stop"}:
		}
	}()

	return ch, nil
}

func mockReply(req Request) (string, *ToolCall) {
	if len(req.Messages) == 0 {
		return mockText(), nil
	}

	last := req.Messages[len(req.Messages)-1]
	switch last.Role {
	case RoleTool:
		content := stripUntrusted(last.Content)
		firstLine := strings.SplitN(strings.TrimSpace(content), "\n", 2)[0]
		return fmt.Sprintf("命令执行完毕（mock）。结果摘要：%s\n\n这是一条离线链路，真实模型会据此继续推理。", firstLine), nil

	case RoleUser:
		prompt := strings.TrimSpace(last.Content)
		if cmd, ok := strings.CutPrefix(prompt, "shell:"); ok {
			cmd = strings.TrimSpace(cmd)
			if cmd == "" {
				cmd = "echo mock"
			}
			args, _ := json.Marshal(map[string]string{"command": cmd})
			return "", &ToolCall{ID: "mock_call_1", Name: "shell", Args: args}
		}
		if text, ok := strings.CutPrefix(prompt, "memory:"); ok {
			text = strings.TrimSpace(text)
			if text == "" {
				text = "mock 记忆条目"
			}
			args, _ := json.Marshal(map[string]string{"text": text})
			return "", &ToolCall{ID: "mock_call_1", Name: "memory_write", Args: args}
		}
		return mockText(), nil
	}

	return mockText(), nil
}

func mockText() string {
	return "你好，我是 mock provider。当前没有需要执行的工具，只验证「终端 → provider → 流式渲染」这条链路。" +
		"想测工具与审批，请把消息写成 “shell: echo hi”。"
}

// stripUntrusted 去掉工具结果的不可信包装，mock 只是做摘要。
func stripUntrusted(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "<untrusted_content") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(s, "</untrusted_content>"))
}

// New 按配置构造 provider。
func New(provider, baseURL, apiKey string) (Provider, error) {
	switch provider {
	case "", "openai", "openai-compatible":
		return NewOpenAI(baseURL, apiKey)
	case "mock":
		return Mock{}, nil
	default:
		return nil, fmt.Errorf("未知 provider %q（支持 openai / mock）", provider)
	}
}
