package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
)

// FuncTool 是「结构体入参 + 函数」形式的工具。
type FuncTool[I any] struct {
	name   string
	desc   string
	risk   Risk
	schema json.RawMessage
	run    func(ctx context.Context, in I, tc *CallContext) (Result, error)
}

// Define 用输入结构体反射生成 JSON Schema，并包装执行函数。
func Define[I any](name, description string, risk Risk, run func(ctx context.Context, in I, tc *CallContext) (Result, error)) *FuncTool[I] {
	var zero I
	reflector := jsonschema.Reflector{DoNotReference: true}
	schema := cleanSchema(reflector.Reflect(&zero))
	return &FuncTool[I]{
		name:   name,
		desc:   description,
		risk:   risk,
		schema: schema,
		run:    run,
	}
}

// Name 返回工具名。
func (t *FuncTool[I]) Name() string { return t.name }

// Description 返回工具描述。
func (t *FuncTool[I]) Description() string { return t.desc }

// Schema 返回入参 JSON Schema。
func (t *FuncTool[I]) Schema() json.RawMessage { return t.schema }

// Risk 返回风险等级。
func (t *FuncTool[I]) Risk() Risk { return t.risk }

// Execute 解析参数并执行。
func (t *FuncTool[I]) Execute(ctx context.Context, args json.RawMessage, tc *CallContext) (Result, error) {
	var in I
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &in); err != nil {
			return Result{}, fmt.Errorf("参数解析失败: %w", err)
		}
	}
	return t.run(ctx, in, tc)
}

// cleanSchema 去掉部分兼容端点不认的顶层字段。
func cleanSchema(s *jsonschema.Schema) json.RawMessage {
	raw, err := json.Marshal(s)
	if err != nil {
		panic("tools: schema 序列化失败: " + err.Error())
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	delete(m, "$schema")
	delete(m, "$id")
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}
