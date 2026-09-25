package agent_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"minimal-agent/internal/agent"
	"minimal-agent/internal/config"
	"minimal-agent/internal/contextmgr"
	"minimal-agent/internal/llm"
	"minimal-agent/internal/model"
	"minimal-agent/internal/store"
	"minimal-agent/internal/testsupport"
	"minimal-agent/internal/tool"
)

// 本文件的用例会调用真实模型，未配置密钥时自动跳过。
// 门控规则见 testsupport.IntegrationConfig。
//
// 它们覆盖的是单元测试**覆盖不到**的部分：注入传输层能验证「解析逻辑正确」，
// 但验证不了「真实模型能否理解我提交的工具 Schema、能否把工具结果用进回答」。
// 而后者恰恰是最先失效的地方——Schema 写得不合模型口味，工具就永远不会被调用，
// 而模拟服务会忠实地按脚本调用，测试照样全绿。

// 工具输出里的温度应当是真实数值。
var tempPattern = regexp.MustCompile(`温度：-?\d+(\.\d+)?`)

// newIntegrationHarness 装配一套接真实模型与真实外部服务的循环。
func newIntegrationHarness(t *testing.T) (*harness, config.Config) {
	t.Helper()

	cfg := testsupport.IntegrationConfig(t)
	testsupport.RequireModelKey(t, cfg)
	t.Logf("使用模型 %s @ %s", cfg.Model, cfg.BaseURL)

	st, err := store.Open(filepath.Join(t.TempDir(), "integration.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if _, err := st.CreateSession(testSession, "集成测试会话"); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	registry := tool.NewRegistry()
	for _, tl := range []tool.Tool{
		tool.NewCalculator(),
		tool.NewWeather(cfg.OpenMeteoBaseURL, cfg.GeocodingBaseURL, nil),
		tool.NewSearch(cfg.BochaAPIKey, cfg.BochaBaseURL, nil),
		tool.NewTodo(st),
	} {
		if err := registry.Register(tl); err != nil {
			t.Fatalf("注册工具失败: %v", err)
		}
	}

	client := llm.New(llm.Options{
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		Model:      cfg.Model,
		MaxRetries: cfg.MaxRetries,
		Timeout:    cfg.LLMTimeout,
	})

	builder := contextmgr.NewBuilder(contextmgr.Config{
		MaxHistoryMsgs:   cfg.MaxHistoryMsgs,
		CompactThreshold: cfg.CompactThreshold,
		KeepRecentTurns:  cfg.KeepRecentTurns,
	}, st, contextmgr.NewCompactor(st,
		contextmgr.NewLLMSummarizer(client), cfg.CompactThreshold, cfg.KeepRecentTurns))

	return &harness{
		t:        t,
		store:    st,
		registry: registry,
		loop: agent.New(agent.Deps{
			Store:           st,
			Registry:        registry,
			Client:          client,
			Builder:         builder,
			Config:          agent.Config{MaxToolTurns: cfg.MaxToolTurns},
			MaxToolParallel: cfg.MaxToolParallel,
			ToolTimeout:     cfg.ToolTimeout,
		}),
	}, cfg
}

// 本地工具 + 真实模型：验证模型能按 Schema 正确构造参数，
// 且能把工具结果原样用进回答。这条不依赖任何外部服务。
func TestIntegration_CalculatorToolChain(t *testing.T) {
	h, _ := newIntegrationHarness(t)

	ex := h.run("请用 calculator 工具计算 1234 * 5678，然后告诉我结果。", llm.NewBufferSink())

	if ex.Failed {
		t.Fatalf("调用失败: %+v", ex)
	}
	tr := h.traces()
	if len(tr) == 0 {
		t.Fatal("模型未调用任何工具——工具 Schema 可能不被模型理解")
	}
	if tr[0].ToolName != "calculator" {
		t.Errorf("调用了 %q，期望 calculator", tr[0].ToolName)
	}
	if tr[0].Status != model.TraceOK {
		t.Errorf("工具执行失败: %s", tr[0].Error)
	}
	// 1234 * 5678 = 7006652。模型必须把这个数字用进回答，
	// 否则说明工具结果没有被正确回填或读取。
	if !strings.Contains(ex.Answer, "7006652") {
		t.Errorf("答复中应包含工具算出的结果 7006652，实际：%s", ex.Answer)
	}
	t.Logf("答复：%s", ex.Answer)
}

// 真实模型 + 真实天气服务：这是一条完整的外部链路。
func TestIntegration_WeatherToolChain(t *testing.T) {
	h, _ := newIntegrationHarness(t)

	ex := h.run("请用 weather 工具查一下北京现在的天气，告诉我温度和天气状况。", llm.NewBufferSink())

	if ex.Failed {
		t.Fatalf("调用失败: %+v", ex)
	}
	tr := h.traces()
	if len(tr) == 0 || tr[0].ToolName != "weather" {
		t.Fatalf("应调用 weather 工具，实际 trace：%+v", tr)
	}
	if tr[0].Status != model.TraceOK {
		t.Fatalf("天气工具执行失败: %s", tr[0].Error)
	}

	// 工具确实取回了真实数据
	var toolOutput string
	for _, m := range h.messages() {
		if m.Role == model.RoleTool {
			toolOutput = m.Content
		}
	}
	if !tempPattern.MatchString(toolOutput) {
		t.Errorf("工具结果中应含真实温度，实际：%s", toolOutput)
	}
	if strings.TrimSpace(ex.Answer) == "" {
		t.Error("应给出非空答复")
	}
	t.Logf("工具结果：%s", truncateForLog(toolOutput, 200))
	t.Logf("答复：%s", ex.Answer)
}

// 流式在真实模型下确实逐块到达，而不是一次性到齐。
func TestIntegration_StreamsIncrementally(t *testing.T) {
	h, _ := newIntegrationHarness(t)

	sink := llm.NewBufferSink()
	ex := h.run("请从 1 数到 10，用中文，只输出数字和顿号，不要解释。", sink)

	if ex.Failed {
		t.Fatalf("调用失败: %+v", ex)
	}
	if strings.TrimSpace(sink.Content()) == "" {
		t.Fatal("未收到任何正文增量")
	}
	if !sink.Done() {
		t.Error("未收到完成信号")
	}

	n := len([]rune(sink.Content()))
	t.Logf("共 %d 个字符，模型返回：%s", n, truncateForLog(sink.Content(), 120))
	if n < 5 {
		t.Errorf("返回内容过短（%d 字符），不足以判断是否流式", n)
	}
}

// 真实模型能依据历史消解追问中的指代——这是 memory 是否真正生效的端到端验证。
func TestIntegration_ResolvesFollowUpReference(t *testing.T) {
	h, _ := newIntegrationHarness(t)

	if ex := h.run("请用 weather 工具查一下杭州的天气。", llm.NewBufferSink()); ex.Failed {
		t.Fatalf("第一轮失败: %+v", ex)
	}

	sink := llm.NewBufferSink()
	ex := h.run("我刚才让你查的是哪个城市？只回答城市名。", sink)
	if ex.Failed {
		t.Fatalf("第二轮失败: %+v", ex)
	}
	if !strings.Contains(ex.Answer, "杭州") {
		t.Errorf("追问应能依据历史答出「杭州」，实际：%s", ex.Answer)
	}
	t.Logf("追问答复：%s", ex.Answer)
}

// 两个会话并行对话，真实模型下也不得串味。
func TestIntegration_SessionsStayIsolated(t *testing.T) {
	h, _ := newIntegrationHarness(t)
	const codeword = "紫色河马"

	if _, err := h.store.CreateSession("sess_other", "另一个会话"); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	// 在会话一里埋一个只有它知道的词。
	if _, err := h.loop.Run(t.Context(), testSession,
		"请记住这个词："+codeword+"。只回复「好的」。", llm.NewBufferSink()); err != nil {
		t.Fatalf("会话一执行失败: %v", err)
	}

	// 会话二不该知道它。即使模型胡编一个词也没关系，
	// 关键断言是它不能说出会话一的那个词。
	sink := llm.NewBufferSink()
	ex, err := h.loop.Run(t.Context(), "sess_other",
		"我刚才让你记的词是什么？如果你不知道，就回答「不知道」。", sink)
	if err != nil {
		t.Fatalf("会话二执行失败: %v", err)
	}
	if strings.Contains(ex.Answer, codeword) {
		t.Errorf("会话二不应知道会话一的词，实际答复：%s", ex.Answer)
	}
	t.Logf("会话二答复：%s", ex.Answer)
}

// 待办工具按会话隔离——用真实模型跑一遍需求里描述的那个场景。
func TestIntegration_TodoToolChain(t *testing.T) {
	h, _ := newIntegrationHarness(t)

	ex := h.run("请用 todo 工具记一条待办：买牛奶。", llm.NewBufferSink())
	if ex.Failed {
		t.Fatalf("调用失败: %+v", ex)
	}

	items, err := h.store.Todos(testSession)
	if err != nil {
		t.Fatalf("读取待办失败: %v", err)
	}
	if len(items) == 0 {
		tr := h.traces()
		t.Fatalf("待办未写入，trace：%+v", tr)
	}
	if !strings.Contains(items[0].Content, "买牛奶") {
		t.Errorf("待办内容 = %q，期望含「买牛奶」", items[0].Content)
	}
	t.Logf("已写入待办：%+v", items[0])

	// 再让它查一次，确认查询链路也通。
	sink := llm.NewBufferSink()
	ex = h.run("请用 todo 工具列出我现在的待办。", sink)
	if ex.Failed {
		t.Fatalf("查询失败: %+v", ex)
	}
	if !strings.Contains(ex.Answer, "买牛奶") {
		t.Errorf("答复中应包含已记录的待办，实际：%s", ex.Answer)
	}
}

func TestIntegration_SearchToolChain(t *testing.T) {
	h, cfg := newIntegrationHarness(t)
	testsupport.RequireSearchKey(t, cfg)

	ex := h.run("请用 search 工具查一下「杭州西湖」的相关信息，然后用一句话概括。", llm.NewBufferSink())
	if ex.Failed {
		t.Fatalf("调用失败: %+v", ex)
	}

	tr := h.traces()
	if len(tr) == 0 || tr[0].ToolName != "search" {
		t.Fatalf("应调用 search 工具，实际 trace：%+v", tr)
	}
	if tr[0].Status != model.TraceOK {
		t.Fatalf("搜索工具执行失败: %s", tr[0].Error)
	}
	if strings.TrimSpace(ex.Answer) == "" {
		t.Error("应给出非空答复")
	}
	t.Logf("答复：%s", ex.Answer)
}

// 真实模型下，一轮内请求多个工具时结果定序仍然稳定。
func TestIntegration_MultipleToolsInOneTurnKeepOrder(t *testing.T) {
	h, _ := newIntegrationHarness(t)

	ex := h.run(
		"请同时做两件事：用 calculator 算 88 * 11，用 weather 查北京天气。两件都要做。",
		llm.NewBufferSink())
	if ex.Failed {
		t.Fatalf("调用失败: %+v", ex)
	}

	tr := h.traces()
	if len(tr) < 2 {
		t.Fatalf("应至少调用两个工具，实际 %d 个：%+v", len(tr), tr)
	}

	// 工具结果消息的顺序必须与 trace 顺序一致（都按模型给出的调用序号）。
	var ids []string
	for _, m := range h.messages() {
		if m.Role == model.RoleTool {
			ids = append(ids, m.ToolCallID)
		}
	}
	if len(ids) != len(tr) {
		t.Errorf("工具结果数 %d 与 trace 数 %d 不一致", len(ids), len(tr))
	}
	// 968 = 88 * 11，应当出现在回答里。
	if !strings.Contains(ex.Answer, "968") {
		t.Errorf("答复中应含 88*11 的结果 968，实际：%s", ex.Answer)
	}
	t.Logf("调用顺序：%v", ids)
	t.Logf("答复：%s", ex.Answer)
}

func truncateForLog(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "……"
}
