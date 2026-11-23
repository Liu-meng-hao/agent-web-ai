package security

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDetectShellProducesRunnableCommand(t *testing.T) {
	shell := DetectShell("bash", "")
	t.Logf("detected shell: %s", shell.String())
	if shell.Path == "" {
		t.Fatal("shell path 为空")
	}

	res, err := NewRunner(shell).Run(context.Background(), ExecRequest{
		Command: "echo hello",
		Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Fatalf("stdout = %q, want hello", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
}

func TestRunnerTimeoutKillsAndReturns(t *testing.T) {
	shell := DetectShell("bash", "")

	var sleep string
	switch shell.Kind {
	case "bash", "sh":
		sleep = "sleep 30"
	case "pwsh":
		sleep = "Start-Sleep -Seconds 30"
	default:
		sleep = "ping -n 31 127.0.0.1 > nul"
	}

	start := time.Now()
	res, err := NewRunner(shell).Run(context.Background(), ExecRequest{
		Command: sleep,
		Timeout: 1 * time.Second,
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Skipf("当前环境无法启动子进程，跳过: %v", err)
	}
	if !res.TimedOut {
		t.Fatalf("应标记超时，res=%+v", res)
	}
	// 超时后必须在 WaitDelay 内返回，不能被孙进程拖住。
	if elapsed > 10*time.Second {
		t.Fatalf("超时后 %s 才返回，杀进程树可能失效", elapsed)
	}
}

func TestCappedBufferKeepsHeadAndTail(t *testing.T) {
	b := newCappedBuffer(8, 8)
	if _, err := b.Write([]byte("0123456789ABCDEFGHIJ")); err != nil {
		t.Fatal(err)
	}
	out, truncated := b.Result()
	if !truncated {
		t.Fatal("应标记截断")
	}
	if !strings.HasPrefix(out, "01234567") {
		t.Fatalf("头部丢失: %q", out)
	}
	if !strings.HasSuffix(out, "CDEFGHIJ") {
		t.Fatalf("尾部丢失: %q", out)
	}
}
