package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/spf13/cobra"

	"agent/internal/artifact"
	"agent/internal/config"
)

func previewCmd() *cobra.Command {
	var addr string
	var open bool

	cmd := &cobra.Command{
		Use:   "preview [slug]",
		Short: "启动本地页面预览服务（只监听回环地址）",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			root := cfg.Artifact.Root
			if !filepath.IsAbs(root) {
				root = filepath.Join(cwd, root)
			}
			if err := os.MkdirAll(root, 0o755); err != nil {
				return fmt.Errorf("创建页面目录失败: %w", err)
			}
			if err := artifact.MirrorAssets(filepath.Join(root, "_shared")); err != nil {
				fmt.Fprintln(os.Stderr, "警告：设计系统资产镜像失败："+err.Error())
			}
			if !cfg.Preview.Enabled {
				fmt.Fprintln(os.Stderr, "提示：config preview.enabled=false，本次为显式启动，仍会使用预览服务")
			}
			if addr == "" {
				addr = cfg.Preview.Addr
			}

			srv := artifact.NewServer(addr, root)
			if err := srv.Ensure(); err != nil {
				return err
			}
			defer srv.Close()

			base := srv.BaseURL()
			fmt.Println("预览服务：" + base)
			pages, err := artifact.ListPages(root)
			if err != nil {
				return err
			}
			target := base + "/"
			if len(args) == 1 {
				slug := args[0]
				found := false
				for _, p := range pages {
					if p.Slug == slug {
						found = true
						target = base + "/a/" + url.PathEscape(slug) + "/"
						break
					}
				}
				if !found {
					return fmt.Errorf("没有找到页面 %q", slug)
				}
			} else if len(pages) > 0 {
				target = base + "/a/" + pages[0].Slug + "/"
			}

			if len(pages) == 0 {
				fmt.Println("还没有页面。先生成一个，或把 HTML 放到 " + root + "/<slug>/index.html")
			} else {
				for _, p := range pages {
					status := ""
					if p.Status == "needs_review" {
						status = "  needs_review"
					}
					fmt.Printf("  %-24s v%-3d %s  %s%s\n", p.Title, p.Version,
						p.UpdatedAt.Format("01-02 15:04"), base+"/a/"+url.PathEscape(p.Slug)+"/", status)
				}
			}
			if open {
				if err := artifact.OpenBrowser(target); err != nil {
					return err
				}
				fmt.Println("已在浏览器打开：" + target)
			}

			fmt.Println("按 Ctrl+C 退出")
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			<-ctx.Done()
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "", "监听地址（默认取 config preview.addr，只允许回环地址）")
	cmd.Flags().BoolVar(&open, "open", false, "启动后打开浏览器")
	return cmd
}
