package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"agent/internal/config"
)

func TestHTTPClientAndSession(t *testing.T) {
	var sessions []string
	var versions []string
	deleted := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted++
			if r.Header.Get("Mcp-Session-Id") != "sess-1" || r.Header.Get("MCP-Protocol-Version") != "2025-06-18" {
				http.Error(w, "missing session headers", 400)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		sessions = append(sessions, r.Header.Get("Mcp-Session-Id"))
		versions = append(versions, r.Header.Get("MCP-Protocol-Version"))
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.Header().Set("Content-Type", "application/json")

		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			resp["result"] = map[string]any{
				"protocolVersion": "2025-06-18",
				"serverInfo":      map[string]string{"name": "http-fake", "version": "1"},
			}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			resp["result"] = map[string]any{"tools": []map[string]any{{
				"name":        "ping",
				"description": "ping",
				"inputSchema": map[string]any{"type": "object"},
			}}}
		case "tools/call":
			resp["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": "pong"}}}
		default:
			resp["error"] = &rpcError{Code: -32601, Message: "method not found"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	ctx := context.Background()
	c, err := NewClient(ctx, "remote", config.MCPServer{
		Transport: "http",
		URL:       srv.URL,
		Approval:  "auto",
		Timeout:   "5s",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()

	list, err := c.ListTools(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListTools: %v %+v", err, list)
	}
	res, err := c.CallTool(ctx, "ping", nil)
	if err != nil || len(res.Content) != 1 || res.Content[0].Text != "pong" {
		t.Fatalf("CallTool: %v %+v", err, res)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(sessions) < 4 || sessions[0] != "" || sessions[1] != "sess-1" || sessions[2] != "sess-1" || sessions[3] != "sess-1" {
		t.Fatalf("session id 未复用: %v", sessions)
	}
	for i, version := range versions {
		if i == 0 && version != "" || i > 0 && version != "2025-06-18" {
			t.Fatalf("协议版本头错误: %v", versions)
		}
	}
	if deleted != 1 {
		t.Fatalf("DELETE 次数 = %d", deleted)
	}
}

func TestReadSSEResponse(t *testing.T) {
	body := strings.Join([]string{
		"event: message",
		`data: {"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"ready"}}`,
		"",
		"event: message",
		`data: {"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
		"",
		"event: message",
		`data: {"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`,
		"",
	}, "\n")
	var notifications []string
	resp, err := readSSEResponseWithHandler(strings.NewReader(body), 2, func(req *rpcRequest) {
		notifications = append(notifications, req.Method)
	})
	if err != nil {
		t.Fatalf("readSSEResponse: %v", err)
	}
	if resp.ID != 2 || !strings.Contains(string(resp.Result), "tools") {
		t.Fatalf("resp = %s", fmt.Sprint(resp))
	}
	if len(notifications) != 1 || notifications[0] != "notifications/message" {
		t.Fatalf("通知未分派: %v", notifications)
	}
}

func TestListToolsStopsOnRepeatedCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			resp["result"] = map[string]any{"protocolVersion": "2025-06-18"}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			params, _ := req.Params.(map[string]any)
			cursor, _ := params["cursor"].(string)
			name := "first"
			if cursor != "" {
				name = "second"
			}
			resp["result"] = map[string]any{
				"tools":      []map[string]any{{"name": name, "inputSchema": map[string]any{"type": "object"}}},
				"nextCursor": "same",
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	c, err := NewClient(context.Background(), "pages", config.MCPServer{Transport: "http", URL: srv.URL, Timeout: "5s"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	list, err := c.ListTools(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("ListTools: len=%d err=%v", len(list), err)
	}
	if !strings.Contains(strings.Join(c.Warnings(), "\n"), "重复游标") {
		t.Fatal("重复游标应产生告警")
	}
}

func TestListToolsCapsToolCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			resp["result"] = map[string]any{"protocolVersion": "2025-06-18"}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			list := make([]map[string]any, maxToolsPerServer+1)
			for i := range list {
				list[i] = map[string]any{"name": fmt.Sprintf("tool-%d", i), "inputSchema": map[string]any{"type": "object"}}
			}
			resp["result"] = map[string]any{"tools": list}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	c, err := NewClient(context.Background(), "many", config.MCPServer{Transport: "http", URL: srv.URL, Timeout: "5s"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	list, err := c.ListTools(context.Background())
	if err != nil || len(list) != maxToolsPerServer {
		t.Fatalf("ListTools: len=%d err=%v", len(list), err)
	}
	if !strings.Contains(strings.Join(c.Warnings(), "\n"), "上限") {
		t.Fatal("工具数量截断应产生告警")
	}
}

func TestManagerHealthAndReconnect(t *testing.T) {
	var fail atomic.Bool
	var startFailed atomic.Bool
	startFailed.Store(true)
	var initializes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "initialize" && startFailed.Load() {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		if req.Method == "tools/call" && fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			initializes.Add(1)
			resp["result"] = map[string]any{"protocolVersion": "2025-06-18"}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			resp["result"] = map[string]any{"tools": []map[string]any{{
				"name": "ping", "inputSchema": map[string]any{"type": "object"},
			}}}
		case "tools/call":
			resp["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": "pong"}}}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	m := NewManager(&config.MCPConfig{Servers: map[string]config.MCPServer{
		"remote": {Transport: "http", URL: srv.URL, Timeout: "5s", CallTimeout: "1s", Approval: "ask"},
	}}, Options{})
	m.Start(context.Background())
	defer m.Close()
	if len(m.Tools()) != 0 {
		t.Fatal("首次启动失败时不应发现工具")
	}
	startFailed.Store(false)
	if err := m.Reconnect(context.Background(), "remote"); err != nil {
		t.Fatalf("首次失败后重连: %v", err)
	}
	list := m.Tools()
	if len(list) != 1 {
		t.Fatalf("tools = %d", len(list))
	}
	tool := list[0]
	fail.Store(true)
	for i := 0; i < failuresBeforeDegraded; i++ {
		if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`), nil); err == nil {
			t.Fatal("故障期间调用应失败")
		}
	}
	if status := strings.Join(m.Status(), "\n"); !strings.Contains(status, StatusDegraded) {
		t.Fatalf("连续失败后未降级: %s", status)
	}

	fail.Store(false)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`), nil); err != nil {
		t.Fatalf("恢复调用失败: %v", err)
	}
	if status := strings.Join(m.Status(), "\n"); !strings.Contains(status, StatusReady) {
		t.Fatalf("成功调用后未恢复: %s", status)
	}

	before := initializes.Load()
	if err := m.Reconnect(context.Background(), "remote"); err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if initializes.Load() != before+1 {
		t.Fatalf("重连未重新 initialize: before=%d after=%d", before, initializes.Load())
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil || !strings.Contains(res.Content, "pong") {
		t.Fatalf("旧工具句柄未使用新连接: res=%+v err=%v", res, err)
	}
}
