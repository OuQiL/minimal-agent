package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"minimal-agent/internal/tool"
)

// Call 是一次待执行的工具调用。
type Call struct {
	// Index 是模型给出的调用序号，用于结果定序。
	Index int
	ID    string
	Name  string
	Args  string
}

// Outcome 是一次工具调用的执行结果。
type Outcome struct {
	Call Call
	// Output 是回填给模型的文本，成功与失败都有值。
	Output string
	// Err 非 nil 表示本次调用未成功。
	Err error
	// Tool 是解析到的规范名；名称无法解析时为空。
	Tool string
	// ViaAlias 表示模型用的是别名。
	ViaAlias bool
	// Unknown 表示工具名完全无法解析。
	Unknown bool
	// StartedAt 与 Duration 供 trace 记录使用。
	StartedAt time.Time
	Duration  time.Duration
}

// Executor 执行一批工具调用并返回结果。
//
// 抽成接口是为了让「并发」成为一个可替换的实现细节：若将来要换成串行、
// 限流或远程执行，循环代码不需要改动。
type Executor interface {
	Execute(ctx context.Context, calls []Call) []Outcome
}

// ConcurrentExecutor 并发执行同轮的多个工具调用。
//
// 为什么并发：接入真实外部服务后，一次搜索是数百毫秒的网络往返。
// 模型若在同一轮请求三个独立查询，串行执行就是三倍等待。
//
// 三条不变式：
//  1. 结果按调用序号定序写入预分配切片，与完成先后无关——否则上下文顺序
//     会抖动，测试将不可复现。
//  2. 单个失败不取消同批其他调用，因此不使用 errgroup 的快速失败语义。
//  3. goroutine 内只做网络调用与解析，不触碰共享可变状态；写库等副作用
//     由调用方在并发结束后串行进行。
type ConcurrentExecutor struct {
	registry *tool.Registry
	limit    int
	timeout  time.Duration
}

// NewConcurrentExecutor 创建并发执行器。limit<=0 时按 1 处理。
func NewConcurrentExecutor(r *tool.Registry, limit int, timeout time.Duration) *ConcurrentExecutor {
	if limit <= 0 {
		limit = 1
	}
	return &ConcurrentExecutor{registry: r, limit: limit, timeout: timeout}
}

// Execute 并发执行全部调用，返回与 calls 等长且顺序一致的结果。
func (e *ConcurrentExecutor) Execute(ctx context.Context, calls []Call) []Outcome {
	out := make([]Outcome, len(calls))
	if len(calls) == 0 {
		return out
	}

	sem := make(chan struct{}, e.limit)
	var wg sync.WaitGroup

	for i, call := range calls {
		wg.Add(1)
		go func(i int, call Call) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			callCtx := ctx
			if e.timeout > 0 {
				var cancel context.CancelFunc
				callCtx, cancel = context.WithTimeout(ctx, e.timeout)
				defer cancel()
			}

			start := time.Now()
			res := e.registry.Call(callCtx, call.Name, json.RawMessage(call.Args))

			// 按下标写入：完成顺序不影响最终排列。
			out[i] = Outcome{
				Call:      call,
				Output:    res.Output,
				Err:       res.Err,
				Tool:      res.Tool,
				ViaAlias:  res.ViaAlias,
				Unknown:   res.Unknown,
				StartedAt: start,
				Duration:  time.Since(start),
			}
		}(i, call)
	}

	wg.Wait()
	return out
}
