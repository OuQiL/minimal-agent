package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"minimal-agent/internal/tool"
)

func TestCall_Success(t *testing.T) {
	r := tool.NewRegistry()
	ft := okTool("calculator")
	ft.schema = stringSchema("expression")
	if err := r.Register(ft); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	res := r.Call(context.Background(), "calculator", json.RawMessage(`{"expression":"1+1"}`))
	if res.Err != nil {
		t.Fatalf("执行失败: %v", res.Err)
	}
	if res.Tool != "calculator" {
		t.Errorf("解析到的工具名 = %q", res.Tool)
	}
	if !strings.Contains(res.Output, "calculator") {
		t.Errorf("输出 = %q", res.Output)
	}
}

func TestCall_UnknownToolBackfillsCandidates(t *testing.T) {
	r := tool.NewRegistry()
	for _, n := range []string{"calculator", "weather"} {
		if err := r.Register(okTool(n)); err != nil {
			t.Fatalf("注册失败: %v", err)
		}
	}

	res := r.Call(context.Background(), "get_wether", json.RawMessage(`{}`))

	if !res.Unknown {
		t.Error("应标记为未知工具")
	}
	if res.Err == nil {
		t.Error("未知工具应报告错误状态")
	}
	for _, n := range []string{"calculator", "weather"} {
		if !strings.Contains(res.Output, n) {
			t.Errorf("回填内容应列出可用工具 %q，实际：%s", n, res.Output)
		}
	}
}

func TestCall_PassesRawArgumentsThrough(t *testing.T) {
	r := tool.NewRegistry()
	ft := okTool("echo")
	ft.schema = stringSchema("text")
	if err := r.Register(ft); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	raw := `{"text":"原样传递"}`
	res := r.Call(context.Background(), "echo", json.RawMessage(raw))
	if res.Err != nil {
		t.Fatalf("执行失败: %v", res.Err)
	}
	if ft.lastRaw != raw {
		t.Errorf("工具收到的原始参数 = %q，期望 %q", ft.lastRaw, raw)
	}
}

// 参数校验失败不应执行工具，而应把可读错误回填，让模型自行纠正。
func TestCall_ValidationFailureDoesNotExecute(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"缺少必填参数", `{}`},
		{"参数类型不符", `{"text":123}`},
		{"参数为 null", `{"text":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tool.NewRegistry()
			ft := okTool("echo")
			ft.schema = stringSchema("text")
			if err := r.Register(ft); err != nil {
				t.Fatalf("注册失败: %v", err)
			}

			res := r.Call(context.Background(), "echo", json.RawMessage(tc.args))
			if res.Err == nil {
				t.Fatal("校验失败应报告错误")
			}
			if ft.calls != 0 {
				t.Errorf("校验失败时不应执行工具，实际执行 %d 次", ft.calls)
			}
			if strings.TrimSpace(res.Output) == "" {
				t.Error("应回填可读的错误说明")
			}
		})
	}
}

// 工具内部的 panic 必须被兜住，不能终止整个进程。
func TestCall_PanicIsRecovered(t *testing.T) {
	r := tool.NewRegistry()
	ft := okTool("explosive")
	ft.panics = true
	ft.schema = stringSchema("text")
	if err := r.Register(ft); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	res := r.Call(context.Background(), "explosive", json.RawMessage(`{"text":"x"}`))

	if res.Err == nil {
		t.Fatal("panic 应被转换成错误")
	}
	if !strings.Contains(res.Output, "内部错误") {
		t.Errorf("回填内容应说明发生了内部错误，实际：%s", res.Output)
	}
}

func TestCall_ExecutionErrorIsReturned(t *testing.T) {
	r := tool.NewRegistry()
	ft := okTool("failing")
	ft.err = errBoom
	ft.schema = stringSchema("text")
	if err := r.Register(ft); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	res := r.Call(context.Background(), "failing", json.RawMessage(`{"text":"x"}`))
	if res.Err == nil {
		t.Fatal("执行失败应报告错误")
	}
	if !strings.Contains(res.Output, "模拟的执行失败") {
		t.Errorf("回填内容应包含失败原因，实际：%s", res.Output)
	}
}

func TestCall_AliasHitIsReported(t *testing.T) {
	r := tool.NewRegistry()
	ft := okTool("weather")
	ft.aliases = []string{"get_weather"}
	ft.schema = stringSchema("city")
	if err := r.Register(ft); err != nil {
		t.Fatalf("注册失败: %v", err)
	}

	res := r.Call(context.Background(), "get_weather", json.RawMessage(`{"city":"北京"}`))
	if res.Err != nil {
		t.Fatalf("别名调用应成功: %v", res.Err)
	}
	if !res.ViaAlias {
		t.Error("应标记为别名命中")
	}
	if res.Tool != "weather" {
		t.Errorf("应解析回规范名 weather，实际 %q", res.Tool)
	}
}

func TestSessionIDRoundTrip(t *testing.T) {
	ctx := tool.WithSessionID(context.Background(), "sess_abc")
	if got := tool.SessionIDFrom(ctx); got != "sess_abc" {
		t.Errorf("会话标识 = %q，期望 sess_abc", got)
	}
	if got := tool.SessionIDFrom(context.Background()); got != "" {
		t.Errorf("未设置时应返回空串，实际 %q", got)
	}
}
