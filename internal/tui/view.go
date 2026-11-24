package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// 暗色蓝紫主题：主色蓝紫、正文近白、工具冷蓝，状态用绿/黄/红区分。
var (
	colPrimary = lipgloss.Color("75")
	colAccent  = lipgloss.Color("141")
	colUser    = lipgloss.Color("117")
	colText    = lipgloss.Color("252")
	colTool    = lipgloss.Color("109")
	colDim     = lipgloss.Color("243")
	colErr     = lipgloss.Color("203")
	colWarn    = lipgloss.Color("214")
	colOK      = lipgloss.Color("114")

	headerStyle = lipgloss.NewStyle().Foreground(colPrimary).Bold(true)
	userStyle   = lipgloss.NewStyle().Foreground(colUser).Bold(true)
	agentStyle  = lipgloss.NewStyle().Foreground(colText)
	toolStyle   = lipgloss.NewStyle().Foreground(colTool)
	dimStyle    = lipgloss.NewStyle().Foreground(colDim)
	errStyle    = lipgloss.NewStyle().Foreground(colErr)
	warnStyle   = lipgloss.NewStyle().Foreground(colWarn).Bold(true)
	okStyle     = lipgloss.NewStyle().Foreground(colOK)
	hintStyle   = lipgloss.NewStyle().Foreground(colDim)

	inputStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colAccent).
			Padding(0, 1)
	inputBusyStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colDim).
			Padding(0, 1)
	approvalStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colWarn).
			Padding(0, 1)
	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Background(lipgloss.Color("236"))
)

// View 组装整屏：标题行、可滚动对话区、输入区或审批面板、底部状态栏。
func (m *model) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}

	header := m.renderHeader(width)
	footer := m.renderFooter(width)
	footerH := lineCount(footer)

	// 1 行标题 + 1 行状态栏，其余留给对话区。
	bodyH := height - 1 - footerH - 1
	if bodyH < 3 {
		bodyH = 3
	}
	m.vp.Width = width
	m.vp.Height = bodyH
	m.syncViewport(width)

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		m.vp.View(),
		footer,
		m.statusLine(width),
	)
}

func (m *model) renderHeader(width int) string {
	left := "◆ agent"
	if m.modelName != "" {
		left += " · " + m.modelName
	}
	right := ""
	if m.sessionID != "" {
		right = "session " + shortID(m.sessionID)
	}
	if right == "" || runewidth.StringWidth(left)+runewidth.StringWidth(right)+2 > width {
		return headerStyle.Render(truncateRunes(left, width))
	}
	gap := width - runewidth.StringWidth(left) - runewidth.StringWidth(right)
	return headerStyle.Render(left) + strings.Repeat(" ", gap) + hintStyle.Render(right)
}

func (m *model) renderFooter(width int) string {
	if m.overlay && m.pendingApproval != nil {
		return m.renderApproval(width)
	}
	m.ta.SetWidth(maxInt(20, width-4))
	style := inputStyle
	if m.streaming {
		style = inputBusyStyle
	}
	return style.Render(m.ta.View())
}

// syncViewport 把渲染块同步到 viewport；贴底状态下始终跟随最新输出。
func (m *model) syncViewport(width int) {
	lines := m.renderBlocks(maxInt(20, width-1))
	m.vp.SetContent(strings.Join(lines, "\n"))
	if m.autoFollow {
		m.vp.GotoBottom()
	}
}

func (m *model) renderBlocks(width int) []string {
	var lines []string
	for _, blk := range m.blocks {
		switch blk.kind {
		case blockText:
			prefix := "agent › "
			style := agentStyle
			if blk.speaker == "you" {
				prefix, style = "you › ", userStyle
			}
			for _, l := range wrapText(prefix+blk.text, width) {
				lines = append(lines, style.Render(l))
			}
			lines = append(lines, "")

		case blockTool:
			lines = append(lines, m.renderTool(blk, width)...)

		case blockError:
			for _, l := range wrapText("✗ "+blk.text, width) {
				lines = append(lines, errStyle.Render(l))
			}
			lines = append(lines, "")

		case blockNotice:
			for _, l := range wrapText("· "+blk.text, width) {
				lines = append(lines, warnStyle.Render(l))
			}
		}
	}

	if m.pending.Len() > 0 {
		for _, l := range wrapText("agent › "+m.pending.String(), width) {
			lines = append(lines, agentStyle.Render(l))
		}
	}

	if len(lines) == 0 {
		lines = append(lines,
			hintStyle.Render("键入消息开始。工具调用会在执行前请求审批。"),
			"",
			hintStyle.Render("/help 查看命令 · PgUp/PgDn 或滚轮回看历史"),
		)
	}
	return lines
}

func (m *model) renderTool(blk block, width int) []string {
	icon, style := "…", toolStyle
	switch blk.status {
	case "ok":
		icon, style = "✓", okStyle
	case "fail":
		icon, style = "✗", errStyle
	case "denied":
		icon, style = "⊘", dimStyle
	}

	head := fmt.Sprintf("%s %s", icon, blk.tool)
	if blk.preview != "" {
		head += "  " + blk.preview
	}
	if blk.duration > 0 {
		head += "  " + blk.duration.Round(time.Millisecond).String()
	}

	var out []string
	for _, l := range wrapText("  "+head, width) {
		out = append(out, style.Render(l))
	}

	switch {
	case blk.status == "ok" && blk.result != "":
		resultLines := strings.Split(strings.TrimSpace(blk.result), "\n")
		for i, l := range resultLines {
			if i >= 2 {
				break
			}
			out = append(out, dimStyle.Render("    │ "+truncateRunes(l, width-7)))
		}
	case blk.status == "fail":
		text := strings.TrimSpace(blk.result)
		if text == "" {
			text = "执行失败（无输出）"
		}
		for i, l := range strings.Split(text, "\n") {
			if i >= 2 {
				break
			}
			out = append(out, errStyle.Render("    │ "+truncateRunes(l, width-7)))
		}
	case blk.status == "denied":
		out = append(out, dimStyle.Render("    │ 已拒绝"))
	}
	return out
}

func (m *model) renderApproval(width int) string {
	req := m.pendingApproval
	if req == nil {
		return ""
	}
	inner := width - 4
	if inner < 20 {
		inner = 20
	}

	riskStyle := okStyle
	switch req.Risk.String() {
	case "write":
		riskStyle = warnStyle
	case "exec", "network":
		riskStyle = errStyle
	}

	lines := []string{
		warnStyle.Render("⚠ 需要审批") + "  " +
			toolStyle.Render(req.Tool) + "  " +
			riskStyle.Render("["+req.Risk.String()+"]"),
	}
	if req.Preview != "" {
		if strings.Contains(req.Preview, "\n") {
			lines = append(lines, renderDiffPreview(req.Preview, inner)...)
		} else {
			for _, l := range wrapText("$ "+req.Preview, inner) {
				lines = append(lines, dimStyle.Render(l))
			}
		}
	}
	lines = append(lines, "")
	hint := "[y] 允许一次   [a] 本会话允许   [n] 拒绝"
	if req.Tool == "memory_update" {
		// 记忆修正不做会话级免审，否则后续修正会跳过 diff 确认。
		hint = "[y] 允许修正   [n] 拒绝"
	}
	lines = append(lines, hintStyle.Render(hint))
	return approvalStyle.Width(maxInt(20, width-4)).Render(strings.Join(lines, "\n"))
}

// renderDiffPreview 渲染多行审批预览，+/- 行分别着色，并限制总行数。
func renderDiffPreview(preview string, width int) []string {
	raw := strings.Split(preview, "\n")
	const maxLines = 16
	truncated := len(raw) > maxLines
	if truncated {
		raw = raw[:maxLines]
	}
	out := make([]string, 0, len(raw)+1)
	for _, line := range raw {
		style := dimStyle
		switch {
		case strings.HasPrefix(line, "+"):
			style = okStyle
		case strings.HasPrefix(line, "-"):
			style = errStyle
		}
		for _, l := range wrapText(line, width) {
			out = append(out, style.Render(l))
		}
	}
	if truncated {
		out = append(out, dimStyle.Render("…（差异已截断）"))
	}
	return out
}

func (m *model) statusLine(width int) string {
	state := "idle"
	switch {
	case m.overlay:
		state = "approval"
	case m.streaming && m.thinking:
		state = "思考中"
	case m.streaming:
		state = "working"
	}

	parts := []string{state}
	if m.memReadOnly {
		parts = append(parts, "记忆只读")
	}
	if m.tokensIn+m.tokensOut > 0 {
		parts = append(parts, fmt.Sprintf("%s in / %s out",
			humanTokens(m.tokensIn), humanTokens(m.tokensOut)))
		if m.reasoningTokens > 0 {
			parts = append(parts, "推理 "+humanTokens(m.reasoningTokens))
		}
		if m.cost > 0 {
			parts = append(parts, formatCost(m.cost, m.currency))
		}
	}
	if !m.autoFollow {
		parts = append(parts, "历史浏览中")
	}
	left := strings.Join(parts, " · ")

	hint := "Enter 发送 · Ctrl+J 换行 · PgUp/PgDn 滚动 · Ctrl+C 退出"
	if m.overlay {
		hint = "[y] 允许一次 · [a] 本会话允许 · [n] 拒绝"
	}
	content := left
	gap := width - runewidth.StringWidth(left) - runewidth.StringWidth(hint)
	if gap < 1 {
		if m.overlay {
			hint = "[y] 允许 · [a] 本会话 · [n] 拒绝"
		} else {
			hint = "Enter 发送 · Ctrl+C 退出"
		}
		gap = width - runewidth.StringWidth(left) - runewidth.StringWidth(hint)
	}
	if gap >= 1 {
		content = left + strings.Repeat(" ", gap) + hint
	}
	return statusStyle.Render(padTo(content, width))
}

// padTo 按终端显示宽度补空格或截断，保证整行铺满。
func padTo(s string, width int) string {
	w := runewidth.StringWidth(s)
	if w > width {
		return truncateRunes(s, width)
	}
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

func lineCount(s string) int { return strings.Count(s, "\n") + 1 }

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func previewArgs(args json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(args, &m); err == nil {
		for _, k := range []string{"command", "path", "url"} {
			if v, ok := m[k]; ok {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return truncateRunes(s, 80)
				}
			}
		}
	}
	s := string(args)
	if s == "" {
		return ""
	}
	return truncateRunes(s, 80)
}

// humanTokens 把 token 数压缩成 1.2k / 3.4M 这类易读形式。
func humanTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// formatCost 用货币文本前缀输出成本；小额保留更多小数，避免显示成 0。
// 不用 ¥/$ 符号：终端字体差异下容易显示成方块或宽字符错位。
func formatCost(v float64, currency string) string {
	unit := currency
	if unit == "" {
		unit = "CNY"
	}
	if v < 0.01 {
		return fmt.Sprintf("%s %.5f", unit, v)
	}
	return fmt.Sprintf("%s %.4f", unit, v)
}

func truncateRunes(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > maxWidth {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}

func wrapText(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var out []string
	var b strings.Builder
	w := 0
	for _, r := range s {
		if r == '\n' {
			out = append(out, b.String())
			b.Reset()
			w = 0
			continue
		}
		rw := runewidth.RuneWidth(r)
		if w+rw > width && w > 0 {
			out = append(out, b.String())
			b.Reset()
			w = 0
		}
		b.WriteRune(r)
		w += rw
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	if len(out) == 0 {
		out = append(out, "")
	}
	return out
}
