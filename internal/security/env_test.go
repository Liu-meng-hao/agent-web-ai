package security

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func envProbe(shell Shell, name string) string {
	switch shell.Kind {
	case "bash", "sh":
		return `printf "%s" "${` + name + `}"`
	case "pwsh":
		return `[Console]::Write($env:` + name + `)`
	default:
		return `echo %` + name + `%`
	}
}

func TestRunnerEnvWhitelistBlocksSecrets(t *testing.T) {
	t.Setenv("AGENT_TEST_SECRET_TOKEN", "leak-me")

	shell := DetectShell("bash", "")
	res, err := NewRunner(shell).Run(context.Background(), ExecRequest{
		Command: envProbe(shell, "AGENT_TEST_SECRET_TOKEN"),
		Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Skipf("当前环境无法启动子进程: %v", err)
	}
	if strings.Contains(res.Stdout, "leak-me") {
		t.Fatalf("白名单失效，密钥泄漏到子进程: %q", res.Stdout)
	}
}

func TestRunnerPassEnvAllowsExplicit(t *testing.T) {
	t.Setenv("AGENT_TEST_PASSED", "visible")

	shell := DetectShell("bash", "")
	res, err := NewRunner(shell, "AGENT_TEST_PASSED").Run(context.Background(), ExecRequest{
		Command: envProbe(shell, "AGENT_TEST_PASSED"),
		Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Skipf("当前环境无法启动子进程: %v", err)
	}
	if !strings.Contains(res.Stdout, "visible") {
		t.Fatalf("pass_env 未生效: %q", res.Stdout)
	}
}

func TestRunnerKeepsBaseEnv(t *testing.T) {
	shell := DetectShell("bash", "")
	var cmd string
	switch shell.Kind {
	case "bash", "sh":
		cmd = "echo $PATH"
	case "pwsh":
		cmd = "echo $env:PATH"
	default:
		cmd = "echo %PATH%"
	}
	res, err := NewRunner(shell).Run(context.Background(), ExecRequest{Command: cmd, Timeout: 15 * time.Second})
	if err != nil {
		t.Skipf("当前环境无法启动子进程: %v", err)
	}
	if len(strings.TrimSpace(res.Stdout)) == 0 {
		t.Fatalf("PATH 被误删，子进程无法找命令: %q", res.Stdout)
	}
	if os.Getenv("PATH") == "" {
		t.Skip("父进程没有 PATH")
	}
}
