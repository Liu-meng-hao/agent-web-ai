package security

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
)

// Shell 是一个可执行命令的 shell。
type Shell struct {
	Kind string // bash | sh | pwsh | cmd
	Path string
}

// String 返回可读描述，用于状态栏与 doctor。
func (s Shell) String() string { return s.Kind + " (" + s.Path + ")" }

// Command 把命令行包装成该 shell 需要的参数。
func (s Shell) Command(command string) (string, []string) {
	switch s.Kind {
	case "bash", "sh":
		return s.Path, []string{"-c", command}
	case "pwsh":
		// -EncodedCommand 接收 UTF-16LE 的 base64，绕开中文与引号转义；
		// 关掉进度流，否则 Windows PowerShell 会把 CLIXML 噪音写进 stderr。
		cmd := "$ProgressPreference='SilentlyContinue'; " + command
		return s.Path, []string{"-NoProfile", "-NonInteractive", "-EncodedCommand", encodePwshCommand(cmd)}
	case "cmd":
		return s.Path, []string{"/c", command}
	default:
		return s.Path, []string{"-c", command}
	}
}

// DetectShell 按 preferred 优先，失败后按平台默认顺序回退。
//
// Windows 上不能只信 exec.LookPath("bash")：它经常命中 WSL 启动器
// （System32\bash.exe 或 WindowsApps 别名），那里是另一个文件系统。
// 因此显式路径 → Git Bash 常见安装位置 → PATH（排除 WSL）→ 能力探测。
func DetectShell(preferred, bashPath string) Shell {
	for _, kind := range detectOrder(preferred) {
		switch kind {
		case "bash":
			if p, ok := findBash(bashPath); ok {
				return Shell{Kind: "bash", Path: p}
			}
		case "sh":
			if p, err := exec.LookPath("sh"); err == nil {
				return Shell{Kind: "sh", Path: p}
			}
		case "pwsh":
			if p, ok := lookPowerShell(); ok && probe(p, []string{"-NoProfile", "-NonInteractive", "-Command", "echo ok"}) {
				return Shell{Kind: "pwsh", Path: p}
			}
		case "cmd":
			if p := os.Getenv("COMSPEC"); p != "" {
				return Shell{Kind: "cmd", Path: p}
			}
		}
	}
	if runtime.GOOS == "windows" {
		return Shell{Kind: "cmd", Path: "cmd.exe"}
	}
	return Shell{Kind: "sh", Path: "/bin/sh"}
}

func detectOrder(preferred string) []string {
	var base []string
	if runtime.GOOS == "windows" {
		base = []string{"bash", "pwsh", "cmd"}
	} else {
		base = []string{"bash", "sh"}
	}
	preferred = strings.ToLower(strings.TrimSpace(preferred))
	if preferred == "" {
		return base
	}
	out := []string{preferred}
	for _, k := range base {
		if k != preferred {
			out = append(out, k)
		}
	}
	return out
}

func findBash(explicit string) (string, bool) {
	var candidates []string
	if explicit != "" {
		candidates = append(candidates, explicit)
	}
	if runtime.GOOS == "windows" {
		bases := []string{
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs"),
		}
		for _, base := range bases {
			if base == "" {
				continue
			}
			candidates = append(candidates,
				filepath.Join(base, "Git", "bin", "bash.exe"),
				filepath.Join(base, "Git", "usr", "bin", "bash.exe"),
			)
		}
	}
	if p, err := exec.LookPath("bash"); err == nil && !isWSLBash(p) {
		candidates = append(candidates, p)
	}

	seen := map[string]bool{}
	for _, c := range candidates {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		if _, err := os.Stat(c); err != nil {
			continue
		}
		if isWSLBash(c) {
			continue
		}
		// 路径存在不代表能跑：MSYS2 依赖命名管道，受限环境下会启动失败。
		if probe(c, []string{"-c", "echo ok"}) {
			return c, true
		}
	}
	return "", false
}

func isWSLBash(p string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	low := strings.ToLower(filepath.Clean(p))
	return strings.Contains(low, `\windows\system32\bash.exe`) ||
		strings.Contains(low, `\windowsapps\`)
}

func lookPowerShell() (string, bool) {
	if p, err := exec.LookPath("pwsh"); err == nil {
		return p, true
	}
	if p, err := exec.LookPath("powershell"); err == nil {
		return p, true
	}
	return "", false
}

func probe(path string, args []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	out, err := cmd.Output()
	return err == nil && strings.Contains(string(out), "ok")
}

func encodePwshCommand(command string) string {
	units := utf16.Encode([]rune(command))
	buf := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(buf[i*2:], u)
	}
	return base64.StdEncoding.EncodeToString(buf)
}
