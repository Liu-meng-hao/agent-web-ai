//go:build windows

package security

import (
	"os/exec"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

func prepare(cmd *exec.Cmd) {}

// jobGuard 用 Job Object 管住整棵进程树：子进程再 fork 也逃不出去，
// 超时 TerminateJobObject 一次性收掉；KILL_ON_JOB_CLOSE 保证 agent 崩溃时不留孤儿。
type jobGuard struct {
	job windows.Handle
}

func newGuard(cmd *exec.Cmd) processGuard {
	if cmd.Process == nil {
		return noopGuard{}
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return taskkillGuard{pid: cmd.Process.Pid}
	}

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return taskkillGuard{pid: cmd.Process.Pid}
	}

	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return taskkillGuard{pid: cmd.Process.Pid}
	}
	defer windows.CloseHandle(h)

	// 嵌套 job（agent 自身已在 job 里）会返回 ERROR_ACCESS_DENIED，回退 taskkill。
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		windows.CloseHandle(job)
		return taskkillGuard{pid: cmd.Process.Pid}
	}
	return &jobGuard{job: job}
}

func (g *jobGuard) Kill() {
	_ = windows.TerminateJobObject(g.job, 1)
}

func (g *jobGuard) Close() {
	_ = windows.CloseHandle(g.job)
}

// taskkillGuard 是 Job Object 不可用时的兜底。
type taskkillGuard struct {
	pid int
}

func (g taskkillGuard) Kill() {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(g.pid)).Run()
}

func (g taskkillGuard) Close() {}

type noopGuard struct{}

func (noopGuard) Kill()  {}
func (noopGuard) Close() {}
