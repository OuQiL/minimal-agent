package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"minimal-agent/internal/tool"
)

func todoArgs(action, content string) json.RawMessage {
	m := map[string]string{"action": action}
	if content != "" {
		m["content"] = content
	}
	b, _ := json.Marshal(m)
	return b
}

func TestTodo_AddAndList(t *testing.T) {
	td := tool.NewTodo(newTestStore(t))
	ctx := tool.WithSessionID(context.Background(), "sess_a")

	if _, err := td.Execute(ctx, todoArgs("add", "买牛奶")); err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if _, err := td.Execute(ctx, todoArgs("add", "写周报")); err != nil {
		t.Fatalf("新增失败: %v", err)
	}

	out, err := td.Execute(ctx, todoArgs("list", ""))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	for _, want := range []string{"买牛奶", "写周报", "2 条"} {
		if !strings.Contains(out, want) {
			t.Errorf("列表中缺少 %q，实际：%s", want, out)
		}
	}
}

func TestTodo_EmptyList(t *testing.T) {
	td := tool.NewTodo(newTestStore(t))
	ctx := tool.WithSessionID(context.Background(), "sess_empty")

	out, err := td.Execute(ctx, todoArgs("list", ""))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if !strings.Contains(out, "没有待办") {
		t.Errorf("空列表应给出可读说明，实际：%s", out)
	}
}

// 这是需求里明确要求的隔离点：两个会话各自记录待办，互不可见。
func TestTodo_IsolatedBetweenSessions(t *testing.T) {
	td := tool.NewTodo(newTestStore(t))
	ctxA := tool.WithSessionID(context.Background(), "sess_a")
	ctxB := tool.WithSessionID(context.Background(), "sess_b")

	if _, err := td.Execute(ctxA, todoArgs("add", "会话一的待办")); err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if _, err := td.Execute(ctxB, todoArgs("add", "会话二的待办")); err != nil {
		t.Fatalf("新增失败: %v", err)
	}

	outA, err := td.Execute(ctxA, todoArgs("list", ""))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	outB, err := td.Execute(ctxB, todoArgs("list", ""))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}

	if !strings.Contains(outA, "会话一的待办") {
		t.Errorf("会话一应看到自己的待办，实际：%s", outA)
	}
	if strings.Contains(outA, "会话二的待办") {
		t.Errorf("会话一不应看到会话二的待办，实际：%s", outA)
	}
	if !strings.Contains(outB, "会话二的待办") {
		t.Errorf("会话二应看到自己的待办，实际：%s", outB)
	}
	if strings.Contains(outB, "会话一的待办") {
		t.Errorf("会话二不应看到会话一的待办，实际：%s", outB)
	}
}

func TestTodo_RequiresContentWhenAdding(t *testing.T) {
	td := tool.NewTodo(newTestStore(t))
	ctx := tool.WithSessionID(context.Background(), "sess_x")

	_, err := td.Execute(ctx, json.RawMessage(`{"action":"add"}`))
	if err == nil {
		t.Fatal("新增时缺少内容应返回错误")
	}
	if !strings.Contains(err.Error(), "content") {
		t.Errorf("错误信息应指明缺失的参数，实际：%v", err)
	}
}

func TestTodo_RejectsUnknownAction(t *testing.T) {
	td := tool.NewTodo(newTestStore(t))
	ctx := tool.WithSessionID(context.Background(), "sess_x")

	_, err := td.Execute(ctx, todoArgs("delete", "x"))
	if err == nil {
		t.Fatal("不支持的操作应返回错误")
	}
}

func TestTodo_WithoutSessionFails(t *testing.T) {
	td := tool.NewTodo(newTestStore(t))

	_, err := td.Execute(context.Background(), todoArgs("list", ""))
	if err == nil {
		t.Fatal("缺少会话上下文时应返回错误，而不是写入未知会话")
	}
}
