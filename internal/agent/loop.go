// Package agent 实现 Agent 主循环。
//
// 循环是显式的有状态循环而非递归：轮次计数是天然的循环不变量，
// for turn := 0; turn < maxTurns; turn++ 直接表达上限语义；递归则需要
// 把计数沿调用栈传递，且栈深度会成为第二个隐性上限。
//
// 每一轮的处理路径：
//
//	组装上下文 → 流式请求模型 → 按结束原因分支
//	                                ├─ 无工具调用 → 收敛，返回答复
//	                                └─ 有工具调用 → 落库 → 并发执行工具
//	                                                → 落库结果与 trace → 下一轮
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"minimal-agent/internal/contextmgr"
	"minimal-agent/internal/llm"
	"minimal-agent/internal/model"
	"minimal-agent/internal/store"
	"minimal-agent/internal/tool"
)

// Config 控制循环行为。
type Config struct {
	// MaxToolTurns 是单轮用户输入允许的最大工具调用轮次。
	MaxToolTurns int
}

// Exchange 是一次用户输入的完整处理结果。
type Exchange struct {
	// Answer 是面向用户的最终答复；失败时是失败说明。
	Answer string
	// Turns 是本次实际执行的工具调用轮数。
	Turns int
	// ReachedLimit 报告是否因达到轮次上限而终止。
	ReachedLimit bool
	// Failed 报告本轮以失败告终（模型请求失败或响应中断）。
	Failed bool
}

// Loop 是 Agent 主循环。
type Loop struct {
	store    *store.Store
	registry *tool.Registry
	client   *llm.Client
	builder  *contextmgr.Builder
	executor Executor
	cfg      Config
}

// Deps 汇总 Loop 的依赖。
type Deps struct {
	Store    *store.Store
	Registry *tool.Registry
	Client   *llm.Client
	Builder  *contextmgr.Builder
	Config   Config

	// Executor 为 nil 时按下面的并发参数构造默认的并发执行器。
	Executor        Executor
	MaxToolParallel int
	ToolTimeout     time.Duration
}

// New 创建主循环。
func New(d Deps) *Loop {
	exec := d.Executor
	if exec == nil {
		exec = NewConcurrentExecutor(d.Registry, d.MaxToolParallel, d.ToolTimeout)
	}
	return &Loop{
		store:    d.Store,
		registry: d.Registry,
		client:   d.Client,
		builder:  d.Builder,
		executor: exec,
		cfg:      d.Config,
	}
}

// Run 处理一次用户输入，直到收敛或达到轮次上限。
//
// 返回的 error 只表示基础设施故障（如写库失败）。模型请求失败、响应中断、
// 工具执行失败都会被转换成 Exchange 中的说明——任何单次异常都不应让会话
// 进入不可用状态。
func (l *Loop) Run(ctx context.Context, sessionID, input string, sink llm.Sink) (*Exchange, error) {
	if sink == nil {
		sink = llm.NopSink{}
	}

	if err := l.store.AppendMessages(model.Message{
		SessionID: sessionID,
		Role:      model.RoleUser,
		Content:   input,
	}); err != nil {
		return nil, err
	}
	slog.Info("收到用户输入", "session", sessionID, "chars", len([]rune(input)))

	ex := &Exchange{}
	// 跨本轮全部工具轮次记录「工具名 + 参数」指纹，用于识别重复调用。
	// 这是诊断信号而非优化：重复调用往往说明工具描述不够清楚，
	// 或者模型陷入了循环，值得告警而不该被静默缓存掉。
	seen := make(map[string]bool)

	for turn := range l.cfg.MaxToolTurns {
		built, err := l.builder.Build(ctx, sessionID)
		if err != nil {
			return nil, err
		}

		slog.Info("请求模型", "session", sessionID, "turn", turn+1,
			"messages", len(built.Messages), "with_summary", built.SummaryUsed)

		resp, err := l.client.Stream(ctx, built.Messages, built.System, l.toolSpecs(), sink)
		if err != nil {
			return l.explainFailure(sessionID, err)
		}

		// 思维链只落库供查看，不进入后续请求的上下文。
		assistant := model.Message{
			SessionID: sessionID,
			Role:      model.RoleAssistant,
			Content:   resp.Content,
			Reasoning: resp.Reasoning,
			ToolCalls: resp.ToolCalls,
		}
		if err := l.store.AppendMessages(assistant); err != nil {
			return nil, err
		}

		if !resp.WantsTools() {
			ex.Answer = resp.Content
			slog.Info("本轮收敛", "session", sessionID,
				"tool_turns", ex.Turns, "finish", string(resp.Finish))
			if strings.TrimSpace(ex.Answer) == "" {
				ex.Answer = "模型返回了空答复。可以换个说法再问一次。"
				ex.Failed = true
			}
			return ex, nil
		}

		calls := make([]Call, 0, len(resp.ToolCalls))
		for i, tc := range resp.ToolCalls {
			calls = append(calls, Call{Index: i, ID: tc.ID, Name: tc.Name, Args: tc.Args})
		}
		slog.Info("执行工具", "session", sessionID, "count", len(calls))

		// 会话标识必须经由 context 传给工具：todo 这类工具需要知道自己作用于
		// 哪个会话，而循环是唯一知道当前会话的地方。工具是在启动时一次性注册的，
		// 无法把会话绑定在工具实例上。
		outcomes := l.executor.Execute(tool.WithSessionID(ctx, sessionID), calls)
		ex.Turns++

		// 并发执行结束后串行落库：工具结果消息与 trace 都在这里写入，
		// 避免 goroutine 触碰共享的数据库连接。
		toolMsgs := make([]model.Message, 0, len(outcomes))
		for _, oc := range outcomes {
			toolMsgs = append(toolMsgs, model.Message{
				SessionID:  sessionID,
				Role:       model.RoleTool,
				Content:    oc.Output,
				ToolCallID: oc.Call.ID,
			})
		}
		if err := l.store.AppendMessages(toolMsgs...); err != nil {
			return nil, err
		}
		for _, oc := range outcomes {
			l.recordTrace(sessionID, oc, seen)
		}
	}

	ex.ReachedLimit = true
	ex.Answer = fmt.Sprintf(
		"已达到单轮工具调用上限（%d 轮），停止继续调用工具。可以换个问法，或把问题拆小一些再问。",
		l.cfg.MaxToolTurns)
	slog.Warn("达到工具轮次上限", "session", sessionID, "limit", l.cfg.MaxToolTurns)
	return ex, nil
}

// explainFailure 把模型请求失败转成面向用户的说明，并保证会话仍然可用。
func (l *Loop) explainFailure(sessionID string, err error) (*Exchange, error) {
	var interrupted *llm.StreamInterruptedError
	if errors.As(err, &interrupted) {
		slog.Warn("流式响应中断", "session", sessionID,
			"emitted_chars", interrupted.EmittedChars, "err", interrupted.Err)

		// 把用户已经看到的内容落库，使历史与实际所见一致。
		if strings.TrimSpace(interrupted.Content) != "" || strings.TrimSpace(interrupted.Reasoning) != "" {
			if aerr := l.store.AppendMessages(model.Message{
				SessionID: sessionID,
				Role:      model.RoleAssistant,
				Content:   interrupted.Content,
				Reasoning: interrupted.Reasoning,
			}); aerr != nil {
				slog.Warn("保存中断内容失败", "session", sessionID, "err", aerr)
			}
		}
		return &Exchange{
			Failed: true,
			Answer: fmt.Sprintf(
				"响应在输出过程中中断（已输出 %d 个字符）。这类中断不自动重试，以免内容重复；请重新提问。",
				interrupted.EmittedChars),
		}, nil
	}

	var reqErr *llm.RequestError
	if errors.As(err, &reqErr) {
		slog.Error("模型请求失败", "session", sessionID,
			"kind", int(reqErr.Kind), "status", reqErr.StatusCode, "err", reqErr.Err)
		return &Exchange{Failed: true, Answer: reqErr.Hint()}, nil
	}

	slog.Error("模型请求出现未分类错误", "session", sessionID, "err", err)
	return &Exchange{Failed: true, Answer: fmt.Sprintf("请求模型时发生错误：%v", err)}, nil
}

// recordTrace 写入一条工具调用记录，并标记别名命中与重复调用。
func (l *Loop) recordTrace(sessionID string, oc Outcome, seen map[string]bool) {
	toolName := oc.Tool
	if toolName == "" {
		toolName = oc.Call.Name
	}

	key := toolName + "\x00" + oc.Call.Args
	repeated := seen[key]
	seen[key] = true

	status := model.TraceOK
	errText := ""
	if oc.Err != nil {
		status = model.TraceError
		errText = oc.Err.Error()
	}

	trace := model.Trace{
		SessionID:  sessionID,
		ToolName:   toolName,
		Args:       oc.Call.Args,
		Result:     truncateForTrace(oc.Output),
		Status:     status,
		Error:      errText,
		StartedAt:  oc.StartedAt,
		EndedAt:    oc.StartedAt.Add(oc.Duration),
		DurationMS: oc.Duration.Milliseconds(),
		Alias:      oc.ViaAlias,
		Repeated:   repeated,
	}
	if err := l.store.AppendTrace(trace); err != nil {
		slog.Warn("写入 trace 失败", "session", sessionID, "err", err)
	}

	if repeated {
		slog.Warn("模型以相同参数重复调用同一工具",
			"session", sessionID, "tool", toolName, "args", oc.Call.Args)
	}
	if oc.ViaAlias {
		slog.Info("模型使用了工具别名",
			"session", sessionID, "requested", oc.Call.Name, "resolved", toolName)
	}
}

// toolSpecs 把已注册工具转换成提交给模型的声明。
func (l *Loop) toolSpecs() []llm.ToolSpec {
	tools := l.registry.All()
	specs := make([]llm.ToolSpec, 0, len(tools))
	for _, t := range tools {
		schema, err := schemaToMap(t.Parameters())
		if err != nil {
			slog.Warn("工具参数 Schema 无法序列化，将跳过该工具",
				"tool", t.Name(), "err", err)
			continue
		}
		specs = append(specs, llm.ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  schema,
		})
	}
	return specs
}

func schemaToMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// truncateForTrace 裁剪写入 trace 的结果文本。
//
// 完整结果已经存在 messages 表里，trace 只需要保留足够定位问题的片段，
// 否则 trace 表会迅速被大段搜索结果撑大。
func truncateForTrace(s string) string {
	const limit = 500
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "……"
}
