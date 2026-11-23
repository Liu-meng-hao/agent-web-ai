package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"agent/internal/config"
	"agent/internal/memory"
)

func memoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "查看与管理长期记忆",
	}

	cmd.AddCommand(
		memoryListCmd(),
		memorySearchCmd(),
		memoryShowCmd(),
		memoryRebuildCmd(),
	)
	return cmd
}

func memoryRoot() (string, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return "", err
	}
	root := cfg.Memory.Root
	if root == "" {
		root = "data/memory"
	}
	if !filepath.IsAbs(root) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		root = filepath.Join(cwd, root)
	}
	return root, nil
}

func memoryListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出活跃记忆",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := memoryRoot()
			if err != nil {
				return err
			}
			s, err := memory.OpenReadOnly(root)
			if err != nil {
				return err
			}
			facts := s.Active("")
			fmt.Printf("共 %d 条活跃记忆（%s）\n", len(facts), root)
			for _, f := range facts {
				fmt.Println(formatFact(f))
			}
			return nil
		},
	}
}

func memorySearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <关键词>",
		Short: "检索记忆",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := memoryRoot()
			if err != nil {
				return err
			}
			s, err := memory.OpenReadOnly(root)
			if err != nil {
				return err
			}
			facts := s.Search(strings.Join(args, " "), 20, "")
			if len(facts) == 0 {
				fmt.Println("没有找到相关记忆")
				return nil
			}
			for _, f := range facts {
				fmt.Println(formatFact(f))
			}
			return nil
		},
	}
}

func memoryShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "打印 MEMORY.md",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := memoryRoot()
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(filepath.Join(root, "MEMORY.md"))
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("还没有 MEMORY.md")
					return nil
				}
				return err
			}
			fmt.Print(string(raw))
			return nil
		},
	}
}

func memoryRebuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild",
		Short: "重新导入手工区并重写 MEMORY.md",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := memoryRoot()
			if err != nil {
				return err
			}
			s, err := memory.Open(root)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()
			if err := s.Rebuild(); err != nil {
				return err
			}
			fmt.Printf("已重建 %s（%d 条活跃记忆）\n", filepath.Join(root, "MEMORY.md"), s.Count())
			return nil
		},
	}
}

func formatFact(f memory.Fact) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s][%s] ", f.ID, f.Scope)
	if f.Key != "" {
		fmt.Fprintf(&b, "%s — ", f.Key)
	}
	b.WriteString(f.Text)
	fmt.Fprintf(&b, "（%.1f，%s）", f.Confidence, f.UpdatedAt.Format("2006-01-02"))
	return b.String()
}
