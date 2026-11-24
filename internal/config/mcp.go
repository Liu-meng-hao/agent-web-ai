package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// MCPServer 是一个 MCP server 的注册信息。
type MCPServer struct {
	Transport   string            `yaml:"transport"` // stdio | http
	Command     string            `yaml:"command"`
	Args        []string          `yaml:"args"`
	Env         map[string]string `yaml:"env"`
	URL         string            `yaml:"url"`
	Headers     map[string]string `yaml:"headers"`
	Approval    string            `yaml:"approval"` // auto | ask | deny
	Lazy        bool              `yaml:"lazy"`
	Timeout     string            `yaml:"timeout"`      // 启动与工具发现超时
	CallTimeout string            `yaml:"call_timeout"` // 单次 tools/call 超时
	// Tools 是远端工具白名单，只注册列出的工具；为空表示全部注册。
	// 工具多的 server（如浏览器）用它把常驻 schema 压到必要范围。
	Tools []string `yaml:"tools"`
	// ToolApproval 按远端工具名覆盖 approval，取值 auto | ask | deny。
	// auto 表示该工具免审批（只应对只读工具使用）。
	ToolApproval map[string]string `yaml:"tool_approval"`
	// BrowserExecutable 显式指定浏览器路径（浏览器类 server 用）。
	// 为空时启动阶段自动探测系统 Chrome/Edge 或 Playwright 缓存。
	BrowserExecutable string `yaml:"browser_executable"`
}

// ApprovalFor 返回某个远端工具的生效审批模式。
// explicit 为 true 表示来自 tool_approval 的单独覆盖。
func (s MCPServer) ApprovalFor(tool string) (mode string, explicit bool) {
	if m, ok := s.ToolApproval[tool]; ok {
		return m, true
	}
	return s.Approval, false
}

// MCPConfig 是 config/mcp.yaml 的结构。
type MCPConfig struct {
	Servers map[string]MCPServer `yaml:"servers"`
}

// TimeoutDuration 返回解析后的超时，非法值回退 30s。
func (s MCPServer) TimeoutDuration() time.Duration {
	if s.Timeout == "" {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(s.Timeout)
	if err != nil || d <= 0 {
		return 30 * time.Second
	}
	return d
}

// CallTimeoutDuration 返回单次工具调用超时，非法值回退 60s。
func (s MCPServer) CallTimeoutDuration() time.Duration {
	if s.CallTimeout == "" {
		return 60 * time.Second
	}
	d, err := time.ParseDuration(s.CallTimeout)
	if err != nil || d <= 0 {
		return 60 * time.Second
	}
	return d
}

// LoadMCP 读取 MCP 配置；文件不存在时返回空配置而不是错误。
//
// extra 用于注入 ${WORKSPACE} 这类运行期变量，之后再展开环境变量。
func LoadMCP(path string, extra map[string]string) (*MCPConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &MCPConfig{}, nil
		}
		return nil, fmt.Errorf("读取 MCP 配置 %s 失败: %w", path, err)
	}
	expanded := string(raw)
	for k, v := range extra {
		expanded = strings.ReplaceAll(expanded, "${"+k+"}", v)
	}
	var cfg MCPConfig
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(expanded)), &cfg); err != nil {
		return nil, fmt.Errorf("解析 MCP 配置 %s 失败: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

func (c *MCPConfig) applyDefaults() {
	for name, s := range c.Servers {
		if s.Transport == "" {
			if s.Command != "" {
				s.Transport = "stdio"
			} else {
				s.Transport = "http"
			}
		}
		if s.Approval == "" {
			s.Approval = "ask"
		}
		if s.Timeout == "" {
			s.Timeout = "30s"
		}
		if s.CallTimeout == "" {
			s.CallTimeout = "60s"
		}
		c.Servers[name] = s
	}
}

func (c *MCPConfig) validate() error {
	for name, s := range c.Servers {
		switch strings.ToLower(s.Transport) {
		case "stdio":
			if strings.TrimSpace(s.Command) == "" {
				return fmt.Errorf("mcp.servers.%s.command 不能为空（transport=stdio）", name)
			}
		case "http", "streamable-http":
			if strings.TrimSpace(s.URL) == "" {
				return fmt.Errorf("mcp.servers.%s.url 不能为空（transport=http）", name)
			}
		default:
			return fmt.Errorf("mcp.servers.%s.transport 只能是 stdio / http，当前 %q", name, s.Transport)
		}
		if err := validateApproval(fmt.Sprintf("mcp.servers.%s.approval", name), s.Approval); err != nil {
			return err
		}
		for tool, mode := range s.ToolApproval {
			if strings.TrimSpace(tool) == "" {
				return fmt.Errorf("mcp.servers.%s.tool_approval 存在空工具名", name)
			}
			if err := validateApproval(fmt.Sprintf("mcp.servers.%s.tool_approval.%s", name, tool), mode); err != nil {
				return err
			}
		}
		seen := map[string]bool{}
		for _, tool := range s.Tools {
			tool = strings.TrimSpace(tool)
			if tool == "" {
				return fmt.Errorf("mcp.servers.%s.tools 存在空工具名", name)
			}
			if seen[tool] {
				return fmt.Errorf("mcp.servers.%s.tools 重复列出 %q", name, tool)
			}
			seen[tool] = true
		}
	}
	return nil
}

func validateApproval(field, mode string) error {
	switch mode {
	case "auto", "ask", "deny":
		return nil
	default:
		return fmt.Errorf("%s 只能是 auto / ask / deny，当前 %q", field, mode)
	}
}
