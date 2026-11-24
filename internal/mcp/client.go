package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"agent/internal/config"
)

const (
	clientProtocolVersion = "2025-06-18"
	maxToolPages          = 100
	maxToolsPerServer     = 200
)

// ServerInfo 是 initialize 返回的服务端信息。
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolAnnotations 是 MCP 工具声明的安全注解。指针用于区分“未声明”和 false。
type ToolAnnotations struct {
	ReadOnlyHint    *bool `json:"readOnlyHint"`
	DestructiveHint *bool `json:"destructiveHint"`
}

// ToolInfo 是 tools/list 返回的一个工具。
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations ToolAnnotations `json:"annotations"`
}

// Content 是 tools/call 返回的一个内容块。
type Content struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	Data     string    `json:"data,omitempty"`
	MIMEType string    `json:"mimeType,omitempty"`
	Resource *Resource `json:"resource,omitempty"`
}

// Resource 是 type=resource 时的嵌套结构。
type Resource struct {
	URI      string `json:"uri"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
}

// CallResult 是 tools/call 的结果。
type CallResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError"`
}

// Client 是单个 MCP server 的会话。
type Client struct {
	name string
	cfg  config.MCPServer
	tr   transport

	nextID int64
	info   ServerInfo

	noticeMu sync.RWMutex
	onNotice func(string)

	warningMu sync.Mutex
	warnings  []string

	// ready 在首次 tools/list 完成后置位。部分 server（如 Playwright）
	// 会在握手阶段先发 list_changed，此时工具表本来就还没建立，告警只是噪音。
	ready atomic.Bool
	// listChangedSeen 用于吞掉连接后的第一次 list_changed：
	// Playwright 在浏览器启动后还会补发一次，同样不代表工具表真的变了。
	listChangedSeen atomic.Bool
}

// NewClient 建立传输并完成 initialize 握手。
func NewClient(ctx context.Context, name string, cfg config.MCPServer) (*Client, error) {
	var (
		tr  transport
		err error
	)
	switch cfg.Transport {
	case "stdio":
		tr, err = startStdio(cfg.Command, cfg.Args, cfg.Env)
	case "http", "streamable-http":
		tr = newHTTPTransport(cfg.URL, cfg.Headers, cfg.TimeoutDuration())
	default:
		return nil, fmt.Errorf("不支持的 MCP transport %q", cfg.Transport)
	}
	if err != nil {
		return nil, err
	}

	c := &Client{name: name, cfg: cfg, tr: tr}
	if nt, ok := tr.(notificationTransport); ok {
		nt.SetNotificationHandler(c.handleNotification)
	}
	if err := c.Initialize(ctx); err != nil {
		_ = tr.Close()
		return nil, err
	}
	return c, nil
}

// Name 返回 server 名。
func (c *Client) Name() string { return c.name }

// MarkReady 标记首次工具发现完成，之后的 list_changed 才需要提示。
func (c *Client) MarkReady() { c.ready.Store(true) }

// SetNoticeHandler 设置异步通知接收器，可在启动后立即调用。
func (c *Client) SetNoticeHandler(handler func(string)) {
	c.noticeMu.Lock()
	c.onNotice = handler
	c.noticeMu.Unlock()
}

// Initialize 完成 MCP 握手。
func (c *Client) Initialize(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": clientProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "terminal-agent",
			"version": "0.1.0",
		},
	}
	var res struct {
		ProtocolVersion string     `json:"protocolVersion"`
		ServerInfo      ServerInfo `json:"serverInfo"`
	}
	if err := c.call(ctx, "initialize", params, &res); err != nil {
		return err
	}
	if strings.TrimSpace(res.ProtocolVersion) == "" {
		return fmt.Errorf("MCP initialize 响应缺少 protocolVersion")
	}
	c.info = res.ServerInfo
	if pt, ok := c.tr.(protocolTransport); ok {
		pt.SetProtocolVersion(res.ProtocolVersion)
	}
	// initialized 是通知，没有 id，也不能等响应。
	return c.tr.Notify(ctx, &rpcRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
		Params:  map[string]any{},
	})
}

// ListTools 拉取工具，限制页数、重复游标与单 server 工具总量。
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	out := make([]ToolInfo, 0)
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < maxToolPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res struct {
			Tools      []ToolInfo `json:"tools"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := c.call(ctx, "tools/list", params, &res); err != nil {
			return nil, err
		}

		remaining := maxToolsPerServer - len(out)
		if len(res.Tools) > remaining {
			out = append(out, res.Tools[:remaining]...)
			c.warn(fmt.Sprintf("工具超过 %d 个上限，仅注册前 %d 个", maxToolsPerServer, maxToolsPerServer))
		} else {
			out = append(out, res.Tools...)
		}
		if len(out) >= maxToolsPerServer {
			return out, nil
		}
		if res.NextCursor == "" {
			return out, nil
		}
		if seen[res.NextCursor] || res.NextCursor == cursor {
			c.warn("tools/list 返回重复游标，已停止翻页")
			return out, nil
		}
		seen[res.NextCursor] = true
		cursor = res.NextCursor
	}
	c.warn(fmt.Sprintf("tools/list 超过 %d 页上限，已停止翻页", maxToolPages))
	return out, nil
}

// Warnings 返回并清空工具发现期间产生的告警。
func (c *Client) Warnings() []string {
	c.warningMu.Lock()
	defer c.warningMu.Unlock()
	out := append([]string(nil), c.warnings...)
	c.warnings = nil
	return out
}

func (c *Client) warn(msg string) {
	c.warningMu.Lock()
	c.warnings = append(c.warnings, msg)
	c.warningMu.Unlock()
}

// CallTool 调用远端工具；每次调用都有独立 deadline，避免卡死整个 turn。
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallResult, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.CallTimeoutDuration())
	defer cancel()
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	params := map[string]any{
		"name":      name,
		"arguments": args,
	}
	var res CallResult
	if err := c.call(ctx, "tools/call", params, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Close 关闭传输。
func (c *Client) Close() error { return c.tr.Close() }

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	id := atomic.AddInt64(&c.nextID, 1)
	resp, err := c.tr.RoundTrip(ctx, &rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return err
	}
	if err := responseError(method, resp.Error); err != nil {
		return err
	}
	if out != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("解析 %s 结果失败: %w", method, err)
		}
	}
	return nil
}

func (c *Client) handleNotification(req *rpcRequest) {
	c.noticeMu.RLock()
	handler := c.onNotice
	c.noticeMu.RUnlock()
	if handler == nil {
		return
	}
	switch req.Method {
	case "notifications/message":
		raw, _ := json.Marshal(req.Params)
		handler(fmt.Sprintf("MCP server %q 消息：%s", c.name, raw))
	case "notifications/tools/list_changed":
		// 启动阶段与首次 list_changed 属于正常握手，不提示。
		if !c.ready.Load() || !c.listChangedSeen.Swap(true) {
			return
		}
		handler(fmt.Sprintf("MCP server %q 的工具列表已变化；当前工具表不会自动刷新", c.name))
	}
}
