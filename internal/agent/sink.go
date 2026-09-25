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
	ansiCyan  = "\033[36m"
	ansiBold  = "\033[1m"
)

// ConsoleSink 把流式增量渲染到终端。
//
// 三类输出用样式区分：思维链暗色前缀、正文常规输出、工具调用高亮提示。
// 这样屏幕上不会出现「看起来像答案、其实是过程说明」的歧义——
// 模型可能先输出一段正文再请求调用工具，那段正文并不是最终答复。
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
func (c *ConsoleSink) OnToolCall(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.lineOpen {
		fmt.Fprintln(c.out)
		c.lineOpen = false
	}
	fmt.Fprintln(c.out, c.paint(ansiCyan, fmt.Sprintf("→ 调用工具 %s", name)))
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
