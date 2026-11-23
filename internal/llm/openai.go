package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAI 实现 OpenAI 兼容的 /chat/completions 流式接口
// （DeepSeek、Moonshot、Ollama、vLLM 等同协议服务均可直接使用）。
type OpenAI struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

// NewOpenAI 构造一个 OpenAI 兼容 provider。
func NewOpenAI(baseURL, apiKey string) (*OpenAI, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("openai: base_url 不能为空")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, ErrNoAPIKey
	}
	return &OpenAI{
		baseURL: baseURL,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

// Name 返回供应商短名。
func (p *OpenAI) Name() string { return "openai" }

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

// oaToolCall 是 OpenAI 的工具调用形状：arguments 必须是 JSON 字符串，
// 而不是 JSON 对象，否则兼容端点会返回 400。
type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// toOAMessages 把内部消息编码成 OpenAI 形状。
func toOAMessages(msgs []Message) []oaMessage {
	out := make([]oaMessage, 0, len(msgs))
	for _, m := range msgs {
		om := oaMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			var otc oaToolCall
			otc.ID = tc.ID
			otc.Type = "function"
			otc.Function.Name = tc.Name
			otc.Function.Arguments = string(tc.Args)
			if otc.Function.Arguments == "" {
				otc.Function.Arguments = "{}"
			}
			om.ToolCalls = append(om.ToolCalls, otc)
		}
		out = append(out, om)
	}
	return out
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaRequest struct {
	Model         string           `json:"model"`
	Messages      []oaMessage      `json:"messages"`
	Stream        bool             `json:"stream"`
	Temperature   float64          `json:"temperature,omitempty"`
	MaxTokens     int              `json:"max_tokens,omitempty"`
	Tools         []oaTool         `json:"tools,omitempty"`
	StreamOptions *oaStreamOptions `json:"stream_options,omitempty"`
}

type oaUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// 推理模型的思考 token 计入 completion_tokens，这里单独取出来用于展示。
	CompletionDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type oaChunk struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
	Usage   *oaUsage `json:"usage"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// buildPayload 组装请求体，独立成函数以便单测断言最终 JSON 形状。
func buildPayload(req Request) oaRequest {
	payload := oaRequest{
		Model:         req.Model,
		Stream:        true,
		Temperature:   req.Temperature,
		MaxTokens:     req.MaxTokens,
		Messages:      toOAMessages(req.Messages),
		StreamOptions: &oaStreamOptions{IncludeUsage: true},
	}
	for _, t := range req.Tools {
		var ot oaTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.Parameters
		payload.Tools = append(payload.Tools, ot)
	}
	return payload
}

// post 编码并发送一次请求；调用方负责关闭响应体。
func (p *OpenAI) post(ctx context.Context, payload oaRequest) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("编码请求失败: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("请求模型失败: %w", err)
	}
	return resp, nil
}

// mentionsStreamOptions 判断 400 是否由 stream_options/include_usage 引起。
func mentionsStreamOptions(raw []byte) bool {
	lower := strings.ToLower(string(raw))
	return strings.Contains(lower, "stream_options") || strings.Contains(lower, "include_usage")
}

// Stream 发起流式请求。
func (p *OpenAI) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	payload := buildPayload(req)

	resp, err := p.post(ctx, payload)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		// 部分兼容端点不认 stream_options：400 且错误提到该字段时，
		// 去掉它重试一次，让对话继续，而不是整轮失败。
		if resp.StatusCode == http.StatusBadRequest && mentionsStreamOptions(raw) && payload.StreamOptions != nil {
			payload.StreamOptions = nil
			resp, err = p.post(ctx, payload)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode != http.StatusOK {
				raw, _ = io.ReadAll(io.LimitReader(resp.Body, 4096))
				_ = resp.Body.Close()
				return nil, fmt.Errorf("模型返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
			}
		} else {
			return nil, fmt.Errorf("模型返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		}
	}

	ch := make(chan Event, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		send := func(ev Event) bool {
			select {
			case ch <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}

		// 工具调用的参数是分片下发的，按 index 累积，结束时一次性交付。
		type pending struct {
			id   string
			name string
			args strings.Builder
		}
		calls := map[int]*pending{}
		emitted := false
		finishReason := ""

		flushCalls := func() {
			if emitted {
				return
			}
			emitted = true
			for i := 0; i < len(calls); i++ {
				c, ok := calls[i]
				if !ok {
					continue
				}
				if !send(Event{Type: EventTool, ToolCall: &ToolCall{ID: c.id, Name: c.name, Args: json.RawMessage(c.args.String())}}) {
					return
				}
			}
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "" {
				continue
			}
			if data == "[DONE]" {
				flushCalls()
				if finishReason == "" {
					finishReason = "stop"
				}
				send(Event{Type: EventDone, FinishReason: finishReason})
				return
			}

			var chunk oaChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue // 心跳或未知帧，忽略
			}
			// 部分兼容端点在流中途用 error 帧报错，不处理会表现为“空回答”。
			if chunk.Error.Message != "" {
				send(Event{Type: EventError, Err: fmt.Errorf("模型返回错误: %s", chunk.Error.Message)})
				return
			}
			// include_usage 时最后一个 chunk 只带 usage、不带 choices。
			if chunk.Usage != nil {
				if !send(Event{Type: EventUsage, Usage: &Usage{
					InputTokens:     chunk.Usage.PromptTokens,
					OutputTokens:    chunk.Usage.CompletionTokens,
					ReasoningTokens: chunk.Usage.CompletionDetails.ReasoningTokens,
				}}) {
					return
				}
			}
			for _, choice := range chunk.Choices {
				// 推理模型会先下发 reasoning_content；不展示原文，只用来提示“思考中”。
				if choice.Delta.ReasoningContent != "" {
					if !send(Event{Type: EventReasoning, Text: choice.Delta.ReasoningContent}) {
						return
					}
				}
				if choice.Delta.Content != "" && !send(Event{Type: EventText, Text: choice.Delta.Content}) {
					return
				}
				for _, tc := range choice.Delta.ToolCalls {
					c, ok := calls[tc.Index]
					if !ok {
						c = &pending{}
						calls[tc.Index] = c
					}
					if tc.ID != "" {
						c.id = tc.ID
					}
					if tc.Function.Name != "" {
						c.name = tc.Function.Name
					}
					c.args.WriteString(tc.Function.Arguments)
				}
				// 不在这里 return：部分端点把 usage 放在 finish 之后的独立 chunk，
				// 提前退出会丢掉用量统计。
				if choice.FinishReason != "" {
					finishReason = choice.FinishReason
					flushCalls()
				}
			}
		}
		if err := scanner.Err(); err != nil {
			send(Event{Type: EventError, Err: fmt.Errorf("读取流失败: %w", err)})
			return
		}
		flushCalls()
		if finishReason == "" {
			finishReason = "stop"
		}
		send(Event{Type: EventDone, FinishReason: finishReason})
	}()

	return ch, nil
}
