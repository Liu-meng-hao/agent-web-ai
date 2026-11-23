package llm

import (
	"encoding/json"
	"testing"
)

// TestBuildPayloadToolRoundTrip 守住多步工具调用的编码形状：
// assistant 的 tool_calls、tool 消息的 tool_call_id、arguments 必须是字符串。
// 字段名对但形状错，兼容端点照样返回 400。
func TestBuildPayloadToolRoundTrip(t *testing.T) {
	req := Request{
		Model: "deepseek-chat",
		Messages: []Message{
			{Role: RoleSystem, Content: "sys"},
			{Role: RoleUser, Content: "列一下目录"},
			{
				Role:    RoleAssistant,
				Content: "我看一下",
				ToolCalls: []ToolCall{{
					ID:   "call_1",
					Name: "shell",
					Args: json.RawMessage(`{"command":"ls"}`),
				}},
			},
			{Role: RoleTool, ToolCallID: "call_1", Content: "a.txt"},
		},
		Tools: []ToolSchema{{
			Name:        "shell",
			Description: "run a command",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
	}

	raw, err := json.Marshal(buildPayload(req))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(got.Messages))
	}

	assistant := got.Messages[2]
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant tool_calls = %d, want 1", len(assistant.ToolCalls))
	}
	call := assistant.ToolCalls[0]
	if call.ID != "call_1" || call.Type != "function" || call.Function.Name != "shell" {
		t.Fatalf("unexpected tool call: %+v", call)
	}
	// arguments 必须是 JSON 字符串（"..."），而不是对象。
	if len(call.Function.Arguments) == 0 || call.Function.Arguments[0] != '"' {
		t.Fatalf("arguments must be a JSON string, got %s", call.Function.Arguments)
	}
	// arguments 必须是 JSON 字符串（"..."），而不是对象：先解一层 string，再解内容。
	var argJSON string
	if err := json.Unmarshal(call.Function.Arguments, &argJSON); err != nil {
		t.Fatalf("arguments is not a JSON string: %v", err)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(argJSON), &args); err != nil {
		t.Fatalf("arguments content invalid: %v", err)
	}
	if args["command"] != "ls" {
		t.Fatalf("arguments lost content: %v", args)
	}

	if got.Messages[3].Role != "tool" || got.Messages[3].ToolCallID != "call_1" {
		t.Fatalf("tool message missing tool_call_id: %+v", got.Messages[3])
	}

	if len(got.Tools) != 1 || got.Tools[0].Type != "function" || got.Tools[0].Function.Name != "shell" {
		t.Fatalf("unexpected tools: %+v", got.Tools)
	}
}

// TestBuildPayloadEmptyArguments 空参数必须编码成 "{}"，部分端点不接受空字符串。
func TestBuildPayloadEmptyArguments(t *testing.T) {
	raw, err := json.Marshal(buildPayload(Request{
		Model: "m",
		Messages: []Message{{
			Role:      RoleAssistant,
			ToolCalls: []ToolCall{{ID: "c1", Name: "noop"}},
		}},
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got struct {
		Messages []struct {
			ToolCalls []struct {
				Function struct {
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Messages) != 1 || len(got.Messages[0].ToolCalls) != 1 {
		t.Fatalf("unexpected payload: %s", raw)
	}
	if got.Messages[0].ToolCalls[0].Function.Arguments != "{}" {
		t.Fatalf("arguments = %q, want {}", got.Messages[0].ToolCalls[0].Function.Arguments)
	}
}
