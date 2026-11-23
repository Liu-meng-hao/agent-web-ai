package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type noopInput struct {
	Name string `json:"name" jsonschema:"名字"`
}

func TestRegistryRegisterAndSchemas(t *testing.T) {
	reg := NewRegistry()
	mk := func(name string) Tool {
		return Define(name, "noop", RiskRead, func(ctx context.Context, in noopInput, tc *CallContext) (Result, error) {
			return Result{Content: in.Name}, nil
		})
	}
	if err := reg.Register(mk("zeta")); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(mk("alpha")); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(mk("alpha")); err == nil {
		t.Fatal("重复注册应报错")
	}

	schemas := reg.Schemas()
	if len(schemas) != 2 {
		t.Fatalf("schemas = %d", len(schemas))
	}
	// 排序必须稳定，否则每轮 prompt 前缀抖动、破坏缓存。
	if schemas[0].Name != "alpha" || schemas[1].Name != "zeta" {
		t.Fatalf("顺序错误: %v, %v", schemas[0].Name, schemas[1].Name)
	}

	var schema map[string]any
	if err := json.Unmarshal(schemas[0].Parameters, &schema); err != nil {
		t.Fatalf("schema 不是合法 JSON: %v", err)
	}
	if _, ok := schema["$schema"]; ok {
		t.Fatal("$schema 应被剥离")
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v", schema["type"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || props["name"] == nil {
		t.Fatalf("schema 缺少 name 属性: %v", schema)
	}
}

// TestBuiltinToolSchemasHaveDescriptions 防止 description 标签格式退化：
// invopop 只认 key=value，写成裸文本会导致描述为空，模型看不到参数含义。
func TestBuiltinToolSchemasHaveDescriptions(t *testing.T) {
	reg := NewRegistry()
	for _, tool := range []Tool{NewShell(), NewFSRead(), NewFSList(), NewArtifactWrite(nil)} {
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}

	for _, s := range reg.Schemas() {
		var schema struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(s.Parameters, &schema); err != nil {
			t.Fatalf("%s schema 解析失败: %v", s.Name, err)
		}
		if len(schema.Properties) == 0 {
			t.Fatalf("%s 没有 properties", s.Name)
		}
		for name, prop := range schema.Properties {
			if strings.TrimSpace(prop.Description) == "" {
				t.Errorf("%s.%s 缺少 description（jsonschema 标签格式退化？）", s.Name, name)
			}
		}
	}
}

func TestFuncToolExecute(t *testing.T) {
	tool := Define("echo", "回显", RiskRead, func(ctx context.Context, in noopInput, tc *CallContext) (Result, error) {
		return Result{Content: "hi " + in.Name}, nil
	})
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"bob"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hi bob" {
		t.Fatalf("content = %q", res.Content)
	}
}
