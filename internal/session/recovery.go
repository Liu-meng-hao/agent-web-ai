package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Recovery 是恢复扫描的结果。
type Recovery struct {
	Events         []Event
	TruncatedBytes int64
	Warnings       []string
	Interrupted    []string
}

// Recover 扫描事件文件并修复：坏尾截断、坏字节另存、校正 meta 事件数。
//
// 规则：遇到无法解析的行立即停止，绝不猜测；截断点之后的内容全部放弃，
// 但会把原始字节保存在 events.bad，便于事后排查。
func Recover(dir string) (*Recovery, error) { return scan(dir, true) }

// Scan 只读扫描，不修改任何文件（replay 用）。
func Scan(dir string) (*Recovery, error) { return scan(dir, false) }

func scan(dir string, repair bool) (*Recovery, error) {
	path := EventsPath(dir)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Recovery{}, nil
		}
		return nil, err
	}

	rec := &Recovery{}
	var offset int64
	lineNo := 0

	for offset < int64(len(raw)) {
		lineNo++
		idx := bytes.IndexByte(raw[offset:], '\n')
		hasNL := idx >= 0
		next := int64(len(raw))
		var line []byte
		if hasNL {
			line = raw[offset : offset+int64(idx)]
			next = offset + int64(idx) + 1
		} else {
			line = raw[offset:]
		}

		trimmed := bytes.TrimSpace(line)
		reason := ""
		var e Event
		switch {
		case len(trimmed) == 0:
			reason = "为空行"
		case json.Unmarshal(trimmed, &e) != nil:
			reason = "无法解析"
		case e.Seq != int64(len(rec.Events)+1):
			reason = fmt.Sprintf("seq=%d 不连续（期望 %d）", e.Seq, len(rec.Events)+1)
		}

		if reason != "" {
			rec.Warnings = append(rec.Warnings, fmt.Sprintf("第 %d 行%s，事件流在此中止", lineNo, reason))
			rec.TruncatedBytes = int64(len(raw)) - offset
			if repair {
				if err := truncate(path, dir, offset, raw[offset:]); err != nil {
					rec.Interrupted = Interrupted(rec.Events)
					return rec, err
				}
				if err := fixMeta(dir, len(rec.Events), "recovered"); err != nil {
					rec.Interrupted = Interrupted(rec.Events)
					return rec, err
				}
			}
			break
		}

		rec.Events = append(rec.Events, e)
		offset = next
	}

	rec.Interrupted = Interrupted(rec.Events)

	// 即使尾部完整，meta.EventCount 也可能落后（崩在 Sync 之前）。
	if repair && len(rec.Warnings) == 0 {
		if err := fixMetaIfNeeded(dir, len(rec.Events)); err != nil {
			return rec, err
		}
	}
	return rec, nil
}

// truncate 把文件截到 offset，坏字节另存，并修正 meta。
func truncate(path, dir string, offset int64, bad []byte) error {
	if err := os.Truncate(path, offset); err != nil {
		return err
	}
	if len(bad) > 0 {
		f, err := os.OpenFile(filepath.Join(dir, badFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = fmt.Fprintf(f, "--- %s ---\n", time.Now().Format(time.RFC3339))
			_, _ = f.Write(bad)
			if len(bad) > 0 && bad[len(bad)-1] != '\n' {
				_, _ = f.WriteString("\n")
			}
			_ = f.Close()
		}
	}
	return nil
}

// fixMeta 校正事件数与状态。
func fixMeta(dir string, count int, status string) error {
	raw, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return nil
	}
	var m Meta
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	m.EventCount = count
	if status != "" {
		m.Status = status
	}
	return writeMeta(dir, m)
}

// fixMetaIfNeeded 只在事件数不一致时改写 meta，避免每次启动都做无谓写入。
func fixMetaIfNeeded(dir string, count int) error {
	raw, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return nil
	}
	var m Meta
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	if m.EventCount == count {
		return nil
	}
	m.EventCount = count
	return writeMeta(dir, m)
}

// Interrupted 返回有 tool.call 但没有 tool.result 的 call id。
func Interrupted(events []Event) []string {
	pending := map[string]bool{}
	var order []string
	for _, e := range events {
		switch e.Type {
		case TypeToolCall:
			var p ToolCall
			if e.Decode(&p) == nil && p.CallID != "" {
				if !pending[p.CallID] {
					order = append(order, p.CallID)
				}
				pending[p.CallID] = true
			}
		case TypeToolResult:
			var p ToolResult
			if e.Decode(&p) == nil {
				delete(pending, p.CallID)
			}
		}
	}
	var out []string
	for _, id := range order {
		if pending[id] {
			out = append(out, id)
		}
	}
	return out
}
