package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"agent/internal/config"
	"agent/internal/tools"
)

// 服务状态：idle → starting → ready / degraded → stopped。
const (
	StatusIdle     = "idle"
	StatusStarting = "starting"
	StatusReady    = "ready"
	StatusDegraded = "degraded"
	StatusStopped  = "stopped"
	StatusDisabled = "disabled"
)

const failuresBeforeDegraded = 3

// Options 是 Manager 的运行期依赖。
type Options struct {
	// ImageDir 存放 MCP 返回的图片与二进制资源。
	ImageDir string
	// OverflowDir 存放超大文本结果；为空时回退 ImageDir。
	OverflowDir string
	// Warn 接收降级、通知与重连消息，可为 nil。
	Warn func(string)
}

// Manager 管理一组 MCP server，并把它们的工具适配为 tools.Tool。
//
// 单个 server 失败只降级该 server，不阻断主流程。
type Manager struct {
	opts    Options
	servers []*server
}

type server struct {
	name string
	cfg  config.MCPServer
	warn func(string)

	callMu      sync.RWMutex // 串行化重连/关闭与在途调用
	mu          sync.RWMutex
	status      string
	err         error
	client      *Client
	tools       []tools.Tool
	failures    int
	namesLocked bool
}

// NewManager 按配置构造 Manager（不连接）。
func NewManager(cfg *config.MCPConfig, opts Options) *Manager {
	m := &Manager{opts: opts}
	if cfg == nil {
		return m
	}
	names := make([]string, 0, len(cfg.Servers))
	for name := range cfg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m.servers = append(m.servers, &server{
			name:   name,
			cfg:    cfg.Servers[name],
			warn:   m.warn,
			status: StatusIdle,
		})
	}
	return m
}

// Start 并发连接所有 server；失败降级并继续。ctx 可用于取消整个启动过程。
func (m *Manager) Start(ctx context.Context) {
	var wg sync.WaitGroup
	for _, s := range m.servers {
		if s.cfg.Approval == "deny" {
			s.mu.Lock()
			s.status = StatusDisabled
			s.mu.Unlock()
			continue
		}
		if s.cfg.Lazy {
			m.warn(fmt.Sprintf("MCP server %q 的 lazy: true 暂未实现，已忽略并按启动时连接处理", s.name))
		}
		wg.Add(1)
		go func(s *server) {
			defer wg.Done()
			m.startOne(ctx, s)
		}(s)
	}
	wg.Wait()
	m.normalizeToolNames()
}

func (m *Manager) startOne(ctx context.Context, s *server) error {
	s.callMu.Lock()
	defer s.callMu.Unlock()
	s.mu.Lock()
	old := s.client
	s.client = nil
	s.status = StatusStarting
	s.err = nil
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	cctx, cancel := context.WithTimeout(ctx, s.cfg.TimeoutDuration())
	defer cancel()

	client, err := NewClient(cctx, s.name, s.cfg)
	if err != nil {
		m.degrade(s, err)
		return err
	}
	client.SetNoticeHandler(m.warn)
	infos, err := client.ListTools(cctx)
	if err != nil {
		_ = client.Close()
		m.degrade(s, err)
		return err
	}
	client.MarkReady()
	for _, warning := range client.Warnings() {
		m.warn(fmt.Sprintf("MCP server %q：%s", s.name, warning))
	}

	infos, missing := filterInfos(s.cfg, infos)
	if len(missing) > 0 {
		m.warn(fmt.Sprintf("MCP server %q 的 tools 白名单有 %d 项未找到：%s", s.name, len(missing), strings.Join(missing, ", ")))
	}

	made := make([]tools.Tool, 0, len(infos))
	for _, info := range infos {
		approval, explicit := s.cfg.ApprovalFor(info.Name)
		t := newTool(s.name, s, info, approval, explicit, m.opts.ImageDir)
		t.overflowDir = m.opts.OverflowDir
		made = append(made, t)
	}
	s.mu.Lock()
	s.client = client
	s.tools = made
	s.status = StatusReady
	s.err = nil
	s.failures = 0
	s.mu.Unlock()
	return nil
}

// filterInfos 按 tools 白名单裁剪远端工具，并返回配置了但远端不存在的名字。
func filterInfos(cfg config.MCPServer, infos []ToolInfo) (kept []ToolInfo, missing []string) {
	if len(cfg.Tools) == 0 {
		return infos, nil
	}
	allow := make(map[string]bool, len(cfg.Tools))
	for _, name := range cfg.Tools {
		allow[strings.TrimSpace(name)] = true
	}
	for _, info := range infos {
		if !allow[info.Name] {
			continue
		}
		kept = append(kept, info)
		delete(allow, info.Name)
	}
	for name := range allow {
		missing = append(missing, name)
	}
	sort.Strings(missing)
	return kept, missing
}

// normalizeToolNames 在全局范围内消除 sanitize 后的同名冲突。
// 已注册工具的名字永久保留；首次失败后新发现的工具只围绕这些名字分配后缀。
func (m *Manager) normalizeToolNames() {
	used := map[string]bool{}
	for _, s := range m.servers {
		s.mu.RLock()
		if s.namesLocked {
			for _, item := range s.tools {
				if t, ok := item.(*tool); ok {
					used[t.name] = true
				}
			}
		}
		s.mu.RUnlock()
	}

	for _, s := range m.servers {
		s.mu.Lock()
		if s.namesLocked {
			s.mu.Unlock()
			continue
		}
		for _, item := range s.tools {
			t, ok := item.(*tool)
			if !ok {
				continue
			}
			base := toolName(s.name, t.remote)
			name := base
			for n := 2; used[name]; n++ {
				name = fmt.Sprintf("%s_%d", base, n)
			}
			if t.name != name {
				m.warn(fmt.Sprintf("MCP 工具重名已改名：%s → %s", t.name, name))
				t.name = name
			}
			used[name] = true
		}
		if len(s.tools) > 0 {
			s.namesLocked = true
		}
		s.mu.Unlock()
	}
}

// Reconnect 重建单个 server 的连接；name=all 时并发重连全部非 deny server。
// 已注册工具继续持有 server 句柄并保持名称；首次启动失败的 server 会补做工具发现。
func (m *Manager) Reconnect(ctx context.Context, name string) error {
	if name != "" && name != "all" {
		for _, s := range m.servers {
			if s.name == name {
				if s.cfg.Approval == "deny" {
					return fmt.Errorf("MCP server %q 已配置 approval=deny", name)
				}
				err := m.reconnectOne(ctx, s)
				m.normalizeToolNames()
				return err
			}
		}
		return fmt.Errorf("未配置 MCP server %q", name)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(m.servers))
	for _, s := range m.servers {
		if s.cfg.Approval == "deny" {
			continue
		}
		wg.Add(1)
		go func(s *server) {
			defer wg.Done()
			if err := m.reconnectOne(ctx, s); err != nil {
				errCh <- err
			}
		}(s)
	}
	wg.Wait()
	close(errCh)
	m.normalizeToolNames()
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (m *Manager) reconnectOne(ctx context.Context, s *server) error {
	s.mu.RLock()
	hasTools := len(s.tools) > 0
	s.mu.RUnlock()
	if !hasTools {
		err := m.startOne(ctx, s)
		if err == nil {
			m.warn(fmt.Sprintf("MCP server %q 已重连并完成工具发现", s.name))
		}
		return err
	}

	s.callMu.Lock()
	defer s.callMu.Unlock()
	s.mu.Lock()
	old := s.client
	s.client = nil
	s.status = StatusStarting
	s.err = nil
	s.failures = 0
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	cctx, cancel := context.WithTimeout(ctx, s.cfg.TimeoutDuration())
	defer cancel()
	client, err := NewClient(cctx, s.name, s.cfg)
	if err != nil {
		m.degrade(s, err)
		return err
	}
	client.SetNoticeHandler(m.warn)
	s.mu.Lock()
	s.client = client
	s.status = StatusReady
	s.err = nil
	s.failures = 0
	s.mu.Unlock()
	m.warn(fmt.Sprintf("MCP server %q 已重连（沿用已发现工具表）", s.name))
	return nil
}

// CallTool 让已注册工具在重连后使用最新 client。
func (s *server) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallResult, error) {
	s.callMu.RLock()
	defer s.callMu.RUnlock()
	s.mu.RLock()
	client := s.client
	if client == nil {
		status := s.status
		s.mu.RUnlock()
		return nil, fmt.Errorf("MCP server %q 当前不可调用（%s）", s.name, status)
	}
	res, err := client.CallTool(ctx, name, args)
	s.mu.RUnlock()
	s.recordCall(err)
	return res, err
}

func (s *server) recordCall(callErr error) {
	warning := ""
	s.mu.Lock()
	if callErr == nil {
		s.failures = 0
		s.err = nil
		if s.status == StatusDegraded && s.client != nil {
			s.status = StatusReady
		}
	} else {
		s.err = callErr
		if !errors.Is(callErr, context.Canceled) {
			s.failures++
			if s.failures >= failuresBeforeDegraded && s.status != StatusDegraded {
				s.status = StatusDegraded
				warning = fmt.Sprintf("MCP server %q 连续 %d 次调用失败，已标记 degraded：%v", s.name, s.failures, callErr)
			}
		}
	}
	s.mu.Unlock()
	if warning != "" && s.warn != nil {
		s.warn(warning)
	}
}

func (m *Manager) degrade(s *server, err error) {
	s.mu.Lock()
	s.status = StatusDegraded
	s.err = err
	s.failures = 0
	s.mu.Unlock()
	m.warn(fmt.Sprintf("MCP server %q 不可用，已跳过: %v", s.name, err))
}

func (m *Manager) warn(msg string) {
	if m.opts.Warn != nil {
		m.opts.Warn(msg)
	}
}

// Tools 返回所有已发现工具；运行期 degraded 不撤销已注册工具。
func (m *Manager) Tools() []tools.Tool {
	var out []tools.Tool
	for _, s := range m.servers {
		s.mu.RLock()
		out = append(out, s.tools...)
		s.mu.RUnlock()
	}
	return out
}

// Status 返回 /mcp 面板用的状态行。
func (m *Manager) Status() []string {
	if len(m.servers) == 0 {
		return []string{"未配置 MCP server（config/mcp.yaml）"}
	}
	out := make([]string, 0, len(m.servers))
	for _, s := range m.servers {
		s.mu.RLock()
		line := fmt.Sprintf("%s · %s · %d 个工具", s.name, s.status, len(s.tools))
		if s.err != nil {
			line += " · " + s.err.Error()
		}
		if s.cfg.Approval == "auto" {
			line += " · auto：仅显式只读工具免审批"
		}
		if s.cfg.Approval == "deny" {
			line += " · 已禁用，未连接"
		}
		if n := len(s.cfg.Tools); n > 0 {
			line += fmt.Sprintf(" · 白名单 %d 项", n)
		}
		if n := len(s.cfg.ToolApproval); n > 0 {
			line += fmt.Sprintf(" · 单项审批覆盖 %d 项", n)
		}
		s.mu.RUnlock()
		out = append(out, line)
	}
	return out
}

// Close 关闭全部连接。
func (m *Manager) Close() {
	for _, s := range m.servers {
		s.callMu.Lock()
		s.mu.Lock()
		client := s.client
		s.client = nil
		s.status = StatusStopped
		s.mu.Unlock()
		if client != nil {
			_ = client.Close()
		}
		s.callMu.Unlock()
	}
}
