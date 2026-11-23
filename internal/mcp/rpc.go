// Package mcp 实现一个最小可用的 MCP 客户端：JSON-RPC 2.0 over stdio /
// streamable HTTP，并把远端工具适配成 tools.Tool。
//
// 不依赖官方 SDK：它需要额外拉取 oauth2/tools 等依赖，而这里只需要
// initialize / tools.list / tools.call 三个方法，内置实现更符合
// “单二进制、无外部服务依赖”的约束。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type transport interface {
	RoundTrip(ctx context.Context, req *rpcRequest) (*rpcResponse, error)
	Notify(ctx context.Context, req *rpcRequest) error
	Close() error
}

type notificationTransport interface {
	SetNotificationHandler(func(*rpcRequest))
}

type protocolTransport interface {
	SetProtocolVersion(string)
}

func responseError(method string, e *rpcError) error {
	if e == nil {
		return nil
	}
	return fmt.Errorf("MCP %s 失败: %s (code %d)", method, e.Message, e.Code)
}
