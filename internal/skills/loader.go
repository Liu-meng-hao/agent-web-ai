// Package skills 发现并渐进加载 skills/<name>/SKILL.md。
//
// 只把名称、描述和适用场景放进 system prompt；完整正文由 skill_load 按需拉取，
// 避免把每个技能都塞进上下文。
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// 技能来源，优先级从高到低。
const (
	SourceProject = "project"
	SourceUser    = "user"
)

const (
	defaultMaxBodyTokens    = 8000
	defaultMaxCatalogTokens = 800
	maxSkillFileBytes       = 256 << 10
	maxDescriptionRunes     = 200
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Options 是技能发现参数。
type Options struct {
	// ProjectRoot 项目级技能目录，通常为 skills/；不存在时跳过。
	ProjectRoot string
	// UserRoot 用户级技能目录，支持 ~ 前缀；不存在时跳过。
	UserRoot string
	// MaxBodyTokens 单次 skill_load 返回正文的预算，默认 8000。
	MaxBodyTokens int
	// MaxCatalogTokens 注入 system prompt 的目录预算，默认 800。
	MaxCatalogTokens int
}

// Skill 是一个已发现的技能。
type Skill struct {
	Name         string
	Description  string
	WhenToUse    string
	Version      string
	AllowedTools []string
	Source       string
	Dir          string

	body      string
	truncated bool
}

// Store 是技能目录的只读快照。
type Store struct {
	skills        map[string]*Skill
	order         []string
	warnings      []string
	maxCatalogTok int
	maxBodyTok    int
}

// Load 扫描项目与用户技能目录。
//
// 同名技能按 project → user 覆盖；非法技能只跳过并记录告警，不影响其他技能。
func Load(opts Options) (*Store, error) {
	if opts.MaxBodyTokens <= 0 {
		opts.MaxBodyTokens = defaultMaxBodyTokens
	}
	if opts.MaxCatalogTokens <= 0 {
		opts.MaxCatalogTokens = defaultMaxCatalogTokens
	}
	s := &Store{skills: map[string]*Skill{}, maxCatalogTok: opts.MaxCatalogTokens, maxBodyTok: opts.MaxBodyTokens}
	roots := []struct {
		path   string
		source string
	}{
		{opts.ProjectRoot, SourceProject},
		{expandHome(opts.UserRoot), SourceUser},
	}
	for _, root := range roots {
		if strings.TrimSpace(root.path) == "" {
			continue
		}
		if err := s.scan(root.path, root.source, opts.MaxBodyTokens); err != nil {
			s.warnings = append(s.warnings, fmt.Sprintf("%s 技能目录不可用：%v", root.source, err))
		}
	}
	sort.Strings(s.order)
	return s, nil
}

func (s *Store) scan(root, source string, maxTokens int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		// 以 . 或 _ 开头的是模板/隐藏目录（如 .example），不参与发现。
		if strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(root, entry.Name())); err == nil && info.IsDir() {
				s.warnings = append(s.warnings, fmt.Sprintf("%s/%s: 符号链接技能目录已跳过（避免越界）", source, entry.Name()))
			}
			continue
		}
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		path := filepath.Join(dir, "SKILL.md")
		info, err := os.Lstat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				s.warnings = append(s.warnings, fmt.Sprintf("%s/%s: 读取 SKILL.md 失败: %v", source, entry.Name(), err))
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			s.warnings = append(s.warnings, fmt.Sprintf("%s/%s: SKILL.md 是符号链接，已跳过", source, entry.Name()))
			continue
		}
		if info.Size() > maxSkillFileBytes {
			s.warnings = append(s.warnings, fmt.Sprintf("%s/%s: SKILL.md 超过 %d KiB，已跳过", source, entry.Name(), maxSkillFileBytes>>10))
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			s.warnings = append(s.warnings, fmt.Sprintf("%s/%s: 读取 SKILL.md 失败: %v", source, entry.Name(), err))
			continue
		}
		sk, err := parseSkill(dir, entry.Name(), string(raw), maxTokens)
		if err != nil {
			s.warnings = append(s.warnings, fmt.Sprintf("%s/%s: %v", source, entry.Name(), err))
			continue
		}
		if prev, ok := s.skills[sk.Name]; ok {
			s.warnings = append(s.warnings, fmt.Sprintf("技能 %q 已由 %s 提供，忽略 %s/%s", sk.Name, prev.Source, source, entry.Name()))
			continue
		}
		sk.Source = source
		s.skills[sk.Name] = sk
		s.order = append(s.order, sk.Name)
	}
	return nil
}

type frontMatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	WhenToUse    string   `yaml:"when_to_use"`
	Version      string   `yaml:"version"`
	AllowedTools []string `yaml:"allowed-tools"`
}

func parseSkill(dir, dirName, raw string, maxTokens int) (*Skill, error) {
	meta, body, ok := splitFrontMatter(raw)
	if !ok {
		return nil, fmt.Errorf("缺少 front matter（文件必须以 --- 开头并有结束行）")
	}
	var fm frontMatter
	if err := yaml.Unmarshal([]byte(meta), &fm); err != nil {
		return nil, fmt.Errorf("front matter 解析失败: %w", err)
	}
	fm.Name = strings.TrimSpace(fm.Name)
	fm.Description = strings.TrimSpace(fm.Description)
	if fm.Name == "" {
		fm.Name = normalizeName(dirName)
	}
	if !namePattern.MatchString(fm.Name) {
		if suggest := normalizeName(fm.Name); suggest != "" {
			return nil, fmt.Errorf("name %q 不合法（只允许小写字母、数字和连字符，最长 64 字符），建议改为 %q", fm.Name, suggest)
		}
		return nil, fmt.Errorf("name %q 不合法（只允许小写字母、数字和连字符，最长 64 字符）", fm.Name)
	}
	if fm.Description == "" {
		return nil, fmt.Errorf("description 不能为空")
	}
	if utf8.RuneCountInString(fm.Description) > maxDescriptionRunes {
		return nil, fmt.Errorf("description 超过 %d 字", maxDescriptionRunes)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("正文为空")
	}
	body, truncated := truncateTokens(body, maxTokens)
	return &Skill{
		Name:         fm.Name,
		Description:  fm.Description,
		WhenToUse:    strings.TrimSpace(fm.WhenToUse),
		Version:      strings.TrimSpace(fm.Version),
		AllowedTools: fm.AllowedTools,
		Dir:          dir,
		body:         body,
		truncated:    truncated,
	}, nil
}

// splitFrontMatter 返回 --- 之间的元数据与正文。
// 只把整行恰好为 --- 的行当作结束行，避免 YAML 块标量里的 --- 提前截断。
func splitFrontMatter(raw string) (meta, body string, ok bool) {
	s := strings.TrimPrefix(raw, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return "", "", false
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), true
		}
	}
	return "", "", false
}

// truncateTokens 按估算 token 截断正文，并保证 UTF-8 边界完整。
func truncateTokens(body string, limit int) (string, bool) {
	ascii, other := 0, 0
	for i, r := range body {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
		if (ascii+3)/4+other > limit {
			return strings.TrimRight(body[:i], " \n") +
				"\n\n…（正文超过 " + fmt.Sprint(limit) + " token 已截断，完整内容请用 skill_read_file 读取 path=\"SKILL.md\"，按 offset/limit 分段）", true
		}
	}
	return body, false
}

// estimateTokens 用与正文截断一致的粗估：ASCII 约 4 字符 1 token，非 ASCII 1 字符 1 token。
func estimateTokens(s string) int {
	ascii, other := 0, 0
	for _, r := range s {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+3)/4 + other
}

// normalizeName 把目录名或非法 name 归一化成合法技能名。
func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := true // 避免开头出现连字符
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 64 {
		out = strings.Trim(out[:64], "-")
	}
	return out
}

func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

// Len 返回技能数量。
func (s *Store) Len() int { return len(s.order) }

// Warnings 返回扫描期被跳过的技能与原因。
func (s *Store) Warnings() []string { return append([]string(nil), s.warnings...) }

// Names 返回排序后的技能名。
func (s *Store) Names() []string { return append([]string(nil), s.order...) }

// Get 按名取技能。
func (s *Store) Get(name string) (*Skill, bool) {
	sk, ok := s.skills[strings.TrimSpace(name)]
	return sk, ok
}

// Body 返回技能正文；不存在时返回 false。
func (s *Store) Body(name string) (string, bool) {
	sk, ok := s.Get(name)
	if !ok {
		return "", false
	}
	return sk.body, true
}

// BodyTokenBudget 返回单次加载正文的 token 预算，供 skill_read_file 共用。
func (s *Store) BodyTokenBudget() int {
	if s.maxBodyTok > 0 {
		return s.maxBodyTok
	}
	return defaultMaxBodyTokens
}

// FullBody 重新从磁盘读取未截断的正文，供 /skill show 展示完整内容。
func (s *Store) FullBody(name string) (string, bool) {
	sk, ok := s.Get(name)
	if !ok {
		return "", false
	}
	raw, err := os.ReadFile(filepath.Join(sk.Dir, "SKILL.md"))
	if err != nil {
		return "", false
	}
	_, body, ok := splitFrontMatter(string(raw))
	if !ok {
		return "", false
	}
	return strings.TrimSpace(body), true
}

// CatalogLines 返回 /skill 面板用的技能列表。
func (s *Store) CatalogLines() []string {
	out := make([]string, 0, len(s.order))
	for _, name := range s.order {
		sk := s.skills[name]
		line := fmt.Sprintf("%s · %s · %s", sk.Name, sk.Source, sk.Description)
		if len(sk.AllowedTools) > 0 {
			line += " · 建议工具（不强制）：" + strings.Join(sk.AllowedTools, ",")
		}
		out = append(out, line)
	}
	return out
}

// CatalogPrompt 生成注入 system prompt 的紧凑目录；没有技能时返回空串。
// 超过预算时依次降级：完整条目 → 技能名 + 适用场景 → 仅名称，仍超限则截断并提示 /skill。
func (s *Store) CatalogPrompt() string {
	if len(s.order) == 0 {
		return ""
	}
	header := "\n可用技能（判断适用时先调用 skill_load 获取完整步骤；同一轮不要重复加载同一技能）：\n"
	var b strings.Builder
	b.WriteString(header)
	used := estimateTokens(header)

	const (
		modeFull = iota
		modeBrief
		modeNames
	)
	mode := modeFull
	for i, name := range s.order {
		sk := s.skills[name]
		switch mode {
		case modeFull:
			entry := fmt.Sprintf("- %s：%s", sk.Name, sk.Description)
			if sk.WhenToUse != "" {
				entry += "（适用：" + sk.WhenToUse + "）"
			}
			entry += "\n"
			if used+estimateTokens(entry) <= s.maxCatalogTok {
				b.WriteString(entry)
				used += estimateTokens(entry)
				continue
			}
			mode = modeBrief
			if !writeCatalogHint(&b, &used, s.maxCatalogTok,
				"（目录超出预算，以下为精简条目：技能名 + 适用场景，详情用 /skill 查看）\n", len(s.order)-i) {
				return b.String()
			}
			fallthrough
		case modeBrief:
			entry := "- " + sk.Name
			if trig := truncateRunes(sk.WhenToUse, 24); trig != "" {
				entry += "：" + trig
			}
			entry += "\n"
			if used+estimateTokens(entry) <= s.maxCatalogTok {
				b.WriteString(entry)
				used += estimateTokens(entry)
				continue
			}
			mode = modeNames
			if !writeCatalogHint(&b, &used, s.maxCatalogTok,
				"（目录超出预算，以下仅列名称）\n", len(s.order)-i) {
				return b.String()
			}
			fallthrough
		case modeNames:
			entry := "- " + sk.Name + "\n"
			if used+estimateTokens(entry) > s.maxCatalogTok {
				fmt.Fprintf(&b, "…（还有 %d 个技能，用 /skill 查看）\n", len(s.order)-i)
				return b.String()
			}
			b.WriteString(entry)
			used += estimateTokens(entry)
		}
	}
	return b.String()
}

// writeCatalogHint 在预算内写入降级提示；放不下时改写“还有 N 个技能”并返回 false。
func writeCatalogHint(b *strings.Builder, used *int, budget int, hint string, remaining int) bool {
	if *used+estimateTokens(hint) > budget {
		fmt.Fprintf(b, "…（还有 %d 个技能，用 /skill 查看）\n", remaining)
		return false
	}
	b.WriteString(hint)
	*used += estimateTokens(hint)
	return true
}

// truncateRunes 截断到 n 个字符，并去掉尾部标点。
func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return strings.TrimRight(string(r[:n]), "，。；;、 ") + "…"
}
