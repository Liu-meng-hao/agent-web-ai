package tools

// truncateMiddle 保留头尾、丢弃中间，避免超长输出撑爆上下文。
// 按 rune 截断，防止切断 UTF-8 字符。
func truncateMiddle(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	head := maxRunes * 2 / 3
	tail := maxRunes - head
	return string(runes[:head]) + "\n… [中间部分已截断] …\n" + string(runes[len(runes)-tail:])
}
