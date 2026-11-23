package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type fsReadInput struct {
	Path   string `json:"path" jsonschema:"description=要读取的文件路径，相对工作区或绝对路径"`
	Offset int    `json:"offset,omitempty" jsonschema:"description=起始行号，从 1 开始，默认 1"`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=最多读取行数，默认 400，最大 2000"`
}

type fsListInput struct {
	Path string `json:"path,omitempty" jsonschema:"description=目录路径，默认工作区"`
	All  bool   `json:"all,omitempty" jsonschema:"description=是否包含以点开头的隐藏文件"`
}

// NewFSRead 返回只读文件读取工具。
func NewFSRead() Tool {
	return Define(
		"fs_read",
		"读取文本文件，按行号输出，支持 offset/limit 分段读取。用于查看代码、配置、日志等。",
		RiskRead,
		func(ctx context.Context, in fsReadInput, tc *CallContext) (Result, error) {
			if tc == nil || tc.Jail == nil {
				return Result{}, fmt.Errorf("fs_read 未配置 jail")
			}
			p, err := resolvePath(tc, in.Path)
			if err != nil {
				return Result{}, err
			}
			if reason := sensitiveReason(p); reason != "" {
				return Result{
					Content: fmt.Sprintf("该文件属于敏感路径（%s），已被策略拒绝。不要重试，也不要尝试用 shell 读取；请让用户自行提供需要的内容。", reason),
					Display: "已拒绝（敏感路径）",
				}, nil
			}

			info, err := os.Stat(p)
			if err != nil {
				return Result{}, fmt.Errorf("读取失败: %w", err)
			}
			if info.IsDir() {
				return Result{}, fmt.Errorf("%s 是目录，请用 fs_list", p)
			}
			if info.Size() > 5<<20 {
				return Result{}, fmt.Errorf("文件过大（%d 字节），请用 offset/limit 工具或 shell 的 head/tail", info.Size())
			}

			raw, err := os.ReadFile(p)
			if err != nil {
				return Result{}, fmt.Errorf("读取失败: %w", err)
			}
			lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
			total := len(lines)

			offset := in.Offset
			if offset <= 0 {
				offset = 1
			}
			if offset > total {
				return Result{Content: fmt.Sprintf("(文件共 %d 行，offset=%d 超出行数)", total, offset), Display: "offset 越界"}, nil
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 400
			}
			if limit > 2000 {
				limit = 2000
			}
			end := offset - 1 + limit
			if end > total {
				end = total
			}

			var b strings.Builder
			for i := offset - 1; i < end; i++ {
				fmt.Fprintf(&b, "%6d│ %s\n", i+1, lines[i])
			}
			if end < total {
				fmt.Fprintf(&b, "…（共 %d 行，已截到第 %d 行，继续读请用 offset=%d）\n", total, end, end+1)
			}

			content := truncateMiddle(b.String(), 24000)
			return Result{
				Content: content,
				Display: fmt.Sprintf("%d 行 · %s", end-offset+1, humanSize(info.Size())),
			}, nil
		},
	)
}

// NewFSList 返回目录列表工具。
func NewFSList() Tool {
	return Define(
		"fs_list",
		"列出目录内容，输出类型、大小、修改时间与文件名。用于快速了解项目结构。",
		RiskRead,
		func(ctx context.Context, in fsListInput, tc *CallContext) (Result, error) {
			if tc == nil || tc.Jail == nil {
				return Result{}, fmt.Errorf("fs_list 未配置 jail")
			}
			p := tc.Cwd
			if strings.TrimSpace(in.Path) != "" {
				resolved, err := resolvePath(tc, in.Path)
				if err != nil {
					return Result{}, err
				}
				p = resolved
			}

			entries, err := os.ReadDir(p)
			if err != nil {
				return Result{}, fmt.Errorf("列目录失败: %w", err)
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

			var b strings.Builder
			count := 0
			for _, e := range entries {
				name := e.Name()
				if !in.All && strings.HasPrefix(name, ".") {
					continue
				}
				if count >= 300 {
					b.WriteString("…（条目过多，已截断到 300 条）\n")
					break
				}
				count++
				info, err := e.Info()
				if err != nil {
					continue
				}
				kind := "f"
				if e.IsDir() {
					kind = "d"
					name += "/"
				}
				fmt.Fprintf(&b, "%s  %-8s  %s  %s\n", kind, humanSize(info.Size()), info.ModTime().Format("01-02 15:04"), name)
			}
			if count == 0 {
				b.WriteString("(空目录)")
			}

			return Result{
				Content: b.String(),
				Display: fmt.Sprintf("%d 项", count),
			}, nil
		},
	)
}

func resolvePath(tc *CallContext, p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("path 不能为空")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(tc.Cwd, p)
	}
	return tc.Jail.Check(p)
}

// sensitiveReason 判断是否属于不允许进上下文的敏感文件。
// 注意：这只挡 fs_read；shell 仍可能读到，所以 exec 审批不可关闭。
func sensitiveReason(p string) string {
	base := strings.ToLower(filepath.Base(p))
	full := strings.ToLower(filepath.ToSlash(p))

	// 模板类文件是安全的，放行。
	for _, suffix := range []string{".example", ".sample", ".template", ".dist"} {
		if strings.HasSuffix(base, suffix) {
			return ""
		}
	}

	switch {
	case base == ".env" || strings.HasPrefix(base, ".env."):
		return "环境变量文件"
	case base == "id_rsa" || base == "id_dsa" || base == "id_ecdsa" || base == "id_ed25519":
		return "SSH 私钥"
	case strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key"):
		return "证书或私钥"
	case base == "credentials" || base == ".git-credentials":
		return "凭据文件"
	case strings.HasSuffix(full, "/.git/config"):
		return "git 配置"
	}
	return ""
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
