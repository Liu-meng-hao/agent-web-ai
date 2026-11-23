package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"

	"github.com/spf13/cobra"

	"agent/internal/agent"
	"agent/internal/config"
	"agent/internal/llm"
	"agent/internal/session"
	"agent/internal/telemetry"
	"agent/internal/tui"
)

func chatCmd() *cobra.Command {
	var role, resume string
	var continueLast, noMCP, noWindow, windowChild bool

	cmd := &cobra.Command{
		Use:   "chat",
		Short: "进入终端界面（TUI）",
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			if !noWindow && !windowChild {
				opened, err := openChatWindow(cmd)
				if opened || err != nil {
					return err
				}
			}
			if windowChild {
				// 启动失败时保留窗口，否则用户还没来得及看错误窗口就关闭了。
				defer func() {
					if runErr == nil {
						return
					}
					fmt.Fprintln(os.Stderr, "错误："+runErr.Error())
					fmt.Fprintln(os.Stderr, "按 Enter 关闭窗口…")
					var answer string
					_, _ = fmt.Scanln(&answer)
				}()
			}
			if resume != "" && continueLast {
				return fmt.Errorf("--resume 与 --continue 不能同时使用")
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}

			events := make(chan agent.Event, 256)
			approver := tui.NewApprover()

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			// 告警既写日志也进 TUI：否则自动压缩、记忆降级这些事用户完全看不见。
			logPath := filepath.Join(cwd, "data", "logs", "agent.log")
			var bootNotices []string
			var noticeMu sync.Mutex
			collectNotices := true
			warn := func(msg string) {
				noticeMu.Lock()
				if collectNotices {
					bootNotices = append(bootNotices, msg)
				}
				noticeMu.Unlock()
				appendLog(logPath, msg)
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			b, err := buildAgent(ctx, cfg, role, approver, func(ev agent.Event) {
				events <- ev
			}, warn, noMCP)
			if err != nil {
				return err
			}
			defer b.Close()

			started := func() session.SessionStarted {
				return session.SessionStarted{
					Cwd:      cwd,
					Model:    b.model,
					Shell:    b.shell.String(),
					Approval: cfg.Agent.Approval,
				}
			}

			store, rec, isNew, err := openSession(cfg, resume, continueLast, started())
			if err != nil {
				return err
			}
			defer func() {
				telemetry.LogEvent(b.logPath, "session.closed", map[string]any{
					"session_id": store.Meta().ID, "model": b.model,
				})
				_ = store.Close()
			}()
			b.SetSessionID(store.Meta().ID)
			b.SeedUsage(store.Meta())
			telemetry.LogEvent(b.logPath, "session.started", map[string]any{
				"session_id": store.Meta().ID, "model": b.model, "resumed": !isNew,
			})

			var notices []string
			var history []llm.Message
			if !isNew {
				notices, history = applyRecovery(store, rec, b.agent)
			}
			noticeMu.Lock()
			notices = append(append([]string(nil), bootNotices...), notices...)
			collectNotices = false
			noticeMu.Unlock()
			b.agent.SetRecorder(store)

			newSession := func() (string, error) {
				ns, _, _, err := openSession(cfg, "", false, started())
				if err != nil {
					return "", err
				}
				_ = store.Close()
				store = ns
				b.agent.SetRecorder(ns)
				b.agent.Reset()
				b.SetSessionID(ns.Meta().ID)
				b.ResetUsage()
				return ns.Meta().ID, nil
			}

			listSessions := func() ([]string, error) {
				root, err := sessionRoot(cfg)
				if err != nil {
					return nil, err
				}
				metas, err := session.List(root)
				if err != nil {
					return nil, err
				}
				out := make([]string, 0, len(metas))
				for _, m := range metas {
					out = append(out, fmt.Sprintf("%s  %s  %s  %d 事件",
						short(m.ID, 8), truncateTitle(m.Title, 24),
						m.UpdatedAt.Format("01-02 15:04"), m.EventCount))
				}
				if len(out) == 0 {
					out = append(out, "(还没有会话)")
				}
				return out, nil
			}

			return tui.Run(tui.Options{
				Agent:        b.agent,
				Model:        b.model,
				Events:       events,
				Approver:     approver,
				SessionID:    store.Meta().ID,
				Notices:      notices,
				History:      history,
				NewSession:   newSession,
				ListSessions: listSessions,
				ListMemory: func() ([]string, error) {
					if b.mem == nil {
						return []string{"长期记忆未启用"}, nil
					}
					facts := b.mem.Active("")
					out := []string{fmt.Sprintf("长期记忆：%d 条活跃事实", len(facts))}
					for i, f := range facts {
						if i >= 8 {
							out = append(out, "…（用 agent memory list 查看全部）")
							break
						}
						line := fmt.Sprintf("[%s][%s] ", f.ID, f.Scope)
						if f.Key != "" {
							line += f.Key + " — "
						}
						out = append(out, line+f.Text)
					}
					return out, nil
				},
				RebuildMemory: func() (int, error) {
					if b.mem == nil {
						return 0, fmt.Errorf("长期记忆未启用")
					}
					if err := b.mem.Rebuild(); err != nil {
						return 0, err
					}
					return b.mem.Count(), nil
				},
				Compact: func(ctx context.Context) (string, error) {
					return b.agent.Compact(ctx)
				},
				MCPStatus:    b.MCPStatus,
				MCPReconnect: b.MCPReconnect,
				SkillList:    b.SkillList,
				SkillShow:    b.SkillShow,
				SkillReload:  b.SkillReload,
				PreviewInfo:  b.PreviewInfo,
				OpenPreview:  b.OpenPreview,
				Usage:        b.Usage(),
				LogTail:      b.LogTail,
			})
		},
	}

	cmd.Flags().StringVar(&role, "role", "main", "模型角色：main / summarizer / extractor")
	cmd.Flags().StringVar(&resume, "resume", "", "恢复指定会话（id 或前缀）")
	cmd.Flags().BoolVar(&continueLast, "continue", false, "恢复最近一个会话")
	cmd.Flags().BoolVar(&noMCP, "no-mcp", false, "跳过 MCP 配置与 server 连接")
	cmd.Flags().BoolVar(&noWindow, "no-window", false, "在当前终端运行，不弹出独立窗口")
	cmd.Flags().BoolVar(&windowChild, "window-child", false, "独立窗口内部启动标记")
	_ = cmd.Flags().MarkHidden("window-child")
	return cmd
}

var logMu sync.Mutex

// appendLog 追加一条 JSON 日志到 data/logs/agent.log。
func appendLog(path, msg string) {
	logMu.Lock()
	defer logMu.Unlock()
	telemetry.LogWarn(path, msg)
}

func short(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func truncateTitle(s string, n int) string {
	if s == "" {
		return "(未命名)"
	}
	return short(s, n)
}
