package contextmgr_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"minimal-agent/internal/contextmgr"
	"minimal-agent/internal/model"
)

// seed 写入 turns 轮「问-答」，每轮内容足够长，便于触发阈值。
func seed(t *testing.T, s interface {
	AppendMessages(...model.Message) error
}, sessionID string, turns int, filler int) {
	t.Helper()
	body := strings.Repeat("内容", filler)
	for i := range turns {
		if err := s.AppendMessages(
			model.Message{SessionID: sessionID, Role: model.RoleUser,
				Content: strings.Repeat("问题", filler) + string(rune('一'+i))},
			model.Message{SessionID: sessionID, Role: model.RoleAssistant,
				Content: body},
		); err != nil {
			t.Fatalf("写入第 %d 轮失败: %v", i, err)
		}
	}
}

func TestCompactor_NoTriggerUnderThreshold(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := s.AppendMessages(model.Message{SessionID: "sess", Role: model.RoleUser, Content: "短"}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	sum := &fakeSummarizer{}
	c := contextmgr.NewCompactor(s, sum, 100000, 2)

	msgs, _ := s.Messages("sess", 0)
	out, err := c.Rewrite(context.Background(), "sess", msgs)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if len(out) != len(msgs) {
		t.Errorf("未超阈值不应压缩，条数 %d -> %d", len(msgs), len(out))
	}
	if len(sum.calls) != 0 {
		t.Error("未超阈值不应调用摘要器")
	}
}

func TestCompactor_TriggersAndKeepsRecentTurns(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	seed(t, s, "sess", 10, 50)

	// 阈值取 800：10 轮约 2100 字符会被触发，而最近 3 轮约 640 字符能装下。
	sum := &fakeSummarizer{}
	c := contextmgr.NewCompactor(s, sum, 800, 3)

	msgs, _ := s.Messages("sess", 0)
	out, err := c.Rewrite(context.Background(), "sess", msgs)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if len(out) >= len(msgs) {
		t.Fatalf("超阈值后应裁剪，条数 %d -> %d", len(msgs), len(out))
	}
	// 最近 3 轮 = 3 组问-答 = 6 条
	if len(out) != 6 {
		t.Errorf("保留条数 = %d，期望 6（最近 3 轮）", len(out))
	}
	if got := contextmgr.TotalChars(out); got > 800 {
		t.Errorf("裁剪后仍超阈值: %d 字符", got)
	}
	if len(sum.calls) != 1 {
		t.Fatalf("摘要调用次数 = %d，期望 1", len(sum.calls))
	}
	if len(sum.calls[0].msgs) != len(msgs)-len(out) {
		t.Errorf("摘要覆盖 %d 条，期望 %d 条", len(sum.calls[0].msgs), len(msgs)-len(out))
	}

	// 摘要应写回会话。
	sess, err := s.GetSession("sess")
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if sess.Summary == "" {
		t.Error("摘要应写回会话")
	}
}

// 增量摘要：第二次压缩时，上一轮的摘要要与新纳入的消息一起交给模型。
// 若只摘要新增部分，摘要会不断覆盖而非累积，早期信息会被逐步挤出。
func TestCompactor_IncrementalSummaryCarriesPrevious(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	seed(t, s, "sess", 10, 50)

	sum := &fakeSummarizer{result: "第一版摘要"}
	c := contextmgr.NewCompactor(s, sum, 100, 3)

	msgs, _ := s.Messages("sess", 0)
	if _, err := c.Rewrite(context.Background(), "sess", msgs); err != nil {
		t.Fatalf("第一次压缩失败: %v", err)
	}

	sum.result = "第二版摘要"
	if _, err := c.Rewrite(context.Background(), "sess", msgs); err != nil {
		t.Fatalf("第二次压缩失败: %v", err)
	}

	if len(sum.calls) != 2 {
		t.Fatalf("摘要调用次数 = %d，期望 2", len(sum.calls))
	}
	if sum.calls[0].previous != "" {
		t.Errorf("首次压缩的旧摘要应为空，实际 %q", sum.calls[0].previous)
	}
	if sum.calls[1].previous != "第一版摘要" {
		t.Errorf("第二次压缩应带上旧摘要，实际 %q", sum.calls[1].previous)
	}
}

// 摘要失败不能阻塞对话：退化为纯规则裁剪，并把结果写进日志。
func TestCompactor_DegradesWhenSummarizerFails(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	seed(t, s, "sess", 10, 50)

	sum := &fakeSummarizer{err: errors.New("模拟的摘要失败")}
	c := contextmgr.NewCompactor(s, sum, 800, 3)

	msgs, _ := s.Messages("sess", 0)
	out, err := c.Rewrite(context.Background(), "sess", msgs)
	if err != nil {
		t.Fatalf("摘要失败不应让压缩报错，实际: %v", err)
	}
	if len(out) != 6 {
		t.Errorf("降级后仍应裁剪到最近 3 轮，实际 %d 条", len(out))
	}
	if got := contextmgr.TotalChars(out); got > 800 {
		t.Errorf("降级路径同样应落在阈值内，实际 %d 字符", got)
	}
}

// 最后一轮是例外：即使它单独就超阈值也保留，否则模型看不到当前的问题。
func TestCompactor_KeepsLastTurnEvenIfOversized(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	seed(t, s, "sess", 6, 50)

	sum := &fakeSummarizer{}
	// 阈值比单独一轮还小，无法满足；此时应退让为「至少保留最后一轮」。
	c := contextmgr.NewCompactor(s, sum, 20, 3)

	msgs, _ := s.Messages("sess", 0)
	out, err := c.Rewrite(context.Background(), "sess", msgs)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("保留条数 = %d，期望 2（最后一轮）", len(out))
	}
	if out[0].Role != model.RoleUser {
		t.Errorf("保留内容应以用户消息开头，实际 %s", out[0].Role)
	}
}

func TestCapTail(t *testing.T) {
	turn := func(size int) []model.Message {
		return []model.Message{
			{Role: model.RoleUser, Content: strings.Repeat("问", size)},
			{Role: model.RoleAssistant, Content: strings.Repeat("答", size)},
		}
	}
	var msgs []model.Message
	for range 5 {
		msgs = append(msgs, turn(50)...)
	}

	t.Run("阈值充裕时不动", func(t *testing.T) {
		if got := contextmgr.CapTail(msgs, 100000); len(got) != len(msgs) {
			t.Errorf("条数 = %d，期望 %d", len(got), len(msgs))
		}
	})

	t.Run("逐轮收缩到阈值内", func(t *testing.T) {
		got := contextmgr.CapTail(msgs, 250)
		if contextmgr.TotalChars(got) > 250 {
			t.Errorf("仍超阈值: %d 字符", contextmgr.TotalChars(got))
		}
		if len(got) == 0 {
			t.Error("不应裁到空")
		}
		if got[0].Role != model.RoleUser {
			t.Errorf("切分点应落在用户消息上，实际 %s", got[0].Role)
		}
	})

	t.Run("阈值过小时至少保留最后一轮", func(t *testing.T) {
		got := contextmgr.CapTail(msgs, 1)
		if len(got) != 2 {
			t.Errorf("条数 = %d，期望 2", len(got))
		}
	})
}

func TestCompactNow_ForcesCompaction(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	seed(t, s, "sess", 10, 50)

	// 阈值设得极高，自动压缩不会触发。
	sum := &fakeSummarizer{}
	c := contextmgr.NewCompactor(s, sum, 10_000_000, 3)

	changed, err := c.CompactNow(context.Background(), "sess")
	if err != nil {
		t.Fatalf("手动压缩失败: %v", err)
	}
	if !changed {
		t.Error("手动压缩应当实际发生，忽略阈值")
	}
	if len(sum.calls) != 1 {
		t.Errorf("摘要调用次数 = %d，期望 1", len(sum.calls))
	}
}

func TestCompactNow_NoContentToCompact(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := s.AppendMessages(model.Message{SessionID: "sess", Role: model.RoleUser, Content: "只有一轮"}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	sum := &fakeSummarizer{}
	c := contextmgr.NewCompactor(s, sum, 10_000_000, 6)

	changed, err := c.CompactNow(context.Background(), "sess")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if changed {
		t.Error("全部消息都在保留窗口内时不应报告发生了压缩")
	}
	if len(sum.calls) != 0 {
		t.Error("无可压缩内容时不应调用摘要器")
	}
}
