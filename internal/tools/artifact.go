package tools

import (
	"context"
	"fmt"
	"strings"

	"agent/internal/artifact"
)

// NewArtifactWrite 返回 artifact_write 工具。
//
// 自检问题不算工具错误：写入成功但返回问题清单，让模型用 patch 模式自修，
// 避免一次不合格就整页重写。
func NewArtifactWrite(w *artifact.Writer) Tool {
	return artifactWriteTool{Define(
		"artifact_write",
		"生成或修改本地页面（HTML/CSS/JS），自动维护版本与 manifest，并返回可打开的预览地址。"+
			"新建页面用 mode=create（或省略，首次自动 create）；整页重写用 replace；小范围修改用 patch 并给出 old 原文片段。"+
			"页面必须自包含：样式/脚本/图片全部内联，禁止 http(s) 外链；先读系统提示给出的设计系统目录（默认 data/artifacts/_shared）下的 tokens.css、base.css、gallery.html，按页面类型参考对应示例；只有需要图表时才读 charts.js。"+
			"不要默认做成数据看板：没有真实数据时不要编造图表或指标。"+
			"单页超过约 300 行或 12KB 时先 create 一个完整可打开的骨架，再用 patch 分块补全；每块尽量大，最多 3 次 patch，避免步数耗尽。"+
			"页面写入成功后最多做一次轻量校验，禁止反复运行 shell 校验；拿到预览地址后立即总结并结束。"+
			"已有页面可用 fs_list 查看页面根目录。",
		RiskWrite,
		func(ctx context.Context, in artifact.WriteInput, tc *CallContext) (Result, error) {
			if w == nil {
				return Result{}, fmt.Errorf("artifact_write 未配置页面写入器")
			}
			res, err := w.Write(ctx, in)
			if err != nil {
				return Result{}, err
			}

			var b strings.Builder
			fmt.Fprintf(&b, "已写入 %s/%s（v%d，%s）\n", res.Slug, res.Path, res.Version, humanSize(res.WrittenBytes))
			if res.Title != "" {
				fmt.Fprintf(&b, "标题：%s\n", res.Title)
			}
			if res.URL != "" {
				fmt.Fprintf(&b, "预览地址：%s\n", res.URL)
			} else if res.PreviewIn != "" {
				fmt.Fprintf(&b, "预览服务未启动：%s（文件已保存，可稍后用 agent preview 打开）\n", res.PreviewIn)
			}
			if res.Blocking > 0 {
				fmt.Fprintf(&b, "自检发现 %d 个阻塞问题，请修复后用 mode=patch 再写入：\n", res.Blocking)
				for _, issue := range res.Issues {
					fmt.Fprintf(&b, "- %s\n", issue)
				}
				if res.Status == "needs_review" {
					b.WriteString("连续两次自检未通过，manifest 已标记 needs_review。\n")
				}
			} else if len(res.Issues) > 0 {
				b.WriteString("自检提示（不影响保存）：\n")
				for _, issue := range res.Issues {
					fmt.Fprintf(&b, "- %s\n", issue)
				}
			}

			display := fmt.Sprintf("%s (v%d, %s)", res.Path, res.Version, humanSize(res.WrittenBytes))
			if res.URL != "" {
				display = fmt.Sprintf("%s · v%d · %s · %s", res.Path, res.Version, humanSize(res.WrittenBytes), res.URL)
			}
			return Result{Content: b.String(), Display: display}, nil
		},
	)}
}

// artifactWriteTool 只写页面根目录，standard 模式下无需逐次审批。
type artifactWriteTool struct{ Tool }

func (artifactWriteTool) AutoApprove() bool { return true }
