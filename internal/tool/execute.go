package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
)

// Result 是一次工具调用的结果。
//
// Output 始终是「可以回填给模型看的文本」——无论成功还是失败。Err 非 nil
// 表示这次调用没有正常完成，供上层写 trace 时判断状态，但调用方通常仍应
// 把 Output 回填，因为工具失败对模型而言是一条新信息，而不是中断信号。
type Result struct {
	// Tool 是解析到的规范名；名称完全无法解析时为空。
	Tool string
	// Output 是回填给模型的文本。
	Output string
	// Err 非 nil 表示调用未成功。
	Err error
	// ViaAlias 表示模型用的是别名而非规范名。
	ViaAlias bool
	// Unknown 表示工具名完全无法解析。
	Unknown bool
}

// Call 执行一次工具调用。
//
// 它承担三件事：解析名称（归一化 + 别名）、按 Schema 校验参数、
// 以及在执行边界兜住 panic。任何失败都转成可读文本返回，
// 不向上抛出——单个工具的崩溃或错误不应终止整个循环。
func (r *Registry) Call(ctx context.Context, name string, args json.RawMessage) Result {
	t, found, viaAlias := r.Lookup(name)
	if !found {
		msg := r.UnknownMessage(name)
		return Result{
			Output:  msg,
			Err:     fmt.Errorf("未知工具 %q", name),
			Unknown: true,
		}
	}

	if err := Validate(t.Parameters(), args); err != nil {
		return Result{
			Tool:     t.Name(),
			Output:   err.Error(),
			Err:      err,
			ViaAlias: viaAlias,
		}
	}

	out, err := safeExecute(ctx, t, args)
	if err != nil {
		return Result{
			Tool:     t.Name(),
			Output:   err.Error(),
			Err:      err,
			ViaAlias: viaAlias,
		}
	}
	return Result{Tool: t.Name(), Output: out, ViaAlias: viaAlias}
}

// safeExecute 在工具执行边界兜住 panic。
//
// 只在这一层 recover：再往外就不该有 panic 了。堆栈完整写入日志，
// 便于定位真实缺陷，而不是被静默吞掉。
func safeExecute(ctx context.Context, t Tool, args json.RawMessage) (out string, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("工具执行发生内部错误",
				"tool", t.Name(),
				"panic", fmt.Sprint(rec),
				"stack", string(debug.Stack()))
			err = fmt.Errorf("工具 %s 执行时发生内部错误：%v", t.Name(), rec)
			out = ""
		}
	}()
	return t.Execute(ctx, args)
}
