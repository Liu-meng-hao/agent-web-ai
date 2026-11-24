package mcp

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"agent/internal/tools"
)

const maxResultBytes = 32 * 1024

// caller 是适配器依赖的最小客户端能力，便于测试时替换。
type caller interface {
	CallTool(ctx context.Context, name string, args json.RawMessage) (*CallResult, error)
}

// tool 把远端 MCP 工具适配成 tools.Tool。
type tool struct {
	name        string
	remote      string
	desc        string
	schema      json.RawMessage
	risk        tools.Risk
	disabled    string
	imageDir    string
	overflowDir string
	client      caller
}

func newTool(server string, client caller, info ToolInfo, approval string, explicit bool, imageDir string) *tool {
	t := &tool{
		name:     toolName(server, info.Name),
		remote:   info.Name,
		desc:     info.Description,
		schema:   cleanSchema(info.InputSchema),
		risk:     riskForApproval(approval, info, explicit),
		imageDir: imageDir,
		client:   client,
	}
	if approval == "deny" {
		if explicit {
			t.disabled = "tool_approval=deny"
		} else {
			t.disabled = "server 配置 approval=deny"
		}
	}
	return t
}

func (t *tool) Name() string            { return t.name }
func (t *tool) Description() string     { return t.desc }
func (t *tool) Schema() json.RawMessage { return t.schema }
func (t *tool) Risk() tools.Risk        { return t.risk }

// DisabledReason 实现 agent 的可选接口：配置为 deny 的工具不弹审批、直接拒绝。
func (t *tool) DisabledReason() string { return t.disabled }

func (t *tool) Execute(ctx context.Context, args json.RawMessage, _ *tools.CallContext) (tools.Result, error) {
	res, err := t.client.CallTool(ctx, t.remote, args)
	if err != nil {
		return tools.Result{}, err
	}
	text := t.render(res.Content)
	if res.IsError {
		text = "MCP 工具返回错误：\n" + text
	}
	text = t.capResult(text)
	display := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	if len([]rune(display)) > 80 {
		display = string([]rune(display)[:80]) + "…"
	}
	return tools.Result{Content: text, Display: display}, nil
}

// toolName 生成 mcp__<server>__<tool>，只保留 [a-zA-Z0-9_-]。
func toolName(server, name string) string {
	return "mcp__" + sanitize(server) + "__" + sanitize(name)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "tool"
	}
	return out
}

func riskForApproval(approval string, info ToolInfo, explicit bool) tools.Risk {
	// tool_approval 的 auto 是“该工具已确认只读”，直接免审批；
	// 不走 server 级 auto 的注解推断，否则没有注解的浏览器只读工具仍需审批。
	if explicit && approval == "auto" {
		return tools.RiskRead
	}
	switch approval {
	case "auto":
		a := info.Annotations
		if a.DestructiveHint != nil && *a.DestructiveHint {
			return tools.RiskWrite
		}
		if a.ReadOnlyHint != nil && *a.ReadOnlyHint {
			return tools.RiskRead
		}
		// auto 只信任显式只读注解；未标注的远端工具仍需人工确认。
		return tools.RiskNetwork
	case "deny":
		return tools.RiskExec
	default:
		return tools.RiskNetwork
	}
}

func (t *tool) render(blocks []Content) string {
	var parts []string
	for _, c := range blocks {
		switch c.Type {
		case "text":
			parts = append(parts, c.Text)
		case "image":
			path, err := t.saveEncoded(c.Data, c.MIMEType)
			if err != nil {
				parts = append(parts, "[图片保存失败: "+err.Error()+"]")
			} else {
				parts = append(parts, "图片已保存："+path)
			}
		case "resource":
			if c.Resource == nil {
				continue
			}
			if c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
				continue
			}
			if c.Resource.Blob != "" {
				path, err := t.saveEncoded(c.Resource.Blob, c.Resource.MIMEType)
				if err != nil {
					parts = append(parts, "[资源保存失败: "+err.Error()+"]")
				} else {
					parts = append(parts, "资源已保存："+path)
				}
			}
		default:
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func (t *tool) saveEncoded(data, mime string) (string, error) {
	if i := strings.Index(data, "base64,"); i >= 0 {
		data = data[i+len("base64,"):]
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", err
	}
	return t.saveBlob(raw, mime)
}

func (t *tool) saveBlob(raw []byte, mime string) (string, error) {
	if t.imageDir == "" {
		return "", fmt.Errorf("未配置产物目录")
	}
	if err := os.MkdirAll(t.imageDir, 0o755); err != nil {
		return "", err
	}
	sum := sha1.Sum(raw)
	path := filepath.Join(t.imageDir, hex.EncodeToString(sum[:])+extForMIME(mime))
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (t *tool) capResult(text string) string {
	if len(text) <= maxResultBytes {
		return text
	}
	headEnd := maxResultBytes / 2
	for headEnd > 0 && !utf8.ValidString(text[:headEnd]) {
		headEnd--
	}
	tailStart := len(text) - maxResultBytes/4
	for tailStart < len(text) && !utf8.ValidString(text[tailStart:]) {
		tailStart++
	}
	head := text[:headEnd]
	tail := text[tailStart:]
	path := ""
	dir := t.overflowDir
	if dir == "" {
		dir = t.imageDir
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err == nil {
			sum := sha1.Sum([]byte(text))
			p := filepath.Join(dir, "overflow-"+hex.EncodeToString(sum[:])+".txt")
			if os.WriteFile(p, []byte(text), 0o644) == nil {
				path = p
			}
		}
	}
	note := "\n…（结果过大已截断"
	if path != "" {
		note += "，完整内容：" + path
	}
	note += "）…\n"
	return head + note + tail
}

func extForMIME(mime string) string {
	switch strings.ToLower(mime) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "application/json":
		return ".json"
	case "text/plain":
		return ".txt"
	default:
		return ".bin"
	}
}

// cleanSchema 归一化 MCP 返回的 JSON Schema。
//
// 供应商的 strict 模式要求 required 穷举全部字段，会大面积误伤 MCP 工具；
// 这里只做保守清洗，不启用 strict。
func cleanSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	delete(m, "$schema")
	delete(m, "$id")

	// 递归展开本地引用；循环引用保留原样，避免无限递归。
	expanded := expandSchemaRefs(m, m, map[string]bool{})
	if em, ok := expanded.(map[string]any); ok {
		m = em
	}
	if !hasSchemaRef(m) {
		delete(m, "$defs")
		delete(m, "definitions")
	}
	downgradeNullable(m)

	if _, ok := m["type"]; !ok {
		m["type"] = "object"
	}
	// 只在已声明 properties 时关闭额外字段，否则会把开放对象误关死。
	if _, ok := m["properties"]; ok {
		if _, ok := m["additionalProperties"]; !ok {
			m["additionalProperties"] = false
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return out
}

func expandSchemaRefs(v any, root map[string]any, stack map[string]bool) any {
	switch x := v.(type) {
	case map[string]any:
		if ref, ok := x["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
			if stack[ref] {
				return x
			}
			if resolved, ok := resolveLocalRef(root, ref); ok {
				nextStack := make(map[string]bool, len(stack)+1)
				for k, v := range stack {
					nextStack[k] = v
				}
				nextStack[ref] = true
				merged := map[string]any{}
				if rm, ok := expandSchemaRefs(resolved, root, nextStack).(map[string]any); ok {
					for k, v := range rm {
						merged[k] = v
					}
				}
				for k, val := range x {
					if k != "$ref" {
						merged[k] = expandSchemaRefs(val, root, stack)
					}
				}
				return merged
			}
		}
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = expandSchemaRefs(val, root, stack)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = expandSchemaRefs(val, root, stack)
		}
		return out
	default:
		return v
	}
}

func resolveLocalRef(root map[string]any, ref string) (any, bool) {
	var current any = root
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func hasSchemaRef(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["$ref"].(string); ok {
			return true
		}
		for _, val := range x {
			if hasSchemaRef(val) {
				return true
			}
		}
	case []any:
		for _, val := range x {
			if hasSchemaRef(val) {
				return true
			}
		}
	}
	return false
}

// downgradeNullable 把 anyOf: [X, null] 降级为 X（可空字段按可选处理）。
func downgradeNullable(v any) {
	switch x := v.(type) {
	case map[string]any:
		if anyOf, ok := x["anyOf"].([]any); ok && len(anyOf) <= 2 {
			var keep map[string]any
			nulls := 0
			for _, item := range anyOf {
				im, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if im["type"] == "null" {
					nulls++
					continue
				}
				if keep == nil {
					keep = im
				}
			}
			if keep != nil && nulls > 0 {
				delete(x, "anyOf")
				for k, val := range keep {
					if _, exists := x[k]; !exists {
						x[k] = val
					}
				}
			}
		}
		for _, val := range x {
			downgradeNullable(val)
		}
	case []any:
		for _, val := range x {
			downgradeNullable(val)
		}
	}
}
