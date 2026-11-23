// Package security 负责工具执行的安全边界：风险分级、路径 jail、受限执行器。
package security

import "fmt"

// Risk 是工具的风险等级，决定是否需要人工审批。
type Risk int

const (
	RiskRead Risk = iota
	RiskWrite
	RiskExec
	RiskNetwork
)

// String 返回可读名称（用于 TUI 与日志）。
func (r Risk) String() string {
	switch r {
	case RiskRead:
		return "read"
	case RiskWrite:
		return "write"
	case RiskExec:
		return "exec"
	case RiskNetwork:
		return "network"
	default:
		return "unknown"
	}
}

// Mode 是审批模式。
type Mode string

const (
	ModeReadOnly Mode = "readonly"
	ModeStandard Mode = "standard"
	ModeYOLO     Mode = "yolo"
)

// Action 是策略对一次调用的判定。
type Action int

const (
	Allow Action = iota
	Ask
	Deny
)

// String 返回可读名称。
func (a Action) String() string {
	switch a {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	case Deny:
		return "deny"
	default:
		return "unknown"
	}
}

// Policy 是「风险 × 模式」的判定矩阵。
type Policy struct {
	Mode Mode
}

// NewPolicy 按配置字符串构造策略，未知值回退 standard。
func NewPolicy(mode string) Policy {
	switch Mode(mode) {
	case ModeReadOnly, ModeStandard, ModeYOLO:
		return Policy{Mode: Mode(mode)}
	default:
		if mode == "" {
			return Policy{Mode: ModeStandard}
		}
		return Policy{Mode: ModeStandard}
	}
}

// Decide 返回该风险在当前模式下的处理方式。
func (p Policy) Decide(risk Risk) Action {
	if p.Mode == ModeYOLO {
		return Allow
	}
	if p.Mode == ModeReadOnly {
		if risk == RiskRead {
			return Allow
		}
		return Deny
	}
	// standard：读直接放行，其余一律人工确认。
	if risk == RiskRead {
		return Allow
	}
	return Ask
}

// ParseMode 校验并归一化审批模式。
func ParseMode(mode string) (Mode, error) {
	switch Mode(mode) {
	case ModeReadOnly, ModeStandard, ModeYOLO:
		return Mode(mode), nil
	}
	return "", fmt.Errorf("无效的审批模式 %q（可选 readonly / standard / yolo）", mode)
}
