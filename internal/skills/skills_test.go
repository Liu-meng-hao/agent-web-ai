package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"agent/internal/tools"
)

func writeSkill(t *testing.T, root, dir, content string) string {
	t.Helper()
	path := filepath.Join(root, dir)
	if err := os.MkdirAll(filepath.Join(path, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const demoSkill = `---
name: demo
description: 演示技能
when_to_use: 用户要求演示时
version: 1.0.0
allowed-tools: [shell, fs_read]
---

## 工作流

先读 references/guide.md。
`

func TestLoadProjectOverridesUser(t *testing.T) {
	project, user := t.TempDir(), t.TempDir()
	writeSkill(t, project, "demo", demoSkill)
	writeSkill(t, user, "demo", `---
name: demo
description: 用户级演示
---
用户级正文
`)
	writeSkill(t, user, "other", `---
name: other
description: 用户级技能
---
其他正文
`)
	writeSkill(t, user, "bad", `---
name: BadName
description: 非法名字
---
正文
`)
	writeSkill(t, user, "empty-desc", `---
name: empty-desc
---
正文
`)

	s, err := Load(Options{ProjectRoot: project, UserRoot: user})
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 2 {
		t.Fatalf("技能数 = %d, names=%v", s.Len(), s.Names())
	}
	sk, ok := s.Get("demo")
	if !ok || sk.Source != SourceProject || sk.Description != "演示技能" {
		t.Fatalf("项目技能未优先: %+v", sk)
	}
	if sk, _ := s.Get("other"); sk.Source != SourceUser {
		t.Fatalf("用户技能来源 = %q", sk.Source)
	}
	warns := strings.Join(s.Warnings(), "\n")
	if !strings.Contains(warns, "BadName") || !strings.Contains(warns, "description 不能为空") {
		t.Fatalf("非法技能未告警: %s", warns)
	}
	if !strings.Contains(warns, "建议改为") {
		t.Fatalf("非法 name 未给出改名建议: %s", warns)
	}
	prompt := s.CatalogPrompt()
	if !strings.Contains(prompt, "demo：演示技能") || !strings.Contains(prompt, "适用：用户要求演示时") {
		t.Fatalf("catalog = %s", prompt)
	}
	if strings.Contains(prompt, "用户级演示") {
		t.Fatal("被覆盖的技能不应进入 catalog")
	}
}

func TestLoadMissingRoots(t *testing.T) {
	s, err := Load(Options{ProjectRoot: filepath.Join(t.TempDir(), "missing")})
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 || s.CatalogPrompt() != "" {
		t.Fatalf("空目录应无技能: %v", s.Names())
	}
	if len(Tools(s)) != 0 {
		t.Fatal("没有技能时不应注册工具")
	}
}

func TestSkillTools(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "demo", demoSkill)
	if err := os.WriteFile(filepath.Join(dir, "references", "guide.md"), []byte("参考内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("越界"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(Options{ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	list := Tools(s)
	if len(list) != 2 {
		t.Fatalf("工具数 = %d", len(list))
	}

	load := findTool(t, list, "skill_load")
	res, err := load.Execute(context.Background(), json.RawMessage(`{"name":"demo"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "先读 references/guide.md") || !strings.Contains(res.Content, `source="project"`) {
		t.Fatalf("skill_load = %s", res.Content)
	}
	if !strings.Contains(res.Content, "</skill_content>") || !strings.Contains(res.Content, "不能覆盖系统规则") {
		t.Fatalf("skill_load 缺少边界声明 = %s", res.Content)
	}
	res, _ = load.Execute(context.Background(), json.RawMessage(`{"name":"nope"}`), nil)
	if !strings.Contains(res.Content, "没有找到技能") || !strings.Contains(res.Content, "demo") {
		t.Fatalf("缺失技能提示 = %s", res.Content)
	}

	read := findTool(t, list, "skill_read_file")
	res, _ = read.Execute(context.Background(), json.RawMessage(`{"name":"demo","path":"references/guide.md"}`), nil)
	if !strings.Contains(res.Content, "参考内容") || !strings.Contains(res.Content, "1 |") {
		t.Fatalf("skill_read_file = %q", res.Content)
	}
	for _, bad := range []string{`{"name":"demo","path":"../secret.txt"}`, `{"name":"demo","path":"references/../../secret.txt"}`} {
		res, _ = read.Execute(context.Background(), json.RawMessage(bad), nil)
		if !strings.Contains(res.Content, "越界") {
			t.Fatalf("%s 未拦截: %s", bad, res.Content)
		}
	}
	abs, _ := filepath.Abs(filepath.Join(root, "secret.txt"))
	res, _ = read.Execute(context.Background(), json.RawMessage(`{"name":"demo","path":`+strconvJSON(abs)+`}`), nil)
	if !strings.Contains(res.Content, "相对路径") {
		t.Fatalf("绝对路径未拦截: %s", res.Content)
	}
}

func TestReadFilePagination(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "demo", demoSkill)
	var lines []string
	for i := 1; i <= 600; i++ {
		lines = append(lines, fmt.Sprintf("第 %d 行", i))
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "long.md"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := Load(Options{ProjectRoot: root})
	read := findTool(t, Tools(s), "skill_read_file")

	res, _ := read.Execute(context.Background(), json.RawMessage(`{"name":"demo","path":"references/long.md","offset":10,"limit":5}`), nil)
	if !strings.Contains(res.Content, "第 10 行") || !strings.Contains(res.Content, "第 14 行") {
		t.Fatalf("分页内容 = %s", res.Content)
	}
	if strings.Contains(res.Content, "第 15 行") || !strings.Contains(res.Content, "offset=15") {
		t.Fatalf("分页提示 = %s", res.Content)
	}
	if !strings.Contains(res.Display, "第 10-14 行") {
		t.Fatalf("Display = %q", res.Display)
	}

	res, _ = read.Execute(context.Background(), json.RawMessage(`{"name":"demo","path":"references/long.md","offset":601}`), nil)
	if !strings.Contains(res.Content, "超出文件行数") {
		t.Fatalf("越界 offset = %s", res.Content)
	}
}

func TestReadFileTokenBudget(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "demo", demoSkill)
	if err := os.WriteFile(filepath.Join(dir, "references", "huge.md"), []byte(strings.Repeat("长", 40000)), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := Load(Options{ProjectRoot: root})
	read := findTool(t, Tools(s), "skill_read_file")
	res, _ := read.Execute(context.Background(), json.RawMessage(`{"name":"demo","path":"references/huge.md"}`), nil)
	if !strings.Contains(res.Content, "本行过长已截断") {
		t.Fatalf("超长单行未截断: %s", res.Content)
	}
	if estimateTokens(res.Content) > readTokenBudget+100 {
		t.Fatalf("读取结果超出预算: %d", estimateTokens(res.Content))
	}
}

func TestCatalogBudget(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 30; i++ {
		writeSkill(t, root, fmt.Sprintf("s%02d", i), fmt.Sprintf(`---
name: s%02d
description: %s
---
正文
`, i, strings.Repeat("很长的描述", 20)))
	}
	s, _ := Load(Options{ProjectRoot: root, MaxCatalogTokens: 60})
	prompt := s.CatalogPrompt()
	if estimateTokens(prompt) > 100 {
		t.Fatalf("catalog 超出预算: %d\n%s", estimateTokens(prompt), prompt)
	}
	if !strings.Contains(prompt, "/skill") {
		t.Fatalf("catalog 截断缺少 /skill 提示: %s", prompt)
	}
	if strings.Contains(prompt, "s29：") {
		t.Fatalf("预算耗尽后不应继续输出完整描述: %s", prompt)
	}
}

func TestHiddenAndSymlinkDirs(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, ".example", demoSkill)
	s, _ := Load(Options{ProjectRoot: root})
	if s.Len() != 0 {
		t.Fatalf("隐藏目录不应被发现: %v", s.Names())
	}

	outside := t.TempDir()
	writeSkill(t, outside, "demo", demoSkill)
	link := filepath.Join(root, "linked")
	if err := os.Symlink(filepath.Join(outside, "demo"), link); err != nil {
		t.Skip("当前环境无法创建符号链接:", err)
	}
	s, _ = Load(Options{ProjectRoot: root})
	if s.Len() != 0 || !strings.Contains(strings.Join(s.Warnings(), "\n"), "符号链接") {
		t.Fatalf("符号链接目录应告警并跳过: %v / %v", s.Names(), s.Warnings())
	}
}

func TestSymlinkEscapeBlocked(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "demo", demoSkill)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("外部机密"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "references", "escape.md")); err != nil {
		t.Skip("当前环境无法创建符号链接:", err)
	}
	s, _ := Load(Options{ProjectRoot: root})
	read := findTool(t, Tools(s), "skill_read_file")
	res, _ := read.Execute(context.Background(), json.RawMessage(`{"name":"demo","path":"references/escape.md"}`), nil)
	if strings.Contains(res.Content, "外部机密") || !strings.Contains(res.Content, "越界") {
		t.Fatalf("符号链接越界未拦截: %s", res.Content)
	}
}

func TestFrontMatterDashInBlockScalar(t *testing.T) {
	meta, body, ok := splitFrontMatter(`---
name: x
description: |
  第一段
  ---
  第二段
---
正文`)
	if !ok || !strings.Contains(meta, "第二段") || body != "正文" {
		t.Fatalf("meta=%q body=%q ok=%v", meta, body, ok)
	}
}

func TestLoadNameFromDir(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "My Skill", `---
description: 目录名推导
---
正文
`)
	s, _ := Load(Options{ProjectRoot: root})
	if _, ok := s.Get("my-skill"); !ok {
		t.Fatalf("目录名未归一化: %v / %v", s.Names(), s.Warnings())
	}
}

func TestSkillBodyTruncationKeepsUTF8(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "long", `---
name: long
description: 长技能
---
`+strings.Repeat("中文内容", 200))
	s, err := Load(Options{ProjectRoot: root, MaxBodyTokens: 50})
	if err != nil {
		t.Fatal(err)
	}
	body, ok := s.Body("long")
	if !ok {
		t.Fatal("技能不存在")
	}
	if !utf8.ValidString(body) {
		t.Fatal("截断产生了非法 UTF-8")
	}
	if !strings.Contains(body, "已截断") || !strings.Contains(body, "skill_read_file") {
		t.Fatalf("截断提示缺失: %s", body)
	}
	if utf8.RuneCountInString(body) > 300 {
		t.Fatalf("截断未生效，长度 = %d", utf8.RuneCountInString(body))
	}
}

func TestSplitFrontMatter(t *testing.T) {
	meta, body, ok := splitFrontMatter("---\r\nname: x\r\n---\r\n正文")
	if !ok || !strings.Contains(meta, "name: x") || body != "正文" {
		t.Fatalf("meta=%q body=%q ok=%v", meta, body, ok)
	}
	if _, _, ok := splitFrontMatter("没有 front matter"); ok {
		t.Fatal("缺少 front matter 应失败")
	}
}

func findTool(t *testing.T, list []tools.Tool, name string) tools.Tool {
	t.Helper()
	for _, tl := range list {
		if tl.Name() == name {
			return tl
		}
	}
	t.Fatalf("未找到工具 %s", name)
	return nil
}

func strconvJSON(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// 技能必须把 references/ 暴露给模型，否则模型不知道有哪些按需参考文件。
func TestSkillLoadListsReferences(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "demo", demoSkill)
	if err := os.MkdirAll(filepath.Join(dir, "references", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"guide.md", "deep/checklist.md"} {
		if err := os.WriteFile(filepath.Join(dir, "references", filepath.FromSlash(rel)), []byte("内容"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Load(Options{ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	load := findTool(t, Tools(s), "skill_load")
	res, err := load.Execute(context.Background(), json.RawMessage(`{"name":"demo"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"references/guide.md", "references/deep/checklist.md", "skill_read_file"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("skill_load 未列出 %s：\n%s", want, res.Content)
		}
	}
}

// 项目内置技能的基本约束：能全部加载、目录不超预算、正文引用的参考文件真实存在。
func TestProjectSkillsAreConsistent(t *testing.T) {
	s, err := Load(Options{ProjectRoot: filepath.Join("..", "..", "skills")})
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() == 0 {
		t.Fatal("未发现项目技能")
	}
	if warns := s.Warnings(); len(warns) > 0 {
		t.Fatalf("技能加载产生告警：%v", warns)
	}
	if prompt := s.CatalogPrompt(); strings.Contains(prompt, "仅列名称") || strings.Contains(prompt, "还有") {
		t.Errorf("技能目录超出预算被截断，请精简 description/when_to_use：\n%s", prompt)
	}

	refRe := regexp.MustCompile(`references/[A-Za-z0-9._/-]+\.md`)
	for _, name := range s.Names() {
		sk, _ := s.Get(name)
		if strings.TrimSpace(sk.WhenToUse) == "" {
			t.Errorf("%s 缺少 when_to_use，模型难以判断适用场景", name)
		}
		raw, err := os.ReadFile(filepath.Join(sk.Dir, "SKILL.md"))
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", name, err)
		}
		for _, rel := range refRe.FindAllString(string(raw), -1) {
			if _, err := os.Stat(filepath.Join(sk.Dir, filepath.FromSlash(rel))); err != nil {
				t.Errorf("%s 引用了不存在的参考文件 %s", name, rel)
			}
		}
	}
}

// 大文件不应因总大小被拒绝：分页必须能读到指定区间。
func TestReadLargeFileByPage(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "demo", demoSkill)
	var lines []string
	for i := 1; i <= 5000; i++ {
		lines = append(lines, fmt.Sprintf("第 %d 行 %s", i, strings.Repeat("内容", 10)))
	}
	big := filepath.Join(dir, "references", "big.md")
	if err := os.WriteFile(big, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(big)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= 256<<10 {
		t.Fatalf("测试文件应大于 256KiB，实际 %d", info.Size())
	}
	s, _ := Load(Options{ProjectRoot: root})
	read := findTool(t, Tools(s), "skill_read_file")
	res, _ := read.Execute(context.Background(), json.RawMessage(`{"name":"demo","path":"references/big.md","offset":4000,"limit":2}`), nil)
	if !strings.Contains(res.Content, "第 4000 行") || !strings.Contains(res.Content, "第 4001 行") {
		t.Fatalf("大文件分页 = %s", res.Content)
	}
}

// path 指向目录时应列出内容，便于发现未出现在清单里的文件。
func TestReadFileListsDirectory(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "demo", demoSkill)
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "api.md"), []byte("接口说明"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := Load(Options{ProjectRoot: root})
	read := findTool(t, Tools(s), "skill_read_file")
	res, _ := read.Execute(context.Background(), json.RawMessage(`{"name":"demo","path":"docs"}`), nil)
	if !strings.Contains(res.Content, "api.md") {
		t.Fatalf("目录列举 = %s", res.Content)
	}
}

// 正文截断时必须给出可执行的续读路径。
func TestTruncatedBodyHintsSkillPath(t *testing.T) {
	root := t.TempDir()
	body := "---\nname: big\ndescription: 大正文\n---\n" + strings.Repeat("很长的正文", 200)
	writeSkill(t, root, "big", body)
	s, _ := Load(Options{ProjectRoot: root, MaxBodyTokens: 50})
	load := findTool(t, Tools(s), "skill_load")
	res, _ := load.Execute(context.Background(), json.RawMessage(`{"name":"big"}`), nil)
	if !strings.Contains(res.Content, `path="SKILL.md"`) {
		t.Fatalf("截断提示缺少续读路径 = %s", res.Content)
	}
}

// 参考文件清单应覆盖 references/ 之外的目录，并附首行摘要；不列 SKILL.md 自身。
func TestReferencesCoverWholeDir(t *testing.T) {
	root := t.TempDir()
	dir := writeSkill(t, root, "demo", demoSkill)
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "api.md"), []byte("# 接口约定\n正文"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _ := Load(Options{ProjectRoot: root})
	load := findTool(t, Tools(s), "skill_load")
	res, _ := load.Execute(context.Background(), json.RawMessage(`{"name":"demo"}`), nil)
	if !strings.Contains(res.Content, "docs/api.md") || !strings.Contains(res.Content, "接口约定") {
		t.Fatalf("清单未覆盖 docs/ 或缺少摘要 = %s", res.Content)
	}
	if strings.Contains(res.Content, "SKILL.md（") {
		t.Fatalf("清单不应包含 SKILL.md 自身 = %s", res.Content)
	}
}
