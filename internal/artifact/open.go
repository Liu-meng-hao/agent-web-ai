package artifact

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OpenBrowser 用系统默认浏览器打开 URL，不阻塞当前进程。
func OpenBrowser(target string) error {
	if target == "" {
		return fmt.Errorf("没有可打开的预览地址")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("打开浏览器失败: %w", err)
	}
	// 不 Wait：浏览器通常是长驻进程，等它会卡住调用方。
	go func() { _ = cmd.Wait() }()
	return nil
}
