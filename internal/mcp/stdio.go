package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxRPCMessageBytes = 32 << 20

var responseIDPattern = regexp.MustCompile(`"id"\s*:\s*([0-9]+)`)

// stdioTransport 通过子进程的 stdin/stdout 交换按行分隔的 JSON-RPC。
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[int64]chan *rpcResponse

	notifyMu       sync.RWMutex
	onNotification func(*rpcRequest)

	stderrMu sync.Mutex
	stderr   strings.Builder

	closeOnce sync.Once
	done      chan struct{}
	err       error
}

// startStdio 启动子进程；除 Windows 必须执行 .cmd/.bat 外，不经过 shell 解释。
func startStdio(command string, args []string, extraEnv map[string]string) (*stdioTransport, error) {
	name, cmdArgs := commandForOS(command, args)
	cmd := exec.Command(name, cmdArgs...)
	cmd.Env = mergeEnv(extraEnv)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动 MCP server %q 失败: %w", command, err)
	}

	t := &stdioTransport{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		pending: map[int64]chan *rpcResponse{},
		done:    make(chan struct{}),
	}
	go t.readLoop()
	go t.drainStderr(stderr)
	return t, nil
}

// commandForOS 处理 Windows 下 .cmd/.bat 需要经 cmd.exe 执行的问题。
// npx/uvx 常以无扩展名的命令写入配置，因此必须先看 LookPath 的实际解析结果。
func commandForOS(command string, args []string) (string, []string) {
	if runtime.GOOS != "windows" {
		return command, args
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		// 保留原命令，让 exec.Start 返回标准的“找不到可执行文件”错误。
		return command, args
	}
	lower := strings.ToLower(resolved)
	if strings.HasSuffix(lower, ".cmd") || strings.HasSuffix(lower, ".bat") {
		return "cmd.exe", append([]string{"/d", "/c", resolved}, args...)
	}
	return resolved, args
}

// mergeEnv 只透传进程运行必需的白名单变量，再叠加配置显式声明的变量。
func mergeEnv(extra map[string]string) []string {
	keep := map[string]bool{
		"PATH": true, "HOME": true, "USERPROFILE": true, "SYSTEMROOT": true,
		"WINDIR": true, "TEMP": true, "TMP": true, "APPDATA": true,
		"LOCALAPPDATA": true, "COMSPEC": true, "PATHEXT": true,
		"LANG": true, "LC_ALL": true,
	}
	var out []string
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 && keep[strings.ToUpper(kv[:i])] {
			out = append(out, kv)
		}
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

func (t *stdioTransport) SetNotificationHandler(handler func(*rpcRequest)) {
	t.notifyMu.Lock()
	t.onNotification = handler
	t.notifyMu.Unlock()
}

func (t *stdioTransport) readLoop() {
	defer close(t.done)
	r := bufio.NewReaderSize(t.stdout, 64*1024)
	var readErr error
	for {
		line, tooLarge, err := readRPCLine(r)
		if tooLarge {
			t.failOversized(line)
		} else if len(bytes.TrimSpace(line)) > 0 {
			t.dispatchLine(line)
		}
		if err != nil {
			readErr = err
			break
		}
	}

	t.pendingMu.Lock()
	if readErr != nil && readErr != io.EOF {
		t.err = fmt.Errorf("读取 MCP server 输出失败: %w；%s", readErr, t.stderrText())
	} else {
		t.err = fmt.Errorf("MCP server 已退出：%s", t.stderrText())
	}
	for id, ch := range t.pending {
		close(ch)
		delete(t.pending, id)
	}
	t.pendingMu.Unlock()
}

// readRPCLine 限制保留的内存，同时丢弃超限行的剩余部分，让后续响应仍可解析。
func readRPCLine(r *bufio.Reader) (line []byte, tooLarge bool, err error) {
	line = make([]byte, 0, 64*1024)
	for {
		chunk, readErr := r.ReadSlice('\n')
		if !tooLarge {
			remaining := maxRPCMessageBytes - len(line)
			if len(chunk) > remaining {
				line = append(line, chunk[:remaining]...)
				tooLarge = true
			} else {
				line = append(line, chunk...)
			}
		}
		switch readErr {
		case nil:
			return line, tooLarge, nil
		case bufio.ErrBufferFull:
			continue
		default:
			return line, tooLarge, readErr
		}
	}
}

func (t *stdioTransport) dispatchLine(line []byte) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err == nil && req.Method != "" {
		if req.ID == 0 {
			t.notifyMu.RLock()
			handler := t.onNotification
			t.notifyMu.RUnlock()
			if handler != nil {
				handler(&req)
			}
		}
		return // 暂不支持 server 主动发起的请求。
	}

	var resp rpcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		// 非 JSON 行（日志）直接忽略，不能让它拖垮会话。
		return
	}
	if resp.ID == 0 {
		return
	}
	t.deliver(&resp)
}

func (t *stdioTransport) failOversized(line []byte) {
	match := responseIDPattern.FindSubmatch(line)
	if len(match) != 2 {
		return // 无法定位调用时保留连接，等待调用自身 deadline。
	}
	id, err := strconv.ParseInt(string(match[1]), 10, 64)
	if err != nil || id == 0 {
		return
	}
	t.deliver(&rpcResponse{ID: id, Error: &rpcError{
		Code:    -32000,
		Message: fmt.Sprintf("单条 MCP 消息超过 %d MiB，已丢弃该响应", maxRPCMessageBytes>>20),
	}})
}

func (t *stdioTransport) deliver(resp *rpcResponse) {
	t.pendingMu.Lock()
	ch := t.pending[resp.ID]
	delete(t.pending, resp.ID)
	t.pendingMu.Unlock()
	if ch != nil {
		ch <- resp
	}
}

func (t *stdioTransport) drainStderr(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			t.stderrMu.Lock()
			if t.stderr.Len() < 8192 {
				t.stderr.Write(buf[:n])
			}
			t.stderrMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (t *stdioTransport) stderrText() string {
	t.stderrMu.Lock()
	defer t.stderrMu.Unlock()
	s := strings.TrimSpace(t.stderr.String())
	if len(s) > 400 {
		s = s[len(s)-400:]
	}
	return s
}

func (t *stdioTransport) RoundTrip(ctx context.Context, req *rpcRequest) (*rpcResponse, error) {
	ch := make(chan *rpcResponse, 1)
	t.pendingMu.Lock()
	if t.err != nil {
		err := t.err
		t.pendingMu.Unlock()
		return nil, err
	}
	t.pending[req.ID] = ch
	t.pendingMu.Unlock()

	if err := t.write(req); err != nil {
		t.pendingMu.Lock()
		delete(t.pending, req.ID)
		t.pendingMu.Unlock()
		return nil, err
	}

	select {
	case resp, ok := <-ch:
		if !ok || resp == nil {
			return nil, fmt.Errorf("MCP server 已退出：%s", t.stderrText())
		}
		return resp, nil
	case <-ctx.Done():
		t.pendingMu.Lock()
		delete(t.pending, req.ID)
		t.pendingMu.Unlock()
		return nil, ctx.Err()
	}
}

func (t *stdioTransport) Notify(ctx context.Context, req *rpcRequest) error {
	return t.write(req)
}

func (t *stdioTransport) write(req *rpcRequest) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(append(raw, '\n')); err != nil {
		return fmt.Errorf("写入 MCP server 失败: %w", err)
	}
	return nil
}

func (t *stdioTransport) Close() error {
	var err error
	t.closeOnce.Do(func() {
		_ = t.stdin.Close()
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		waitDone := make(chan struct{})
		go func() {
			_ = t.cmd.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
			err = fmt.Errorf("MCP server 未在 2s 内退出")
		}
	})
	return err
}
