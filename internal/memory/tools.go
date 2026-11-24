package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent/internal/tools"
)

// Tools 返回记忆相关工具，由装配层注册进注册表。
func Tools(s *Store) []tools.Tool {
	return []tools.Tool{newWriteTool(s), newSearchTool(s), newUpdateTool(s), newForgetTool(s)}
}

// ReadOnlyTools 返回只读视图下可用的记忆工具。
func ReadOnlyTools(s *Store) []tools.Tool {
	return []tools.Tool{newSearchTool(s)}
}

type writeInput struct {
	Text       string   `json:"text" jsonschema:"description=要记住的事实，一句话写清结论，例如 本仓库使用 pnpm 而不是 npm"`
	Key        string   `json:"key,omitempty" jsonschema:"description=可选的稳定键，如 shell.default；同 key 的新事实会取代旧事实"`
	Scope      string   `json:"scope,omitempty" jsonschema:"description=作用域：user 跨会话偏好，project 本仓库事实，session 仅本会话；默认 project"`
	Tags       []string `json:"tags,omitempty" jsonschema:"description=可选标签，用于检索"`
	Confidence float64  `json:"confidence,omitempty" jsonschema:"description=置信度 0 到 1，默认 0.8"`
}

func newWriteTool(s *Store) tools.Tool {
	return tools.Define("memory_write", "把一条值得长期保留的事实写入长期记忆。只写稳定结论（偏好、约定、环境、决策），不要写临时状态或大段代码。", tools.RiskWrite,
		func(ctx context.Context, in writeInput, tc *tools.CallContext) (tools.Result, error) {
			f := NewFact(in.Text)
			f.Key = in.Key
			if in.Scope != "" {
				f.Scope = in.Scope
			}
			if in.Confidence > 0 {
				f.Confidence = in.Confidence
			}
			f.Tags = in.Tags
			f.Origin = OriginTool
			if tc != nil && tc.SessionID != nil {
				f.Source.Session = tc.SessionID()
			}
			if f.Scope == ScopeSession && f.Source.Session == "" {
				return tools.Result{Content: "session 作用域需要会话上下文，请改用 project 作用域"}, nil
			}

			saved, changed, err := s.Write(f)
			if err != nil {
				return tools.Result{Content: "写入记忆被拒绝：" + err.Error()}, nil
			}
			if !changed {
				return tools.Result{Content: fmt.Sprintf("已存在相同事实 [%s]，未重复写入", saved.ID)}, nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "已记住 [%s]", saved.ID)
			if saved.Key != "" {
				fmt.Fprintf(&b, "（key=%s）", saved.Key)
			}
			fmt.Fprintf(&b, "：%s", saved.Text)
			if saved.Supersedes != "" {
				fmt.Fprintf(&b, "；已取代旧事实 [%s]", saved.Supersedes)
			}
			return tools.Result{Content: b.String()}, nil
		})
}

type searchInput struct {
	Query string `json:"query" jsonschema:"description=检索关键词或自然语言问题"`
	Limit int    `json:"limit,omitempty" jsonschema:"description=最多返回条数，默认 8，最大 20"`
}

func newSearchTool(s *Store) tools.Tool {
	return tools.Define("memory_search", "检索长期记忆中的事实。当用户提到过去、之前、上次、偏好、约定时，先查记忆再回答。", tools.RiskRead,
		func(ctx context.Context, in searchInput, tc *tools.CallContext) (tools.Result, error) {
			q := strings.TrimSpace(in.Query)
			if q == "" {
				return tools.Result{Content: "query 不能为空"}, nil
			}
			limit := in.Limit
			if limit <= 0 || limit > 20 {
				limit = 8
			}
			sid := ""
			if tc != nil && tc.SessionID != nil {
				sid = tc.SessionID()
			}
			facts := s.Search(q, limit, sid)
			if len(facts) == 0 {
				return tools.Result{Content: "没有找到相关记忆"}, nil
			}
			var b strings.Builder
			for _, f := range facts {
				fmt.Fprintf(&b, "- [%s][%s] ", f.ID, f.Scope)
				if f.Key != "" {
					fmt.Fprintf(&b, "`%s` — ", f.Key)
				}
				fmt.Fprintf(&b, "%s（%s）\n", f.Text, f.UpdatedAt.Format("2006-01-02"))
			}
			return tools.Result{Content: strings.TrimRight(b.String(), "\n")}, nil
		})
}

type updateInput struct {
	Key        string   `json:"key" jsonschema:"description=要修正的事实的稳定键，必须已经存在"`
	Scope      string   `json:"scope,omitempty" jsonschema:"description=作用域：user 跨会话偏好，project 本仓库事实，session 仅本会话；默认 project"`
	Old        string   `json:"old" jsonschema:"description=修正前的当前值，必须与记忆中的内容完全一致"`
	New        string   `json:"new" jsonschema:"description=修正后的新值"`
	Tags       []string `json:"tags,omitempty" jsonschema:"description=可选：替换标签；省略则保留原标签"`
	Confidence float64  `json:"confidence,omitempty" jsonschema:"description=可选：新的置信度 0 到 1；省略则保留原值"`
}

// updateTool 在通用工具之上附带审批预览（diff）能力。
type updateTool struct {
	tools.Tool
	s *Store
}

func newUpdateTool(s *Store) tools.Tool {
	base := tools.Define("memory_update", "修正一条已有 key 的长期记忆：必须提供旧值 old 与新值 new。只改已存在的 key，新事实请用 memory_write。MEMORY.md 手工区的事实只能由用户编辑。修正会保留历史快照。", tools.RiskWrite,
		func(ctx context.Context, in updateInput, tc *tools.CallContext) (tools.Result, error) {
			sid := ""
			if tc != nil && tc.SessionID != nil {
				sid = tc.SessionID()
			}
			updated, changed, err := s.Update(UpdateInput{
				Key:        in.Key,
				Scope:      in.Scope,
				OldText:    in.Old,
				NewText:    in.New,
				Tags:       in.Tags,
				Confidence: in.Confidence,
				SessionID:  sid,
			})
			if err != nil {
				return tools.Result{Content: "修正记忆被拒绝：" + err.Error()}, nil
			}
			if !changed {
				return tools.Result{Content: fmt.Sprintf("记忆 [%s] 未变化，无需修正", updated.ID)}, nil
			}
			return tools.Result{Content: fmt.Sprintf("已修正 [%s]（key=%s）：%s → %s；历史快照已保留。",
				updated.ID, updated.Key, strings.TrimSpace(in.Old), updated.Text)}, nil
		})
	return &updateTool{Tool: base, s: s}
}

// Preview 在审批前展示旧值、新值与行级差异。
func (t *updateTool) Preview(args json.RawMessage, tc *tools.CallContext) string {
	var in updateInput
	if err := json.Unmarshal(args, &in); err != nil {
		return ""
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		return "memory_update：缺少 key"
	}
	sid := ""
	if tc != nil && tc.SessionID != nil {
		sid = tc.SessionID()
	}
	cur, err := t.s.Lookup(key, in.Scope, sid)
	if err != nil {
		return "memory_update：" + err.Error()
	}
	newText := strings.TrimSpace(in.New)

	var b strings.Builder
	fmt.Fprintf(&b, "key: %s   scope: %s   id: %s\n", cur.Key, cur.Scope, cur.ID)
	fmt.Fprintf(&b, "旧值：%s\n", cur.Text)
	fmt.Fprintf(&b, "新值：%s\n", newText)
	if strings.TrimSpace(in.Old) != strings.TrimSpace(cur.Text) {
		b.WriteString("⚠ old 与当前值不一致，确认后执行仍会被拒绝\n")
	}
	for _, line := range diffLines(cur.Text, newText, 20) {
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

type forgetInput struct {
	ID string `json:"id" jsonschema:"description=要归档的事实 id，可先用 memory_search 查到"`
}

func newForgetTool(s *Store) tools.Tool {
	return tools.Define("memory_forget", "归档一条不再正确的记忆。只归档不物理删除，用户仍可在 MEMORY.md 中看到历史。", tools.RiskWrite,
		func(ctx context.Context, in forgetInput, tc *tools.CallContext) (tools.Result, error) {
			id := strings.TrimSpace(in.ID)
			if id == "" {
				return tools.Result{Content: "id 不能为空"}, nil
			}
			full := id
			if f, err := s.Find(id); err == nil {
				full = f.ID
			} else {
				return tools.Result{Content: err.Error()}, nil
			}
			if err := s.Forget(full); err != nil {
				return tools.Result{Content: err.Error()}, nil
			}
			return tools.Result{Content: fmt.Sprintf("已归档 [%s]", full)}, nil
		})
}
