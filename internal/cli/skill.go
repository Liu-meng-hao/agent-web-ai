package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"agent/internal/config"
	"agent/internal/skills"
)

func skillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "查看技能目录（技能本身由 agent 在运行时按需加载）",
	}
	cmd.AddCommand(skillListCmd(), skillShowCmd())
	return cmd
}

func skillListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出项目与用户技能目录",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := loadSkillStore()
			if err != nil {
				return err
			}
			printSkillWarnings(store)
			if store.Len() == 0 {
				fmt.Fprintln(os.Stderr, "未发现技能。把技能放到 skills/<name>/SKILL.md，格式可参考 skills/.example。")
				return nil
			}
			for _, line := range store.CatalogLines() {
				fmt.Println(line)
			}
			return nil
		},
	}
}

func skillShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "查看单个技能正文",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := loadSkillStore()
			if err != nil {
				return err
			}
			printSkillWarnings(store)
			body, ok := store.Body(args[0])
			if !ok {
				return fmt.Errorf("没有找到技能 %q", args[0])
			}
			fmt.Println(body)
			return nil
		},
	}
}

func loadSkillStore() (*skills.Store, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root := cfg.Skills.Root
	if !filepath.IsAbs(root) {
		root = filepath.Join(cwd, root)
	}
	return skills.Load(skills.Options{
		ProjectRoot:      root,
		UserRoot:         cfg.Skills.UserRoot,
		MaxBodyTokens:    cfg.Skills.MaxBodyTokens,
		MaxCatalogTokens: cfg.Skills.MaxCatalogTokens,
	})
}

func printSkillWarnings(store *skills.Store) {
	for _, w := range store.Warnings() {
		fmt.Fprintln(os.Stderr, "警告："+w)
	}
}
