package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"agent/internal/security"
)

type shellInput struct {
	Command    string `json:"command" jsonschema:"description=要执行的完整命令；工作目录默认为工作区，可用相对路径"`
	Cwd        string `json:"cwd,omitempty" jsonschema:"description=工作目录，相对或绝对，必须落在允许目录内"`
	TimeoutSec int    `json:"timeout_sec,omitempty" jsonschema:"description=超时秒数，默认 120，最大 600"`
}

// NewShell 返回 shell 工具。
func NewShell() Tool {
	return Define(
		"shell",
		"在受控 shell 中执行命令，适合列目录、跑测试、构建、git 操作。命令会继承工作目录，超时会杀掉整棵进程树；可能需要用户审批。",
		RiskExec,
		func(ctx context.Context, in shellInput, tc *CallContext) (Result, error) {
			if tc == nil || tc.Runner == nil {
				return Result{}, fmt.Errorf("shell 未配置执行器")
			}
			if strings.TrimSpace(in.Command) == "" {
				return Result{}, fmt.Errorf("command 不能为空")
			}

			cwd := tc.Cwd
			if strings.TrimSpace(in.Cwd) != "" {
				p := in.Cwd
				if !filepath.IsAbs(p) {
					p = filepath.Join(tc.Cwd, p)
				}
				real, err := tc.Jail.CheckDir(p)
				if err != nil {
					return Result{}, err
				}
				cwd = real
			}

			timeout := time.Duration(in.TimeoutSec) * time.Second
			if timeout <= 0 {
				timeout = 120 * time.Second
			}
			if timeout > 600*time.Second {
				timeout = 600 * time.Second
			}

			res, err := tc.Runner.Run(ctx, security.ExecRequest{
				Command: in.Command,
				Cwd:     cwd,
				Timeout: timeout,
			})
			if err != nil {
				return Result{}, err
			}

			var b strings.Builder
			fmt.Fprintf(&b, "exit_code: %d\n", res.ExitCode)
			fmt.Fprintf(&b, "duration: %s\n", res.Duration.Round(time.Millisecond))
			if res.TimedOut {
				b.WriteString("timed_out: true（命令已被终止，可能已产生副作用）\n")
			}
			if s := strings.TrimRight(res.Stdout, "\r\n"); s != "" {
				fmt.Fprintf(&b, "stdout:\n%s\n", s)
			}
			if s := strings.TrimRight(res.Stderr, "\r\n"); s != "" {
				fmt.Fprintf(&b, "stderr:\n%s\n", s)
			}
			if b.Len() == 0 {
				b.WriteString("(无输出)")
			}

			full := b.String()
			content := truncateMiddle(full, 8000)
			display := fmt.Sprintf("exit %d · %s", res.ExitCode, res.Duration.Round(time.Millisecond))
			if res.TimedOut {
				display = fmt.Sprintf("超时终止 · %s", res.Duration.Round(time.Millisecond))
			}
			return Result{
				Content:   content,
				Display:   display,
				Truncated: res.Truncated || len([]rune(content)) < len([]rune(full)),
			}, nil
		},
	)
}
