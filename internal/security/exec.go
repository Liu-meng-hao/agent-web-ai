package security

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ExecRequest 是一次命令执行请求。
type ExecRequest struct {
	Command string
	Cwd     string
	Timeout time.Duration
	Env     map[string]string
}

// ExecResult 是命令执行结果。
type ExecResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Duration  time.Duration
	TimedOut  bool
	Truncated bool
}

// processGuard 负责终止整棵进程树。
type processGuard interface {
	Kill()
	Close()
}

// baselineEnv 是始终透传的基础变量。
// 不在这里的变量一律不注入，避免模型用 `env` 把 API key 读走。
var baselineEnv = []string{
	// 通用
	"PATH", "HOME", "LANG", "LC_ALL", "PWD",
	// Windows 进程启动必需，缺 SystemRoot 时 node/pwsh 会直接异常
	"USERPROFILE", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP",
	"COMSPEC", "SystemRoot", "SystemDrive", "WINDIR", "PATHEXT",
	"ProgramFiles", "ProgramFiles(x86)", "ProgramData",
	"USERNAME", "NUMBER_OF_PROCESSORS", "OS", "PROCESSOR_ARCHITECTURE",
}

// Runner 通过选定的 shell 执行命令，并保证超时能杀掉整棵进程树。
type Runner struct {
	shell          Shell
	headMax        int
	tailMax        int
	DefaultTimeout time.Duration

	passEnv []string
}

// NewRunner 构造执行器；passEnv 是配置里显式允许透传的额外变量名。
func NewRunner(shell Shell, passEnv ...string) *Runner {
	return &Runner{
		shell:          shell,
		headMax:        32 * 1024,
		tailMax:        32 * 1024,
		DefaultTimeout: 120 * time.Second,
		passEnv:        passEnv,
	}
}

// Shell 返回当前使用的 shell。
func (r *Runner) Shell() Shell { return r.shell }

// Run 执行命令；非零退出码通过 ExitCode 返回，不算 error。
func (r *Runner) Run(ctx context.Context, req ExecRequest) (ExecResult, error) {
	if strings.TrimSpace(req.Command) == "" {
		return ExecResult{}, errors.New("命令为空")
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = r.DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	name, args := r.shell.Command(req.Command)
	cmd := exec.CommandContext(cctx, name, args...)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}
	cmd.Env = r.buildEnv(req.Env)
	prepare(cmd)
	// 注意：Start 之后不再写 cmd 的任何字段。
	// os/exec 在 Start 内部就启动了 watchCtx goroutine，并发写属于数据竞争。
	cmd.WaitDelay = 3 * time.Second

	stdout := newCappedBuffer(r.headMax, r.tailMax)
	stderr := newCappedBuffer(r.headMax, r.tailMax)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return ExecResult{}, err
	}

	g := newGuard(cmd)
	waitDone := make(chan struct{})
	killerDone := make(chan struct{})
	go func() {
		defer close(killerDone)
		select {
		case <-cctx.Done():
			g.Kill()
		case <-waitDone:
		}
	}()

	waitErr := cmd.Wait()
	close(waitDone)
	<-killerDone
	g.Close()

	res := ExecResult{Duration: time.Since(start)}
	res.Stdout, res.Truncated = stdout.Result()
	stderrText, stderrTruncated := stderr.Result()
	res.Stderr = stderrText
	res.Truncated = res.Truncated || stderrTruncated
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
	}
	if waitErr != nil && !res.TimedOut {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return res, waitErr
		}
	}
	return res, nil
}

// buildEnv 只透传白名单与显式声明的变量。
func (r *Runner) buildEnv(extra map[string]string) []string {
	allowed := make(map[string]bool, len(baselineEnv)+len(r.passEnv))
	for _, name := range baselineEnv {
		allowed[strings.ToUpper(name)] = true
	}
	for _, name := range r.passEnv {
		if strings.TrimSpace(name) != "" {
			allowed[strings.ToUpper(name)] = true
		}
	}

	env := make([]string, 0, len(allowed)+3)
	seen := make(map[string]bool, len(allowed))
	for name := range allowed {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
			seen[name] = true
		}
	}
	for k, v := range extra {
		key := strings.ToUpper(k)
		if seen[key] {
			continue
		}
		seen[key] = true
		env = append(env, k+"="+v)
	}

	// bash 分支固定 UTF-8，避免中文输出变成乱码。
	if r.shell.Kind == "bash" || r.shell.Kind == "sh" {
		if !seen["LANG"] {
			env = append(env, "LANG=C.UTF-8")
		}
		if !seen["LC_ALL"] {
			env = append(env, "LC_ALL=C.UTF-8")
		}
	}
	return env
}
