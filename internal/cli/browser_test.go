package cli

import (
	"reflect"
	"testing"

	"agent/internal/config"
)

func TestIsBrowserServer(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.MCPServer
		want bool
	}{
		{"npx playwright", config.MCPServer{Command: "npx", Args: []string{"-y", "@playwright/mcp@0.0.86"}}, true},
		{"npx other", config.MCPServer{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem"}}, false},
		{"direct binary", config.MCPServer{Command: "C:\\tools\\mcp.exe", Args: []string{"--browser", "playwright"}}, false},
	}
	for _, c := range cases {
		if got := isBrowserServer(c.cfg); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestApplyBrowserExecutable(t *testing.T) {
	cfg := &config.MCPConfig{Servers: map[string]config.MCPServer{
		"pw": {
			Command:           "npx",
			Args:              []string{"-y", "@playwright/mcp@0.0.86"},
			BrowserExecutable: `C:\fake\chrome.exe`,
		},
		"fs": {
			Command: "npx",
			Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
		},
	}}
	var warns []string
	applyBrowserExecutable(cfg, func(s string) { warns = append(warns, s) })

	got := cfg.Servers["pw"]
	if !hasFlag(got.Args, "--executable-path") {
		t.Fatalf("playwright server 未注入 --executable-path: %v", got.Args)
	}
	if !reflect.DeepEqual(cfg.Servers["fs"].Args, []string{"-y", "@modelcontextprotocol/server-filesystem"}) {
		t.Errorf("非浏览器 server 被误改: %v", cfg.Servers["fs"].Args)
	}
	if len(warns) != 0 {
		t.Errorf("显式路径不应产生告警: %v", warns)
	}

	// 已有 --executable-path 时不重复追加。
	cfg.Servers["pw"] = config.MCPServer{
		Command: "npx",
		Args:    []string{"-y", "@playwright/mcp@0.0.86", "--executable-path", "x"},
	}
	applyBrowserExecutable(cfg, func(string) {})
	if n := countFlag(cfg.Servers["pw"].Args, "--executable-path"); n != 1 {
		t.Errorf("--executable-path 出现 %d 次，期望 1", n)
	}
}

func countFlag(args []string, flag string) int {
	n := 0
	for _, a := range args {
		if a == flag {
			n++
		}
	}
	return n
}
