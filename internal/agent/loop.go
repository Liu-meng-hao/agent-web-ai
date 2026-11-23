package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"agent/internal/llm"
	"agent/internal/security"
	"agent/internal/session"
	"agent/internal/telemetry"
	"agent/internal/tools"
)

// ErrMaxSteps 表示达到最大步数仍未收敛。
var ErrMaxSteps = errors.New("达到最大步数上限，本轮已终止")

// Recorder 是会话事件记录器（通常由 session.Store 实现）。
type Recorder interface {
	Record(session.Event) error
	Sync() error
}

// UsageRecorder 接收每次模型调用的 token 用量，用于写会话 meta。
type UsageRecorder interface {
	AddUsage(in, out int, cost float64) error
}

// Options 是构造 Agent 的依赖。
type Options struct {
	Provider     llm.Provider
	Model        string
	MaxTokens    int
	Registry     *tools.Registry
	Policy       security.Policy
	Approver     Approver
	MaxSteps     int
	Call         *tools.CallContext
	Emit         func(Event)
	Recorder     Recorder
	SyncEachTool bool
	OnWarn       func(string)
	// Recall 在每轮开始时按用户输入召回长期记忆，返回可直接注入的文本块。
	Recall func(query string) string
	// 摘要与预算：Window 为 0 时关闭自动压缩。
	Budget           Budget
	Summarizer       llm.Provider
	SummarizerModel  string
	SummaryMaxTokens int
	MinKeep          int
	// Meter 累计 token 与成本；nil 时只按本轮统计。
	Meter *telemetry.Meter
	// TraceDir 非空时每个 turn 写一份 JSON trace。
	TraceDir string
	// LogPath 非空时把 turn 起止、工具失败、压缩结果写入结构化日志。
	LogPath string
	// SessionID 返回当前会话 id，用于 trace 与日志。
	SessionID func() string
	// Role 是模型角色（main / summarizer / extractor），写入 trace。
	Role string
}

// Agent 持有对话状态并驱动 turn loop。
type Agent struct {
	opts Options
	conv *Conversation

	// 压缩状态：摘要模型故障时退避，避免每个 step 都重试形成调用风暴。
	turnCount        int
	compactTried     bool
	compactFails     int
	compactSkipUntil int

	// 本轮摘要开销；每轮开始时清零，写进 trace 的 summary_* 字段。
	sumIn, sumOut int
	sumCost       float64
}

// New 构造 Agent，并补齐默认值。
func New(opts Options) *Agent {
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = 48
	}
	if opts.Approver == nil {
		opts.Approver = DenyAll{}
	}
	if opts.Emit == nil {
		opts.Emit = func(Event) {}
	}
	if opts.OnWarn == nil {
		opts.OnWarn = func(string) {}
	}
	if opts.Registry == nil {
		opts.Registry = tools.NewRegistry()
	}
	return &Agent{opts: opts, conv: NewConversation("")}
}

// SetSystem 设置 system prompt。
func (a *Agent) SetSystem(system string) { a.conv.system = system }

// Conversation 返回对话句柄。
func (a *Agent) Conversation() *Conversation { return a.conv }

// Tools 返回工具注册表。
func (a *Agent) Tools() *tools.Registry { return a.opts.Registry }

// SetRecorder 替换事件记录器（会话切换时用）。
func (a *Agent) SetRecorder(r Recorder) { a.opts.Recorder = r }

// Reset 清空对话历史与摘要，保留 system prompt。
func (a *Agent) Reset() {
	a.conv.messages = nil
	a.conv.clearSummary()
}

// Restore 把历史消息灌回对话（会话恢复用）。
func (a *Agent) Restore(msgs []llm.Message) { a.conv.Restore(msgs) }

// RestoreSummary 恢复较早对话的摘要与已压缩消息数。
func (a *Agent) RestoreSummary(summary string, covered int) {
	a.conv.SetSummary(summary, covered)
}

// RunTurn 执行一轮：追加用户消息，循环调用模型与工具，直到模型不再请求工具。
func (a *Agent) RunTurn(ctx context.Context, input string) (err error) {
	emit := a.opts.Emit
	turnID := ulid.Make().String()
	stop := "error"
	startedAt := time.Now()
	steps := 0
	turnIn, turnOut, turnReasoning := 0, 0, 0
	var costBefore float64
	if a.opts.Meter != nil {
		costBefore = a.opts.Meter.Snapshot().Cost
	}
	sessionID := ""
	if a.opts.SessionID != nil {
		sessionID = a.opts.SessionID()
	}
	trace := telemetry.TurnTrace{
		TurnID:    turnID,
		SessionID: sessionID,
		Role:      a.opts.Role,
		Model:     a.opts.Model,
		StartedAt: startedAt,
	}
	a.sumIn, a.sumOut, a.sumCost = 0, 0, 0
	a.turnCount++
	// 每个 turn 重新给一次压缩机会；失败过的本轮不再重试。
	a.compactTried = false

	a.record(session.TypeTurnStarted, session.TurnStarted{TurnID: turnID, UserText: input})
	emit(Event{Type: EventTurnStarted})

	// 无论是正常结束、出错还是中断，都要落一条 turn.finished，
	// 否则恢复时无法判断这一轮是否完整。
	defer func() {
		a.record(session.TypeTurnFinished, session.TurnFinished{TurnID: turnID, Stop: stop})
		_ = a.sync()
		trace.Stop = stop
		trace.DurationMS = time.Since(startedAt).Milliseconds()
		trace.Steps = steps
		trace.Messages = a.conv.Len()
		trace.TokensIn = turnIn
		trace.TokensOut = turnOut
		trace.ReasoningTokens = turnReasoning
		trace.SummaryTokensIn = a.sumIn
		trace.SummaryTokensOut = a.sumOut
		trace.SummaryCost = a.sumCost
		if err != nil {
			trace.Error = err.Error()
		}
		if a.opts.Meter != nil {
			trace.Cost = a.opts.Meter.Snapshot().Cost - costBefore
		}
		if a.opts.TraceDir != "" {
			if err := telemetry.WriteTurn(a.opts.TraceDir, trace); err != nil {
				a.notice("写入 turn trace 失败：" + err.Error())
			}
		}
		attrs := map[string]any{
			"turn_id":      turnID,
			"session_id":   sessionID,
			"stop":         stop,
			"steps":        steps,
			"duration_ms":  trace.DurationMS,
			"tokens_in":    turnIn,
			"tokens_out":   turnOut,
			"reasoning":    turnReasoning,
			"cost":         trace.Cost,
			"summary_in":   a.sumIn,
			"summary_out":  a.sumOut,
			"summary_cost": a.sumCost,
		}
		if err != nil {
			attrs["error"] = err.Error()
			a.logWarn("turn.failed", attrs)
		} else {
			a.logEvent("turn.finished", attrs)
		}
		emit(Event{Type: EventTurnFinished, Stop: stop})
	}()

	a.conv.Append(llm.Message{Role: llm.RoleUser, Content: input})

	// 记忆召回只做一次，避免每个 step 都重复检索与拼接。
	memBlock := ""
	if a.opts.Recall != nil {
		memBlock = a.opts.Recall(input)
	}
	// 固定开销每轮算一次：召回块与工具 schema 都随请求发送，
	// 不把它们计入预算，估算就会系统性偏低。
	overhead := 8 + estimateText(memBlock) + EstimateToolSchemas(a.opts.Registry.Schemas())

	for step := 1; step <= a.opts.MaxSteps; step++ {
		steps = step
		if err := ctx.Err(); err != nil {
			emit(Event{Type: EventError, Step: step, Err: err})
			return err
		}

		// 压缩失败不阻断本轮：宁可上下文偏大，也不要因为摘要模型故障无法对话。
		if err := a.maybeCompact(ctx, overhead); err != nil {
			a.notice("上下文压缩失败：" + err.Error())
		}

		// 接近上限时要求模型收尾，避免把时间耗在重复读取和校验上。
		// 提示只进入本次请求，不写入会话历史。
		stepHint := ""
		if a.opts.MaxSteps >= 12 {
			switch step {
			case a.opts.MaxSteps - 5:
				stepHint = "\n\n[步数提醒] 本轮还剩约 5 步。停止重复读取和校验；若页面已写入，立即给出预览地址并结束；若尚未写入，下一步必须先生成可打开的页面。"
			case a.opts.MaxSteps - 1:
				stepHint = "\n\n[步数提醒] 即将达到步数上限。不要再调用工具，直接总结已完成内容和可访问的预览地址。"
			}
		}

		stream, err := a.opts.Provider.Stream(ctx, llm.Request{
			Model:     a.opts.Model,
			Messages:  a.conv.WithSystemExtra(memBlock + stepHint),
			Tools:     a.opts.Registry.Schemas(),
			MaxTokens: a.opts.MaxTokens,
		})
		if err != nil {
			emit(Event{Type: EventError, Step: step, Err: err})
			return err
		}

		var text strings.Builder
		var calls []llm.ToolCall
		finishReason := ""
		for ev := range stream {
			switch ev.Type {
			case llm.EventReasoning:
				// 只广播“正在思考”，不把推理原文写进对话或界面。
				emit(Event{Type: EventReasoning, Step: step})
			case llm.EventText:
				text.WriteString(ev.Text)
				emit(Event{Type: EventText, Step: step, Text: ev.Text})
			case llm.EventTool:
				if ev.ToolCall != nil {
					calls = append(calls, *ev.ToolCall)
				}
			case llm.EventUsage:
				if ev.Usage != nil {
					turnIn += ev.Usage.InputTokens
					turnOut += ev.Usage.OutputTokens
					turnReasoning += ev.Usage.ReasoningTokens
					snap, _ := a.accountUsage(a.opts.Model, ev.Usage.InputTokens, ev.Usage.OutputTokens)
					emit(Event{
						Type:            EventUsage,
						Step:            step,
						TokensIn:        snap.In,
						TokensOut:       snap.Out,
						ReasoningTokens: turnReasoning,
						Cost:            snap.Cost,
					})
				}
			case llm.EventDone:
				finishReason = ev.FinishReason
			case llm.EventError:
				if ev.Err != nil {
					emit(Event{Type: EventError, Step: step, Err: ev.Err})
					return ev.Err
				}
			}
		}

		// 用户中断：保留已产出的部分回复，按 canceled 收尾而不是伪装成正常结束。
		if err := ctx.Err(); err != nil {
			stop = "canceled"
			if text.Len() > 0 || len(calls) > 0 {
				a.conv.Append(llm.Message{Role: llm.RoleAssistant, Content: text.String(), ToolCalls: calls})
				a.recordAssistant(turnID, step, text.String(), calls)
			}
			return nil
		}

		// 推理模型会先把预算花在 reasoning 上：达到 max_tokens 时可能
		// 一个正文 token 都没有。这种情况必须明确告诉用户，不能静默回到 idle。
		if len(calls) == 0 && strings.TrimSpace(text.String()) == "" {
			if finishReason == "length" {
				stop = "length"
				a.notice(fmt.Sprintf("模型输出达到 max_tokens 上限（%d），本轮没有生成可见内容；请调大 models.main.max_tokens 或拆分任务。", a.opts.MaxTokens))
			} else {
				stop = "empty"
				a.notice("模型返回空内容（可能被供应商截断或异常），可以重试。")
			}
			return nil
		}
		if finishReason == "length" {
			a.notice(fmt.Sprintf("模型输出达到 max_tokens 上限（%d），回复或工具参数可能不完整；建议调大 max_tokens 或拆分页面分块写入。", a.opts.MaxTokens))
		}

		a.conv.Append(llm.Message{
			Role:      llm.RoleAssistant,
			Content:   text.String(),
			ToolCalls: calls,
		})
		a.recordAssistant(turnID, step, text.String(), calls)

		if len(calls) == 0 {
			stop = "stop"
			return nil
		}

		for i := range calls {
			if err := a.execTool(ctx, turnID, step, &calls[i], &trace); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				emit(Event{Type: EventError, Step: step, Err: err})
				return err
			}
		}
	}

	stop = "max_steps"
	// 页面已经落盘就不再把整轮标成失败：工具结果里的预览地址仍然有效，
	// 用户可以直接打开，也可以让模型从当前版本继续完善。
	for _, call := range trace.Tools {
		if call.Name == "artifact_write" && call.Status == "ok" {
			a.notice("已达到本轮最大步数，但页面已保存，预览地址见上方 artifact_write 结果。需要继续修改请发送“继续完善”。")
			return nil
		}
	}
	return ErrMaxSteps
}

func (a *Agent) execTool(ctx context.Context, turnID string, step int, call *llm.ToolCall, trace *telemetry.TurnTrace) error {
	emit := a.opts.Emit
	name := call.Name
	args := call.Args
	callID := call.ID

	tool, ok := a.opts.Registry.Get(name)
	if !ok {
		msg := fmt.Sprintf("未知工具 %q。可用工具：%s", name, strings.Join(a.opts.Registry.Names(), ", "))
		a.conv.Append(llm.Message{Role: llm.RoleTool, ToolCallID: callID, Content: msg})
		a.record(session.TypeToolResult, session.ToolResult{CallID: callID, Name: name, Status: "fail", Content: msg})
		emit(Event{Type: EventToolResult, Step: step, Call: call, Result: &tools.Result{Content: msg}})
		return nil
	}

	// 工具可以声明自己被禁用（如 MCP server 配置 approval=deny），
	// 这种情况下不弹审批、直接回灌拒绝原因。
	if d, ok := tool.(interface{ DisabledReason() string }); ok {
		if reason := d.DisabledReason(); reason != "" {
			msg := fmt.Sprintf("工具 %s 已被禁用：%s", name, reason)
			a.conv.Append(llm.Message{Role: llm.RoleTool, ToolCallID: callID, Content: msg})
			a.record(session.TypeToolResult, session.ToolResult{CallID: callID, Name: name, Status: "denied", Content: msg})
			emit(Event{Type: EventToolResult, Step: step, Call: call, Result: &tools.Result{Content: msg, Display: "已禁用"}})
			return nil
		}
	}

	emit(Event{Type: EventToolRequested, Step: step, Call: call, Risk: tool.Risk()})

	decision := AllowOnce
	action := a.opts.Policy.Decide(tool.Risk())
	// 写入范围完全受控的工具可声明免审批（如 artifact_write）；
	// readonly 模式仍然走 Deny，不在这里放宽。
	if action == security.Ask {
		if aa, ok := tool.(tools.AutoApprover); ok && aa.AutoApprove() {
			action = security.Allow
		}
	}
	if action != security.Allow {
		if ctx.Err() == nil {
			d, err := a.opts.Approver.Approve(ctx, ApprovalRequest{
				Tool:    name,
				Args:    args,
				Risk:    tool.Risk(),
				Preview: previewOf(call),
			})
			if err != nil {
				d = Deny
			}
			decision = d
		} else {
			decision = Deny
		}
	}

	if decision == Deny {
		const denied = "用户拒绝执行该操作。请不要重试同一命令，换一种方案或询问用户。"
		a.conv.Append(llm.Message{Role: llm.RoleTool, ToolCallID: callID, Content: denied})
		a.record(session.TypeToolResult, session.ToolResult{CallID: callID, Name: name, Status: "denied", Content: denied})
		trace.Tools = append(trace.Tools, telemetry.ToolTrace{CallID: callID, Name: name, Status: "denied"})
		a.logEvent("tool.denied", map[string]any{
			"turn_id": turnID, "tool": name, "call_id": callID, "step": step,
		})
		emit(Event{
			Type:     EventToolResult,
			Step:     step,
			Call:     call,
			Decision: Deny,
			Result:   &tools.Result{Content: denied, Display: "已拒绝"},
		})
		return nil
	}

	// 执行前先落 tool.call：写失败必须中断本轮，否则崩溃后无法识别这次调用。
	if err := a.recordErr(session.TypeToolCall, session.ToolCall{
		TurnID: turnID,
		Step:   step,
		CallID: callID,
		Name:   name,
		Args:   args,
	}); err != nil {
		emit(Event{Type: EventError, Step: step, Err: err})
		return err
	}
	if a.opts.SyncEachTool {
		_ = a.sync()
	}

	start := time.Now()
	res, err := tool.Execute(ctx, args, a.opts.Call)
	dur := time.Since(start)
	status := "ok"
	if err != nil {
		status = "fail"
		res = tools.Result{Content: "工具执行失败: " + err.Error(), Display: "失败"}
	}
	trace.Tools = append(trace.Tools, telemetry.ToolTrace{
		CallID:     callID,
		Name:       name,
		Status:     status,
		DurationMS: dur.Milliseconds(),
	})
	if err != nil {
		a.logWarn("tool.failed", map[string]any{
			"turn_id": turnID, "tool": name, "call_id": callID,
			"step": step, "duration_ms": dur.Milliseconds(), "error": err.Error(),
		})
	}

	// 工具输出是外部内容：包一层标记，配合 system prompt 防止提示注入。
	wrapped := fmt.Sprintf("<untrusted_content source=%q>\n%s\n</untrusted_content>", name, res.Content)
	a.conv.Append(llm.Message{Role: llm.RoleTool, ToolCallID: callID, Content: wrapped})
	a.record(session.TypeToolResult, session.ToolResult{
		CallID:     callID,
		Name:       name,
		Status:     status,
		Content:    res.Content,
		DurationMS: dur.Milliseconds(),
	})
	emit(Event{Type: EventToolResult, Step: step, Call: call, Result: &res, Duration: dur, Decision: decision})
	return nil
}

func (a *Agent) recordAssistant(turnID string, step int, text string, calls []llm.ToolCall) {
	payload := session.AssistantMessage{TurnID: turnID, Step: step, Text: text}
	for _, c := range calls {
		payload.ToolCalls = append(payload.ToolCalls, session.ToolCallInfo{ID: c.ID, Name: c.Name, Args: c.Args})
	}
	a.record(session.TypeAssistantMessage, payload)
}

func (a *Agent) record(t session.EventType, payload any) {
	_ = a.recordErr(t, payload)
}

func (a *Agent) recordErr(t session.EventType, payload any) error {
	if a.opts.Recorder == nil {
		return nil
	}
	ev, err := session.New(t, payload)
	if err != nil {
		a.opts.OnWarn("构造会话事件失败: " + err.Error())
		return nil
	}
	if err := a.opts.Recorder.Record(ev); err != nil {
		a.opts.OnWarn("写入会话事件失败: " + err.Error())
		return err
	}
	return nil
}

func (a *Agent) sync() error {
	if a.opts.Recorder == nil {
		return nil
	}
	return a.opts.Recorder.Sync()
}

// accountUsage 把一次模型调用计入 meter 与会话 meta，返回最新快照与本次成本增量。
func (a *Agent) accountUsage(model string, in, out int) (telemetry.Snapshot, float64) {
	before := 0.0
	if a.opts.Meter != nil {
		before = a.opts.Meter.Snapshot().Cost
	}
	snap := a.opts.Meter.Add(model, in, out)
	delta := snap.Cost - before
	if rec, ok := a.opts.Recorder.(UsageRecorder); ok {
		if err := rec.AddUsage(in, out, delta); err != nil {
			a.notice("写入用量统计失败：" + err.Error())
		}
	}
	return snap, delta
}

// accountSummary 记录摘要调用的开销：同样是真实请求，必须计入成本与 trace。
func (a *Agent) accountSummary(in, out int) {
	if in == 0 && out == 0 {
		return
	}
	snap, delta := a.accountUsage(a.opts.SummarizerModel, in, out)
	a.sumIn += in
	a.sumOut += out
	a.sumCost += delta
	a.opts.Emit(Event{Type: EventUsage, TokensIn: snap.In, TokensOut: snap.Out, Cost: snap.Cost})
}

func (a *Agent) logEvent(msg string, attrs map[string]any) {
	if a.opts.LogPath != "" {
		telemetry.LogEvent(a.opts.LogPath, msg, attrs)
	}
}

func (a *Agent) logWarn(msg string, attrs map[string]any) {
	if a.opts.LogPath != "" {
		telemetry.LogWarnEvent(a.opts.LogPath, msg, attrs)
	}
}

// previewOf 生成审批弹层显示的人读摘要。
func previewOf(call *llm.ToolCall) string {
	var m map[string]any
	if err := json.Unmarshal(call.Args, &m); err == nil {
		for _, key := range []string{"command", "path", "url"} {
			if v, ok := m[key]; ok {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return s
				}
			}
		}
	}
	s := string(call.Args)
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	if s == "" {
		return call.Name
	}
	return s
}
