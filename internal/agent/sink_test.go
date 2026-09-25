package agent_test

import (
	"bytes"
	"strings"
	"testing"

	"minimal-agent/internal/agent"
)

// 三类输出必须能被区分，否则屏幕上会出现「看起来像答案、其实是过程说明」
// 的歧义——模型可能先输出一段正文再请求调用工具，那段正文并非最终答复。
func TestConsoleSink_DistinguishesOutputKinds(t *testing.T) {
	var buf bytes.Buffer
	s := agent.NewConsoleSink(&buf, true)

	s.OnReasoning("让我想想")
	s.OnContent("结论是甲")
	s.OnToolCall("weather")
	s.OnDone()

	out := buf.String()

	if !strings.Contains(out, "[思考]") {
		t.Error("思维链应带可识别的前缀")
	}
	if !strings.Contains(out, "让我想想") {
		t.Error("思维链内容应被输出")
	}
	if !strings.Contains(out, "结论是甲") {
		t.Error("正文应被输出")
	}
	if !strings.Contains(out, "weather") {
		t.Error("工具调用应被提示")
	}

	// 思维链要暗色化，正文不能——否则两者在视觉上无法区分。
	dimmedReasoning := "\033[2m让我想想"
	if !strings.Contains(out, dimmedReasoning) {
		t.Errorf("思维链应以暗色呈现，实际输出：%q", out)
	}
	if strings.Contains(out, "\033[2m结论是甲") {
		t.Errorf("正文不应被当作过程说明着色，实际输出：%q", out)
	}

	// 顺序：思维链 → 正文 → 工具提示
	iReason := strings.Index(out, "让我想想")
	iContent := strings.Index(out, "结论是甲")
	iTool := strings.Index(out, "weather")
	if !(iReason < iContent && iContent < iTool) {
		t.Errorf("输出顺序应为思维链 → 正文 → 工具调用，实际位置 %d/%d/%d", iReason, iContent, iTool)
	}
}

func TestConsoleSink_NoColorEmitsNoEscapes(t *testing.T) {
	var buf bytes.Buffer
	s := agent.NewConsoleSink(&buf, false)

	s.OnReasoning("思考")
	s.OnContent("正文")
	s.OnToolCall("calculator")
	s.OnDone()

	out := buf.String()

	if strings.Contains(out, "\033[") {
		t.Errorf("关闭着色后不应输出任何转义序列，实际：%q", out)
	}
	// 内容本身不能因为关闭着色而丢失。
	for _, want := range []string{"思考", "正文", "calculator"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出中缺少 %q", want)
		}
	}
}

// 没有思维链时不应凭空出现思维链前缀。
func TestConsoleSink_NoPrefixWithoutReasoning(t *testing.T) {
	var buf bytes.Buffer
	s := agent.NewConsoleSink(&buf, true)
	s.OnContent("直接回答")
	s.OnDone()

	if strings.Contains(buf.String(), "[思考]") {
		t.Errorf("未产生思维链时不应输出其前缀，实际：%q", buf.String())
	}
}

func TestConsoleSink_HandlesDeltasWithoutExtraNewlines(t *testing.T) {
	var buf bytes.Buffer
	s := agent.NewConsoleSink(&buf, false)

	// 模拟逐字到达：不应在每个分片后插入换行。
	for _, r := range []string{"你", "好", "，", "世", "界"} {
		s.OnContent(r)
	}
	s.OnDone()

	got := strings.TrimRight(buf.String(), "\n")
	if got != "你好，世界" {
		t.Errorf("逐字输出应当连成一句话，实际：%q", got)
	}
}

func TestConsoleSink_DoneIsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	s := agent.NewConsoleSink(&buf, false)
	s.OnContent("回答")
	s.OnDone()
	first := buf.String()
	s.OnDone()

	if buf.String() != first {
		t.Error("重复收到完成信号不应产生额外输出")
	}
}
