package mcp

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"agent/internal/config"
	"agent/internal/tools"
)

// TestMCPHelperProcess 既是一个普通测试（直接返回），也是测试用的
// stdio MCP server：被 NewClient 以 -test.run 重新拉起后走下面的循环。
func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	enc := json.NewEncoder(os.Stdout)

	for sc.Scan() {
		var req rpcRequest
		if json.Unmarshal(sc.Bytes(), &req) != nil {
			continue
		}
		if req.Method == "notifications/initialized" {
			continue
		}
		var (
			result any
			rerr   *rpcError
		)
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-06-18",
				"serverInfo":      map[string]string{"name": "fake", "version": "1.0"},
			}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "回显输入",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
				},
			}}}
		case "tools/call":
			params, _ := req.Params.(map[string]any)
			name, _ := params["name"].(string)
			args, _ := params["arguments"].(map[string]any)
			if name != "echo" {
				rerr = &rpcError{Code: -32601, Message: "unknown tool"}
				break
			}
			text, _ := args["text"].(string)
			if text == "hang" {
				time.Sleep(200 * time.Millisecond)
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "echo:" + text}}}
		default:
			rerr = &rpcError{Code: -32601, Message: "method not found"}
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rerr != nil {
			resp["error"] = rerr
		} else {
			resp["result"] = result
		}
		_ = enc.Encode(resp)
	}
	os.Exit(0)
}

func TestStdioClient(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.MCPServer{
		Transport: "stdio",
		Command:   exe,
		Args:      []string{"-test.run=TestMCPHelperProcess"},
		Env:       map[string]string{"GO_WANT_MCP_HELPER": "1"},
		Approval:  "ask",
		Timeout:   "10s",
	}

	ctx := context.Background()
	c, err := NewClient(ctx, "fake", cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()

	list, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(list) != 1 || list[0].Name != "echo" {
		t.Fatalf("tools = %+v", list)
	}

	res, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"你好"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "echo:你好" {
		t.Fatalf("result = %+v", res)
	}
}

type fakeCaller struct {
	calls int
	args  json.RawMessage
}

func (f *fakeCaller) CallTool(_ context.Context, _ string, args json.RawMessage) (*CallResult, error) {
	f.calls++
	f.args = args
	return &CallResult{Content: []Content{
		{Type: "text", Text: "hello"},
		{Type: "image", Data: base64.StdEncoding.EncodeToString([]byte("pngdata")), MIMEType: "image/png"},
		{Type: "resource", Resource: &Resource{URI: "file://x", Text: "resource text"}},
	}}, nil
}

func TestToolAdapter(t *testing.T) {
	dir := t.TempDir()
	caller := &fakeCaller{}
	schema := json.RawMessage(`{
		"$schema": "http://json-schema.org/draft-07/schema#",
		"$ref": "#/$defs/Args",
		"$defs": {
			"Args": {"type": "object", "properties": {
				"path": {"anyOf": [{"type": "string"}, {"type": "null"}]},
				"nested": {"$ref": "#/$defs/Nested"}
			}},
			"Nested": {"type": "object", "properties": {"value": {"type": "string"}}}
		}
	}`)
	tl := newTool("my server", caller, ToolInfo{Name: "read file/x", Description: "读文件", InputSchema: schema}, "ask", dir)

	if tl.Name() != "mcp__my_server__read_file_x" {
		t.Fatalf("name = %q", tl.Name())
	}
	if tl.Risk() != tools.RiskNetwork {
		t.Fatalf("risk = %v", tl.Risk())
	}
	if tl.DisabledReason() != "" {
		t.Fatalf("ask 不应被禁用: %q", tl.DisabledReason())
	}
	disabled := newTool("s", caller, ToolInfo{Name: "x"}, "deny", dir)
	if disabled.DisabledReason() == "" {
		t.Fatal("approval=deny 应直接禁用")
	}

	var got map[string]any
	if err := json.Unmarshal(tl.Schema(), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["$schema"]; ok {
		t.Fatal("$schema 未清理")
	}
	if got["type"] != "object" || got["additionalProperties"] != false {
		t.Fatalf("schema = %v", got)
	}
	props, _ := got["properties"].(map[string]any)
	path, _ := props["path"].(map[string]any)
	if _, ok := path["anyOf"]; ok {
		t.Fatalf("anyOf 未降级: %v", path)
	}
	if path["type"] != "string" {
		t.Fatalf("path type = %v", path["type"])
	}
	nested, _ := props["nested"].(map[string]any)
	if nested["type"] != "object" || strings.Contains(string(tl.Schema()), "$ref") {
		t.Fatalf("嵌套 $ref 未展开: %s", tl.Schema())
	}

	res, err := tl.Execute(context.Background(), json.RawMessage(`{"path":"a"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if caller.calls != 1 {
		t.Fatalf("calls = %d", caller.calls)
	}
	if !strings.Contains(res.Content, "hello") || !strings.Contains(res.Content, "resource text") {
		t.Fatalf("content = %q", res.Content)
	}
	if !strings.Contains(res.Content, "图片已保存：") {
		t.Fatalf("图片未保存: %q", res.Content)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	if len(files) != 1 {
		t.Fatalf("图片文件 = %v", files)
	}
}

func TestManagerDegradesOnBadCommand(t *testing.T) {
	var warns []string
	m := NewManager(&config.MCPConfig{Servers: map[string]config.MCPServer{
		"broken": {Transport: "stdio", Command: "definitely-not-a-real-binary-xyz", Lazy: true},
	}}, Options{Warn: func(s string) { warns = append(warns, s) }})
	m.Start(context.Background())
	defer m.Close()

	if len(m.Tools()) != 0 {
		t.Fatal("坏 server 不应提供工具")
	}
	status := strings.Join(m.Status(), "\n")
	if !strings.Contains(status, "broken") || !strings.Contains(status, StatusDegraded) {
		t.Fatalf("status = %s", status)
	}
	if len(warns) == 0 {
		t.Fatal("降级应产生告警")
	}
	if !strings.Contains(strings.Join(warns, "\n"), "lazy: true 暂未实现") {
		t.Fatalf("lazy 应明确告警已忽略: %v", warns)
	}
}

func TestStdioCallTimeout(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(context.Background(), "slow", config.MCPServer{
		Transport:   "stdio",
		Command:     exe,
		Args:        []string{"-test.run=TestMCPHelperProcess"},
		Env:         map[string]string{"GO_WANT_MCP_HELPER": "1"},
		Timeout:     "5s",
		CallTimeout: "30ms",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, err = c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hang"}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CallTool 应在 call_timeout 到期后返回 deadline，实际: %v", err)
	}
}

func TestCommandForOSResolvesCmd(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows 命令解析专用用例")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mcpfake.cmd"), []byte("@echo off\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PATHEXT", ".CMD;.EXE;.BAT")
	name, args := commandForOS("mcpfake", []string{"-y", "pkg"})
	if !strings.EqualFold(name, "cmd.exe") || len(args) < 3 || !strings.HasSuffix(strings.ToLower(args[2]), "mcpfake.cmd") {
		t.Fatalf("未包装解析后的 .cmd：name=%q args=%v", name, args)
	}
}

func TestCapResultPreservesUTF8AndOverflow(t *testing.T) {
	imageDir := filepath.Join(t.TempDir(), "artifacts")
	overflowDir := filepath.Join(t.TempDir(), "logs")
	tl := newTool("s", &fakeCaller{}, ToolInfo{Name: "x"}, "ask", imageDir)
	tl.overflowDir = overflowDir
	original := strings.Repeat("你", maxResultBytes)
	got := tl.capResult(original)
	if !utf8.ValidString(got) {
		t.Fatal("截断结果不是合法 UTF-8")
	}
	files, err := filepath.Glob(filepath.Join(overflowDir, "overflow-*.txt"))
	if err != nil || len(files) != 1 {
		t.Fatalf("溢出文件 = %v, err=%v", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil || string(raw) != original {
		t.Fatalf("溢出文件未保留完整原文: err=%v", err)
	}
}

func TestAutoRiskUsesAnnotations(t *testing.T) {
	readOnly := true
	destructive := true
	if got := riskForApproval("auto", ToolInfo{}); got != tools.RiskNetwork {
		t.Fatalf("未标注工具不应免审批: %v", got)
	}
	if got := riskForApproval("auto", ToolInfo{Annotations: ToolAnnotations{ReadOnlyHint: &readOnly}}); got != tools.RiskRead {
		t.Fatalf("显式只读工具应直通: %v", got)
	}
	if got := riskForApproval("auto", ToolInfo{Annotations: ToolAnnotations{ReadOnlyHint: &readOnly, DestructiveHint: &destructive}}); got != tools.RiskWrite {
		t.Fatalf("破坏性工具必须审批: %v", got)
	}
}

func TestManagerDenyDoesNotConnect(t *testing.T) {
	m := NewManager(&config.MCPConfig{Servers: map[string]config.MCPServer{
		"disabled": {Transport: "stdio", Command: "definitely-not-a-real-binary-xyz", Approval: "deny"},
	}}, Options{})
	m.Start(context.Background())
	defer m.Close()
	if len(m.Tools()) != 0 || !strings.Contains(strings.Join(m.Status(), "\n"), StatusDisabled) {
		t.Fatalf("deny 不应连接或注册工具: %v", m.Status())
	}
}

func TestManagerResolvesCrossServerNames(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	serverCfg := func() config.MCPServer {
		return config.MCPServer{
			Transport: "stdio", Command: exe, Args: []string{"-test.run=TestMCPHelperProcess"},
			Env: map[string]string{"GO_WANT_MCP_HELPER": "1"}, Timeout: "5s", Approval: "ask",
		}
	}
	m := NewManager(&config.MCPConfig{Servers: map[string]config.MCPServer{
		"a b": serverCfg(), "a_b": serverCfg(),
	}}, Options{})
	m.Start(context.Background())
	defer m.Close()
	list := m.Tools()
	if len(list) != 2 {
		t.Fatalf("工具数 = %d", len(list))
	}
	names := map[string]bool{list[0].Name(): true, list[1].Name(): true}
	if !names["mcp__a_b__echo"] || !names["mcp__a_b__echo_2"] {
		t.Fatalf("跨 server 重名未解决: %v", names)
	}
}

func TestMergeEnvKeepsLocale(t *testing.T) {
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("LC_ALL", "C.UTF-8")
	env := strings.Join(mergeEnv(nil), "\n")
	if !strings.Contains(env, "LANG=C.UTF-8") || !strings.Contains(env, "LC_ALL=C.UTF-8") {
		t.Fatalf("locale 未透传: %s", env)
	}
}
