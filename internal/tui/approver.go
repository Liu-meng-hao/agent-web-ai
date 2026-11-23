package tui

import (
	"context"
	"sync"

	"agent/internal/agent"
)

// Approver 是 TUI 的审批实现：把请求送到界面，等用户按 y/a/n。
// 「本会话允许」由它自己记住，loop 不感知。
type Approver struct {
	requests  chan agent.ApprovalRequest
	decisions chan agent.Decision

	mu      sync.Mutex
	allowed map[string]bool
}

// NewApprover 构造审批人。
func NewApprover() *Approver {
	return &Approver{
		requests:  make(chan agent.ApprovalRequest, 4),
		decisions: make(chan agent.Decision, 4),
		allowed:   map[string]bool{},
	}
}

// Requests 返回审批请求通道（TUI 消费）。
func (a *Approver) Requests() <-chan agent.ApprovalRequest { return a.requests }

// Approve 阻塞等待用户决定。
func (a *Approver) Approve(ctx context.Context, req agent.ApprovalRequest) (agent.Decision, error) {
	a.mu.Lock()
	sessionAllowed := a.allowed[req.Tool]
	a.mu.Unlock()
	if sessionAllowed {
		return agent.AllowSession, nil
	}

	select {
	case a.requests <- req:
	case <-ctx.Done():
		return agent.Deny, ctx.Err()
	}

	select {
	case d := <-a.decisions:
		if d == agent.AllowSession {
			a.mu.Lock()
			a.allowed[req.Tool] = true
			a.mu.Unlock()
		}
		return d, nil
	case <-ctx.Done():
		return agent.Deny, ctx.Err()
	}
}

// Resolve 由界面调用，提交用户的决定。
func (a *Approver) Resolve(d agent.Decision) {
	select {
	case a.decisions <- d:
	default:
	}
}
