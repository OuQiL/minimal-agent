package contextmgr_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"minimal-agent/internal/contextmgr"
	"minimal-agent/internal/model"
	"minimal-agent/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fakeSummarizer 是一个确定性的摘要器，避免测试依赖真实模型。
type fakeSummarizer struct {
	calls  []summaryCall
	result string
	err    error
}

type summaryCall struct {
	previous string
	msgs     []model.Message
}

func (f *fakeSummarizer) Summarize(_ context.Context, previous string, msgs []model.Message) (string, error) {
	f.calls = append(f.calls, summaryCall{previous: previous, msgs: msgs})
	if f.err != nil {
		return "", f.err
	}
	if f.result != "" {
		return f.result, nil
	}
	return fmt.Sprintf("摘要（覆盖 %d 条消息）", len(msgs)), nil
}

// --- 上下文组装 ---

func TestBuild_OrderIsSystemThenSummaryThenHistory(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := s.AppendMessages(
		model.Message{SessionID: "sess", Role: model.RoleUser, Content: "第一问"},
		model.Message{SessionID: "sess", Role: model.RoleAssistant, Content: "第一答"},
	); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := s.UpdateSessionSummary("sess", "这是此前对话的摘要"); err != nil {
		t.Fatalf("写摘要失败: %v", err)
	}

	b := contextmgr.NewBuilder(contextmgr.Config{MaxHistoryMsgs: 40}, s)
	got, err := b.Build(context.Background(), "sess")
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}

	if !strings.Contains(got.System, "sess") {
		t.Error("系统提示中应包含当前会话标识")
	}
	if !strings.Contains(got.System, "这是此前对话的摘要") {
		t.Error("系统提示中应注入摘要")
	}
	if !got.SummaryUsed {
		t.Error("应报告本次使用了摘要")
	}

	// 摘要必须在系统提示之内、历史消息之前——这样它是背景知识，
	// 不会与真实历史混淆。
	idxPrompt := strings.Index(got.System, "工具调用能力")
	idxSummary := strings.Index(got.System, "这是此前对话的摘要")
	if idxPrompt < 0 || idxSummary < 0 || idxPrompt > idxSummary {
		t.Error("摘要应位于系统提示之后")
	}

	if len(got.Messages) != 2 {
		t.Fatalf("历史消息数量 = %d，期望 2", len(got.Messages))
	}
	if got.Messages[0].Content != "第一问" || got.Messages[1].Content != "第一答" {
		t.Error("历史消息应保持时间顺序")
	}
}

// 系统提示必须声明工具返回内容的边界，这是接入外部服务后的提示注入防线。
func TestBuildSystemPrompt_DeclaresDataBoundary(t *testing.T) {
	p := contextmgr.BuildSystemPrompt("sess_x")

	if !strings.Contains(p, "sess_x") {
		t.Error("应包含当前会话标识")
	}
	if !strings.Contains(p, "数据") || !strings.Contains(p, "指令") {
		t.Errorf("应声明工具返回内容是数据而非指令，实际：%s", p)
	}
}

func TestBuild_WithoutSummary(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	b := contextmgr.NewBuilder(contextmgr.Config{MaxHistoryMsgs: 40}, s)
	got, err := b.Build(context.Background(), "sess")
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	if got.SummaryUsed {
		t.Error("无摘要时不应报告使用了摘要")
	}
	if strings.Contains(got.System, "摘要") {
		t.Error("无摘要时不应注入摘要段落")
	}
}

// Rewriter 应当可以被整体替换，这是把它抽成钩子的意义所在。
func TestBuild_RewriterIsReplaceable(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := s.AppendMessages(
		model.Message{SessionID: "sess", Role: model.RoleUser, Content: "原始消息"},
	); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	replacer := rewriterFunc(func(_ context.Context, _ string, msgs []model.Message) ([]model.Message, error) {
		return append(msgs, model.Message{Role: model.RoleSystem, Content: "由改写器注入"}), nil
	})

	b := contextmgr.NewBuilder(contextmgr.Config{MaxHistoryMsgs: 40}, s, replacer)
	got, err := b.Build(context.Background(), "sess")
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("消息数量 = %d，期望 2（原消息 + 改写器注入）", len(got.Messages))
	}
	if got.Messages[1].Content != "由改写器注入" {
		t.Error("改写器的输出应当生效")
	}
}

type rewriterFunc func(context.Context, string, []model.Message) ([]model.Message, error)

func (f rewriterFunc) Rewrite(ctx context.Context, id string, m []model.Message) ([]model.Message, error) {
	return f(ctx, id, m)
}

// --- 工具函数 ---

func TestTotalChars(t *testing.T) {
	msgs := []model.Message{
		{Role: model.RoleUser, Content: "一二三"}, // 3
		{Role: model.RoleAssistant, Content: "四五六", ToolCalls: []model.ToolCall{
			{Name: "weather", Args: `{"city":"北京"}`}, // 7 + 15
		}},
	}
	got := contextmgr.TotalChars(msgs)
	if got <= 0 {
		t.Fatal("字符数应大于零")
	}
	// 中文按字符计数而非字节：3 个汉字不算 9。
	if got < 3 {
		t.Errorf("字符数 = %d，中文应按字符计数", got)
	}
}

func TestRecentTail(t *testing.T) {
	msgs := []model.Message{
		{Role: model.RoleUser, Content: "问1"},
		{Role: model.RoleAssistant, Content: "答1"},
		{Role: model.RoleTool, Content: "工具1"},
		{Role: model.RoleUser, Content: "问2"},
		{Role: model.RoleAssistant, Content: "答2"},
		{Role: model.RoleUser, Content: "问3"},
		{Role: model.RoleAssistant, Content: "答3"},
	}

	t.Run("保留最近两轮", func(t *testing.T) {
		tail := contextmgr.RecentTail(msgs, 2)
		// 最近两轮 = 问2/答2 与 问3/答3。切分点必须落在用户消息上，
		// 否则会留下属于第一轮、却没有对应提问的「答1」与「工具1」。
		if len(tail) != 4 {
			t.Fatalf("条数 = %d，期望 4（问2、答2、问3、答3）: %+v", len(tail), tail)
		}
		if tail[0].Content != "问2" || tail[0].Role != model.RoleUser {
			t.Errorf("起点 = (%s, %q)，期望以用户消息「问2」开头", tail[0].Role, tail[0].Content)
		}
		for _, m := range tail {
			if m.Content == "答1" || m.Content == "工具1" {
				t.Errorf("输出中不应残留上一轮的 %q", m.Content)
			}
		}
	})

	t.Run("轮数足够时全部保留", func(t *testing.T) {
		if tail := contextmgr.RecentTail(msgs, 99); len(tail) != len(msgs) {
			t.Errorf("条数 = %d，期望 %d", len(tail), len(msgs))
		}
	})

	t.Run("轮数为零时全部保留", func(t *testing.T) {
		if tail := contextmgr.RecentTail(msgs, 0); len(tail) != len(msgs) {
			t.Errorf("条数 = %d，期望 %d", len(tail), len(msgs))
		}
	})
}
