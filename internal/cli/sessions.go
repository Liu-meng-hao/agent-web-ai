package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"agent/internal/config"
	"agent/internal/session"
)

func sessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "列出历史会话",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			root, err := sessionRoot(cfg)
			if err != nil {
				return err
			}
			metas, err := session.List(root)
			if err != nil {
				return err
			}
			if len(metas) == 0 {
				fmt.Println("(还没有会话)")
				return nil
			}

			fmt.Printf("%-10s %-30s %-17s %-10s %s\n", "ID", "TITLE", "UPDATED", "STATUS", "EVENTS")
			for _, m := range metas {
				fmt.Printf("%-10s %-30s %-17s %-10s %d\n",
					short(m.ID, 8),
					truncateTitle(m.Title, 28),
					m.UpdatedAt.Format("2006-01-02 15:04"),
					m.Status,
					m.EventCount,
				)
			}
			return nil
		},
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "rm <id>",
		Short: "删除会话（需要输入 yes 确认）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			root, err := sessionRoot(cfg)
			if err != nil {
				return err
			}
			dir, err := session.Find(root, args[0])
			if err != nil {
				return err
			}
			fmt.Printf("删除会话 %s？输入 yes 确认: ", dir)
			var ans string
			_, _ = fmt.Scanln(&ans)
			if strings.TrimSpace(ans) != "yes" {
				fmt.Println("已取消")
				return nil
			}
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			fmt.Println("已删除")
			return nil
		},
	})
	return cmd
}
