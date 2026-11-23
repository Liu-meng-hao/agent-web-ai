package artifact

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Issue 是自检发现的一条问题。
// Blocking 为 false 时只作为提示，不计入连续失败，也不会把页面标成 needs_review。
type Issue struct {
	Text     string
	Blocking bool
}

var (
	strictPlaceholderPattern = regexp.MustCompile(`(?i)lorem ipsum|example\.com|占位符|待补充`)
	todoPattern              = regexp.MustCompile(`(?i)\bTODO\b`)
	externalResourcePattern  = regexp.MustCompile(`(?is)<(?:script|img|link|source|video|audio|iframe|embed|object|track|input|image|use)\b[^>]*\b(?:src|srcset|href|poster|data)\s*=\s*["']?\s*(?:https?:)?//`)
	cssExternalPattern       = regexp.MustCompile(`(?i)(?:url\(\s*["']?\s*(?:https?:)?//)|(?:@import\s+(?:url\()?\s*["']?\s*(?:https?:)?//)`)
	anchorExternalPattern    = regexp.MustCompile(`(?is)<a\b[^>]*\bhref\s*=\s*["']\s*(?:https?:)?//`)
	resourcePattern          = regexp.MustCompile(`(?i)(?:src|href)\s*=\s*["']([^"']+)["']`)
	langPattern              = regexp.MustCompile(`(?i)<html\b[^>]*\blang\s*=`)
)

var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// optionalEndElements 是 HTML 允许省略闭合标签的元素。
// 栈式校验必须认可它们，否则 <ul><li>a<li>b</ul> 这类合法写法会被误报。
var optionalEndElements = map[string]bool{
	"li": true, "dt": true, "dd": true, "p": true, "rt": true, "rp": true,
	"optgroup": true, "option": true,
	"thead": true, "tbody": true, "tfoot": true, "tr": true, "td": true, "th": true,
	"colgroup": true, "caption": true,
}

// closesParagraph 是会让未闭合 <p> 隐式结束的块级标签。
var closesParagraph = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true, "details": true,
	"div": true, "dl": true, "fieldset": true, "figcaption": true, "figure": true,
	"footer": true, "form": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "header": true, "hgroup": true, "hr": true, "main": true,
	"menu": true, "nav": true, "ol": true, "pre": true, "section": true, "table": true,
	"ul": true,
}

// Check 是写后自检：标签闭合、外链、占位内容、缺失的本地资源。
// 返回空切片表示通过；问题会回灌给模型自修。
func Check(content, dir string) []string {
	detailed := CheckDetailed(content, dir)
	out := make([]string, 0, len(detailed))
	for _, issue := range detailed {
		out = append(out, issue.Text)
	}
	return out
}

// CheckDetailed 在 Check 的基础上标记问题是否阻塞。
// 缺失资源、TODO、导航型外链只提示，避免多文件写入顺序造成误报。
func CheckDetailed(content, dir string) []Issue {
	var issues []Issue
	for _, text := range checkTags(content) {
		issues = append(issues, Issue{Text: text, Blocking: true})
	}

	if m := strictPlaceholderPattern.FindString(content); m != "" {
		issues = append(issues, Issue{
			Text:     fmt.Sprintf("发现占位内容 %q，成品页不能保留 lorem ipsum / example.com / 待补充", m),
			Blocking: true,
		})
	}
	if m := todoPattern.FindString(content); m != "" {
		issues = append(issues, Issue{
			Text: fmt.Sprintf("提示：页面中仍有 %q，确认是否为有意文案", m),
		})
	}
	if m := externalResourcePattern.FindString(content); m != "" {
		issues = append(issues, Issue{
			Text:     fmt.Sprintf("发现外链资源 %q；成品页必须内联样式、脚本、字体和图片", m),
			Blocking: true,
		})
	}
	if m := cssExternalPattern.FindString(content); m != "" {
		issues = append(issues, Issue{
			Text:     fmt.Sprintf("发现 CSS 外链 %q；成品页必须内联样式与字体", m),
			Blocking: true,
		})
	}
	if m := anchorExternalPattern.FindString(content); m != "" {
		issues = append(issues, Issue{
			Text: fmt.Sprintf("提示：发现导航型外链 %q，不影响页面自包含", m),
		})
	}
	if strings.Contains(strings.ToLower(content), "<html") && !langPattern.MatchString(content) {
		issues = append(issues, Issue{Text: "<html> 缺少 lang 属性", Blocking: true})
	}

	for _, m := range resourcePattern.FindAllStringSubmatch(content, -1) {
		ref := strings.TrimSpace(m[1])
		if ref == "" || strings.HasPrefix(ref, "#") {
			continue
		}
		lower := strings.ToLower(ref)
		skip := false
		for _, prefix := range []string{"data:", "mailto:", "tel:", "javascript:", "http:", "https:", "//"} {
			if strings.HasPrefix(lower, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		if strings.HasPrefix(ref, "/") {
			issues = append(issues, Issue{
				Text:     fmt.Sprintf("资源 %q 使用绝对路径，脱离预览服务会失效", ref),
				Blocking: true,
			})
			continue
		}
		clean := strings.SplitN(strings.SplitN(ref, "?", 2)[0], "#", 2)[0]
		if clean == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(clean))); err != nil {
			issues = append(issues, Issue{
				Text: fmt.Sprintf("提示：本地资源不存在：%s（若稍后写入可忽略）", ref),
			})
		}
	}
	return dedupeIssues(issues)
}

func checkTags(content string) []string {
	var issues []string
	var stack []string
	for i := 0; i < len(content); {
		if strings.HasPrefix(content[i:], "<!--") {
			end := strings.Index(content[i+4:], "-->")
			if end < 0 {
				return append(issues, "HTML 注释未闭合")
			}
			i += 4 + end + 3
			continue
		}
		if content[i] != '<' {
			i++
			continue
		}
		if i+1 < len(content) && !isTagStart(content[i+1]) {
			i++
			continue
		}
		end := tagEnd(content, i+1)
		if end < 0 {
			return append(issues, "存在未闭合的 < 标签")
		}
		raw := strings.TrimSpace(content[i+1 : end])
		i = end + 1
		if raw == "" || raw[0] == '!' || raw[0] == '?' {
			continue
		}
		closing := raw[0] == '/'
		if closing {
			raw = strings.TrimSpace(raw[1:])
		}
		name := tagName(raw)
		if name == "" {
			continue
		}
		if closing {
			if voidElements[name] {
				continue
			}
			idx := -1
			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j] == name {
					idx = j
					break
				}
			}
			if idx < 0 {
				if !optionalEndElements[name] {
					issues = append(issues, fmt.Sprintf("多余的闭合标签 </%s>", name))
				}
				continue
			}
			if idx != len(stack)-1 {
				bad := false
				for _, open := range stack[idx+1:] {
					if !optionalEndElements[open] {
						bad = true
						break
					}
				}
				if bad {
					issues = append(issues, fmt.Sprintf("标签嵌套错误：<%s> 内出现 </%s>", stack[len(stack)-1], name))
				}
			}
			stack = stack[:idx]
			continue
		}
		if strings.HasSuffix(raw, "/") || voidElements[name] {
			continue
		}
		for len(stack) > 0 && shouldImplicitlyClose(stack[len(stack)-1], name) {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, name)
		if name == "script" || name == "style" {
			closeTag := "</" + name
			idx := strings.Index(strings.ToLower(content[i:]), closeTag)
			if idx < 0 {
				issues = append(issues, fmt.Sprintf("<%s> 未闭合", name))
				stack = stack[:len(stack)-1]
				continue
			}
			i += idx
		}
	}
	for _, name := range stack {
		if optionalEndElements[name] {
			continue
		}
		issues = append(issues, fmt.Sprintf("标签未闭合：<%s>", name))
	}
	return issues
}

func shouldImplicitlyClose(top, name string) bool {
	switch name {
	case "li":
		return top == "li"
	case "dt", "dd":
		return top == "dt" || top == "dd"
	case "p":
		return top == "p"
	case "td", "th":
		return top == "td" || top == "th"
	case "tr":
		return top == "tr"
	case "thead", "tbody", "tfoot":
		return top == "thead" || top == "tbody" || top == "tfoot"
	case "option":
		return top == "option"
	case "optgroup":
		return top == "optgroup" || top == "option"
	case "rt", "rp":
		return top == "rt" || top == "rp"
	}
	return closesParagraph[name] && top == "p"
}

func isTagStart(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '/' || b == '!' || b == '?'
}

// tagEnd 找到标签结尾，跳过属性值里的 >。
func tagEnd(s string, start int) int {
	var quote byte
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '\'', '"':
			if quote == 0 {
				quote = s[i]
			} else if quote == s[i] {
				quote = 0
			}
		case '>':
			if quote == 0 {
				return i
			}
		}
	}
	return -1
}

func tagName(raw string) string {
	raw = strings.TrimSpace(raw)
	end := 0
	for end < len(raw) {
		c := raw[end]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '/' {
			break
		}
		end++
	}
	return strings.ToLower(raw[:end])
}

func dedupeIssues(in []Issue) []Issue {
	seen := map[string]bool{}
	out := in[:0]
	for _, issue := range in {
		if seen[issue.Text] {
			continue
		}
		seen[issue.Text] = true
		out = append(out, issue)
	}
	if len(out) > 8 {
		extra := 0
		for _, issue := range out[8:] {
			if issue.Blocking {
				extra++
			}
		}
		out = append(out[:8], Issue{Text: fmt.Sprintf("…（另有 %d 项问题）", len(out)-8), Blocking: extra > 0})
	}
	return out
}
