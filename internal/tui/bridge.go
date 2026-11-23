package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"agent/internal/agent"
)

type eventMsg struct{ ev agent.Event }
type approvalMsg struct{ req agent.ApprovalRequest }
type turnDoneMsg struct{}
type turnErrMsg struct{ err error }
type compactMsg struct {
	text string
	err  error
}
type mcpReconnectMsg struct {
	name string
	err  error
}
type skillReloadMsg struct {
	text string
	err  error
}
type previewMsg struct {
	url string
	err error
}

// waitEvent 阻塞等一帧 agent 事件。返回的 Msg 会被 Update 处理，
// 处理完再重新挂起一次（单消费者模型）。
func waitEvent(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return turnDoneMsg{}
		}
		return eventMsg{ev: ev}
	}
}

func waitApproval(ch <-chan agent.ApprovalRequest) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-ch
		if !ok {
			return nil
		}
		return approvalMsg{req: req}
	}
}
