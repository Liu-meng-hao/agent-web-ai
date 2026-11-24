package memory

import "strings"

// diffLines 生成 oldText → newText 的行级差异，最多返回 maxLines 行。
// 输出行以 "  "/"- "/"+ " 开头，便于审批弹层着色。
func diffLines(oldText, newText string, maxLines int) []string {
	if maxLines <= 0 {
		maxLines = 32
	}
	a, b := factLines(oldText), factLines(newText)

	// 记忆事实通常很短；超长文本退化为两行对比，避免 LCS 表过大。
	if len(a)*len(b) > 200000 {
		return capDiff([]string{"- " + strings.TrimSpace(oldText), "+ " + strings.TrimSpace(newText)}, maxLines)
	}

	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	out := make([]string, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, "  "+a[i])
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, "- "+a[i])
			i++
		default:
			out = append(out, "+ "+b[j])
			j++
		}
	}
	for ; i < len(a); i++ {
		out = append(out, "- "+a[i])
	}
	for ; j < len(b); j++ {
		out = append(out, "+ "+b[j])
	}
	return capDiff(out, maxLines)
}

func capDiff(lines []string, maxLines int) []string {
	if len(lines) <= maxLines {
		return lines
	}
	out := append([]string(nil), lines[:maxLines]...)
	return append(out, "…（差异已截断）")
}

func factLines(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
