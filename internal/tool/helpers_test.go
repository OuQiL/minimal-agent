package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"minimal-agent/internal/store"
	"minimal-agent/internal/tool"
)

// newTestStore 在每个用例的临时目录里建库，用例之间天然隔离。
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fakeTool 是一个可配置的测试工具。
//
// 它让注册表、参数校验、panic 兜底等与具体工具无关的行为可以被独立验证，
// 不必依赖 calculator 或 search 这些真实实现。
type fakeTool struct {
	name    string
	desc    string
	aliases []string
	schema  tool.Schema

	output  string
	err     error
	delay   time.Duration
	panics  bool
	lastRaw string
	calls   int
}

func (f *fakeTool) Name() string            { return f.name }
func (f *fakeTool) Description() string     { return f.desc }
func (f *fakeTool) Aliases() []string       { return f.aliases }
func (f *fakeTool) Parameters() tool.Schema { return f.schema }

func (f *fakeTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	f.calls++
	f.lastRaw = string(args)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.panics {
		panic("测试工具故意崩溃")
	}
	if f.err != nil {
		return "", f.err
	}
	return f.output, nil
}

// okTool 构造一个声明完整、执行成功的工具。
func okTool(name string) *fakeTool {
	return &fakeTool{
		name:   name,
		desc:   "测试用工具 " + name,
		output: "来自 " + name + " 的结果",
		schema: tool.Schema{Type: "object"},
	}
}

// stringSchema 构造一个只含一个必填字符串参数的工具。
func stringSchema(param string) tool.Schema {
	return tool.Schema{
		Type: "object",
		Properties: map[string]tool.Property{
			param: {Type: "string", Description: "测试参数"},
		},
		Required: []string{param},
	}
}

var errBoom = errors.New("模拟的执行失败")
