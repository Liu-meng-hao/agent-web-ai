//go:build !windows

package security

import (
	"os/exec"
	"syscall"
)

func prepare(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type pgGuard struct {
	pid int
}

func newGuard(cmd *exec.Cmd) processGuard {
	if cmd.Process == nil {
		return noopGuard{}
	}
	return pgGuard{pid: cmd.Process.Pid}
}

func (g pgGuard) Kill() {
	// 负 pid 表示整个进程组。
	_ = syscall.Kill(-g.pid, syscall.SIGKILL)
}

func (g pgGuard) Close() {}

type noopGuard struct{}

func (noopGuard) Kill()  {}
func (noopGuard) Close() {}
