package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"agent/internal/agent"
	"agent/internal/llm"
	"agent/internal/telemetry"
)

// Options 是启动 TUI 需要的依赖。
type Options struct {
	Agent    *agent.Agent
	Model    string
	Events   <-chan agent.Event
	Approver *Approver

	SessionID string
	Notices   []string
	History   []llm.Message
	// MemoryReadOnly 为 true 时状态栏常驻“记忆只读”标识。
	MemoryReadOnly bool

	// NewSession / ListSessions 供 /new、/sessions 命令使用，可为 nil。
	NewSession   func() (string, error)
	ListSessions func() ([]string, error)
	// ListMemory / RebuildMemory / MemoryReconnect 供 /memory 命令使用，可为 nil。
	ListMemory      func() ([]string, error)
	RebuildMemory   func() (int, error)
	MemoryReconnect func() (string, error)
	// Compact 供 /compact 命令使用，可为 nil。传入 ctx 以便 Ctrl+C 能中断摘要请求。
	Compact func(context.Context) (string, error)
	// MCPStatus / MCPReconnect 供 /mcp 与 /mcp reconnect 使用，可为 nil。
	MCPStatus    func() []string
	MCPReconnect func(context.Context, string) error
	// SkillList / SkillShow / SkillReload 供 /skill 命令使用，可为 nil。
	SkillList   func() ([]string, error)
	SkillShow   func(string) (string, error)
	SkillReload func() (string, error)
	// PreviewInfo / OpenPreview 供 /preview 与 ctrl+o 使用，可为 nil。
	PreviewInfo func() []string
	OpenPreview func() (string, error)
	// Usage 是启动时的累计用量；LogTail 供 /log 读取最近日志。
	Usage   telemetry.Snapshot
	LogTail func(int) ([]string, error)
}

// Run 启动界面并阻塞到用户退出。
func Run(opts Options) error {
	_, err := tea.NewProgram(
		newModel(opts),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(), // 对话区支持滚轮
	).Run()
	return err
}

type blockKind int

const (
	blockText blockKind = iota
	blockTool
	blockError
	blockNotice
)

type block struct {
	kind     blockKind
	speaker  string
	text     string
	callID   string
	tool     string
	risk     string
	preview  string
	status   string // running | ok | fail | denied
	result   string
	duration time.Duration
}

type model struct {
	ag        *agent.Agent
	modelName string
	events    <-chan agent.Event
	ap        *Approver

	ta textarea.Model
	vp viewport.Model

	// autoFollow 为 true 时始终贴底显示最新输出；用户上翻后暂停跟随。
	autoFollow bool

	blocks    []block
	pending   strings.Builder
	streaming bool
	err       error

	ctx    context.Context
	cancel context.CancelFunc

	pendingApproval *agent.ApprovalRequest
	overlay         bool

	sessionID    string
	memReadOnly  bool
	newSession   func() (string, error)
	listSessions func() ([]string, error)
	listMemory   func() ([]string, error)
	rebuildMem   func() (int, error)
	memReconnect func() (string, error)
	compact      func(context.Context) (string, error)
	mcpStatus    func() []string
	mcpReconnect func(context.Context, string) error
	skillList    func() ([]string, error)
	skillShow    func(string) (string, error)
	skillReload  func() (string, error)
	previewInfo  func() []string
	openPreview  func() (string, error)
	logTail      func(int) ([]string, error)

	tokensIn, tokensOut int
	// reasoningTokens 是本轮输出中用于思考的部分，已包含在 tokensOut 内。
	reasoningTokens int
	thinking        bool
	cost            float64
	currency        string

	width, height int
}

func newModel(opts Options) *model {
	ta := textarea.New()
	ta.Placeholder = "输入消息，/命令；Enter 发送，Ctrl+J 换行"
	ta.ShowLineNumbers = false
	ta.CharLimit = 4000
	ta.MaxHeight = 5
	ta.SetHeight(1)
	ta.Prompt = "› "
	ta.Focus()
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")
	ta.FocusedStyle.Base = lipgloss.NewStyle().Foreground(colText)
	ta.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(colDim)
	ta.BlurredStyle.Base = lipgloss.NewStyle().Foreground(colText)
	ta.BlurredStyle.Prompt = lipgloss.NewStyle().Foreground(colDim)
	ta.BlurredStyle.Placeholder = lipgloss.NewStyle().Foreground(colDim)

	vp := viewport.New(80, 10)
	vp.MouseWheelEnabled = true
	vp.MouseWheelDelta = 3

	ctx, cancel := context.WithCancel(context.Background())
	m := &model{
		ag:           opts.Agent,
		modelName:    opts.Model,
		events:       opts.Events,
		ap:           opts.Approver,
		ta:           ta,
		vp:           vp,
		autoFollow:   true,
		ctx:          ctx,
		cancel:       cancel,
		sessionID:    opts.SessionID,
		memReadOnly:  opts.MemoryReadOnly,
		newSession:   opts.NewSession,
		listSessions: opts.ListSessions,
		listMemory:   opts.ListMemory,
		rebuildMem:   opts.RebuildMemory,
		memReconnect: opts.MemoryReconnect,
		compact:      opts.Compact,
		mcpStatus:    opts.MCPStatus,
		mcpReconnect: opts.MCPReconnect,
		skillList:    opts.SkillList,
		skillShow:    opts.SkillShow,
		skillReload:  opts.SkillReload,
		previewInfo:  opts.PreviewInfo,
		openPreview:  opts.OpenPreview,
		logTail:      opts.LogTail,
		tokensIn:     opts.Usage.In,
		tokensOut:    opts.Usage.Out,
		cost:         opts.Usage.Cost,
		currency:     opts.Usage.Currency,
		blocks:       blocksFromMessages(opts.History),
	}
	for _, n := range opts.Notices {
		m.blocks = append(m.blocks, block{kind: blockNotice, text: n})
	}
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(waitEvent(m.events), waitApproval(m.ap.Requests()))
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.fitInput()
		return m, nil

	case tea.KeyMsg:
		if m.overlay {
			switch msg.String() {
			case "y":
				return m, m.resolveApproval(agent.AllowOnce)
			case "a":
				// 记忆修正必须逐次确认，不提供会话级免审。
				if m.pendingApproval != nil && m.pendingApproval.Tool == "memory_update" {
					return m, nil
				}
				return m, m.resolveApproval(agent.AllowSession)
			case "n", "esc":
				return m, m.resolveApproval(agent.Deny)
			case "ctrl+c":
				m.cancel()
				return m, tea.Quit
			}
			return m, nil
		}

		switch msg.String() {
		case "pgup":
			m.autoFollow = false
			m.vp.PageUp()
			return m, nil
		case "pgdown":
			m.vp.PageDown()
			m.autoFollow = m.vp.AtBottom()
			return m, nil
		case "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "ctrl+o":
			return m, m.openPreviewCmd()
		case "enter":
			if m.streaming {
				return m, nil
			}
			prompt := strings.TrimSpace(m.ta.Value())
			if prompt == "" {
				return m, nil
			}
			m.ta.Reset()
			m.fitInput()
			if strings.HasPrefix(prompt, "/") {
				return m, m.command(prompt)
			}
			return m, m.submit(prompt)
		}

	case tea.MouseMsg:
		if m.overlay {
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		m.autoFollow = m.vp.AtBottom()
		return m, cmd

	case eventMsg:
		m.handleEvent(msg.ev)
		return m, waitEvent(m.events)

	case approvalMsg:
		req := msg.req
		m.pendingApproval = &req
		m.overlay = true
		return m, nil

	case turnDoneMsg:
		m.streaming = false
		m.flushPending()
		return m, nil

	case turnErrMsg:
		m.streaming = false
		m.flushPending()
		m.err = msg.err
		m.blocks = append(m.blocks, block{kind: blockError, text: msg.err.Error()})
		return m, nil

	case mcpReconnectMsg:
		if msg.err != nil {
			m.notice("MCP 重连失败：" + msg.err.Error())
		} else {
			m.notice("MCP 重连完成：" + msg.name)
			if m.mcpStatus != nil {
				for _, line := range m.mcpStatus() {
					m.notice(line)
				}
			}
		}
		return m, nil

	case skillReloadMsg:
		if msg.err != nil {
			m.notice("技能重载失败：" + msg.err.Error())
			return m, nil
		}
		m.notice(msg.text)
		if m.skillList != nil {
			if lines, err := m.skillList(); err == nil {
				for _, l := range lines {
					m.notice(l)
				}
			}
		}
		return m, nil

	case compactMsg:
		m.streaming = false
		if msg.err != nil {
			m.notice("压缩失败：" + msg.err.Error())
		} else {
			m.notice(msg.text)
		}
		return m, nil

	case previewMsg:
		if msg.err != nil {
			if msg.url != "" {
				m.notice("打开浏览器失败：" + msg.err.Error() + "；预览地址：" + msg.url)
			} else {
				m.notice("打开预览失败：" + msg.err.Error())
			}
			return m, nil
		}
		m.notice("已在浏览器打开 " + msg.url)
		return m, nil
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.fitInput()
	return m, cmd
}

// fitInput 让输入框随内容在 1-5 行之间自适应。
func (m *model) fitInput() {
	lines := m.ta.LineCount()
	if lines < 1 {
		lines = 1
	}
	if lines > 5 {
		lines = 5
	}
	if m.ta.Height() != lines {
		m.ta.SetHeight(lines)
	}
}

func (m *model) submit(prompt string) tea.Cmd {
	m.blocks = append(m.blocks, block{kind: blockText, speaker: "you", text: prompt})
	m.streaming = true
	m.thinking = false
	m.reasoningTokens = 0
	m.err = nil
	if m.cancel != nil {
		m.cancel()
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	ctx := m.ctx
	ag := m.ag

	return func() tea.Msg {
		err := ag.RunTurn(ctx, prompt)
		if err != nil && !errors.Is(err, context.Canceled) {
			return turnErrMsg{err: err}
		}
		return turnDoneMsg{}
	}
}

func (m *model) handleEvent(ev agent.Event) {
	switch ev.Type {
	case agent.EventReasoning:
		m.thinking = true

	case agent.EventText:
		m.thinking = false
		m.pending.WriteString(ev.Text)

	case agent.EventToolRequested:
		m.thinking = false
		m.flushPending()
		callID, name := "", ""
		args := ""
		if ev.Call != nil {
			callID, name = ev.Call.ID, ev.Call.Name
			args = previewArgs(ev.Call.Args)
		}
		m.blocks = append(m.blocks, block{
			kind:    blockTool,
			callID:  callID,
			tool:    name,
			risk:    ev.Risk.String(),
			preview: args,
			status:  "running",
		})

	case agent.EventToolResult:
		contactID := ""
		if ev.Call != nil {
			contactID = ev.Call.ID
		}
		idx := m.toolIndex(contactID)
		if idx < 0 {
			name := ""
			if ev.Call != nil {
				name = ev.Call.Name
			}
			m.blocks = append(m.blocks, block{kind: blockTool, callID: contactID, tool: name, status: "ok"})
			idx = len(m.blocks) - 1
		}
		b := &m.blocks[idx]
		switch {
		case ev.Decision == agent.Deny:
			b.status = "denied"
		case ev.Result != nil && strings.HasPrefix(ev.Result.Content, "工具执行失败"):
			b.status = "fail"
		default:
			b.status = "ok"
		}
		b.duration = ev.Duration
		if ev.Result != nil {
			b.result = ev.Result.Content
			if ev.Result.Display != "" {
				b.result = ev.Result.Display
			}
		}

	case agent.EventNotice:
		m.notice(ev.Text)

	case agent.EventUsage:
		m.tokensIn, m.tokensOut, m.cost = ev.TokensIn, ev.TokensOut, ev.Cost
		m.reasoningTokens = ev.ReasoningTokens

	case agent.EventTurnFinished:
		m.flushPending()
		m.streaming = false
		m.thinking = false

	case agent.EventError:
		// 约定：EventError 只是记录，渲染统一由 turnErrMsg / 工具卡片负责，
		// 否则同一个错误会出现两次。
		m.err = ev.Err
		m.streaming = false
		m.thinking = false
	}
}

func (m *model) toolIndex(callID string) int {
	if callID == "" {
		return -1
	}
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == blockTool && m.blocks[i].callID == callID {
			return i
		}
	}
	return -1
}

func (m *model) flushPending() {
	if s := strings.TrimSpace(m.pending.String()); s != "" {
		m.blocks = append(m.blocks, block{kind: blockText, speaker: "agent", text: s})
	}
	m.pending.Reset()
}

func (m *model) resolveApproval(d agent.Decision) tea.Cmd {
	if m.ap != nil {
		m.ap.Resolve(d)
	}
	m.overlay = false
	m.pendingApproval = nil
	return waitApproval(m.ap.Requests())
}

func (m *model) openPreviewCmd() tea.Cmd {
	if m.openPreview == nil {
		m.notice("当前模式不支持预览")
		return nil
	}
	open := m.openPreview
	return func() tea.Msg {
		url, err := open()
		return previewMsg{url: url, err: err}
	}
}

// command 处理斜杠命令。
func (m *model) command(input string) tea.Cmd {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return nil
	}

	switch fields[0] {
	case "/new":
		if m.newSession == nil {
			m.notice("当前模式不支持新建会话")
			return nil
		}
		id, err := m.newSession()
		if err != nil {
			m.notice("新建会话失败：" + err.Error())
			return nil
		}
		m.blocks = nil
		m.pending.Reset()
		m.sessionID = id
		// 新会话的账本已在 agent 侧清零；头部不等下一次 usage 事件，直接归零。
		m.tokensIn, m.tokensOut, m.cost = 0, 0, 0
		m.notice("已新建会话 " + shortID(id))

	case "/sessions":
		if m.listSessions == nil {
			m.notice("当前模式不支持会话列表")
			return nil
		}
		lines, err := m.listSessions()
		if err != nil {
			m.notice("读取会话失败：" + err.Error())
			return nil
		}
		for _, l := range lines {
			m.notice(l)
		}

	case "/memory":
		if len(fields) > 1 && fields[1] == "reconnect" {
			if m.memReconnect == nil {
				m.notice("当前模式不支持恢复记忆写入")
				return nil
			}
			msg, err := m.memReconnect()
			if err != nil {
				m.notice("恢复记忆写入失败：" + err.Error())
				return nil
			}
			m.memReadOnly = false
			m.notice(msg)
			return nil
		}
		// /memory rebuild：在进程内重读 MEMORY.md 手工区并重建视图，
		// 否则运行中的会话持有记忆库排他锁，外部 `agent memory rebuild` 会被拒。
		if len(fields) > 1 && fields[1] == "rebuild" {
			if m.rebuildMem == nil {
				m.notice("当前模式不支持重建记忆")
				return nil
			}
			n, err := m.rebuildMem()
			if err != nil {
				m.notice("重建记忆失败：" + err.Error())
				return nil
			}
			m.notice(fmt.Sprintf("已重建记忆视图（%d 条活跃事实）", n))
			return nil
		}
		if m.listMemory == nil {
			m.notice("当前模式不支持记忆面板")
			return nil
		}
		lines, err := m.listMemory()
		if err != nil {
			m.notice("读取记忆失败：" + err.Error())
			return nil
		}
		for _, l := range lines {
			m.notice(l)
		}

	case "/compact":
		if m.compact == nil {
			m.notice("当前模式不支持压缩")
			return nil
		}
		if m.streaming {
			m.notice("正在生成回复，稍后再试")
			return nil
		}
		m.streaming = true
		compact := m.compact
		ctx := m.ctx
		// 摘要要等模型返回，放到 Cmd 里执行，避免阻塞界面。
		return func() tea.Msg {
			text, err := compact(ctx)
			return compactMsg{text: text, err: err}
		}

	case "/mcp":
		if len(fields) > 1 {
			if len(fields) != 3 || fields[1] != "reconnect" {
				m.notice("用法：/mcp 或 /mcp reconnect <server|all>")
				return nil
			}
			if m.mcpReconnect == nil {
				m.notice("未配置 MCP server")
				return nil
			}
			if m.streaming {
				m.notice("正在生成回复，稍后再重连")
				return nil
			}
			name := fields[2]
			reconnect := m.mcpReconnect
			ctx := m.ctx
			m.notice("正在重连 MCP：" + name)
			return func() tea.Msg {
				return mcpReconnectMsg{name: name, err: reconnect(ctx, name)}
			}
		}
		if m.mcpStatus == nil {
			m.notice("未配置 MCP server")
			return nil
		}
		for _, l := range m.mcpStatus() {
			m.notice(l)
		}

	case "/skill":
		if len(fields) > 2 {
			m.notice("用法：/skill、/skill <name> 或 /skill reload")
			return nil
		}
		if len(fields) == 2 && fields[1] == "reload" {
			if m.skillReload == nil {
				m.notice("未发现技能")
				return nil
			}
			if m.streaming {
				m.notice("正在生成回复，稍后再重载")
				return nil
			}
			reload := m.skillReload
			m.notice("正在重新加载技能…")
			return func() tea.Msg {
				text, err := reload()
				return skillReloadMsg{text: text, err: err}
			}
		}
		if len(fields) == 2 {
			if m.skillShow == nil {
				m.notice("未发现技能")
				return nil
			}
			body, err := m.skillShow(fields[1])
			if err != nil {
				m.notice(err.Error())
				return nil
			}
			m.notice(body)
			return nil
		}
		if m.skillList == nil {
			m.notice("未发现技能")
			return nil
		}
		lines, err := m.skillList()
		if err != nil {
			m.notice("读取技能失败：" + err.Error())
			return nil
		}
		for _, l := range lines {
			m.notice(l)
		}

	case "/preview":
		if len(fields) > 1 {
			if len(fields) != 2 || fields[1] != "open" {
				m.notice("用法：/preview 或 /preview open")
				return nil
			}
			return m.openPreviewCmd()
		}
		if m.previewInfo == nil {
			m.notice("当前模式不支持预览")
			return nil
		}
		for _, l := range m.previewInfo() {
			m.notice(l)
		}

	case "/log":
		if m.logTail == nil {
			m.notice("当前模式不支持日志查看")
			return nil
		}
		lines, err := m.logTail(200)
		if err != nil {
			m.notice("读取日志失败：" + err.Error())
			return nil
		}
		if len(lines) == 0 {
			m.notice("日志为空（data/logs/agent.log）")
			return nil
		}
		for _, l := range lines {
			m.notice(l)
		}

	case "/help":
		m.notice("/new 新建会话 · /sessions 会话列表 · /memory 长期记忆 · /memory rebuild 重读手工区 · /memory reconnect 恢复写入 · /skill 技能列表 · /skill <name> 查看技能 · /skill reload 重新扫描 · /compact 压缩历史 · /mcp MCP 状态 · /mcp reconnect <server|all> 重连 · /preview 预览地址 · ctrl+o 打开预览 · /log 最近日志 · /help 帮助")

	default:
		m.notice("未知命令：" + fields[0] + "（/help 查看）")
	}
	return nil
}

func (m *model) notice(text string) {
	m.blocks = append(m.blocks, block{kind: blockNotice, text: text})
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// blocksFromMessages 把恢复出来的消息还原成渲染块。
func blocksFromMessages(msgs []llm.Message) []block {
	var out []block
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			out = append(out, block{kind: blockText, speaker: "you", text: m.Content})

		case llm.RoleAssistant:
			if strings.TrimSpace(m.Content) != "" {
				out = append(out, block{kind: blockText, speaker: "agent", text: m.Content})
			}
			for _, c := range m.ToolCalls {
				out = append(out, block{
					kind:    blockTool,
					callID:  c.ID,
					tool:    c.Name,
					preview: previewArgs(c.Args),
					status:  "running",
				})
			}

		case llm.RoleTool:
			content := stripUntrusted(m.Content)
			idx := -1
			for i := len(out) - 1; i >= 0; i-- {
				if out[i].kind == blockTool && out[i].callID == m.ToolCallID {
					idx = i
					break
				}
			}
			status := "ok"
			if strings.Contains(content, "用户拒绝执行") {
				status = "denied"
			}
			if idx >= 0 {
				out[idx].status = status
				out[idx].result = content
				continue
			}
			out = append(out, block{kind: blockTool, tool: "tool", status: status, result: content})
		}
	}
	return out
}

// stripUntrusted 去掉展示用的 <untrusted_content> 包装。
func stripUntrusted(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "<untrusted_content") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	} else {
		return s
	}
	s = strings.TrimSuffix(s, "</untrusted_content>")
	return strings.TrimSpace(s)
}
