// Package config 加载并校验 config/agent.yaml。
//
// 规则：文件里的 ${VAR} 会按进程环境变量展开；缺省值在 applyDefaults 里补；
// 结构性错误（缺 main 模型、provider 参数不全）在加载期就报错，不要留到运行时。
package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Model 是一个模型端点的配置。
type Model struct {
	Provider  string `yaml:"provider"`
	BaseURL   string `yaml:"base_url"`
	APIKey    string `yaml:"api_key"`
	Model     string `yaml:"model"`
	Window    int    `yaml:"window"`
	MaxTokens int    `yaml:"max_tokens"`
}

// Agent 是主循环与权限相关配置。
type Agent struct {
	MaxSteps    int    `yaml:"max_steps"`
	Approval    string `yaml:"approval"`
	Workspace   string `yaml:"workspace"`
	AutoExtract bool   `yaml:"auto_extract"`
}

// Context 是上下文预算配置。
type Context struct {
	OutputReserve int     `yaml:"output_reserve"`
	Safety        float64 `yaml:"safety"`
	CompactAt     float64 `yaml:"compact_at"`
}

// Memory 是记忆检索配置。
type Memory struct {
	Root        string `yaml:"root"`
	TopK        int    `yaml:"top_k"`
	TokenBudget int    `yaml:"token_budget"`
	DecayDays   int    `yaml:"decay_days"`
}

// Skills 是技能发现配置。
type Skills struct {
	Root             string `yaml:"root"`
	UserRoot         string `yaml:"user_root"`
	MaxBodyTokens    int    `yaml:"max_body_tokens"`
	MaxCatalogTokens int    `yaml:"max_catalog_tokens"`
}

// Shell 是命令执行配置。
type Shell struct {
	Default        string   `yaml:"default"`
	BashPath       string   `yaml:"bash_path"`
	PassEnv        []string `yaml:"pass_env"`
	TimeoutDefault string   `yaml:"timeout_default"`
}

// Artifact 是页面生成配置。
type Artifact struct {
	Root      string `yaml:"root"`
	SelfCheck bool   `yaml:"self_check"`
	MaxFileKB int    `yaml:"max_file_kb"`
}

// UI 是终端界面配置。
type UI struct {
	Theme            string `yaml:"theme"`
	StreamDebounceMS int    `yaml:"stream_debounce_ms"`
	ShowReasoning    bool   `yaml:"show_reasoning"`
}

// Preview 是页面预览服务配置。
type Preview struct {
	Enabled  bool   `yaml:"enabled"`
	Addr     string `yaml:"addr"`
	AutoOpen bool   `yaml:"auto_open"`
}

// Session 是会话存储配置。
type Session struct {
	Root         string `yaml:"root"`
	KeepDays     int    `yaml:"keep_days"`
	SyncEachTool *bool  `yaml:"sync_each_tool"`
}

// Config 是整份配置。
type Config struct {
	Models   map[string]Model `yaml:"models"`
	Agent    Agent            `yaml:"agent"`
	Context  Context          `yaml:"context"`
	Memory   Memory           `yaml:"memory"`
	Skills   Skills           `yaml:"skills"`
	Shell    Shell            `yaml:"shell"`
	Artifact Artifact         `yaml:"artifact"`
	UI       UI               `yaml:"ui"`
	Preview  Preview          `yaml:"preview"`
	Session  Session          `yaml:"session"`
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandEnv(raw []byte) []byte {
	return envPattern.ReplaceAllFunc(raw, func(m []byte) []byte {
		name := string(envPattern.FindSubmatch(m)[1])
		return []byte(os.Getenv(name))
	})
}

// Load 读取并校验配置文件。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s 失败: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(expandEnv(raw), &cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s 失败: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Models == nil {
		c.Models = map[string]Model{}
	}
	for name, m := range c.Models {
		if m.Provider == "" {
			m.Provider = "openai"
		}
		if m.Window <= 0 {
			m.Window = 131072
		}
		if m.MaxTokens <= 0 {
			m.MaxTokens = 4096
		}
		c.Models[name] = m
	}

	if c.Agent.MaxSteps <= 0 {
		c.Agent.MaxSteps = 48
	}
	if c.Agent.Approval == "" {
		c.Agent.Approval = "standard"
	}
	if c.Agent.Workspace == "" {
		c.Agent.Workspace = "data/workspace"
	}
	if c.Context.OutputReserve <= 0 {
		c.Context.OutputReserve = 8000
	}
	if c.Context.Safety <= 0 {
		c.Context.Safety = 0.1
	}
	if c.Context.CompactAt <= 0 {
		c.Context.CompactAt = 0.75
	}
	if c.Memory.TopK <= 0 {
		c.Memory.TopK = 12
	}
	if c.Memory.TokenBudget <= 0 {
		c.Memory.TokenBudget = 2000
	}
	if c.Memory.DecayDays <= 0 {
		c.Memory.DecayDays = 90
	}
	if c.Memory.Root == "" {
		c.Memory.Root = "data/memory"
	}
	if c.Skills.Root == "" {
		c.Skills.Root = "skills"
	}
	if c.Skills.UserRoot == "" {
		c.Skills.UserRoot = "~/.agent/skills"
	}
	if c.Skills.MaxBodyTokens <= 0 {
		c.Skills.MaxBodyTokens = 8000
	}
	if c.Skills.MaxCatalogTokens <= 0 {
		c.Skills.MaxCatalogTokens = 800
	}
	if c.Shell.Default == "" {
		c.Shell.Default = "bash"
	}
	if c.Shell.TimeoutDefault == "" {
		c.Shell.TimeoutDefault = "120s"
	}
	if c.Artifact.Root == "" {
		c.Artifact.Root = "data/artifacts"
	}
	if c.Artifact.MaxFileKB <= 0 {
		c.Artifact.MaxFileKB = 200
	}
	if c.UI.Theme == "" {
		c.UI.Theme = "dark"
	}
	if c.Preview.Addr == "" {
		c.Preview.Addr = "127.0.0.1:4318"
	}
	if c.Session.Root == "" {
		c.Session.Root = "data/sessions"
	}
	if c.Session.KeepDays <= 0 {
		c.Session.KeepDays = 30
	}
}

// SyncEachTool 返回是否在每次工具调用后强制刷盘（默认 true）。
func (c *Config) SyncEachTool() bool {
	if c.Session.SyncEachTool == nil {
		return true
	}
	return *c.Session.SyncEachTool
}

func (c *Config) validate() error {
	if len(c.Models) == 0 {
		return fmt.Errorf("没有任何模型：请在 models.main 下至少定义一项")
	}
	if _, ok := c.Models["main"]; !ok {
		return fmt.Errorf("缺少 models.main：主模型是必填项")
	}
	for name, m := range c.Models {
		if strings.TrimSpace(m.Model) == "" {
			return fmt.Errorf("models.%s.model 不能为空", name)
		}
		if m.Provider == "openai" && strings.TrimSpace(m.BaseURL) == "" {
			return fmt.Errorf("models.%s.base_url 不能为空（provider=openai）", name)
		}
	}
	switch c.Agent.Approval {
	case "readonly", "standard", "yolo":
	default:
		return fmt.Errorf("agent.approval 只能是 readonly / standard / yolo，当前 %q", c.Agent.Approval)
	}
	return nil
}

// Main 返回主模型配置。
func (c *Config) Main() Model { return c.Models["main"] }
