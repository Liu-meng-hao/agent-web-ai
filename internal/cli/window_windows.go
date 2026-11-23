//go:build windows

package cli

import (
	"fmt"
	"os"
	"unsafe"

	"github.com/spf13/cobra"
	"golang.org/x/sys/windows"
)

// openChatWindow 用新控制台运行当前二进制。父进程等待子进程结束，
// 这样通过 go run 启动时，临时可执行文件不会在子进程启动前被清理。
func openChatWindow(cmd *cobra.Command) (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("定位 agent 可执行文件失败: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return false, err
	}

	args := append([]string{exe}, os.Args[1:]...)
	if cmd.Name() != "chat" {
		args = append(args, "chat")
	}
	args = append(args, "--window-child")

	// 不能用 os/exec 的默认句柄：nil Stdin/Stdout 会接到 NUL，
	// Bubble Tea 因而看不到新控制台，只显示一块空白的 Alt Screen。
	// 原生 CreateProcess 不设置 STARTF_USESTDHANDLES，子进程才会使用新控制台。
	commandLine, err := windows.UTF16FromString(windows.ComposeCommandLine(args))
	if err != nil {
		return false, fmt.Errorf("构造独立窗口启动参数失败: %w", err)
	}
	dir, err := windows.UTF16PtrFromString(cwd)
	if err != nil {
		return false, fmt.Errorf("独立窗口工作目录无效: %w", err)
	}
	si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(
		nil,
		&commandLine[0],
		nil,
		nil,
		false,
		windows.CREATE_NEW_CONSOLE,
		nil,
		dir,
		&si,
		&pi,
	); err != nil {
		return false, fmt.Errorf("打开独立终端窗口失败: %w", err)
	}
	_ = windows.CloseHandle(pi.Thread)
	_, _ = windows.WaitForSingleObject(pi.Process, windows.INFINITE)
	_ = windows.CloseHandle(pi.Process)
	return true, nil
}
