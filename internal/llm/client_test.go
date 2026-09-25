package llm_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"minimal-agent/internal/llm"
	"minimal-agent/internal/model"
	"minimal-agent/internal/testsupport"
)

func newClient(f *testsupport.FakeLLM) *llm.Client {
	return llm.New(llm.Options{
		BaseURL:    f.URL(),
		APIKey:     "test-key",
		Model:      "mock-model",
		MaxRetries: 2,
		Timeout:    20 * time.Second,
	})
}

func userMsg(s string) []model.Message {
	return []model.Message{{Role: model.RoleUser, Content: s}}
}

func TestStream_PlainText(t *testing.T) {
	fake := testsupport.NewFakeLLM(testsupport.Answer("你好，世界"))
	defer fake.Close()

	sink := llm.NewBufferSink()
	resp, err := newClient(fake).Stream(context.Background(), userMsg("你好"), "系统提示", nil, sink)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if resp.Content != "你好，世界" {
		t.Errorf("累加内容 = %q，期望 %q", resp.Content, "你好，世界")
	}
	if sink.Content() != resp.Content {
		t.Errorf("逐片呈现的内容 %q 与累加结果 %q 不一致", sink.Content(), resp.Content)
	}
	if !sink.Done() {
		t.Error("未收到完成信号")
	}
	if resp.Finish != model.FinishStop {
		t.Errorf("结束原因 = %q，期望 %q", resp.Finish, model.FinishStop)
	}
	if resp.WantsTools() {
		t.Error("纯文本响应不应要求执行工具")
	}
}

// 这是本层最容易出错的地方：reasoning_content 是非标准字段，SDK 的类型里
// 没有它，且 Field.Raw() 返回的是带引号的 JSON 字面量，必须再解一次。
func TestStream_ReasoningExtractedUnescaped(t *testing.T) {
	fake := testsupport.NewFakeLLM(testsupport.Script{
		Reasoning: []string{"让我想想", `他说"你好"`, "\n换行"},
		Content:   []string{"结论"},
	})
	defer fake.Close()

	sink := llm.NewBufferSink()
	resp, err := newClient(fake).Stream(context.Background(), userMsg("问题"), "", nil, sink)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}

	want := "让我想想" + `他说"你好"` + "\n换行"
	if resp.Reasoning != want {
		t.Errorf("思维链 = %q，期望 %q", resp.Reasoning, want)
	}
	if sink.Reasoning() != want {
		t.Errorf("逐片呈现的思维链 = %q，期望 %q", sink.Reasoning(), want)
	}
	if resp.Content != "结论" {
		t.Errorf("正文 = %q，期望 %q", resp.Content, "结论")
	}
}

func TestStream_ReasoningAbsentIsEmpty(t *testing.T) {
	fake := testsupport.NewFakeLLM(testsupport.Answer("没有思维链"))
	defer fake.Close()

	resp, err := newClient(fake).Stream(context.Background(), userMsg("问题"), "", nil, nil)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if resp.Reasoning != "" {
		t.Errorf("字段缺失时思维链应为空，实际 %q", resp.Reasoning)
	}
}

// 覆盖工具调用参数跨分片到达时的拼装：id 只在首片出现，
// arguments 被拆成两段，靠 index 归位。
func TestStream_ToolCallFragmentsAssembled(t *testing.T) {
	const args = `{"city":"北京","unit":"celsius"}`
	fake := testsupport.NewFakeLLM(testsupport.ToolCall("call_1", "weather", args))
	defer fake.Close()

	sink := llm.NewBufferSink()
	resp, err := newClient(fake).Stream(context.Background(), userMsg("北京天气"), "", nil, sink)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("工具调用数量 = %d，期望 1", len(resp.ToolCalls))
	}
	got := resp.ToolCalls[0]
	if got.ID != "call_1" {
		t.Errorf("调用标识 = %q，期望 call_1", got.ID)
	}
	if got.Name != "weather" {
		t.Errorf("工具名 = %q，期望 weather", got.Name)
	}
	if got.Args != args {
		t.Errorf("拼装后的参数 = %q，期望 %q", got.Args, args)
	}
	if resp.Finish != model.FinishToolCalls {
		t.Errorf("结束原因 = %q，期望 %q", resp.Finish, model.FinishToolCalls)
	}
	if !resp.WantsTools() {
		t.Error("带工具调用的响应应报告需要执行工具")
	}
	if tools := sink.Tools(); len(tools) != 1 || tools[0] != "weather" {
		t.Errorf("完成通知 = %v，期望 [weather]", tools)
	}
}

func TestStream_MultipleToolCallsKeepOrder(t *testing.T) {
	fake := testsupport.NewFakeLLM(testsupport.Script{
		ToolCalls: []testsupport.ToolCallScript{
			{ID: "c1", Name: "weather", ArgFragments: []string{`{"city":`, `"北京"}`}},
			{ID: "c2", Name: "calculator", ArgFragments: []string{`{"expr":`, `"1+1"}`}},
			{ID: "c3", Name: "todo", ArgFragments: []string{`{"action":`, `"list"}`}},
		},
	})
	defer fake.Close()

	resp, err := newClient(fake).Stream(context.Background(), userMsg("一次问三件事"), "", nil, nil)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}

	if len(resp.ToolCalls) != 3 {
		t.Fatalf("工具调用数量 = %d，期望 3", len(resp.ToolCalls))
	}
	for i, want := range []struct{ id, name string }{
		{"c1", "weather"}, {"c2", "calculator"}, {"c3", "todo"},
	} {
		if resp.ToolCalls[i].ID != want.id || resp.ToolCalls[i].Name != want.name {
			t.Errorf("第 %d 个调用 = (%s, %s)，期望 (%s, %s)",
				i, resp.ToolCalls[i].ID, resp.ToolCalls[i].Name, want.id, want.name)
		}
	}
}

func TestStream_AuthErrorNotRetried(t *testing.T) {
	fake := testsupport.NewFakeLLM(testsupport.Script{Status: 401})
	defer fake.Close()

	_, err := newClient(fake).Stream(context.Background(), userMsg("你好"), "", nil, nil)
	if err == nil {
		t.Fatal("鉴权失败时应返回错误")
	}
	if n := fake.Requests(); n != 1 {
		t.Errorf("鉴权失败不应重试，实际请求 %d 次", n)
	}

	var reqErr *llm.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("错误类型 = %T，期望 *llm.RequestError", err)
	}
	if reqErr.Kind != llm.KindAuth {
		t.Errorf("错误分类 = %v，期望 KindAuth", reqErr.Kind)
	}
	if !strings.Contains(reqErr.Hint(), "OPENAI_API_KEY") {
		t.Errorf("错误提示未指向配置项：%s", reqErr.Hint())
	}
}

// 固化了 SDK 的既有行为：请求建立阶段的失败会重试，且最终能恢复。
func TestStream_RetriesServerErrorThenSucceeds(t *testing.T) {
	fake := testsupport.NewFakeLLM(
		testsupport.Script{Status: 500},
		testsupport.Script{Status: 500},
		testsupport.Answer("恢复成功"),
	)
	defer fake.Close()

	resp, err := newClient(fake).Stream(context.Background(), userMsg("你好"), "", nil, nil)
	if err != nil {
		t.Fatalf("重试后本应成功，实际失败: %v", err)
	}
	if resp.Content != "恢复成功" {
		t.Errorf("内容 = %q，期望「恢复成功」", resp.Content)
	}
	if n := fake.Requests(); n != 3 {
		t.Errorf("服务端收到 %d 次请求，期望 3 次（两败一成）", n)
	}
}

// 一旦已向用户输出内容后中断，就不应重试——否则内容会重复显示。
func TestStream_InterruptedAfterOutputIsNotRetried(t *testing.T) {
	fake := testsupport.NewFakeLLM(testsupport.Script{
		Content:    []string{"这是", "一段", "很长", "的回答"},
		CloseAfter: 2,
	})
	defer fake.Close()

	sink := llm.NewBufferSink()
	_, err := newClient(fake).Stream(context.Background(), userMsg("你好"), "", nil, sink)
	if err == nil {
		t.Fatal("连接中断时应返回错误")
	}

	var interrupted *llm.StreamInterruptedError
	if !errors.As(err, &interrupted) {
		t.Fatalf("错误类型 = %T，期望 *llm.StreamInterruptedError（原始错误：%v）", err, err)
	}
	if interrupted.EmittedChars == 0 {
		t.Error("应报告已输出的字符数")
	}
	if n := fake.Requests(); n != 1 {
		t.Errorf("已输出后中断不应重试，实际请求 %d 次", n)
	}
}

// 思维链只落库供查看，绝不回填到后续请求的上下文。
func TestStream_ReasoningIsNotSentBack(t *testing.T) {
	const secret = "内部的思维链内容XYZ"
	fake := testsupport.NewFakeLLM(testsupport.Answer("好的"))
	defer fake.Close()

	msgs := []model.Message{
		{Role: model.RoleUser, Content: "第一问"},
		{Role: model.RoleAssistant, Content: "第一答", Reasoning: secret},
		{Role: model.RoleUser, Content: "第二问"},
	}
	if _, err := newClient(fake).Stream(context.Background(), msgs, "系统提示", nil, nil); err != nil {
		t.Fatalf("请求失败: %v", err)
	}

	body := fake.Body(0)
	if strings.Contains(body, secret) {
		t.Error("思维链不应出现在请求体中")
	}
	if !strings.Contains(body, "第一答") {
		t.Error("助手的历史答复应当出现在请求体中")
	}
	if !strings.Contains(body, "系统提示") {
		t.Error("系统提示应当出现在请求体中")
	}
}

// 工具声明必须作为独立字段提交，且保留名称、描述与参数 Schema。
func TestStream_ToolsAreSubmitted(t *testing.T) {
	fake := testsupport.NewFakeLLM(testsupport.Answer("好的"))
	defer fake.Close()

	tools := []llm.ToolSpec{{
		Name:        "weather",
		Description: "查询天气",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"city": map[string]any{"type": "string"}},
			"required":   []string{"city"},
		},
	}}
	if _, err := newClient(fake).Stream(context.Background(), userMsg("北京天气"), "", tools, nil); err != nil {
		t.Fatalf("请求失败: %v", err)
	}

	body := fake.Body(0)
	for _, want := range []string{"weather", "查询天气", "city", "required"} {
		if !strings.Contains(body, want) {
			t.Errorf("请求体中缺少 %q", want)
		}
	}
}

func TestBufferSink_RecordsEverything(t *testing.T) {
	sink := llm.NewBufferSink()
	sink.OnReasoning("思考")
	sink.OnContent("正文")
	sink.OnToolCall("weather")
	sink.OnDone()

	if sink.Reasoning() != "思考" {
		t.Errorf("思维链 = %q", sink.Reasoning())
	}
	if sink.Content() != "正文" {
		t.Errorf("正文 = %q", sink.Content())
	}
	if tools := sink.Tools(); len(tools) != 1 || tools[0] != "weather" {
		t.Errorf("工具通知 = %v", tools)
	}
	if !sink.Done() {
		t.Error("应记录完成信号")
	}
}
