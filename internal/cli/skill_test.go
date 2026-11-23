package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent/internal/skills"
	"agent/internal/tools"
)

func TestSkillReloadReplacesTools(t *testing.T) {
	root := t.TempDir()
	b := &built{
		registry:  tools.NewRegistry(),
		skillOpts: skills.Options{ProjectRoot: root},
	}
	if _, err := b.SkillReload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.registry.Get("skill_load"); ok {
		t.Fatal("空目录不应注册技能工具")
	}

	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: demo\ndescription: 重载测试\n---\n正文\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	msg, err := b.SkillReload()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "1 个") {
		t.Fatalf("重载结果 = %q", msg)
	}
	if _, ok := b.registry.Get("skill_load"); !ok {
		t.Fatal("新增技能后未注册 skill_load")
	}
	if _, ok := b.registry.Get("skill_read_file"); !ok {
		t.Fatal("新增技能后未注册 skill_read_file")
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SkillReload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.registry.Get("skill_load"); ok {
		t.Fatal("删除技能后旧工具未移除")
	}
}
