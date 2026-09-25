package contextmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"minimal-agent/internal/llm"
	"minimal-agent/internal/model"
	"minimal-agent/internal/testsupport"
)

func TestExtractSummary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "标准两段式",
			in:   "<analysis>\n用户问了天气\n</analysis>\n\n<summary>\n1. 请求与意图\n- 查天气\n</summary>",
			want: "1. 请求与意图\n- 查天气",
		},
		{
			name: "只有 summary",
			in:   "<summary>只有正文</summary>",
			want: "只有正文",
		},
		{
			name: "summary 未闭合时取到结尾",
			in:   "<analysis>草稿</analysis>\n<summary>没有闭合标签的正文",
			want: "没有闭合标签的正文",
		},
		{
			name: "只输出了草稿时取草稿之后的内容",
			in:   "<analysis>\n逐条梳理……\n</analysis>\n\n摘要正文在这里",
			want: "摘要正文在这里",
		},
		{
			name: "完全没按格式来时整段保留但剥掉标签",
			in:   "直接给了段摘要，没有任何标签",
			want: "直接给了段摘要，没有任何标签",
		},
		{
			name: "残留的标签会被剥掉",
			in:   "<analysis>草稿</analysis><summary></summary>",
			want: "",
		},
		{
			name: "首尾空白被清理",
			in:   "\n\n<summary>\n  正文  \n</summary>\n\n",
			want: "正文",
		},
		{
			name: "空输入",
			in:   "   ",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractSummary(tc.in); got != tc.want {
				t.Errorf("extractSummary() = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// 草稿是给模型自己用的，绝不能被当成摘要存下来。
func TestExtractSummary_DropsAnalysis(t *testing.T) {
	raw := `<analysis>
用户先问了北京天气，我调用了 weather 工具，得到 23.7°C，
然后用户让我记一条待办，我调用了 todo。
</analysis>

<summary>
1. 用户的请求与意图
- 查询北京天气
</summary>`

	got := extractSummary(raw)

	if strings.Contains(got, "草稿") || strings.Contains(got, "我调用了") || strings.Contains(got, "analysis") {
		t.Errorf("草稿内容不应出现在摘要中：%q", got)
	}
	if !strings.Contains(got, "用户的请求与意图") {
		t.Errorf("摘要正文缺失：%q", got)
	}
}

func newSummarizer(t *testing.T, response string) (*LLMSummarizer, *testsupport.FakeLLM) {
	t.Helper()
	fake := testsupport.NewFakeLLM(testsupport.Script{Content: []string{response}})
	t.Cleanup(fake.Close)

	client := llm.New(llm.Options{
		BaseURL: fake.URL(), APIKey: "test", Model: "mock", MaxRetries: 0,
	})
	return NewLLMSummarizer(client), fake
}

// 提示模板必须被完整提交——它是压缩质量的全部依据。
func TestLLMSummarizer_SendsStructuredTemplate(t *testing.T) {
	sum, fake := newSummarizer(t, "<analysis>草稿</analysis><summary>正文</summary>")

	if _, err := sum.Summarize(context.Background(), "", []model.Message{
		{Role: model.RoleUser, Content: "北京天气怎么样"},
	}); err != nil {
		t.Fatalf("摘要失败: %v", err)
	}

	prompt := decodedPrompt(t, fake.Body(0))
	for _, want := range []string{
		"<analysis>", "<summary>",
		"用户的请求与意图", "用户消息原文", "实体与事实",
		"工具调用结论", "未完成事项", "当前状态", "下一步",
		"不设字数上限", "500 字",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("提交给模型的提示中缺少 %q", want)
		}
	}
	// 待压缩的对话本身也必须在请求里
	if !strings.Contains(prompt, "北京天气怎么样") {
		t.Error("请求中应包含待压缩的对话内容")
	}
}

// decodedPrompt 取出请求体中模型实际会读到的提示文本。
//
// 不能直接对原始请求体做字符串匹配：Go 的 encoding/json 默认把 < > & 转义成
// < 等形式，所以线上格式里不会有字面量的 <analysis>，而服务端解析后
// 模型拿到的正是 <analysis>。断言应当针对含义，而不是传输编码。
func decodedPrompt(t *testing.T, body string) string {
	t.Helper()

	var payload struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("解析请求体失败: %v", err)
	}

	var b strings.Builder
	for _, m := range payload.Messages {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

func TestLLMSummarizer_ReturnsOnlySummaryBlock(t *testing.T) {
	sum, _ := newSummarizer(t,
		"<analysis>\n这段是草稿，不该被保存\n</analysis>\n\n<summary>\n1. 用户的请求与意图\n- 查天气\n</summary>")

	got, err := sum.Summarize(context.Background(), "", []model.Message{
		{Role: model.RoleUser, Content: "北京天气怎么样"},
	})
	if err != nil {
		t.Fatalf("摘要失败: %v", err)
	}
	if strings.Contains(got, "草稿") {
		t.Errorf("草稿不应被保存：%q", got)
	}
	if !strings.Contains(got, "查天气") {
		t.Errorf("摘要正文缺失：%q", got)
	}
}

// 增量摘要：上一轮的摘要要一并提交，让新摘要覆盖两处的内容。
func TestLLMSummarizer_MergesPreviousSummary(t *testing.T) {
	sum, fake := newSummarizer(t, "<summary>合并后的摘要</summary>")

	if _, err := sum.Summarize(context.Background(), "这是上一轮的摘要XYZ", []model.Message{
		{Role: model.RoleUser, Content: "新的追问"},
	}); err != nil {
		t.Fatalf("摘要失败: %v", err)
	}

	prompt := decodedPrompt(t, fake.Body(0))
	if !strings.Contains(prompt, "这是上一轮的摘要XYZ") {
		t.Error("上一轮摘要应一并提交，否则早期的信息会随轮次推移被挤出")
	}
	if !strings.Contains(prompt, "合并") {
		t.Error("应指示模型把旧摘要与新对话合并，而不是只摘要新增部分")
	}
}

func TestLLMSummarizer_NoPreviousSummaryOmitsSection(t *testing.T) {
	sum, fake := newSummarizer(t, "<summary>首版摘要</summary>")

	if _, err := sum.Summarize(context.Background(), "   ", []model.Message{
		{Role: model.RoleUser, Content: "第一句"},
	}); err != nil {
		t.Fatalf("摘要失败: %v", err)
	}
	if strings.Contains(decodedPrompt(t, fake.Body(0)), "【已有摘要】") {
		t.Error("没有上一轮摘要时不应出现该小节")
	}
}

func TestLLMSummarizer_EmptyResponseIsError(t *testing.T) {
	sum, _ := newSummarizer(t, "<analysis>只有草稿</analysis>")

	// 只输出草稿时会退化为「取草稿之后的内容」，那部分是空的，
	// 此时应报错，让压缩器走纯规则裁剪的降级路径。
	_, err := sum.Summarize(context.Background(), "", []model.Message{
		{Role: model.RoleUser, Content: "内容"},
	})
	if err == nil {
		t.Error("摘要为空时应报错")
	}
}

// 提示模板是否有效，只有真实模型能回答——脚本化响应必然按格式来，
// 单测只能证明「拿到规整输出后解析正确」，证明不了「真实模型会给出规整输出」。
func TestIntegration_SummarizerProducesStructuredOutput(t *testing.T) {
	cfg := testsupport.IntegrationConfig(t)
	testsupport.RequireModelKey(t, cfg)

	client := llm.New(llm.Options{
		BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model,
		MaxRetries: cfg.MaxRetries, Timeout: cfg.LLMTimeout,
	})
	sum := NewLLMSummarizer(client)

	msgs := []model.Message{
		{Role: model.RoleUser, Content: "北京今天天气怎么样？"},
		{Role: model.RoleAssistant, Content: "我查一下。", ToolCalls: []model.ToolCall{
			{ID: "call_00_aBcDeFgHiJkLmNoPqRsTuV", Name: "weather", Args: `{"city":"北京"}`},
		}},
		{Role: model.RoleTool, Content: "北京（北京市）中国 局部多云 温度：23.7°C 风速：4.3km/h",
			ToolCallID: "call_00_aBcDeFgHiJkLmNoPqRsTuV"},
		{Role: model.RoleAssistant, Content: "北京今天局部多云，气温 23.7°C。"},
		{Role: model.RoleUser, Content: "帮我用 todo 记一条待办：买牛奶"},
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{
			{ID: "call_01_zYxWvUtSrQpOnMlKjIhGf", Name: "todo", Args: `{"action":"add","content":"买牛奶"}`},
		}},
		{Role: model.RoleTool, Content: "已记录待办（编号 1）：买牛奶",
			ToolCallID: "call_01_zYxWvUtSrQpOnMlKjIhGf"},
		{Role: model.RoleAssistant, Content: "已记录待办（编号 1）：买牛奶"},
		{Role: model.RoleUser, Content: "那杭州呢？"},
	}

	got, err := sum.Summarize(context.Background(), "", msgs)
	if err != nil {
		t.Fatalf("摘要失败: %v", err)
	}
	t.Logf("模型给出的摘要：\n%s", got)

	if strings.Contains(got, "<analysis>") || strings.Contains(got, "</analysis>") {
		t.Errorf("草稿标签不应出现在保存的摘要里")
	}

	// 用户原话必须一条不漏——这是模板里唯一不设字数上限的一节。
	for _, want := range []string{"北京今天天气怎么样", "买牛奶", "那杭州呢"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要中丢失了用户原话 %q", want)
		}
	}
	// 工具调用得到的关键事实要保留，但不必保留完整返回。
	if !strings.Contains(got, "23.7") {
		t.Errorf("摘要中丢失了工具查到的关键事实（温度）")
	}
	if !strings.Contains(got, "weather") && !strings.Contains(got, "天气") {
		t.Errorf("摘要中未体现调用了工具")
	}
}

// 工具结果那行必须写工具名。调用标识形如 call_00_j4ppBskuLmdE734MytOH6512，
// 对模型是一串无意义的随机字符，无法据此判断结果是哪个工具给出的。
func TestRenderMessages_UsesToolNameNotCallID(t *testing.T) {
	const callID = "call_00_j4ppBskuLmdE734MytOH6512"
	msgs := []model.Message{
		{Role: model.RoleUser, Content: "北京天气怎么样"},
		{Role: model.RoleAssistant, Content: "我查一下。", ToolCalls: []model.ToolCall{
			{ID: callID, Name: "weather", Args: `{"city":"北京"}`},
		}},
		{Role: model.RoleTool, Content: "北京 局部多云 23.7°C", ToolCallID: callID},
	}

	got := renderMessages(msgs)

	if strings.Contains(got, callID) {
		t.Errorf("不应把调用标识写给模型：\n%s", got)
	}
	if !strings.Contains(got, "工具 weather 返回：") {
		t.Errorf("工具结果应标注工具名：\n%s", got)
	}
}

// 一轮里调用了多个工具时，每条结果要对应到各自的名称。
func TestRenderMessages_MapsEachResultToItsOwnTool(t *testing.T) {
	msgs := []model.Message{
		{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{
			{ID: "c1", Name: "weather", Args: `{"city":"北京"}`},
			{ID: "c2", Name: "calculator", Args: `{"expression":"1+1"}`},
		}},
		{Role: model.RoleTool, Content: "晴", ToolCallID: "c1"},
		{Role: model.RoleTool, Content: "2", ToolCallID: "c2"},
	}

	got := renderMessages(msgs)

	if !strings.Contains(got, "工具 weather 返回：晴") {
		t.Errorf("第一条结果应标注 weather：\n%s", got)
	}
	if !strings.Contains(got, "工具 calculator 返回：2") {
		t.Errorf("第二条结果应标注 calculator：\n%s", got)
	}
}

// 对应的助手消息被历史窗口截断时，如实说明而不是编造工具名，
// 也不把无意义的标识塞给模型。
func TestRenderMessages_UnknownToolIsReportedHonestly(t *testing.T) {
	msgs := []model.Message{
		{Role: model.RoleTool, Content: "孤立的结果", ToolCallID: "call_孤立"},
	}

	got := renderMessages(msgs)

	if strings.Contains(got, "call_孤立") {
		t.Errorf("不应把无法解析的标识写给模型：\n%s", got)
	}
	if !strings.Contains(got, "未能确定") || !strings.Contains(got, "孤立的结果") {
		t.Errorf("应如实说明无法确定工具并保留内容：\n%s", got)
	}
}

func TestLLMSummarizer_NoMessagesReturnsPrevious(t *testing.T) {
	sum, fake := newSummarizer(t, "<summary>不该被调用</summary>")

	got, err := sum.Summarize(context.Background(), "原摘要", nil)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if got != "原摘要" {
		t.Errorf("没有新消息时应原样返回旧摘要，实际 %q", got)
	}
	if fake.Requests() != 0 {
		t.Error("没有新消息时不应发起模型调用")
	}
}
