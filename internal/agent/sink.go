package agent

import (
	"fmt"
	"io"
	"sync"

	"minimal-agent/internal/llm"
)

// ANSI 转义序列。着色在非 TTY 环境下会被关闭。
const (
	ansiReset = "\033[0m"
	ansiDim   = "\033[2m"
)

// ConsoleSink 把流式增量渲染到终端。
//
// 视觉规则只有一条：**只有最终答复是明亮的**。
// 思维链与工具调用都属于「过程」，一律暗色，让它们退到背景里。
//
// 正文则一律明亮。这里有个无法回避的取舍：一段正文究竟是最终答复还是
// 过程说明，要等 finish_reason 到达才知道，而那时它已经打印出去了——
// 流式渲染没有「反悔」的余地。两害相权取其轻：把正文渲染成常规样式，
// 保证**最终答复一定是亮的**；代价是极少数「先输出一段说明再调工具」的
// 情形下，那段说明也会是亮的。反过来做（正文一律暗色）则会牺牲最终答复
// 的醒目程度，那是更糟的失误。
type ConsoleSink struct {
	out   io.Writer
	color bool

	mu           sync.Mutex
	wroteReason  bool
	wroteContent bool
	lineOpen     bool // 当前是否处于未换行的输出行中
}

// NewConsoleSink 创建终端渲染器。color 为 false 时不输出任何转义序列。
func NewConsoleSink(out io.Writer, color bool) *ConsoleSink {
	return &ConsoleSink{out: out, color: color}
}

func (c *ConsoleSink) paint(code, s string) string {
	if !c.color {
		return s
	}
	return code + s + ansiReset
}

// OnReasoning 输出一段思维链增量。
func (c *ConsoleSink) OnReasoning(delta string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.wroteReason {
		fmt.Fprint(c.out, c.paint(ansiDim, "[思考] "))
		c.wroteReason = true
	}
	fmt.Fprint(c.out, c.paint(ansiDim, delta))
	c.lineOpen = true
}

// OnContent 输出一段正文增量。
//
// 正文在流式阶段统一按常规样式呈现。它究竟是不是最终答复，要等响应收尾
// 时由结束原因决定；落库与 /history 回看时会按那个结论准确标注角色。
func (c *ConsoleSink) OnContent(delta string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.wroteReason && !c.wroteContent {
		// 思维链与正文之间断开一行，避免粘在一起
		fmt.Fprintln(c.out)
		c.lineOpen = false
	}
	fmt.Fprint(c.out, delta)
	c.wroteContent = true
	c.lineOpen = true
}

// OnToolCall 报告一个工具调用已拼装完成。
//
// 与思维链同样按暗色呈现：工具调用是过程，不是结果，
// 视觉上应当退到背景里，把注意力让给最终的答复。
func (c *ConsoleSink) OnToolCall(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lineOpen {
		fmt.Fprintln(c.out)
		c.lineOpen = false
	}
	fmt.Fprintln(c.out, c.paint(ansiDim, fmt.Sprintf("→ 调用工具 %s", name)))
}

// OnDone 在响应收齐后收尾换行。
func (c *ConsoleSink) OnDone() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lineOpen {
		fmt.Fprintln(c.out)
		c.lineOpen = false
	}
}

// 确保 ConsoleSink 满足 llm.Sink。
var _ llm.Sink = (*ConsoleSink)(nil)
