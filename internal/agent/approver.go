package agent

import (
	"context"
	"encoding/json"

	"agent/internal/tools"
)

// Decision 是审批结果。
type Decision int

const (
	Deny Decision = iota
	AllowOnce
	AllowSession
)

// String 返回可读名称。
func (d Decision) String() string {
	switch d {
	case AllowOnce:
		return "allow_once"
	case AllowSession:
		return "allow_session"
	default:
		return "deny"
	}
}

// ApprovalRequest 是一次待审批的调用。
type ApprovalRequest struct {
	Tool    string
	Args    json.RawMessage
	Risk    tools.Risk
	Preview string
}

// Approver 决定是否放行一次工具调用。
//
// 实现方（TUI / headless）自己维护「本会话允许」的记忆，loop 不关心。
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// DenyAll 是非交互模式下的默认审批人：一律拒绝危险操作。
type DenyAll struct{}

// Approve 永远拒绝。
func (DenyAll) Approve(ctx context.Context, req ApprovalRequest) (Decision, error) {
	return Deny, nil
}

// AllowAll 用于 --yes：放行一切（仍受 readonly 模式的策略限制）。
type AllowAll struct{}

// Approve 永远放行。
func (AllowAll) Approve(ctx context.Context, req ApprovalRequest) (Decision, error) {
	return AllowOnce, nil
}
