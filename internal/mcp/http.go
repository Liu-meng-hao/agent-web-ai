package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// httpTransport 实现 MCP streamable HTTP：POST JSON-RPC，响应可能是
// application/json 或 text/event-stream。
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu              sync.Mutex
	sessionID       string
	protocolVersion string
	onNotification  func(*rpcRequest)
}

func newHTTPTransport(url string, headers map[string]string, _ time.Duration) *httpTransport {
	return &httpTransport{
		url:     url,
		headers: headers,
		client:  &http.Client{},
	}
}

func (t *httpTransport) SetProtocolVersion(version string) {
	t.mu.Lock()
	t.protocolVersion = version
	t.mu.Unlock()
}

func (t *httpTransport) SetNotificationHandler(handler func(*rpcRequest)) {
	t.mu.Lock()
	t.onNotification = handler
	t.mu.Unlock()
}

func (t *httpTransport) RoundTrip(ctx context.Context, req *rpcRequest) (*rpcResponse, error) {
	resp, body, err := t.post(ctx, req)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	t.captureSession(resp)
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") {
		return readSSEResponseWithHandler(body, req.ID, t.handleNotification)
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxRPCMessageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxRPCMessageBytes {
		return nil, fmt.Errorf("单条 MCP HTTP 响应超过 %d MiB", maxRPCMessageBytes>>20)
	}
	var out rpcResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 MCP 响应失败: %w（body: %s）", err, snippet(raw))
	}
	return &out, nil
}

func (t *httpTransport) Notify(ctx context.Context, req *rpcRequest) error {
	resp, body, err := t.post(ctx, req)
	if err != nil {
		return err
	}
	defer body.Close()
	t.captureSession(resp)
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("MCP 通知失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (t *httpTransport) post(ctx context.Context, req *rpcRequest) (*http.Response, io.ReadCloser, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range t.headers {
		httpReq.Header.Set(k, v)
	}
	t.mu.Lock()
	sid := t.sessionID
	version := t.protocolVersion
	t.mu.Unlock()
	if sid != "" {
		httpReq.Header.Set("Mcp-Session-Id", sid)
	}
	if version != "" {
		httpReq.Header.Set("MCP-Protocol-Version", version)
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, nil, fmt.Errorf("请求 MCP server 失败: %w", err)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, nil, fmt.Errorf("MCP server 返回 HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	return resp, resp.Body, nil
}

func (t *httpTransport) captureSession(resp *http.Response) {
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}
}

func (t *httpTransport) handleNotification(req *rpcRequest) {
	t.mu.Lock()
	handler := t.onNotification
	t.mu.Unlock()
	if handler != nil {
		handler(req)
	}
}

// Close 按 streamable HTTP 约定终止服务端会话；不支持 DELETE 的 server 可安全忽略。
func (t *httpTransport) Close() error {
	t.mu.Lock()
	sid := t.sessionID
	version := t.protocolVersion
	t.sessionID = ""
	t.mu.Unlock()
	if sid == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil)
	if err != nil {
		return err
	}
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Mcp-Session-Id", sid)
	if version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("终止 MCP HTTP 会话失败: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		return fmt.Errorf("终止 MCP HTTP 会话失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

// readSSEResponse 从事件流里找到与 req.ID 匹配的 JSON-RPC 响应。
func readSSEResponse(r io.Reader, id int64) (*rpcResponse, error) {
	return readSSEResponseWithHandler(r, id, nil)
}

func readSSEResponseWithHandler(r io.Reader, id int64, onNotification func(*rpcRequest)) (*rpcResponse, error) {
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			if len(buf) > maxRPCMessageBytes {
				return nil, fmt.Errorf("MCP 事件流缓冲超过 %d MiB", maxRPCMessageBytes>>20)
			}
			for {
				i := bytes.IndexByte(buf, '\n')
				if i < 0 {
					break
				}
				line := strings.TrimSpace(string(buf[:i]))
				buf = buf[i+1:]
				data, ok := strings.CutPrefix(line, "data:")
				if !ok {
					continue
				}
				data = strings.TrimSpace(data)
				if data == "" {
					continue
				}
				var resp rpcResponse
				if json.Unmarshal([]byte(data), &resp) != nil {
					continue
				}
				if resp.ID == id {
					return &resp, nil
				}
				var note rpcRequest
				if onNotification != nil && resp.ID == 0 && json.Unmarshal([]byte(data), &note) == nil && note.Method != "" {
					onNotification(&note)
				}
			}
		}
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				line := strings.TrimSpace(string(buf))
				if data, ok := strings.CutPrefix(line, "data:"); ok {
					var resp rpcResponse
					if json.Unmarshal([]byte(strings.TrimSpace(data)), &resp) == nil && resp.ID == id {
						return &resp, nil
					}
				}
			}
			return nil, fmt.Errorf("MCP 事件流结束但未收到响应: %w", err)
		}
	}
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		end := 300
		for end > 0 && !utf8.ValidString(s[:end]) {
			end--
		}
		return s[:end] + "…"
	}
	return s
}
