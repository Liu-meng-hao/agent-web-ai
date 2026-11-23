package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ToolTrace 是单轮 trace 里的一次工具调用。
type ToolTrace struct {
	CallID     string `json:"call_id,omitempty"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
}

// TurnTrace 是一轮对话的复盘记录，写到 data/logs/turns/<turn_id>.json。
type TurnTrace struct {
	TurnID     string    `json:"turn_id"`
	SessionID  string    `json:"session_id,omitempty"`
	Role       string    `json:"role,omitempty"`
	Model      string    `json:"model"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	Steps      int       `json:"steps"`
	Messages   int       `json:"messages"`
	TokensIn   int       `json:"tokens_in"`
	TokensOut  int       `json:"tokens_out"`
	// ReasoningTokens 是输出中用于思考的部分，已包含在 TokensOut 内。
	ReasoningTokens int     `json:"reasoning_tokens,omitempty"`
	Cost            float64 `json:"cost"`
	// 摘要（压缩历史）产生的额外开销，单独列出，避免和正常对话混淆。
	SummaryTokensIn  int         `json:"summary_tokens_in,omitempty"`
	SummaryTokensOut int         `json:"summary_tokens_out,omitempty"`
	SummaryCost      float64     `json:"summary_cost,omitempty"`
	Stop             string      `json:"stop"`
	Error            string      `json:"error,omitempty"`
	Tools            []ToolTrace `json:"tools,omitempty"`
}

// WriteTurn 原子写入一份 trace；目录不存在时自动创建。
func WriteTurn(dir string, tr TurnTrace) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建 trace 目录失败: %w", err)
	}
	raw, err := json.MarshalIndent(tr, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 trace 失败: %w", err)
	}
	path := filepath.Join(dir, tr.TurnID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// PruneTurns 删除超过 keepDays 天的 turn trace；keepDays<=0 时按 30 天。
// 返回删除的文件数。目录不存在不算错误。
func PruneTurns(dir string, keepDays int) (int, error) {
	if dir == "" {
		return 0, nil
	}
	if keepDays <= 0 {
		keepDays = 30
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().AddDate(0, 0, -keepDays)
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}
