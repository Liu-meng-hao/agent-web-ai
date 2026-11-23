// Package cli 是命令行入口：子命令注册、依赖装配、生命周期。
package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

var configPath string

// Execute 解析参数并执行子命令。
func Execute() error {
	root := &cobra.Command{
		Use:           "agent",
		Short:         "终端 AI agent：长记忆 + 摘要 + MCP + Skill + Shell + 页面生成",
		Long:          "单二进制终端 agent：终端对话与审批、长期记忆、上下文摘要、MCP、Skill、受控 shell，以及可预览的单页 HTML 生成。",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&configPath, "config", "config/agent.yaml", "配置文件路径")
	chat := chatCmd()
	root.AddCommand(runCmd(), chat, sessionsCmd(), replayCmd(), memoryCmd(), mcpCmd(), skillCmd(), previewCmd(), doctorCmd(), versionCmd())
	// 不带子命令时直接进入对话，双击 Windows 可执行文件也能启动 TUI。
	root.RunE = chat.RunE
	return root.Execute()
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "打印版本信息",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("agent dev (%s/%s)\n", runtime.GOOS, runtime.GOARCH)
		},
	}
}
