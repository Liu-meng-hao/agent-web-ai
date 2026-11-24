package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"agent/internal/config"
)

// applyBrowserExecutable 给浏览器类 MCP server 补上 --executable-path。
//
// Playwright 默认只找自己缓存的 Chromium 或用户级安装的浏览器，
// 系统级安装（C:\Program Files\...）会被判定为“未安装”。
// 这里在启动前探测一次，避免把某台机器的路径写死在配置里。
func applyBrowserExecutable(cfg *config.MCPConfig, warn func(string)) {
	for name, s := range cfg.Servers {
		if !isBrowserServer(s) || hasFlag(s.Args, "--executable-path") {
			continue
		}
		p := strings.TrimSpace(s.BrowserExecutable)
		explicit := p != ""
		if !explicit {
			p = detectBrowserExecutable()
		}
		if p == "" {
			warn("MCP server " + name + " 未找到可用浏览器：请安装 Chrome/Edge，或执行 npx playwright install chromium，或在 mcp.yaml 配置 browser_executable")
			continue
		}
		s.Args = append(s.Args, "--executable-path", shortPath(p))
		cfg.Servers[name] = s
		if explicit {
			continue
		}
	}
}

// isBrowserServer 判断一个 server 是否是需要浏览器二进制的类型。
// 只对 npx/node 启动且参数里出现 playwright 的 server 生效，避免误伤其他 MCP。
func isBrowserServer(s config.MCPServer) bool {
	base := strings.ToLower(filepath.Base(s.Command))
	switch base {
	case "npx", "npx.cmd", "node", "node.exe", "pnpm", "pnpm.cmd", "yarn", "yarn.cmd":
	default:
		return false
	}
	for _, a := range s.Args {
		if strings.Contains(strings.ToLower(a), "playwright") {
			return true
		}
	}
	return false
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

// detectBrowserExecutable 按“显式环境变量 → 常见安装位置 → Playwright 缓存”顺序探测。
func detectBrowserExecutable() string {
	if p := strings.TrimSpace(os.Getenv("CHROME_PATH")); p != "" {
		if fileExists(p) {
			return p
		}
	}
	for _, p := range browserCandidates() {
		if fileExists(p) {
			return p
		}
	}
	if p := playwrightCachedChromium(); p != "" {
		return p
	}
	return findInPath("google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "msedge", "microsoft-edge")
}

func browserCandidates() []string {
	var out []string
	add := func(parts ...string) {
		if len(parts) == 0 {
			return
		}
		out = append(out, filepath.Join(parts...))
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		add(pf, "Google", "Chrome", "Application", "chrome.exe")
		add(pf, "Microsoft", "Edge", "Application", "msedge.exe")
	}
	if pf := os.Getenv("ProgramFiles(x86)"); pf != "" {
		add(pf, "Google", "Chrome", "Application", "chrome.exe")
		add(pf, "Microsoft", "Edge", "Application", "msedge.exe")
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		add(la, "Google", "Chrome", "Application", "chrome.exe")
		add(la, "Microsoft", "Edge", "Application", "msedge.exe")
	}
	return out
}

// playwrightCachedChromium 找 Playwright 自己缓存的浏览器。
func playwrightCachedChromium() string {
	root := os.Getenv("PLAYWRIGHT_BROWSERS_PATH")
	if root == "" {
		la := os.Getenv("LOCALAPPDATA")
		if la == "" {
			return ""
		}
		root = filepath.Join(la, "ms-playwright")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "chromium-") {
			continue
		}
		dirs = append(dirs, e.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		for _, rel := range [][]string{
			{"chrome-win64", "chrome.exe"},
			{"chrome-win", "chrome.exe"},
			{"chrome-linux", "chrome"},
			{"chrome-mac", "Chromium.app", "Contents", "MacOS", "Chromium"},
		} {
			p := filepath.Join(append([]string{root, d}, rel...)...)
			if fileExists(p) {
				return p
			}
		}
	}
	return ""
}

func findInPath(names ...string) string {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
