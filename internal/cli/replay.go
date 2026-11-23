package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"agent/internal/config"
	"agent/internal/session"
)

// replayCmd 只回放事件文件，绝不实例化 provider——这样才能证明事件流是自足的。
func replayCmd() *cobra.Command {
	var from, to int64

	cmd := &cobra.Command{
		Use:   "replay <id>",
		Short: "按事件流回放会话（不调用模型、不需要 API key）",
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

			rec, err := session.Scan(dir)
			if err != nil {
				return err
			}
			if len(rec.Events) == 0 {
				fmt.Println("(没有可回放的事件)")
				return nil
			}

			filtered := make([]session.Event, 0, len(rec.Events))
			for _, e := range rec.Events {
				if from > 0 && e.Seq < from {
					continue
				}
				if to > 0 && e.Seq > to {
					continue
				}
				filtered = append(filtered, e)
			}

			fmt.Print(session.Render(filtered))
			if len(rec.Warnings) > 0 {
				fmt.Fprintln(os.Stderr, "警告："+strings.Join(rec.Warnings, "；"))
			}
			return nil
		},
	}

	cmd.Flags().Int64Var(&from, "from", 0, "起始 seq（含）")
	cmd.Flags().Int64Var(&to, "to", 0, "结束 seq（含）")
	return cmd
}
