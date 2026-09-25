package tool_test

import (
	"strings"
	"testing"

	"minimal-agent/internal/tool"
)

func TestTruncate_UnderLimitIsUntouched(t *testing.T) {
	b := tool.Budget{MaxItemChars: 100, MaxTotalChars: 500}
	out, truncated := b.Truncate([]string{"短内容一", "短内容二"})

	if truncated {
		t.Error("未超限时不应报告截断")
	}
	if !strings.Contains(out, "短内容一") || !strings.Contains(out, "短内容二") {
		t.Errorf("输出应保留全部内容，实际：%s", out)
	}
}

func TestTruncate_LongItemIsTrimmedAndMarked(t *testing.T) {
	b := tool.Budget{MaxItemChars: 10, MaxTotalChars: 1000}
	out, truncated := b.Truncate([]string{strings.Repeat("很", 50)})

	if !truncated {
		t.Error("单条超限应报告截断")
	}
	// 截断必须被标注：模型需要知道信息不完整，否则会把残缺内容当作全部事实。
	if !strings.Contains(out, "截断") {
		t.Errorf("输出应标注截断，实际：%s", out)
	}
	if n := len([]rune(out)); n > 60 {
		t.Errorf("输出长度 = %d，明显超出单条上限", n)
	}
}

func TestTruncate_TotalLimitIsEnforced(t *testing.T) {
	b := tool.Budget{MaxItemChars: 1000, MaxTotalChars: 50}
	items := []string{strings.Repeat("甲", 40), strings.Repeat("乙", 40)}

	out, truncated := b.Truncate(items)

	if !truncated {
		t.Error("总量超限应报告截断")
	}
	if !strings.Contains(out, "总量") {
		t.Errorf("输出应说明总量被截断，实际：%s", out)
	}
	if n := len([]rune(out)); n > 80 {
		t.Errorf("输出长度 = %d，明显超出总量上限", n)
	}
}

func TestTruncate_CountsRunesNotBytes(t *testing.T) {
	// 中文一个字三个字节；按字节截断会把字符切断成乱码。
	b := tool.Budget{MaxItemChars: 5, MaxTotalChars: 100}
	out, _ := b.Truncate([]string{"一二三四五六七八九十"})

	if !strings.HasPrefix(out, "一二三四五") {
		t.Errorf("应按字符截断，实际：%s", out)
	}
}

func TestTruncate_SkipsBlankItems(t *testing.T) {
	b := tool.DefaultBudget()
	out, _ := b.Truncate([]string{"有内容", "   ", ""})
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("空白条目应被跳过，实际：%q", out)
	}
}

func TestWrapExternal_MarksSourceAndBoundary(t *testing.T) {
	wrapped := tool.WrapExternal("bocha-search", "结果正文")

	if !strings.Contains(wrapped, "bocha-search") {
		t.Error("包裹内容应标注来源")
	}
	if !strings.Contains(wrapped, "结果正文") {
		t.Error("包裹内容应保留正文")
	}
	if !strings.Contains(wrapped, "external-content") {
		t.Error("应使用明确的边界标记，使外部内容与对话本身可区分")
	}
}
