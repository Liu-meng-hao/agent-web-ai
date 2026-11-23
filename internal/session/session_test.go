package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := Create(root, Meta{Cwd: "D:/x", Model: "mock", Shell: "bash"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, store.Dir()
}

func appendEvent(t *testing.T, s *Store, typ EventType, payload any) {
	t.Helper()
	ev, err := New(typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(ev); err != nil {
		t.Fatal(err)
	}
}

func TestStoreAppendAndLock(t *testing.T) {
	store, dir := newTestStore(t)

	appendEvent(t, store, TypeTurnStarted, TurnStarted{TurnID: "t1", UserText: "你好，帮我看下项目结构"})
	if err := store.Sync(); err != nil {
		t.Fatal(err)
	}

	meta := store.Meta()
	if meta.EventCount != 1 {
		t.Fatalf("event_count = %d, want 1", meta.EventCount)
	}
	if !strings.HasPrefix(meta.Title, "你好") {
		t.Fatalf("title = %q", meta.Title)
	}

	// 同一目录再打开必须失败（不等待锁）。
	if _, err := Open(dir); err == nil {
		t.Fatal("同一会话不应能被打开两次")
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("关闭后重新打开失败: %v", err)
	}
	defer s2.Close()

	raw, err := os.ReadFile(EventsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("events 行数 = %d, want 1", len(lines))
	}
	var e Event
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Seq != 1 || e.Type != TypeTurnStarted || e.V != SchemaVersion {
		t.Fatalf("事件内容异常: %+v", e)
	}
}

func TestRecoverTruncatedTail(t *testing.T) {
	store, dir := newTestStore(t)
	appendEvent(t, store, TypeTurnStarted, TurnStarted{TurnID: "t1", UserText: "hi"})
	_ = store.Close()

	path := EventsPath(dir)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"v":1,"seq":2,"type":"turn.started","payload":{"PARTIAL_TAIL`)
	_ = f.Close()

	rec, err := Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Events) != 1 {
		t.Fatalf("恢复事件数 = %d, want 1", len(rec.Events))
	}
	if rec.TruncatedBytes == 0 {
		t.Fatal("应报告截断字节数")
	}

	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "PARTIAL_TAIL") {
		t.Fatal("半行未被截掉")
	}
	if bad, _ := os.ReadFile(filepath.Join(dir, badFile)); !strings.Contains(string(bad), "PARTIAL_TAIL") {
		t.Fatal("坏字节未另存到 events.bad")
	}

	metaRaw, _ := os.ReadFile(filepath.Join(dir, metaFile))
	var meta Meta
	_ = json.Unmarshal(metaRaw, &meta)
	if meta.EventCount != 1 {
		t.Fatalf("meta.EventCount = %d, want 1", meta.EventCount)
	}
}

func TestRecoverStopsAtBadLine(t *testing.T) {
	cases := []struct {
		name    string
		append  string
		wantLen int
	}{
		{"空行", "\n", 1},
		{"非法 JSON", "not-json\n", 1},
		{"seq 缺口", `{"v":1,"seq":9,"ts":"2026-11-20T10:00:00Z","type":"turn.started"}` + "\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, dir := newTestStore(t)
			appendEvent(t, store, TypeTurnStarted, TurnStarted{TurnID: "t1", UserText: "hi"})
			_ = store.Close()

			f, err := os.OpenFile(EventsPath(dir), os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.WriteString(tc.append)
			_ = f.Close()

			rec, err := Recover(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(rec.Events) != tc.wantLen {
				t.Fatalf("事件数 = %d, want %d", len(rec.Events), tc.wantLen)
			}
			if len(rec.Warnings) == 0 {
				t.Fatal("应产生恢复警告")
			}
		})
	}
}

func TestRecoverFlagsInterruptedCall(t *testing.T) {
	store, dir := newTestStore(t)
	appendEvent(t, store, TypeToolCall, ToolCall{TurnID: "t1", Step: 1, CallID: "c1", Name: "shell"})
	appendEvent(t, store, TypeToolCall, ToolCall{TurnID: "t1", Step: 1, CallID: "c2", Name: "shell"})
	appendEvent(t, store, TypeToolResult, ToolResult{CallID: "c2", Name: "shell", Status: "ok", Content: "done"})
	_ = store.Close()

	rec, err := Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Interrupted) != 1 || rec.Interrupted[0] != "c1" {
		t.Fatalf("interrupted = %v, want [c1]", rec.Interrupted)
	}
}

func TestToMessagesRestoresAndSynthesizes(t *testing.T) {
	store, dir := newTestStore(t)
	appendEvent(t, store, TypeTurnStarted, TurnStarted{TurnID: "t1", UserText: "跑个命令"})
	appendEvent(t, store, TypeAssistantMessage, AssistantMessage{
		TurnID: "t1", Step: 1, Text: "我执行一下",
		ToolCalls: []ToolCallInfo{{ID: "c1", Name: "shell", Args: json.RawMessage(`{"command":"ls"}`)}},
	})
	appendEvent(t, store, TypeToolCall, ToolCall{TurnID: "t1", Step: 1, CallID: "c1", Name: "shell"})
	_ = store.Close()

	rec, err := Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	msgs := ToMessages(rec.Events)

	if len(msgs) != 3 { // user, assistant, 合成的 tool
		t.Fatalf("消息数 = %d, want 3", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "跑个命令" {
		t.Fatalf("user 消息异常: %+v", msgs[0])
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].ID != "c1" {
		t.Fatalf("assistant 工具调用丢失: %+v", msgs[1])
	}
	last := msgs[len(msgs)-1]
	if last.Role != "tool" || last.ToolCallID != "c1" {
		t.Fatalf("缺少合成的 tool 消息: %+v", last)
	}
	if !strings.Contains(last.Content, "中断") {
		t.Fatalf("合成消息内容不对: %q", last.Content)
	}
}

func TestRenderIsReadable(t *testing.T) {
	store, dir := newTestStore(t)
	appendEvent(t, store, TypeTurnStarted, TurnStarted{TurnID: "t1", UserText: "hi"})
	appendEvent(t, store, TypeAssistantMessage, AssistantMessage{TurnID: "t1", Step: 1, Text: "hello"})
	_ = store.Close()

	rec, err := Recover(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := Render(rec.Events)
	if !strings.Contains(out, "[you] hi") || !strings.Contains(out, "[agent] hello") {
		t.Fatalf("渲染结果异常:\n%s", out)
	}
}

func TestEventsHaveTimestampAndSeq(t *testing.T) {
	s, _ := newTestStore(t)
	start := time.Now().Add(-time.Second)
	appendEvent(t, s, TypeTurnStarted, TurnStarted{TurnID: "t", UserText: "x"})
	if s.seq != 1 {
		t.Fatalf("seq = %d", s.seq)
	}
	if s.meta.UpdatedAt.Before(start) {
		t.Fatal("meta 未更新时间")
	}
}

// 崩在 Sync 之前时 meta.EventCount 会落后，Open 不能拿它当 seq 的真相源。
func TestOpenDerivesSeqFromFileNotMeta(t *testing.T) {
	store, dir := newTestStore(t)
	appendEvent(t, store, TypeTurnStarted, TurnStarted{TurnID: "t1", UserText: "a"})
	appendEvent(t, store, TypeTurnFinished, TurnFinished{TurnID: "t1", Stop: "stop"})
	_ = store.Close()

	metaRaw, _ := os.ReadFile(filepath.Join(dir, metaFile))
	var m Meta
	if err := json.Unmarshal(metaRaw, &m); err != nil {
		t.Fatal(err)
	}
	m.EventCount = 0 // 模拟"事件已落盘、meta 没来得及写"
	if err := writeMeta(dir, m); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if reopened.seq != 2 {
		t.Fatalf("Open 后 seq = %d, want 2（应来自事件文件）", reopened.seq)
	}
	appendEvent(t, reopened, TypeTurnStarted, TurnStarted{TurnID: "t2", UserText: "b"})
	if reopened.seq != 3 {
		t.Fatalf("追加后 seq = %d, want 3", reopened.seq)
	}
}

// Recover 在没有坏尾时也要校正落后的 meta。
func TestRecoverFixesStaleMeta(t *testing.T) {
	store, dir := newTestStore(t)
	appendEvent(t, store, TypeTurnStarted, TurnStarted{TurnID: "t1", UserText: "a"})
	appendEvent(t, store, TypeToolCall, ToolCall{TurnID: "t1", Step: 1, CallID: "c1", Name: "shell"})
	_ = store.Close()

	metaRaw, _ := os.ReadFile(filepath.Join(dir, metaFile))
	var m Meta
	_ = json.Unmarshal(metaRaw, &m)
	m.EventCount = 1
	_ = writeMeta(dir, m)

	if _, err := Recover(dir); err != nil {
		t.Fatal(err)
	}
	metaRaw, _ = os.ReadFile(filepath.Join(dir, metaFile))
	_ = json.Unmarshal(metaRaw, &m)
	if m.EventCount != 2 {
		t.Fatalf("meta.EventCount = %d, want 2", m.EventCount)
	}
}

// Scan 是只读的：replay 不能顺手把会话修了。
func TestScanDoesNotModifyFiles(t *testing.T) {
	store, dir := newTestStore(t)
	appendEvent(t, store, TypeTurnStarted, TurnStarted{TurnID: "t1", UserText: "a"})
	_ = store.Close()

	f, err := os.OpenFile(EventsPath(dir), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("BROKEN_TAIL")
	_ = f.Close()

	before, _ := os.ReadFile(EventsPath(dir))
	metaBefore, _ := os.ReadFile(filepath.Join(dir, metaFile))

	rec, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.TruncatedBytes == 0 {
		t.Fatal("应报告尾部不完整")
	}

	after, _ := os.ReadFile(EventsPath(dir))
	metaAfter, _ := os.ReadFile(filepath.Join(dir, metaFile))
	if string(before) != string(after) {
		t.Fatal("Scan 不应修改事件文件")
	}
	if string(metaBefore) != string(metaAfter) {
		t.Fatal("Scan 不应修改 meta")
	}
	if _, err := os.Stat(filepath.Join(dir, badFile)); !os.IsNotExist(err) {
		t.Fatal("Scan 不应写 events.bad")
	}
}
