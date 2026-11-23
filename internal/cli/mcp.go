package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/spf13/cobra"

	"agent/internal/config"
	"agent/internal/mcp"
)

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp [status|tools|reconnect <server|all>]",
		Short: "查看 MCP 状态/工具，或重连 server",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return nil
			}
			switch args[0] {
			case "status":
				if len(args) != 1 {
					return fmt.Errorf("usage: agent mcp status")
				}
			case "tools":
				if len(args) != 1 {
					return fmt.Errorf("usage: agent mcp tools")
				}
			case "reconnect":
				if len(args) != 2 || args[1] == "" {
					return fmt.Errorf("usage: agent mcp reconnect <server|all>")
				}
			default:
				return fmt.Errorf("未知 MCP 子命令 %q（可选 status / tools / reconnect）", args[0])
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			workspace := cfg.Agent.Workspace
			if workspace == "" {
				workspace = "workspace"
			}
			if !filepath.IsAbs(workspace) {
				workspace = filepath.Join(cwd, workspace)
			}
			path := filepath.Join(filepath.Dir(configPath), "mcp.yaml")
			mcpCfg, err := config.LoadMCP(path, map[string]string{"WORKSPACE": workspace})
			if err != nil {
				return err
			}
			if len(mcpCfg.Servers) == 0 {
				fmt.Println("未配置 MCP server：" + path)
				return nil
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			mgr := mcp.NewManager(mcpCfg, mcp.Options{
				ImageDir:    filepath.Join(cwd, "data", "artifacts", "_mcp"),
				OverflowDir: filepath.Join(cwd, "data", "logs", "mcp"),
				Warn:        func(msg string) { fmt.Fprintln(os.Stderr, "警告："+msg) },
			})
			defer mgr.Close()

			if len(args) == 2 && args[0] == "reconnect" {
				err := mgr.Reconnect(ctx, args[1])
				for _, line := range mgr.Status() {
					fmt.Println(line)
				}
				return err
			}

			mgr.Start(ctx)
			for _, line := range mgr.Status() {
				fmt.Println(line)
			}
			if len(args) == 1 && args[0] == "tools" {
				for _, t := range mgr.Tools() {
					fmt.Printf("  %s\t%s\n", t.Name(), t.Description())
				}
			}
			return nil
		},
	}
}
