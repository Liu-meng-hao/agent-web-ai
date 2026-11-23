package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent/internal/tools"
)

// 手工区里以 # 开头的事实不能被当成标题丢掉。
func TestReadManualKeepsHashFacts(t *testing.T) {
	md := manualBegin + "\n- #123 工单需要保留\n## 分组标题\n- 普通事实\n" + manualEnd
	lines, found := readManual(md)
	if !found {
		t.Fatal("手工区未识别")
	}
	if len(lines) != 2 || lines[0] != "#123 工单需要保留" || lines[1] != "普通事实" {
		t.Fatalf("手工区解析 = %#v", lines)
	}
}

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, root
}

func countFacts(t *testing.T, root string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, factsFile))
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func TestWriteSupersedesSameKey(t *testing.T) {
	s, root := newStore(t)

	first := NewFact("本仓库使用 npm")
	first.Key = "tooling.pm"
	if _, changed, err := s.Write(first); err != nil || !changed {
		t.Fatalf("首次写入失败: %v changed=%v", err, changed)
	}

	second := NewFact("本仓库使用 pnpm")
	second.Key = "tooling.pm"
	saved, changed, err := s.Write(second)
	if err != nil || !changed {
		t.Fatalf("第二次写入失败: %v changed=%v", err, changed)
	}
	if saved.Supersedes == "" {
		t.Fatal("应记录 supersedes")
	}
	if got := s.Count(); got != 1 {
		t.Fatalf("活跃事实 = %d, want 1", got)
	}

	md := s.Render()
	if !strings.Contains(md, "pnpm") || strings.Contains(md, "使用 npm") {
		t.Fatalf("MEMORY.md 未只保留新事实:\n%s", md)
	}
	if n := countFacts(t, root); n != 3 { // 初版 + superseded 快照 + 新版
		t.Fatalf("facts.jsonl 行数 = %d, want 3", n)
	}
}

func TestWriteDuplicateIsNoop(t *testing.T) {
	s, root := newStore(t)
	f := NewFact("默认 shell 是 bash")
	f.Key = "shell.default"
	if _, _, err := s.Write(f); err != nil {
		t.Fatal(err)
	}
	before := countFacts(t, root)

	dup := NewFact("默认 shell 是 bash")
	dup.Key = "shell.default"
	if _, changed, err := s.Write(dup); err != nil || changed {
		t.Fatalf("重复事实不应写入: changed=%v err=%v", changed, err)
	}
	if after := countFacts(t, root); after != before {
		t.Fatalf("行数从 %d 变成 %d", before, after)
	}
}

func TestManualSectionSurvivesRebuild(t *testing.T) {
	s, root := newStore(t)
	md := "# 长期记忆\n\n" + manualBegin + "\n- 用户偏好用 Go 写工具\n- 不要用 PowerShell\n" + manualEnd + "\n"
	if err := os.WriteFile(filepath.Join(root, memoryFile), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Rebuild(); err != nil {
		t.Fatal(err)
	}

	got := s.Render()
	if !strings.Contains(got, "用户偏好用 Go 写工具") || !strings.Contains(got, "不要用 PowerShell") {
		t.Fatalf("手工区内容丢失:\n%s", got)
	}
	if len(s.Search("PowerShell", 5, "")) == 0 {
		t.Fatal("手工区事实应可被检索")
	}
}

func TestRecallChineseAndScope(t *testing.T) {
	s, _ := newStore(t)

	base := NewFact("本项目的默认 shell 是 bash")
	base.Key = "shell.default"
	base.Scope = ScopeProject
	if _, _, err := s.Write(base); err != nil {
		t.Fatal(err)
	}

	sessionOnly := NewFact("这次会话的临时约定是只读模式")
	sessionOnly.Scope = ScopeSession
	sessionOnly.Source.Session = "s1"
	if _, _, err := s.Write(sessionOnly); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	if got := s.Recall("默认 shell 用的是什么", "s1", 5, 2000, 90, now); !strings.Contains(got, "bash") {
		t.Fatalf("中文召回失败: %q", got)
	}
	if got := s.Recall("临时约定", "s2", 5, 2000, 90, now); strings.Contains(got, "只读模式") {
		t.Fatalf("session 事实不应泄漏到其它会话: %q", got)
	}
	if got := s.Recall("临时约定", "s1", 5, 2000, 90, now); !strings.Contains(got, "只读模式") {
		t.Fatalf("本会话应能召回 session 事实: %q", got)
	}
}

func TestForgetArchives(t *testing.T) {
	s, _ := newStore(t)
	f := NewFact("过期的约定")
	if _, _, err := s.Write(f); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(f.ID); err != nil {
		t.Fatal(err)
	}
	if s.Count() != 0 {
		t.Fatal("归档后不应计入活跃事实")
	}
	if strings.Contains(s.Render(), "过期的约定") {
		t.Fatal("归档事实不应出现在 MEMORY.md")
	}
}

func TestOpenRejectsSecondWriter(t *testing.T) {
	s, root := newStore(t)
	if _, err := Open(root); err == nil {
		t.Fatal("第二个写者应被拒绝")
	}
	_ = s.Close()
	s2, err := Open(root)
	if err != nil {
		t.Fatalf("关闭后应能重新打开: %v", err)
	}
	_ = s2.Close()
}

// TestOriginRules 守住覆盖优先级：模型显式写入可以修正自动抽取，
// 自动抽取不能覆盖模型/人工维护的事实。
func TestOriginRules(t *testing.T) {
	s, _ := newStore(t)

	auto := NewFact("自动抽取的旧值")
	auto.Key = "k1"
	auto.Origin = OriginAuto
	if _, _, err := s.Write(auto); err != nil {
		t.Fatal(err)
	}

	tool := NewFact("模型显式修正后的新值")
	tool.Key = "k1"
	tool.Origin = OriginTool
	saved, changed, err := s.Write(tool)
	if err != nil || !changed {
		t.Fatalf("模型应能覆盖自动事实: err=%v changed=%v", err, changed)
	}
	if saved.Supersedes == "" {
		t.Fatal("应记录被取代的旧事实")
	}

	tool2 := NewFact("模型写入的可信事实")
	tool2.Key = "k2"
	tool2.Origin = OriginTool
	if _, _, err := s.Write(tool2); err != nil {
		t.Fatal(err)
	}

	auto2 := NewFact("自动抽取试图覆盖可信事实")
	auto2.Key = "k2"
	auto2.Origin = OriginAuto
	if _, _, err := s.Write(auto2); err == nil {
		t.Fatal("自动抽取不应覆盖模型写入的事实")
	}
}

func TestKeylessDuplicateIsMerged(t *testing.T) {
	s, _ := newStore(t)
	for i := 0; i < 2; i++ {
		if _, _, err := s.Write(NewFact("本机 Node 版本是 v20.19.5")); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Count(); got != 1 {
		t.Fatalf("无 key 的重复事实应合并，当前活跃 %d 条", got)
	}
}

// TestManualDeletionArchives 守住“手改 MEMORY.md 就是修正/删除记忆”的约定。
func TestManualDeletionArchives(t *testing.T) {
	s, root := newStore(t)
	md := manualBegin + "\n- 用户喜欢深色主题\n- 不要用 PowerShell\n" + manualEnd + "\n"
	if err := os.WriteFile(filepath.Join(root, memoryFile), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if got := s.Count(); got != 2 {
		t.Fatalf("导入后活跃 %d 条, want 2", got)
	}

	md2 := manualBegin + "\n- 用户喜欢深色主题\n" + manualEnd + "\n"
	if err := os.WriteFile(filepath.Join(root, memoryFile), []byte(md2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if got := s.Count(); got != 1 {
		t.Fatalf("删除手工行后活跃 %d 条, want 1", got)
	}
	if len(s.Search("PowerShell", 5, "")) != 0 {
		t.Fatal("已删除的手工事实不应还能被检索")
	}
}

// TestSessionScopeTagging 守住 session 作用域的写入标注与跨会话隔离。
func TestSessionScopeTagging(t *testing.T) {
	s, _ := newStore(t)
	tool := Tools(s)[0] // memory_write

	args, _ := json.Marshal(map[string]any{"text": "本次会话只读模式", "scope": "session"})
	ctx := &tools.CallContext{SessionID: func() string { return "s1" }}
	res, err := tool.Execute(context.Background(), args, ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("写入: %s", res.Content)

	facts := s.Active(ScopeSession)
	if len(facts) != 1 || facts[0].Source.Session != "s1" {
		t.Fatalf("session 事实未标注来源: %+v", facts)
	}
	if len(s.Search("只读模式", 5, "s2")) != 0 {
		t.Fatal("session 事实不应泄漏到其它会话")
	}
	if len(s.Search("只读模式", 5, "s1")) != 1 {
		t.Fatal("本会话应能检索到 session 事实")
	}
}

func TestFindRejectsAmbiguousPrefix(t *testing.T) {
	s, _ := newStore(t)
	for _, text := range []string{"事实一", "事实二"} {
		if _, _, err := s.Write(NewFact(text)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Find("f_"); err == nil {
		t.Fatal("歧义前缀应报错，避免归档错事实")
	}
}

// TestWriteKeepsRuntimeManualEdit 守住运行期手工编辑不被下一次写入覆盖。
func TestWriteKeepsRuntimeManualEdit(t *testing.T) {
	s, root := newStore(t)
	if _, _, err := s.Write(NewFact("自动事实")); err != nil {
		t.Fatal(err)
	}

	// 模拟用户在进程外往手工区加了一行。
	path := filepath.Join(root, memoryFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), manualBegin+"\n", manualBegin+"\n- 手工添加的偏好\n", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.Write(NewFact("第二条自动事实")); err != nil {
		t.Fatal(err)
	}

	md, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "手工添加的偏好") {
		t.Fatalf("运行期手工编辑被覆盖:\n%s", md)
	}
	found := false
	for _, f := range s.Active("") {
		if f.Origin == OriginManual && strings.Contains(f.Text, "手工添加的偏好") {
			found = true
		}
	}
	if !found {
		t.Fatal("运行期新增的手工事实没有被导入")
	}
}

// TestMissingMarkersDoNotArchiveManual 守住误删标记不会清空手工记忆。
func TestMissingMarkersDoNotArchiveManual(t *testing.T) {
	s, root := newStore(t)
	path := filepath.Join(root, memoryFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), manualBegin+"\n", manualBegin+"\n- 手工事实\n", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if len(s.Active(ScopeProject)) != 1 {
		t.Fatalf("手工事实未导入: %+v", s.Active(""))
	}
	_ = s.Close()

	// 标记被误删：重开时不能把手工事实当成“用户删光了”。
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	broke := strings.ReplaceAll(string(raw), manualBegin+"\n", "")
	broke = strings.ReplaceAll(broke, manualEnd+"\n", "")
	if err := os.WriteFile(path, []byte(broke), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if len(s2.Active(ScopeProject)) != 1 {
		t.Fatalf("标记缺失时手工事实被归档: %+v", s2.Active(""))
	}
}
