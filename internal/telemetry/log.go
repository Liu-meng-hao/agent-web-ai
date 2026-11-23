// Package telemetry 提供日志落盘、单轮 trace 与 token/成本记账。
//
// 三条约定：
//   - 日志是 slog JSON，一行一条，追加到 data/logs/agent.log；
//   - trace 每个 turn 一份 JSON，失败不影响主流程；
//   - 成本按 config/pricing.yaml 的单价计算，缺配置时按 0 计。
package telemetry

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxLogBytes 是单个日志文件的轮转阈值；超过后改名为 agent.log.1。
const maxLogBytes = 5 << 20

// LogWarn 追加一条 JSON 告警日志。
//
// 每次打开再关闭文件：调用频率很低（启动告警、压缩降级等），
// 换取进程异常退出时也不会丢最后一条。
func LogWarn(path, msg string) {
	writeLog(path, slog.LevelWarn, msg, nil)
}

// LogInfo 追加一条 JSON 信息日志。
func LogInfo(path, msg string) {
	writeLog(path, slog.LevelInfo, msg, nil)
}

// LogEvent 追加一条带结构化字段的信息日志（turn、工具调用等关键事件）。
func LogEvent(path, msg string, attrs map[string]any) {
	writeLog(path, slog.LevelInfo, msg, attrs)
}

// LogWarnEvent 追加一条带结构化字段的告警日志（工具失败、压缩失败等）。
func LogWarnEvent(path, msg string, attrs map[string]any) {
	writeLog(path, slog.LevelWarn, msg, attrs)
}

func writeLog(path string, level slog.Level, msg string, attrs map[string]any) {
	writeLogAttrs(path, level, msg, attrs)
}

func writeLogAttrs(path string, level slog.Level, msg string, attrs map[string]any) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	rotate(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	logger := slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	args := make([]any, 0, len(attrs)*2)
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, k, attrs[k])
	}
	if level >= slog.LevelWarn {
		logger.Warn(msg, args...)
		return
	}
	logger.Info(msg, args...)
}

// rotate 在日志超过阈值时把当前文件改名为 .1（只保留一份历史）。
func rotate(path string) {
	st, err := os.Stat(path)
	if err != nil || st.Size() < maxLogBytes {
		return
	}
	_ = os.Remove(path + ".1")
	_ = os.Rename(path, path+".1")
}

// Tail 返回日志文件最后 n 行（n<=0 时按 200 计）。
//
// 从文件尾部按块倒读，避免 /log 每次都把整个文件从头扫一遍。
func Tail(path string, n int) ([]string, error) {
	if n <= 0 {
		n = 200
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const chunkSize = 32 << 10
	var data []byte
	pos := st.Size()
	for pos > 0 {
		readLen := int64(chunkSize)
		if pos < readLen {
			readLen = pos
		}
		pos -= readLen
		buf := make([]byte, readLen)
		if _, err := f.ReadAt(buf, pos); err != nil && err != io.EOF {
			return nil, err
		}
		data = append(buf, data...)
		// 多留一行，抵消末尾空行与半行。
		if bytes.Count(data, []byte{'\n'}) > n+1 {
			break
		}
	}

	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}
