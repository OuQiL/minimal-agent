package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"strconv"
)

// CalculatorTool 对数学表达式求值。
//
// 实现方式是用标准库的 go/parser 把表达式解析成 AST，再只对算术节点求值。
// 这带来两个好处：不必自己写词法分析与语法分析；且不存在 eval 类方案的
// 代码执行风险——任何非算术语法都会在求值阶段被拒绝。
type CalculatorTool struct{}

// NewCalculator 创建计算器工具。
func NewCalculator() *CalculatorTool { return &CalculatorTool{} }

func (c *CalculatorTool) Name() string { return "calculator" }

func (c *CalculatorTool) Description() string {
	return "对数学表达式求值。支持加(+)、减(-)、乘(*)、除(/)、取余(%)、乘方(^)与括号。" +
		"涉及数值计算时应使用本工具，不要心算。"
}

func (c *CalculatorTool) Aliases() []string { return []string{"calc", "compute"} }

func (c *CalculatorTool) Parameters() Schema {
	return Schema{
		Type: "object",
		Properties: map[string]Property{
			"expression": {
				Type:        "string",
				Description: `要求值的数学表达式，例如 "(1+2)*3" 或 "2^10"`,
			},
		},
		Required: []string{"expression"},
	}
}

func (c *CalculatorTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Expression string `json:"expression"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("参数解析失败：%v", err)
	}
	if in.Expression == "" {
		return "", fmt.Errorf("表达式为空")
	}

	expr, err := parser.ParseExpr(in.Expression)
	if err != nil {
		return "", fmt.Errorf("表达式无法解析，请检查括号与运算符：%v", err)
	}
	v, err := evalExpr(expr)
	if err != nil {
		return "", err
	}
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return "", fmt.Errorf("计算结果不是有限数")
	}
	return fmt.Sprintf("%s = %s", in.Expression, formatNumber(v)), nil
}

func evalExpr(n ast.Expr) (float64, error) {
	switch v := n.(type) {
	case *ast.BasicLit:
		if v.Kind != token.INT && v.Kind != token.FLOAT {
			return 0, fmt.Errorf("表达式中含有不支持的常量：%s", v.Value)
		}
		f, err := strconv.ParseFloat(v.Value, 64)
		if err != nil {
			return 0, fmt.Errorf("无法解析数值 %s", v.Value)
		}
		return f, nil

	case *ast.ParenExpr:
		return evalExpr(v.X)

	case *ast.UnaryExpr:
		x, err := evalExpr(v.X)
		if err != nil {
			return 0, err
		}
		switch v.Op {
		case token.SUB:
			return -x, nil
		case token.ADD:
			return x, nil
		default:
			return 0, fmt.Errorf("不支持的一元运算符：%s", v.Op)
		}

	case *ast.BinaryExpr:
		x, err := evalExpr(v.X)
		if err != nil {
			return 0, err
		}
		y, err := evalExpr(v.Y)
		if err != nil {
			return 0, err
		}
		switch v.Op {
		case token.ADD:
			return x + y, nil
		case token.SUB:
			return x - y, nil
		case token.MUL:
			return x * y, nil
		case token.QUO:
			if y == 0 {
				return 0, fmt.Errorf("除数不能为零")
			}
			return x / y, nil
		case token.REM:
			if y == 0 {
				return 0, fmt.Errorf("取余的除数不能为零")
			}
			return math.Mod(x, y), nil
		case token.XOR:
			// 在 Go 中 ^ 是按位异或，但数学表达式的书写习惯里它是乘方。
			// 这里按乘方解释，因为本工具的语境是数学计算。
			return math.Pow(x, y), nil
		default:
			return 0, fmt.Errorf("不支持的运算符：%s", v.Op)
		}

	default:
		return 0, fmt.Errorf("表达式中含有不支持的语法，仅支持数字、四则运算、取余、乘方与括号")
	}
}

// formatNumber 去掉整数值多余的小数部分，让 3 显示为 3 而不是 3.000000。
func formatNumber(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
