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

// summarySystemPrompt 是摘要调用的系统提示。
const summarySystemPrompt = "你负责把对话压缩成结构化摘要。严格按用户给出的格式输出，不要添加格式之外的内容。"

// summaryTemplate 是压缩对话的提示模板。
//
// 两段式设计：
//   - <analysis> 是草稿，按时间顺序逐条梳理对话。它不会被保留（extractSummary
//     会丢弃它），作用是让模型在写摘要前先完整过一遍原始对话，减少遗漏。
//   - <summary> 才是要落库的正文，按固定小节组织。
//
// 第 2 节（用户消息原文）刻意不设字数上限：用户说过的话是最不该丢的信息，
// 其余各节合计限制在 500 字以内，把预算让给它。
const summaryTemplate = `你是对话历史压缩器。把给定的对话压缩成结构化摘要，供后续对话作为背景参考。

【第一步】先输出 <analysis> 块：按时间顺序逐条梳理这段对话——每轮用户说了什么、你做了什么、调用了哪些工具、得到什么结论。这是草稿，不会被保留。

【第二步】再输出 <summary> 块，按下列小节组织，缺哪节写「无」：
1. 用户的请求与意图：逐条列出用户显式提出的要求，保留关键措辞。
2. 用户消息原文：逐条列出用户说过的每一句话（工具结果不算）。这一节不设字数上限，一条都不能漏。
3. 实体与事实：出现过的地名、人名、数字、时间、结论。
4. 工具调用结论：调用了哪个工具、得到的关键事实。不必保留完整返回。
5. 未完成事项：用户要求但尚未办完的事。
6. 当前状态：最后一次交互时正在处理什么。
7. 下一步：仅当与用户最近一次显式请求直接相关时才写。

【约束】不要编造原文没有的信息，不确定的宁可不写。全部用中文。除第 2 节外，其余各节合计控制在 500 字以内。`

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
	b.WriteString(summaryTemplate)
	b.WriteString("\n\n")

	if prev := strings.TrimSpace(previous); prev != "" {
		b.WriteString("【已有摘要】\n")
		b.WriteString(prev)
		b.WriteString("\n\n上面是此前生成的摘要。请把它与下面的新对话合并成一份新的结构化摘要，" +
			"第 2 节要完整包含两处的用户消息原文。\n\n")
	}

	b.WriteString("【需要压缩的对话】\n")
	b.WriteString(renderMessages(msgs))

	resp, err := s.client.Complete(ctx, []model.Message{
		{Role: model.RoleUser, Content: b.String()},
	}, summarySystemPrompt)
	if err != nil {
		return "", err
	}

	summary := extractSummary(resp.Content)
	if summary == "" {
		return "", fmt.Errorf("摘要生成为空")
	}
	return summary, nil
}

// extractSummary 从模型返回中取出 <summary> 块，丢弃 <analysis> 草稿。
//
// 模型的输出格式不总是规整，因此逐级降级，而不是一遇意外就整段丢弃：
//
//  1. 正常路径：取 <summary> 与 </summary> 之间的内容；
//  2. 只开了 <summary> 没闭合：取到结尾——总比丢掉整段摘要好；
//  3. 只输出了草稿：取 </analysis> 之后的内容，并去掉可能残留的标签；
//  4. 完全没按格式来：整段当作摘要，但剥掉已知的标签。
//
// 最后两级是兜底：宁可存下一段不那么规整的摘要，也好过让调用方退回
// 纯规则裁剪、把这段历史彻底丢掉。
func extractSummary(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}

	// 缺少闭合标签时 Cut 会返回剩余全部内容——总比丢掉整段摘要好。
	if _, rest, ok := strings.Cut(text, "<summary>"); ok {
		body, _, _ := strings.Cut(rest, "</summary>")
		return strings.TrimSpace(body)
	}

	if _, rest, ok := strings.Cut(text, "</analysis>"); ok {
		return stripTags(rest)
	}

	return stripTags(text)
}

func stripTags(s string) string {
	s = strings.ReplaceAll(s, "<analysis>", "")
	s = strings.ReplaceAll(s, "</analysis>", "")
	s = strings.ReplaceAll(s, "<summary>", "")
	s = strings.ReplaceAll(s, "</summary>", "")
	return strings.TrimSpace(s)
}

// renderMessages 把消息渲染成便于模型阅读的纯文本。
//
// 工具结果那行必须写工具名而不是调用标识：标识形如
// call_00_j4ppBskuLmdE734MytOH6512，对模型而言是一串无意义的随机字符，
// 无法据此判断这条结果是哪个工具给出的。实测中模型会因此写下
// 「未展示具体工具调用」这类含糊结论。
//
// 工具名不在 tool 消息上——它在前一条 assistant 消息的 tool_calls 里，
// 而那条消息一定先出现，所以边遍历边记下映射即可。
func renderMessages(msgs []model.Message) string {
	var b strings.Builder
	toolNames := make(map[string]string, len(msgs))

	for _, m := range msgs {
		switch m.Role {
		case model.RoleUser:
			fmt.Fprintf(&b, "用户：%s\n", m.Content)

		case model.RoleAssistant:
			if m.Content != "" {
				fmt.Fprintf(&b, "助手：%s\n", m.Content)
			}
			for _, tc := range m.ToolCalls {
				toolNames[tc.ID] = tc.Name
				fmt.Fprintf(&b, "（助手调用工具 %s，参数 %s）\n", tc.Name, tc.Args)
			}

		case model.RoleTool:
			if name, ok := toolNames[m.ToolCallID]; ok {
				fmt.Fprintf(&b, "工具 %s 返回：%s\n", name, m.Content)
			} else {
				// 对应的助手消息被历史窗口截断时无法还原工具名。
				// 如实说明，既不编造一个名字，也不把无意义的标识塞给模型。
				fmt.Fprintf(&b, "（某工具，未能确定是哪一个）返回：%s\n", m.Content)
			}

		case model.RoleSystem:
			// 系统消息不参与摘要，它每次请求都会重新注入。
		}
	}
	return b.String()
}
