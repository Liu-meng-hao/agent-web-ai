package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"

	"github.com/spf13/cobra"

	"agent/internal/agent"
	"agent/internal/config"
	"agent/internal/session"
)

func runCmd() *cobra.Command {
	var role, system string
	var yes, noSession, noMCP bool
	var resume string
	var continueLast bool

	cmd := &cobra.Command{
		Use:   "run [prompt]",
		Short: "一次性执行并把流式结果打印到 stdout（适合脚本与冒烟测试）",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}

			// 非交互模式默认拒绝危险操作；--yes 显式放行。
			var approver agent.Approver = agent.DenyAll{}
			if yes {
				approver = agent.AllowAll{}
			}

			emit := func(ev agent.Event) {
				switch ev.Type {
				case agent.EventText:
					fmt.Print(ev.Text)
				case agent.EventToolRequested:
					fmt.Fprintf(os.Stderr, "\n→ %s  %s\n", ev.Call.Name, previewText(ev.Call.Args))
				case agent.EventToolResult:
					if ev.Decision == agent.Deny {
						fmt.Fprintln(os.Stderr, "  ⊘ 已拒绝")
						return
					}
					display := "完成"
					if ev.Result != nil && ev.Result.Display != "" {
						display = ev.Result.Display
					}
					fmt.Fprintf(os.Stderr, "  ← %s\n", display)
				case agent.EventNotice:
					fmt.Fprintln(os.Stderr, "提示："+ev.Text)
				case agent.EventTurnFinished:
					fmt.Println()
				}
			}
			// 启动期告警直接打出来；运行期告警落日志，避免与 EventNotice 重复。
			var booted atomic.Bool
			warn := func(msg string) {
				if !booted.Load() {
					fmt.Fprintln(os.Stderr, "警告："+msg)
					return
				}
				// 运行期 agent 告警已由 EventNotice 展示；这里同时落日志，
				// 也让 MCP 的异步通知与重连结果不会静默丢失。
				appendLog("data/logs/agent.log", msg)
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			b, err := buildAgent(ctx, cfg, role, approver, emit, warn, noMCP)
			booted.Store(true)
			if err != nil {
				return err
			}
			defer b.Close()
			if strings.TrimSpace(system) != "" {
				b.agent.SetSystem(system)
			}

			cwd, _ := os.Getwd()
			started := session.SessionStarted{
				Cwd:      cwd,
				Model:    b.model,
				Shell:    b.shell.String(),
				Approval: cfg.Agent.Approval,
			}

			var store *session.Store
			if !noSession {
				var rec *session.Recovery
				var isNew bool
				store, rec, isNew, err = openSession(cfg, resume, continueLast, started)
				if err != nil {
					return err
				}
				defer store.Close()
				b.SetSessionID(store.Meta().ID)
				if !isNew {
					notices, _ := applyRecovery(store, rec, b.agent)
					for _, n := range notices {
						fmt.Fprintln(os.Stderr, "恢复："+n)
					}
				}
				b.agent.SetRecorder(store)
			}

			err = b.agent.RunTurn(ctx, strings.Join(args, " "))
			if errors.Is(err, agent.ErrMaxSteps) {
				fmt.Fprintln(os.Stderr, "警告：达到最大步数，已终止本轮")
				return nil
			}
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}

	cmd.Flags().StringVar(&role, "role", "main", "模型角色：main / summarizer / extractor")
	cmd.Flags().StringVar(&system, "system", "", "覆盖默认 system prompt")
	cmd.Flags().BoolVar(&yes, "yes", false, "自动批准所有需要审批的工具调用（危险）")
	cmd.Flags().BoolVar(&noSession, "no-session", false, "不记录会话事件")
	cmd.Flags().BoolVar(&noMCP, "no-mcp", false, "跳过 MCP 配置与 server 连接")
	cmd.Flags().StringVar(&resume, "resume", "", "恢复指定会话（id 或前缀）")
	cmd.Flags().BoolVar(&continueLast, "continue", false, "恢复最近一个会话")
	return cmd
}

func previewText(args []byte) string {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err == nil {
		for _, k := range []string{"command", "path", "url"} {
			if v, ok := m[k]; ok {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return s
				}
			}
		}
	}
	s := string(args)
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}
