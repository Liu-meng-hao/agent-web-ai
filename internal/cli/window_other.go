//go:build !windows

package cli

import "github.com/spf13/cobra"

// 非 Windows 平台继续在当前终端运行，避免依赖具体桌面环境。
func openChatWindow(cmd *cobra.Command) (bool, error) {
	return false, nil
}
