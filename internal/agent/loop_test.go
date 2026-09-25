package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minimal-agent/internal/model"
	"minimal-agent/internal/testsupport"
	"minimal-agent/internal/tool"
)

func TestRun_PlainAnswerConvergesInOneTurn(t *testing.T) {
	h := newHarness(t, []testsupport.Script{testsupport.Answer("你好，有什么可以帮你？")}, harnessOpts{})

	ex := h.run("你好", nil)

	if ex.Failed || ex.ReachedLimit {
		t.Fatalf("不应失败或触上限: %+v", ex)
	}
	if ex.Turns != 0 {
		t.Errorf("工具轮数 = %d，期望 0", ex.Turns)
	}
	if ex.Answer != "你好，有什么可以帮你？" {
		t.Errorf("答复 = %q", ex.Answer)
	}

	msgs := h.messages()
	if len(msgs) != 2 {
		t.Fatalf("消息数量 = %d，期望 2（用户 + 助手）", len(msgs))
	}
	if msgs[0].Role != model.RoleUser || msgs[0].Content != "你好" {
		t.Errorf("第一条消息 = %+v", msgs[0])
	}
	if msgs[1].Role != model.RoleAssistant {
		t.Errorf("第二条消息角色 = %s", msgs[1].Role)
	}
}

func TestRun_SingleToolCall(t *testing.T) {
	h := newHarness(t, []testsupport.Script{
		testsupport.ToolCall("call_1", "calculator", `{"expression":"(1+2)*3"}`),
		testsupport.Answer("计算结果是 9。"),
	}, harnessOpts{})

	ex := h.run("算一下 (1+2)*3", nil)

	if ex.Failed {
		t.Fatalf("不应失败: %+v", ex)
	}
	if ex.Turns != 1 {
		t.Errorf("工具轮数 = %d，期望 1", ex.Turns)
	}
	if !strings.Contains(ex.Answer, "9") {
		t.Errorf("答复 = %q", ex.Answer)
	}

	msgs := h.messages()
	// 用户 → 助手(含工具调用) → 工具结果 → 助手(答复)
	if len(msgs) != 4 {
		t.Fatalf("消息数量 = %d，期望 4: %+v", len(msgs), msgs)
	}
	if len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "calculator" {
		t.Errorf("助手消息应记录工具调用: %+v", msgs[1].ToolCalls)
	}
	if msgs[2].Role != model.RoleTool {
		t.Errorf("第三条应为工具结果，实际 %s", msgs[2].Role)
	}
	if msgs[2].ToolCallID != "call_1" {
		t.Errorf("工具结果的关联标识 = %q，期望 call_1", msgs[2].ToolCallID)
	}
	if !strings.Contains(msgs[2].Content, "9") {
		t.Errorf("工具结果内容 = %q", msgs[2].Content)
	}
}

// 同轮多个工具并发执行，且回填顺序必须按调用序号，与完成先后无关。
func TestRun_MultipleToolsRunConcurrentlyAndKeepOrder(t *testing.T) {
	// 故意让耗时与序号相反：慢的最先、快的最慢。
	// 若实现按完成顺序追加，结果顺序就会乱。
	slow := &delayTool{name: "slow", delay: 300 * time.Millisecond, output: "SLOW"}
	fast := &delayTool{name: "fast", delay: 30 * time.Millisecond, output: "FAST"}
	mid := &delayTool{name: "mid", delay: 150 * time.Millisecond, output: "MID"}

	h := newHarness(t, []testsupport.Script{
		{
			ToolCalls: []testsupport.ToolCallScript{
				{ID: "c1", Name: "slow", ArgFragments: []string{"{}"}},
				{ID: "c2", Name: "fast", ArgFragments: []string{"{}"}},
				{ID: "c3", Name: "mid", ArgFragments: []string{"{}"}},
			},
		},
		testsupport.Answer("三个都做完了。"),
	}, harnessOpts{})

	h.register(slow)
	h.register(fast)
	h.register(mid)

	start := time.Now()
	ex := h.run("把慢中快都跑一遍", nil)
	elapsed := time.Since(start)

	if ex.Failed {
		t.Fatalf("不应失败: %+v", ex)
	}
	// 串行需要约 480ms；并发应接近最慢的那个（300ms）。
	if elapsed > 450*time.Millisecond {
		t.Errorf("耗时 %v，接近串行耗时，说明未并发执行", elapsed)
	}

	msgs := h.messages()
	var toolMsgs []model.Message
	for _, m := range msgs {
		if m.Role == model.RoleTool {
			toolMsgs = append(toolMsgs, m)
		}
	}
	if len(toolMsgs) != 3 {
		t.Fatalf("工具结果数量 = %d，期望 3", len(toolMsgs))
	}

	want := []struct{ id, out string }{
		{"c1", "SLOW"}, {"c2", "FAST"}, {"c3", "MID"},
	}
	for i, w := range want {
		if toolMsgs[i].ToolCallID != w.id {
			t.Errorf("第 %d 条结果的关联标识 = %q，期望 %q（回填顺序应按调用序号）",
				i, toolMsgs[i].ToolCallID, w.id)
		}
		if !strings.Contains(toolMsgs[i].Content, w.out) {
			t.Errorf("第 %d 条结果内容 = %q，期望包含 %q", i, toolMsgs[i].Content, w.out)
		}
	}
}

// 并发正确性的替代证据。
//
// 本机没有 C 编译器，而 Windows 上的 -race 依赖 cgo，竞态检测不可用
// （见 tasks.md 9.6）。这里改用两项不变量断言：回填顺序恒等于调用序号，
// 且该结论在重复运行中稳定——顺序若依赖完成先后，交错耗时会立刻让它抖动。
func TestRun_ConcurrentOrderingIsDeterministicAcrossRuns(t *testing.T) {
	const runs = 20

	for i := range runs {
		// 每次新建，避免跨次运行共享状态掩盖问题。
		slow := &delayTool{name: "slow", delay: 40 * time.Millisecond, output: "SLOW"}
		fast := &delayTool{name: "fast", delay: 2 * time.Millisecond, output: "FAST"}
		mid := &delayTool{name: "mid", delay: 20 * time.Millisecond, output: "MID"}

		h := newHarness(t, []testsupport.Script{
			{
				ToolCalls: []testsupport.ToolCallScript{
					{ID: "c1", Name: "slow", ArgFragments: []string{"{}"}},
					{ID: "c2", Name: "fast", ArgFragments: []string{"{}"}},
					{ID: "c3", Name: "mid", ArgFragments: []string{"{}"}},
				},
			},
			testsupport.Answer("完成"),
		}, harnessOpts{})
		h.register(slow)
		h.register(fast)
		h.register(mid)

		if ex := h.run("跑一遍", nil); ex.Failed {
			t.Fatalf("第 %d 次运行失败: %+v", i, ex)
		}

		var ids []string
		for _, m := range h.messages() {
			if m.Role == model.RoleTool {
				ids = append(ids, m.ToolCallID)
			}
		}
		want := []string{"c1", "c2", "c3"}
		if len(ids) != len(want) {
			t.Fatalf("第 %d 次运行的工具结果数 = %d，期望 %d", i, len(ids), len(want))
		}
		for j := range want {
			if ids[j] != want[j] {
				t.Fatalf("第 %d 次运行的回填顺序 = %v，期望 %v——顺序不应取决于完成先后",
					i, ids, want)
			}
		}
	}
}

func TestRun_ConcurrencyIsBounded(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32

	makeTool := func(name string) tool.Tool {
		return &countingTool{name: name, inFlight: &inFlight, max: &maxInFlight, delay: 60 * time.Millisecond}
	}

	h := newHarness(t, []testsupport.Script{
		{
			ToolCalls: []testsupport.ToolCallScript{
				{ID: "c1", Name: "t1", ArgFragments: []string{"{}"}},
				{ID: "c2", Name: "t2", ArgFragments: []string{"{}"}},
				{ID: "c3", Name: "t3", ArgFragments: []string{"{}"}},
				{ID: "c4", Name: "t4", ArgFragments: []string{"{}"}},
				{ID: "c5", Name: "t5", ArgFragments: []string{"{}"}},
				{ID: "c6", Name: "t6", ArgFragments: []string{"{}"}},
			},
		},
		testsupport.Answer("全部完成。"),
	}, harnessOpts{maxParallel: 2})

	for _, n := range []string{"t1", "t2", "t3", "t4", "t5", "t6"} {
		h.register(makeTool(n))
	}

	if ex := h.run("并发跑六个", nil); ex.Failed {
		t.Fatalf("不应失败: %+v", ex)
	}

	if got := maxInFlight.Load(); got > 2 {
		t.Errorf("同时在飞的工具数达到 %d，超出配置上限 2", got)
	}
	if got := maxInFlight.Load(); got < 2 {
		t.Errorf("最大并发数只有 %d，未观察到并发执行", got)
	}
}

// 单个工具失败不能拖垮整轮：错误作为该次调用的结果回填，循环继续。
func TestRun_ToolFailureKeepsLoopGoing(t *testing.T) {
	h := newHarness(t, []testsupport.Script{
		testsupport.ToolCall("c1", "calculator", `{"expression":"1/0"}`), // 会失败
		testsupport.Answer("除零了，换个表达式吧。"),
	}, harnessOpts{})

	ex := h.run("算一下 1/0", nil)

	if ex.Failed {
		t.Fatalf("工具失败不应让整轮失败: %+v", ex)
	}
	if ex.Turns != 1 {
		t.Errorf("工具轮数 = %d，期望 1", ex.Turns)
	}

	msgs := h.messages()
	var toolResult string
	for _, m := range msgs {
		if m.Role == model.RoleTool {
			toolResult = m.Content
		}
	}
	if !strings.Contains(toolResult, "除数") {
		t.Errorf("失败原因应回填给模型，实际: %q", toolResult)
	}

	// trace 里应记成失败状态，并带上原因。
	traces := h.traces()
	if len(traces) != 1 {
		t.Fatalf("trace 数量 = %d，期望 1", len(traces))
	}
	if traces[0].Status != model.TraceError {
		t.Errorf("trace 状态 = %s，期望 error", traces[0].Status)
	}
	if traces[0].Error == "" {
		t.Error("失败的 trace 应记录原因")
	}
}

func TestRun_UnknownToolBackfillsCandidates(t *testing.T) {
	h := newHarness(t, []testsupport.Script{
		testsupport.ToolCall("c1", "get_wether", `{"city":"北京"}`),
		testsupport.Answer("抱歉，我换用正确的工具再试。"),
	}, harnessOpts{})

	ex := h.run("北京天气", nil)
	if ex.Failed {
		t.Fatalf("未知工具不应让整轮失败: %+v", ex)
	}

	var toolResult string
	for _, m := range h.messages() {
		if m.Role == model.RoleTool {
			toolResult = m.Content
		}
	}
	for _, name := range []string{"calculator", "weather", "todo"} {
		if !strings.Contains(toolResult, name) {
			t.Errorf("回填内容应列出可用工具 %q，实际：%s", name, toolResult)
		}
	}

	tr := h.traces()
	if len(tr) != 1 || tr[0].Status != model.TraceError {
		t.Errorf("未知工具应记为失败 trace: %+v", tr)
	}
}

// 模型请求失败要给出可读说明，且会话必须保持可用。
func TestRun_LLMErrorKeepsSessionUsable(t *testing.T) {
	h := newHarness(t, []testsupport.Script{
		{Status: 401},
		testsupport.Answer("这次好了。"),
	}, harnessOpts{})

	ex := h.run("第一次会失败", nil)
	if !ex.Failed {
		t.Fatal("鉴权失败应报告为失败")
	}
	if !strings.Contains(ex.Answer, "OPENAI_API_KEY") {
		t.Errorf("失败说明应指向配置项，实际：%s", ex.Answer)
	}

	// 同一会话的下一次输入必须能正常处理。
	ex2 := h.run("第二次", nil)
	if ex2.Failed {
		t.Fatalf("异常之后会话应当仍可用，实际: %+v", ex2)
	}
	if ex2.Answer != "这次好了。" {
		t.Errorf("第二次答复 = %q", ex2.Answer)
	}
}

// 已输出后中断不重试，但要如实说明并保留已产出的内容。
func TestRun_StreamInterruptionIsReported(t *testing.T) {
	h := newHarness(t, []testsupport.Script{
		{Content: []string{"正在", "回答", "到一半"}, CloseAfter: 2},
	}, harnessOpts{})

	ex := h.run("问一个问题", nil)

	if !ex.Failed {
		t.Fatal("中断应报告为失败")
	}
	if !strings.Contains(ex.Answer, "中断") {
		t.Errorf("说明中应提到中断，实际：%s", ex.Answer)
	}
	if h.fake.Requests() != 1 {
		t.Errorf("已输出后中断不应重试，实际请求 %d 次", h.fake.Requests())
	}

	// 已产出的部分应当落库，使历史与实际所见一致。
	var found bool
	for _, m := range h.messages() {
		if m.Role == model.RoleAssistant && strings.Contains(m.Content, "正在") {
			found = true
		}
	}
	if !found {
		t.Errorf("已输出的内容应被保存，实际消息: %+v", h.messages())
	}
}

func TestRun_ReachesTurnLimit(t *testing.T) {
	loop := testsupport.ToolCall("c", "calculator", `{"expression":"1+1"}`)
	h := newHarness(t, []testsupport.Script{loop, loop, loop, loop}, harnessOpts{maxTurns: 2})

	ex := h.run("一直算下去", nil)

	if !ex.ReachedLimit {
		t.Fatalf("应报告达到轮次上限: %+v", ex)
	}
	if ex.Turns != 2 {
		t.Errorf("工具轮数 = %d，期望恰好 2", ex.Turns)
	}
	if !strings.Contains(ex.Answer, "上限") {
		t.Errorf("应返回上限说明，实际：%s", ex.Answer)
	}
	// 达到上限后会话仍可用。
	if ex2 := h.run("换个问题", nil); ex2.Failed {
		t.Errorf("触上限后会话仍应可用: %+v", ex2)
	}
}

// 思维链落库供查看，但绝不进入后续请求的上下文。
func TestRun_ReasoningPersistedButNotResent(t *testing.T) {
	const secret = "这段思维链不应回填"
	h := newHarness(t, []testsupport.Script{
		{Reasoning: []string{secret}, Content: []string{"第一答"}},
		testsupport.Answer("第二答"),
	}, harnessOpts{})

	h.run("第一问", nil)
	h.run("第二问", nil)

	// 一半：落库了。
	var persisted bool
	for _, m := range h.messages() {
		if m.Reasoning == secret {
			persisted = true
		}
	}
	if !persisted {
		t.Error("思维链应当落库")
	}

	// 另一半：第二次请求体里没有它。
	second := h.fake.Body(1)
	if strings.Contains(second, secret) {
		t.Errorf("思维链不应出现在后续请求中，请求体：%s", second)
	}
	if !strings.Contains(second, "第一答") {
		t.Error("历史答复应当出现在后续请求中")
	}
}

// 两个会话并行对话，彼此的历史不得混入对方的请求。
func TestRun_SessionsAreIsolated(t *testing.T) {
	h := newHarness(t, []testsupport.Script{
		testsupport.Answer("甲会话的答复"),
		testsupport.Answer("乙会话的答复"),
	}, harnessOpts{})

	if _, err := h.store.CreateSession("sess_other", "另一个会话"); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	if _, err := h.loop.Run(t.Context(), testSession, "甲的问题", nil); err != nil {
		t.Fatalf("会话甲执行失败: %v", err)
	}
	if _, err := h.loop.Run(t.Context(), "sess_other", "乙的问题", nil); err != nil {
		t.Fatalf("会话乙执行失败: %v", err)
	}

	second := h.fake.Body(1)
	if strings.Contains(second, "甲的问题") || strings.Contains(second, "甲会话的答复") {
		t.Errorf("会话乙的请求中不应出现会话甲的内容，实际：%s", second)
	}
	if !strings.Contains(second, "乙的问题") {
		t.Error("会话乙的请求应包含自己的输入")
	}
}

// 别名命中与重复调用是诊断信号，必须被记录。
func TestRun_ReportsAliasAndRepeatedCalls(t *testing.T) {
	h := newHarness(t, []testsupport.Script{
		testsupport.ToolCall("c1", "calc", `{"expression":"1+1"}`),       // calc 是 calculator 的别名
		testsupport.ToolCall("c2", "calculator", `{"expression":"1+1"}`), // 相同参数重复调用
		testsupport.Answer("算完了。"),
	}, harnessOpts{})

	if ex := h.run("算两次", nil); ex.Failed {
		t.Fatalf("不应失败: %+v", ex)
	}

	tr := h.traces()
	if len(tr) != 2 {
		t.Fatalf("trace 数量 = %d，期望 2", len(tr))
	}
	if !tr[0].Alias {
		t.Error("第一次调用经由别名 calc，应被标记")
	}
	if tr[0].ToolName != "calculator" {
		t.Errorf("别名应解析回规范名，实际 %q", tr[0].ToolName)
	}
	// 第一次出现不算重复，第二次相同参数才算。
	if tr[0].Repeated {
		t.Error("首次调用不应标记为重复")
	}
	if !tr[1].Repeated {
		t.Error("以相同参数再次调用同一工具，应被标记为重复")
	}
}

func TestRun_ToolResultsAreTruncatedInTraceOnly(t *testing.T) {
	long := strings.Repeat("很长的结果", 200)
	h := newHarness(t, []testsupport.Script{
		testsupport.ToolCall("c1", "echo", `{}`),
		testsupport.Answer("收到了。"),
	}, harnessOpts{})
	h.register(&echoTool{output: long})

	if ex := h.run("给我一段长结果", nil); ex.Failed {
		t.Fatalf("不应失败: %+v", ex)
	}

	tr := h.traces()
	if len(tr) != 1 {
		t.Fatalf("trace 数量 = %d", len(tr))
	}
	if n := len([]rune(tr[0].Result)); n > 600 {
		t.Errorf("trace 中的结果应被裁剪，实际 %d 字符", n)
	}

	// 完整结果仍应存在于消息表中——trace 只是索引，不是真源。
	var full string
	for _, m := range h.messages() {
		if m.Role == model.RoleTool {
			full = m.Content
		}
	}
	if full != long {
		t.Errorf("消息表中的工具结果应保持完整：长度 %d，期望 %d", len([]rune(full)), len([]rune(long)))
	}
}

// --- 测试用小工具 ---

type countingTool struct {
	name     string
	inFlight *atomic.Int32
	max      *atomic.Int32
	delay    time.Duration
}

func (c *countingTool) Name() string            { return c.name }
func (c *countingTool) Description() string     { return "并发计数工具" }
func (c *countingTool) Aliases() []string       { return nil }
func (c *countingTool) Parameters() tool.Schema { return tool.Schema{Type: "object"} }

func (c *countingTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	cur := c.inFlight.Add(1)
	for {
		prev := c.max.Load()
		if cur <= prev || c.max.CompareAndSwap(prev, cur) {
			break
		}
	}
	defer c.inFlight.Add(-1)
	time.Sleep(c.delay)
	return "done:" + c.name, nil
}

type echoTool struct{ output string }

func (e *echoTool) Name() string            { return "echo" }
func (e *echoTool) Description() string     { return "原样返回一段固定内容" }
func (e *echoTool) Aliases() []string       { return nil }
func (e *echoTool) Parameters() tool.Schema { return tool.Schema{Type: "object"} }

func (e *echoTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return e.output, nil
}
