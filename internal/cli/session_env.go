package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"agent/internal/agent"
	"agent/internal/config"
	"agent/internal/llm"
	"agent/internal/session"
)

// openSession 按 --resume / --continue 打开已有会话，否则新建。
// 返回的 recovery 对新会话是空结果。
func openSession(cfg *config.Config, resume string, continueLast bool, started session.SessionStarted) (*session.Store, *session.Recovery, bool, error) {
	root, err := sessionRoot(cfg)
	if err != nil {
		return nil, nil, false, err
	}

	if resume != "" || continueLast {
		var dir string
		if continueLast {
			metas, err := session.List(root)
			if err != nil {
				return nil, nil, false, err
			}
			if len(metas) > 0 {
				dir, err = session.Find(root, metas[0].ID)
				if err != nil {
					return nil, nil, false, err
				}
			}
		} else {
			dir, err = session.Find(root, resume)
			if err != nil {
				return nil, nil, false, err
			}
		}

		if dir != "" {
			rec, err := session.Recover(dir)
			if err != nil {
				return nil, nil, false, err
			}
			store, err := session.Open(dir)
			if err != nil {
				if errors.Is(err, session.ErrLocked) {
					return nil, nil, false, fmt.Errorf("会话 %s 正在被另一个进程使用", dir)
				}
				return nil, nil, false, err
			}
			return store, rec, false, nil
		}
		if !continueLast {
			return nil, nil, false, fmt.Errorf("找不到会话 %q", resume)
		}
	}

	store, err := session.Create(root, session.Meta{
		Cwd:    started.Cwd,
		Model:  started.Model,
		Shell:  started.Shell,
		Status: "active",
	})
	if err != nil {
		return nil, nil, false, err
	}
	if ev, err := session.New(session.TypeSessionStarted, started); err == nil {
		_ = store.Append(ev)
	}
	return store, &session.Recovery{}, true, nil
}

func sessionRoot(cfg *config.Config) (string, error) {
	root := cfg.Session.Root
	if root == "" {
		root = "data/sessions"
	}
	if !filepath.IsAbs(root) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		root = filepath.Join(cwd, root)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("创建会话目录失败: %w", err)
	}
	return root, nil
}

// applyRecovery 写恢复事件，并把历史消息灌回对话。
func applyRecovery(store *session.Store, rec *session.Recovery, a *agent.Agent) ([]string, []llm.Message) {
	if rec == nil {
		return nil, nil
	}
	var notices []string

	if len(rec.Interrupted) > 0 || rec.TruncatedBytes > 0 || len(rec.Warnings) > 0 {
		ev, err := session.New(session.TypeSessionRecovered, session.SessionRecovered{
			InterruptedCalls: rec.Interrupted,
			TruncatedBytes:   rec.TruncatedBytes,
		})
		if err == nil {
			_ = store.Append(ev)
		}
		_ = store.SetStatus("recovered")
	}
	if rec.TruncatedBytes > 0 {
		notices = append(notices, fmt.Sprintf("上次异常退出，已丢弃 %d 字节不完整事件", rec.TruncatedBytes))
	}
	notices = append(notices, rec.Warnings...)
	if len(rec.Interrupted) > 0 {
		notices = append(notices, fmt.Sprintf("检测到 %d 个未完成的工具调用，已标记为“可能已产生副作用”", len(rec.Interrupted)))
	}

	// 中断的调用必须落盘成正式的 tool.result，而不是只在内存里补一条合成消息：
	// 不落盘会导致下次恢复时 tool_calls 后面缺结果，且每恢复一次就多一条。
	extra := make([]session.Event, 0, len(rec.Interrupted))
	if len(rec.Interrupted) > 0 {
		names := map[string]string{}
		for _, e := range rec.Events {
			if e.Type == session.TypeToolCall {
				var p session.ToolCall
				if e.Decode(&p) == nil && p.Name != "" {
					names[p.CallID] = p.Name
				}
			}
		}
		for _, id := range rec.Interrupted {
			ev, err := session.New(session.TypeToolResult, session.ToolResult{
				CallID:  id,
				Name:    names[id],
				Status:  "interrupted",
				Content: "该工具调用在上次异常退出时未完成，命令可能已产生副作用；请先确认当前状态，再决定是否重试。",
			})
			if err != nil {
				continue
			}
			if err := store.Append(ev); err != nil {
				notices = append(notices, fmt.Sprintf("写入中断结果失败: %v", err))
				continue
			}
			extra = append(extra, ev)
		}
	}

	all := append(append([]session.Event{}, rec.Events...), extra...)
	msgs, summary, covered := session.BuildRestore(all)
	a.Restore(msgs)
	if summary != "" {
		a.RestoreSummary(summary, covered)
	}
	return notices, msgs
}
