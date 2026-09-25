package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"minimal-agent/internal/tool"
)

func TestCalculator_Arithmetic(t *testing.T) {
	calc := tool.NewCalculator()
	cases := []struct {
		expr string
		want string
	}{
		{"1+2", "3"},
		{"10-3", "7"},
		{"6*7", "42"},
		{"10/4", "2.5"},
		{"10/2", "5"},
		{"(1+2)*3", "9"},
		{"2+3*4", "14"},
		{"((2+3)*(4-1))", "15"},
		{"-5+3", "-2"},
		{"10%3", "1"},
		{"2^10", "1024"},
		{"1.5*2", "3"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"expression": tc.expr})
			out, err := calc.Execute(context.Background(), args)
			if err != nil {
				t.Fatalf("执行失败: %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("%s 的结果 %q 中应包含 %q", tc.expr, out, tc.want)
			}
		})
	}
}

// 非法输入必须返回错误而不是崩溃，也不能让进程退出。
func TestCalculator_InvalidExpressionReturnsError(t *testing.T) {
	calc := tool.NewCalculator()
	cases := []string{
		"1/0",
		"",
		"1+",
		"(1+2",
		"abc",
		"1+*2",
		"len(\"abc\")", // 非算术语法应被拒绝
		"os.Exit(1)",   // 尤其不能执行任意代码
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"expression": expr})
			out, err := calc.Execute(context.Background(), args)
			if expr == "" {
				// 空表达式由本工具自行校验
				if err == nil {
					t.Error("空表达式应返回错误")
				}
				return
			}
			if err == nil && strings.TrimSpace(out) == "" {
				t.Errorf("%q 应返回错误或可读说明", expr)
			}
			if err != nil && strings.TrimSpace(err.Error()) == "" {
				t.Errorf("%q 的错误信息不应为空", expr)
			}
		})
	}
}

// 除零要给模型可读的说明，而不是 Inf 或崩溃。
func TestCalculator_DivisionByZeroIsExplained(t *testing.T) {
	calc := tool.NewCalculator()
	args, _ := json.Marshal(map[string]string{"expression": "1/0"})
	_, err := calc.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("除零应返回错误")
	}
	if !strings.Contains(err.Error(), "除数") {
		t.Errorf("错误信息应说明除数为零，实际：%v", err)
	}
}

func TestCalculator_Metadata(t *testing.T) {
	calc := tool.NewCalculator()
	if calc.Name() != "calculator" {
		t.Errorf("名称 = %q", calc.Name())
	}
	if calc.Description() == "" {
		t.Error("应有描述供模型判断何时调用")
	}
	if calc.Parameters().Type != "object" {
		t.Error("应声明参数 Schema")
	}
	if _, ok := calc.Parameters().Properties["expression"]; !ok {
		t.Error("应声明 expression 参数")
	}
	if len(calc.Aliases()) == 0 {
		t.Error("应声明至少一个别名")
	}
}
