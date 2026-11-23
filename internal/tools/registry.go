// Package tools 提供工具抽象、注册表与内置工具。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"agent/internal/llm"
	"agent/internal/security"
)

// Risk 复用 security 的风险分级。
type Risk = security.Risk

const (
	RiskRead    = security.RiskRead
	RiskWrite   = security.RiskWrite
	RiskExec    = security.RiskExec
	RiskNetwork = security.RiskNetwork
)

// Result 是一次工具调用的结果。
//
// Content 回灌给模型；Display 给 TUI 渲染（为空时用 Content）。
// 两者分离可以避免为了 TUI 好看而污染模型上下文。
type Result struct {
	Content    string
	Display    string
	Truncated  bool
	Structured json.RawMessage
}

// CallContext 携带工具执行所需的运行时依赖。
type CallContext struct {
	Cwd    string
	Jail   *security.Jail
	Runner *security.Runner
	// SessionID 返回当前会话 id，供 memory 工具标注 session 作用域；可为 nil。
	SessionID func() string
}

// Tool 是所有工具的统一接口。
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Risk() Risk
	Execute(ctx context.Context, args json.RawMessage, tc *CallContext) (Result, error)
}

// AutoApprover 是工具的可选能力：在 standard 模式下无需人工审批。
// 仅用于写入范围完全受控、不会触碰工具目录之外的工具（如 artifact_write）。
type AutoApprover interface {
	AutoApprove() bool
}

// Registry 是工具注册表。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry 构造空注册表。
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register 注册工具，重名返回错误。
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return fmt.Errorf("tools: 不能注册 nil")
	}
	name := t.Name()
	if name == "" {
		return fmt.Errorf("tools: 工具名不能为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; ok {
		return fmt.Errorf("tools: 工具 %q 重复注册", name)
	}
	r.tools[name] = t
	return nil
}

// Unregister 移除工具；不存在时忽略，供技能热重载替换旧工具。
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	delete(r.tools, name)
	r.mu.Unlock()
}

// Get 按名取工具。
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Schemas 返回按名字排序的工具声明。
//
// 顺序必须稳定：map 遍历是随机的，随机顺序会让每轮 prompt 前缀抖动，
// 直接破坏服务端的 prefix cache。
func (r *Registry) Schemas() []llm.ToolSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]llm.ToolSchema, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, llm.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names 返回排序后的工具名。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Len 返回工具数量。
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}
