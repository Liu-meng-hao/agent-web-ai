package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStreamErrorFrame 覆盖兼容端点在流中途用 error 帧报错的情况：
// 以前会静默 continue，表现为“空回答”。
func TestStreamErrorFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"rate limited\",\"type\":\"api_error\"}}\n\n")
	}))
	defer srv.Close()

	p, err := NewOpenAI(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := p.Stream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var gotErr error
	for ev := range stream {
		if ev.Type == EventError {
			gotErr = ev.Err
		}
	}
	if gotErr == nil {
		t.Fatal("error 帧未转换成 EventError")
	}
	if !strings.Contains(gotErr.Error(), "rate limited") {
		t.Fatalf("错误信息丢失: %v", gotErr)
	}
}

// TestStreamRetriesWithoutStreamOptions 覆盖不认 stream_options 的兼容端点：
// 400 且错误提到该字段时应去掉它重试一次，而不是整轮失败。
func TestStreamRetriesWithoutStreamOptions(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"unknown field stream_options"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := NewOpenAI(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := p.Stream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("应降级重试而不是失败: %v", err)
	}
	var text string
	for ev := range stream {
		if ev.Type == EventText {
			text += ev.Text
		}
	}
	if text != "ok" {
		t.Fatalf("重试后文本丢失: %q", text)
	}
	if len(bodies) != 2 {
		t.Fatalf("应请求两次，实际 %d", len(bodies))
	}
	if _, exists := bodies[0]["stream_options"]; !exists {
		t.Fatal("第一次请求应带 stream_options")
	}
	if _, exists := bodies[1]["stream_options"]; exists {
		t.Fatal("重试请求不应再带 stream_options")
	}
}

// TestStreamReasoningContent 覆盖推理模型：reasoning_content 单独成事件，
// 不能混进正文；usage 里的 reasoning_tokens 要解析出来。
func TestStreamReasoningContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"先想一下\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"答案\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":8,\"completion_tokens_details\":{\"reasoning_tokens\":5}}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := NewOpenAI(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := p.Stream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var text, reasoning string
	var usage *Usage
	for ev := range stream {
		switch ev.Type {
		case EventText:
			text += ev.Text
		case EventReasoning:
			reasoning += ev.Text
		case EventUsage:
			usage = ev.Usage
		}
	}
	if text != "答案" {
		t.Fatalf("正文应只包含 content: %q", text)
	}
	if reasoning != "先想一下" {
		t.Fatalf("reasoning_content 未解析: %q", reasoning)
	}
	if usage == nil || usage.ReasoningTokens != 5 {
		t.Fatalf("reasoning_tokens 未解析: %+v", usage)
	}
}

// TestStreamUsageFrame 覆盖 include_usage：最后一个 chunk 只带 usage、不带 choices，
// 且必须仍然收到 Done 与文本。
func TestStreamUsageFrame(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":7}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p, err := NewOpenAI(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := p.Stream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var text string
	var usage *Usage
	var done bool
	for ev := range stream {
		switch ev.Type {
		case EventText:
			text += ev.Text
		case EventUsage:
			usage = ev.Usage
		case EventDone:
			done = true
		}
	}
	if text != "你好" {
		t.Fatalf("文本丢失: %q", text)
	}
	if usage == nil || usage.InputTokens != 120 || usage.OutputTokens != 7 {
		t.Fatalf("usage 未解析: %+v", usage)
	}
	if !done {
		t.Fatal("没有收到 Done")
	}

	opts, _ := gotBody["stream_options"].(map[string]any)
	if opts == nil || opts["include_usage"] != true {
		t.Fatalf("请求未开启 include_usage: %+v", gotBody["stream_options"])
	}
}
