package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"go/version"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"agent/internal/agent"
	"agent/internal/config"
	"agent/internal/llm"
	"agent/internal/mcp"
	"agent/internal/skills"
	"agent/internal/telemetry"
)

// ErrDoctorFailed 表示 doctor 有失败项。main 见到它时不再重复打印错误。
var ErrDoctorFailed = errors.New("doctor 检查未通过")

// errDirMissing 区分“目录还不存在”与“目录存在但不可写”：
// doctor 是只读诊断，不应该顺手创建目录。
var errDirMissing = errors.New("目录不存在（首次运行会自动创建）")

// doctorCmd 检查运行环境与配置，失败时返回非零退出码。
func doctorCmd() *cobra.Command {
	var connectMCP bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "检查 Go 版本、配置、API key、目录权限、MCP、单价与运行环境",
		RunE: func(cmd *cobra.Command, args []string) error {
			var failed, warned int
			ok := func(name, detail string) { fmt.Printf("[ok]   %s：%s\n", name, detail) }
			warn := func(name, detail string) {
				warned++
				fmt.Printf("[warn] %s：%s\n", name, detail)
			}
			fail := func(name, detail string) {
				failed++
				fmt.Printf("[fail] %s：%s\n", name, detail)
			}

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			// 1. Go 版本：以 go.mod 的 go 指令为准，避免硬编码漂移。
			rv := runtime.Version()
			required, err := moduleGoVersion(filepath.Join(cwd, "go.mod"))
			if err != nil {
				warn("Go 版本", rv+"（读不到 go.mod 的 go 指令，跳过比较）")
			} else if !version.IsValid(rv) {
				warn("Go 版本", rv+"（开发版，跳过语义比较）")
			} else if version.Compare(rv, "go"+required) < 0 {
				fail("Go 版本", fmt.Sprintf("%s（go.mod 要求 go%s）", rv, required))
			} else {
				ok("Go 版本", fmt.Sprintf("%s（go.mod 要求 go%s）", rv, required))
			}

			// 2. 配置
			cfg, err := config.Load(configPath)
			if err != nil {
				fail("配置文件", err.Error())
				fmt.Printf("\n检查中断：配置无法加载，后续项未执行\n")
				return fmt.Errorf("%w：配置无法加载", ErrDoctorFailed)
			}
			ok("配置文件", configPath)

			// 3. API key（mock 不需要）
			names := make([]string, 0, len(cfg.Models))
			for name := range cfg.Models {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				m := cfg.Models[name]
				if m.Provider == "mock" {
					ok("API key ["+name+"]", "mock provider，无需 key")
					continue
				}
				if strings.TrimSpace(m.APIKey) == "" {
					if name == "main" {
						fail("API key [main]", "为空：请设置 AGENT_API_KEY 或在配置里填写 api_key")
					} else {
						warn("API key ["+name+"]", "为空，该能力会降级")
					}
					continue
				}
				ok("API key ["+name+"]", "已配置")
			}

			// 4. 目录可写：只测已存在的目录，缺失只告警，不创建任何东西。
			dirs := []struct{ name, path string }{
				{"工作区", cfg.Agent.Workspace},
				{"会话目录", cfg.Session.Root},
				{"记忆目录", cfg.Memory.Root},
				{"页面目录", cfg.Artifact.Root},
				{"日志目录", filepath.Join("data", "logs")},
			}
			for _, d := range dirs {
				path := d.path
				if !filepath.IsAbs(path) {
					path = filepath.Join(cwd, path)
				}
				switch err := checkWritable(path); {
				case errors.Is(err, errDirMissing):
					warn("目录可写 ["+d.name+"]", path+"："+err.Error())
				case err != nil:
					fail("目录可写 ["+d.name+"]", path+"："+err.Error())
				default:
					ok("目录可写 ["+d.name+"]", path)
				}
			}

			// 5. token 估算：实际跑一遍内置近似，给出可对照的数字。
			sample := strings.Repeat("上下文预算估算 ", 20) + "hello world, 1234567890"
			n := agent.EstimateTokens([]llm.Message{{Role: llm.RoleUser, Content: sample}})
			ok("token 估算", fmt.Sprintf("内置近似（ASCII/4、CJK/1）：样例 %d 字符 ≈ %d token",
				len([]rune(sample)), n))

			// 6. 单价配置与主模型覆盖
			pricingPath := filepath.Join(filepath.Dir(configPath), "pricing.yaml")
			pricing, err := telemetry.LoadPricing(pricingPath)
			if err != nil {
				fail("单价配置", err.Error())
			} else if len(pricing.Models) == 0 {
				warn("单价配置", pricingPath+" 未列出模型，成本按 0 显示")
			} else {
				ok("单价配置", fmt.Sprintf("%s（%d 个模型，%s）", pricingPath, len(pricing.Models), pricing.Currency))
			}
			if err == nil {
				checkPricingCoverage(cfg, pricing, ok, warn)
			}

			// 7. 日志与 trace 体积：长期运行最先失控的是这里。
			logDir := filepath.Join(cwd, "data", "logs")
			if size, count, err := dirSize(logDir); err == nil {
				ok("日志目录", fmt.Sprintf("%s（%d 个文件，%s）", logDir, count, humanBytes(size)))
			} else {
				warn("日志目录", err.Error())
			}
			turnDir := filepath.Join(logDir, "turns")
			if size, count, err := dirSize(turnDir); err == nil {
				ok("turn trace", fmt.Sprintf("%s（%d 份，%s）", turnDir, count, humanBytes(size)))
			} else {
				warn("turn trace", err.Error())
			}

			// 8. 技能目录
			skillRoot := cfg.Skills.Root
			if !filepath.IsAbs(skillRoot) {
				skillRoot = filepath.Join(cwd, skillRoot)
			}
			store, err := skills.Load(skills.Options{
				ProjectRoot:      skillRoot,
				UserRoot:         cfg.Skills.UserRoot,
				MaxBodyTokens:    cfg.Skills.MaxBodyTokens,
				MaxCatalogTokens: cfg.Skills.MaxCatalogTokens,
			})
			if err != nil {
				warn("技能目录", err.Error())
			} else if store.Len() == 0 {
				ok("技能目录", "未发现技能（可选）")
			} else {
				ok("技能目录", fmt.Sprintf("%d 个技能：%s", store.Len(), strings.Join(store.Names(), ", ")))
			}

			// 9. 预览端口
			if cfg.Preview.Enabled {
				checkPort(cfg.Preview.Addr, ok, warn)
			} else {
				ok("预览端口", "预览服务未启用")
			}

			// 10. MCP
			workspace := cfg.Agent.Workspace
			if !filepath.IsAbs(workspace) {
				workspace = filepath.Join(cwd, workspace)
			}
			mcpPath := filepath.Join(filepath.Dir(configPath), "mcp.yaml")
			mcpCfg, err := config.LoadMCP(mcpPath, map[string]string{"WORKSPACE": workspace})
			if err != nil {
				fail("MCP 配置", err.Error())
			} else if len(mcpCfg.Servers) == 0 {
				ok("MCP", "未配置 server（可选）")
			} else if !connectMCP {
				ok("MCP", fmt.Sprintf("配置了 %d 个 server；加 --mcp 可实测连接", len(mcpCfg.Servers)))
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				mgr := mcp.NewManager(mcpCfg, mcp.Options{
					ImageDir:    filepath.Join(cwd, "data", "artifacts", "_mcp"),
					OverflowDir: filepath.Join(cwd, "data", "logs", "mcp"),
					Warn:        func(s string) { fmt.Println("       · " + s) },
				})
				mgr.Start(ctx)
				for _, line := range mgr.Status() {
					fmt.Println("       · " + line)
				}
				count := len(mgr.Tools())
				mgr.Close()
				cancel()
				if count == 0 {
					warn("MCP 连接", "已尝试连接，但没有发现任何工具")
				} else {
					ok("MCP 连接", fmt.Sprintf("%d 个工具可用", count))
				}
			}

			fmt.Printf("\n完成：%d 项失败，%d 项告警\n", failed, warned)
			if failed > 0 {
				return fmt.Errorf("%w：%d 项失败", ErrDoctorFailed, failed)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&connectMCP, "mcp", false, "实际连接 MCP server 并列出工具（默认只校验配置）")
	return cmd
}

// checkWritable 验证已存在的目录可写入临时文件；不会创建目录。
func checkWritable(dir string) error {
	st, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return errDirMissing
		}
		return err
	}
	if !st.IsDir() {
		return errors.New("不是目录")
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// checkPricingCoverage 提醒未配置单价的模型：否则成本会静默显示为 0。
func checkPricingCoverage(cfg *config.Config, pricing *telemetry.Pricing, ok, warn func(name, detail string)) {
	if pricing == nil {
		return
	}
	for _, role := range []string{"main", "summarizer"} {
		m, exists := cfg.Models[role]
		if !exists || m.Provider == "mock" || m.Model == "" {
			continue
		}
		if _, found := pricing.Models[m.Model]; !found {
			warn("单价覆盖 ["+role+"]", fmt.Sprintf("模型 %s 未配置单价，成本会显示为 0（%s）",
				m.Model, filepath.Join(filepath.Dir(configPath), "pricing.yaml")))
			continue
		}
		ok("单价覆盖 ["+role+"]", m.Model)
	}
}

// checkPort 通过尝试连接判断端口是否已被占用。
func checkPort(addr string, ok, warn func(name, detail string)) {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		warn("预览端口", addr+" 已被占用（可能已有 agent 实例在运行）")
		return
	}
	ok("预览端口", addr+" 可用")
}

// moduleGoVersion 读取 go.mod 的 go 指令版本，如 "1.24"。
func moduleGoVersion(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "go ") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "go "))
			if v != "" {
				return v, nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", errors.New("go.mod 没有 go 指令")
}

// dirSize 统计目录顶层文件的数量与总大小。
func dirSize(path string) (int64, int, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	var total int64
	count := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		count++
	}
	return total, count, nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
