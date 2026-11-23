package session

import (
	"testing"
)

func TestBuildRestoreAppliesSummary(t *testing.T) {
	events := []Event{}
	add := func(typ EventType, payload any) {
		ev, err := New(typ, payload)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}

	add(TypeTurnStarted, TurnStarted{TurnID: "t1", UserText: "第一轮"})
	add(TypeAssistantMessage, AssistantMessage{TurnID: "t1", Step: 1, Text: "回答一"})
	add(TypeTurnStarted, TurnStarted{TurnID: "t2", UserText: "第二轮"})
	add(TypeAssistantMessage, AssistantMessage{TurnID: "t2", Step: 1, Text: "回答二"})
	add(TypeSummary, Summary{Text: "第一轮的结论", Covered: 2})
	add(TypeTurnStarted, TurnStarted{TurnID: "t3", UserText: "第三轮"})
	add(TypeAssistantMessage, AssistantMessage{TurnID: "t3", Step: 1, Text: "回答三"})

	msgs, summary, covered := BuildRestore(events)
	if summary != "第一轮的结论" || covered != 2 {
		t.Fatalf("summary=%q covered=%d", summary, covered)
	}
	if len(msgs) != 4 {
		t.Fatalf("消息数 = %d, want 4（丢掉第一轮的 2 条）", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "第二轮" {
		t.Fatalf("被摘要覆盖的前缀没有丢掉: %+v", msgs[0])
	}
}

// 硬截断事件也要推进恢复时的 covered，否则被截掉的消息会回来。
func TestBuildRestoreUsesTruncatedCovered(t *testing.T) {
	var events []Event
	add := func(typ EventType, payload any) {
		ev, err := New(typ, payload)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	for i := 0; i < 4; i++ {
		add(TypeTurnStarted, TurnStarted{TurnID: "t", UserText: "消息"})
	}
	add(TypeSummary, Summary{Text: "较早结论", Covered: 2})
	add(TypeTruncated, Truncated{Covered: 3})

	msgs, summary, covered := BuildRestore(events)
	if covered != 3 {
		t.Fatalf("covered = %d, want 3（取 summary 与 truncated 的最大值）", covered)
	}
	if summary != "较早结论" {
		t.Fatalf("summary = %q", summary)
	}
	if len(msgs) != 1 {
		t.Fatalf("消息数 = %d, want 1", len(msgs))
	}
}

func TestBuildRestoreWithoutSummaryKeepsAll(t *testing.T) {
	events := []Event{}
	ev, err := New(TypeTurnStarted, TurnStarted{TurnID: "t1", UserText: "你好"})
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, ev)

	msgs, summary, covered := BuildRestore(events)
	if summary != "" || covered != 0 || len(msgs) != 1 {
		t.Fatalf("summary=%q covered=%d msgs=%d", summary, covered, len(msgs))
	}
}

func TestBuildRestoreClampsCovered(t *testing.T) {
	events := []Event{}
	ev, err := New(TypeTurnStarted, TurnStarted{TurnID: "t1", UserText: "你好"})
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, ev)
	sum, err := New(TypeSummary, Summary{Text: "摘要", Covered: 99})
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, sum)

	msgs, _, covered := BuildRestore(events)
	if len(msgs) != 0 || covered != 1 {
		t.Fatalf("covered 未夹紧: msgs=%d covered=%d", len(msgs), covered)
	}
}
