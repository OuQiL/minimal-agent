package contextmgr

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"minimal-agent/internal/llm"
	"minimal-agent/internal/model"
	"minimal-agent/internal/store"
)

// Summarizer 把一段较早的对话压缩成摘要。
//
// 抽象成接口是为了让压缩逻辑可单测：测试可以注入一个确定性的假实现，
// 而不必真的发起模型调用。
type Summarizer interface {
	// Summarize 生成新摘要。previous 是上一轮的摘要（可能为空），
	// msgs 是本次新纳入压缩范围的消息。
	Summarize(ctx context.Context, previous string, msgs []model.Message) (string, error)
}

// Compactor 是默认的压缩实现：超过阈值时保留最近若干轮原文，
// 更早的内容交给 Summarizer 生成摘要并写回会话。
type Compactor struct {
	store      *store.Store
	summarizer Summarizer
	threshold  int
	keepTurns  int
}

// NewCompactor 创建压缩器。
func NewCompactor(s *store.Store, sum Summarizer, threshold, keepTurns int) *Compactor {
	return &Compactor{store: s, summarizer: sum, threshold: threshold, keepTurns: keepTurns}
}

// Rewrite 实现 Rewriter。
//
// 摘要采用「滚动归并」而非「只摘要新增部分」：每次把上一轮的摘要与新纳入
// 压缩范围的消息一起交给模型。若只摘要新增部分，摘要会不断覆盖而非累积，
// 早期信息会随轮次推移被逐步挤出。
func (c *Compactor) Rewrite(ctx context.Context, sessionID string, msgs []model.Message) ([]model.Message, error) {
	if c.threshold <= 0 || TotalChars(msgs) <= c.threshold {
		return msgs, nil
	}
	trimmed, _, err := c.compact(ctx, sessionID, msgs)
	return trimmed, err
}

// CompactNow 强制执行一次压缩，忽略阈值判断。
//
// 供 /compact 命令使用：阈值是自动触发的门槛，但用户应当能主动要求压缩。
// 返回是否实际发生了压缩。
func (c *Compactor) CompactNow(ctx context.Context, sessionID string) (bool, error) {
	msgs, err := c.store.Messages(sessionID, 0)
	if err != nil {
		return false, err
	}
	_, changed, err := c.compact(ctx, sessionID, msgs)
	return changed, err
}

// compact 执行压缩，返回裁剪后的消息与是否发生了变化。
func (c *Compactor) compact(ctx context.Context, sessionID string, msgs []model.Message) ([]model.Message, bool, error) {
	tail := RecentTail(msgs, c.keepTurns)
	if len(tail) >= len(msgs) {
		// 全部消息都在保留窗口内，没有可压缩的部分。
		return msgs, false, nil
	}
	// 保留最近若干轮只是第一步：若这几轮本身就超阈值，还需继续收缩，
	// 否则「压缩」并未真正把上下文压到界内。
	tail = CapTail(tail, c.threshold)
	older := msgs[:len(msgs)-len(tail)]

	sess, err := c.store.GetSession(sessionID)
	if err != nil {
		return nil, false, err
	}

	summary, err := c.summarizer.Summarize(ctx, sess.Summary, older)
	if err != nil {
		// 降级：摘要失败不阻塞对话，退化为纯规则裁剪。
		slog.Warn("生成摘要失败，本次退化为纯规则裁剪",
			"session", sessionID, "messages", len(older), "err", err)
		return tail, true, nil
	}

	if err := c.store.UpdateSessionSummary(sessionID, summary); err != nil {
		slog.Warn("写回摘要失败，本次仍按裁剪后的上下文继续",
			"session", sessionID, "err", err)
		return tail, true, nil
	}

	slog.Info("已压缩历史", "session", sessionID,
		"压缩掉的消息", len(older), "保留的最近轮数", c.keepTurns)
	return tail, true, nil
}

// LLMSummarizer 用模型生成摘要。
type LLMSummarizer struct {
	client *llm.Client
}

// NewLLMSummarizer 创建基于模型的摘要器。
func NewLLMSummarizer(c *llm.Client) *LLMSummarizer { return &LLMSummarizer{client: c} }

// Summarize 实现 Summarizer。
func (s *LLMSummarizer) Summarize(ctx context.Context, previous string, msgs []model.Message) (string, error) {
	if len(msgs) == 0 {
		return previous, nil
	}

	var b strings.Builder
	b.WriteString("请把下面这段对话压缩成一段简洁的摘要，供后续对话作为背景参考。\n\n")
	b.WriteString("要求：\n")
	b.WriteString("1. 保留事实性信息：出现过的实体（人名、地名、数字、结论）、用户表达的偏好与目标、未完成的事项。\n")
	b.WriteString("2. 保留工具调用的关键结论（例如查到的天气、搜索结果的核心事实），但不必保留完整的原始返回。\n")
	b.WriteString("3. 不要编造原文没有的信息。如果某条信息不确定，宁可不写。\n")
	b.WriteString("4. 用中文，控制在 300 字以内，直接输出摘要正文，不要加标题或客套话。\n\n")

	if strings.TrimSpace(previous) != "" {
		b.WriteString("【已有摘要】\n")
		b.WriteString(previous)
		b.WriteString("\n\n请把已有摘要与下面的新对话合并成一份新的摘要。\n\n")
	}

	b.WriteString("【需要压缩的对话】\n")
	b.WriteString(renderMessages(msgs))

	resp, err := s.client.Complete(ctx, []model.Message{
		{Role: model.RoleUser, Content: b.String()},
	}, "你是一个负责压缩对话历史的助手，只输出摘要正文。")
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(resp.Content)
	if text == "" {
		return "", fmt.Errorf("摘要生成为空")
	}
	return text, nil
}

// renderMessages 把消息渲染成便于模型阅读的纯文本。
func renderMessages(msgs []model.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case model.RoleUser:
			fmt.Fprintf(&b, "用户：%s\n", m.Content)
		case model.RoleAssistant:
			if m.Content != "" {
				fmt.Fprintf(&b, "助手：%s\n", m.Content)
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "（助手调用工具 %s，参数 %s）\n", tc.Name, tc.Args)
			}
		case model.RoleTool:
			fmt.Fprintf(&b, "工具 %s 返回：%s\n", m.ToolCallID, m.Content)
		case model.RoleSystem:
			// 系统消息不参与摘要，它每次请求都会重新注入。
		}
	}
	return b.String()
}
